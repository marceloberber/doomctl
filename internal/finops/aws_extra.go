package finops

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// ScanRDS coleta as instâncias RDS da região (rds:DescribeDBInstances).
func (a *AWSClient) ScanRDS(ctx context.Context, region string) ([]Resource, error) {
	ep := a.RDSEndpoint
	if ep == "" {
		ep = "https://rds.{region}.amazonaws.com"
	}
	now := a.now()
	var out []Resource
	marker := ""
	for page := 0; page < 100; page++ {
		p := url.Values{"MaxRecords": {"100"}}
		if marker != "" {
			p.Set("Marker", marker)
		}
		var r struct {
			Items []struct {
				ID         string   `xml:"DBInstanceIdentifier"`
				Class      string   `xml:"DBInstanceClass"`
				Engine     string   `xml:"Engine"`
				Status     string   `xml:"DBInstanceStatus"`
				MultiAZ    bool     `xml:"MultiAZ"`
				Storage    int      `xml:"AllocatedStorage"`
				StorageTyp string   `xml:"StorageType"`
				Created    string   `xml:"InstanceCreateTime"`
				Tags       []ec2Tag `xml:"TagList>Tag"`
			} `xml:"DescribeDBInstancesResult>DBInstances>DBInstance"`
			Marker string `xml:"DescribeDBInstancesResult>Marker"`
		}
		if err := a.queryCall(ctx, "rds", ep, region, "2014-10-31", "DescribeDBInstances", p, &r); err != nil {
			return nil, err
		}
		for _, d := range r.Items {
			tg := tagsMap(d.Tags)
			out = append(out, Resource{Provider: "aws", Region: region, ResourceID: d.ID, Type: "database", Name: d.ID,
				SKU: d.Class, SizeGB: float64(d.Storage), State: d.Status, AgeDays: ageDays(d.Created, now), Tags: tg,
				Raw: map[string]any{"engine": d.Engine, "multi_az": d.MultiAZ, "storage_type": d.StorageTyp}})
		}
		if r.Marker == "" {
			break
		}
		marker = r.Marker
	}
	return out, nil
}

// CPUStats devolve a média e o máximo diário de CPUUtilization (CloudWatch) nos últimos dias.
// namespace: AWS/EC2 (dimensão InstanceId) ou AWS/RDS (DBInstanceIdentifier).
func (a *AWSClient) CPUStats(ctx context.Context, region, namespace, dimName, id string, days int) (avg, max *float64, err error) {
	ep := a.CloudWatchEndpoint
	if ep == "" {
		ep = "https://monitoring.{region}.amazonaws.com"
	}
	end := a.now().UTC().Truncate(time.Hour)
	start := end.Add(-time.Duration(days) * 24 * time.Hour)
	p := url.Values{
		"Namespace": {namespace}, "MetricName": {"CPUUtilization"},
		"Dimensions.member.1.Name": {dimName}, "Dimensions.member.1.Value": {id},
		"StartTime": {start.Format(time.RFC3339)}, "EndTime": {end.Format(time.RFC3339)},
		"Period": {"86400"}, "Statistics.member.1": {"Average"}, "Statistics.member.2": {"Maximum"},
	}
	var r struct {
		Points []struct {
			Average string `xml:"Average"`
			Maximum string `xml:"Maximum"`
		} `xml:"GetMetricStatisticsResult>Datapoints>member"`
	}
	if err := a.queryCall(ctx, "monitoring", ep, region, "2010-08-01", "GetMetricStatistics", p, &r); err != nil {
		return nil, nil, err
	}
	if len(r.Points) == 0 {
		return nil, nil, nil
	}
	var sum, mx float64
	for _, x := range r.Points {
		av, _ := strconv.ParseFloat(x.Average, 64)
		m, _ := strconv.ParseFloat(x.Maximum, 64)
		sum += av
		if m > mx {
			mx = m
		}
	}
	av := round2(sum / float64(len(r.Points)))
	mx = round2(mx)
	return &av, &mx, nil
}

// RemediationActions são as ações automatizadas suportadas (somente AWS EC2).
var RemediationActions = map[string]struct {
	Label, ResourceType string
	Destructive         bool
}{
	"stop_instance":     {"Parar instância", "instance", false},
	"modify_volume_gp3": {"Migrar volume para gp3", "volume", false},
	"snapshot_volume":   {"Criar snapshot do volume", "volume", false},
	"release_ip":        {"Liberar IP elástico", "public_ip", true},
	"delete_snapshot":   {"Excluir snapshot", "snapshot", true},
	"delete_volume":     {"Excluir volume", "volume", true},
}

// Remediate executa (ou simula, com dryRun) uma ação de remediação na API do EC2.
// Em dry-run a AWS responde DryRunOperation quando a ação seria permitida.
func (a *AWSClient) Remediate(ctx context.Context, region, action, id string, dryRun bool) (string, error) {
	p := url.Values{}
	var api string
	switch action {
	case "stop_instance":
		api = "StopInstances"
		p.Set("InstanceId.1", id)
	case "modify_volume_gp3":
		api = "ModifyVolume"
		p.Set("VolumeId", id)
		p.Set("VolumeType", "gp3")
	case "snapshot_volume":
		api = "CreateSnapshot"
		p.Set("VolumeId", id)
		p.Set("Description", "doomctl FinOps: backup antes de remover o volume "+id)
	case "release_ip":
		api = "ReleaseAddress"
		p.Set("AllocationId", id)
	case "delete_snapshot":
		api = "DeleteSnapshot"
		p.Set("SnapshotId", id)
	case "delete_volume":
		api = "DeleteVolume"
		p.Set("VolumeId", id)
	default:
		return "", fmt.Errorf("ação não suportada: %q", action)
	}
	if dryRun {
		p.Set("DryRun", "true")
	}
	var raw struct {
		SnapshotID string `xml:"snapshotId"`
	}
	err := a.ec2Call(ctx, region, api, p, &raw)
	var qe *QueryError
	if dryRun && errors.As(err, &qe) && qe.Code == "DryRunOperation" {
		return "simulação OK: a AWS confirmou que " + api + " seria permitido para " + id, nil
	}
	if err != nil {
		return "", err
	}
	if dryRun {
		return "", errors.New("resposta inesperada da AWS em modo dry-run")
	}
	msg := api + " executado para " + id
	if raw.SnapshotID != "" {
		msg += " (snapshot " + raw.SnapshotID + ")"
	}
	return msg, nil
}
