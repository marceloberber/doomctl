package finops

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)

func TestSigV4Vector(t *testing.T) {
	// Exemplo oficial da documentação do SigV4 (IAM ListUsers).
	req, _ := http.NewRequest("GET", "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	now, _ := time.Parse("20060102T150405Z", "20150830T123600Z")
	SignV4(req, nil, AWSCreds{AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, "us-east-1", "iam", now)
	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7") {
		t.Fatalf("assinatura incorreta: %s", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=content-type;host;x-amz-date") || !strings.Contains(auth, "Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request") {
		t.Fatalf("cabeçalho incorreto: %s", auth)
	}
}

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := x509.MarshalPKCS8PrivateKey(k)
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
}

func TestOCISignatureAndUsageAPI(t *testing.T) {
	key, pemKey := testKey(t)
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// verifica a assinatura como a OCI faria
		auth := r.Header.Get("Authorization")
		m := regexp.MustCompile(`headers="([^"]+)",signature="([^"]+)"`).FindStringSubmatch(auth)
		if m == nil || !strings.Contains(auth, `keyId="ocid1.tenancy.oc1..t/ocid1.user.oc1..u/aa:bb"`) {
			http.Error(w, `{"code":"NotAuthenticated","message":"bad header"}`, 401)
			return
		}
		req := r.Clone(context.Background())
		req.Host = r.Host
		ss := OCISigningString(req, strings.Split(m[1], " "))
		sig, _ := base64.StdEncoding.DecodeString(m[2])
		h := sha256.Sum256([]byte(ss))
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h[:], sig); err != nil {
			http.Error(w, `{"code":"NotAuthenticated","message":"signature"}`, 401)
			return
		}
		sum := sha256.Sum256(body)
		if r.Header.Get("X-Content-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			http.Error(w, `{"code":"InvalidParameter","message":"hash"}`, 400)
			return
		}
		var in map[string]any
		json.Unmarshal(body, &in)
		if in["granularity"] != "DAILY" || in["queryType"] != "COST" {
			http.Error(w, `{"code":"InvalidParameter","message":"query"}`, 400)
			return
		}
		pages++
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("opc-next-page", "p2")
			w.Write([]byte(`{"items":[{"timeUsageStarted":"2026-10-01T00:00:00.000Z","service":"Compute","region":"sa-saopaulo-1","computedAmount":12.5,"currency":"USD"}]}`))
			return
		}
		w.Write([]byte(`{"items":[{"timeUsageStarted":"2026-10-02T00:00:00.000Z","service":"Block Storage","region":"sa-saopaulo-1","computedAmount":"3.25","currency":"USD","tags":[{"namespace":"ops","key":"Project","value":"erp"}]}]}`))
	}))
	defer srv.Close()
	cl := &OCIClient{Creds: OCICreds{Tenancy: "ocid1.tenancy.oc1..t", User: "ocid1.user.oc1..u", Fingerprint: "aa:bb", PrivateKey: pemKey, Region: "sa-saopaulo-1"},
		Endpoint: srv.URL, Now: func() time.Time { return testNow }}
	items, err := cl.RequestSummarizedUsages(context.Background(), testNow.AddDate(0, 0, -10), testNow, []string{"service", "region"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(items) != 2 || items[1].Amount != 3.25 || items[1].Tags["ops.Project"] != "erp" || items[0].Day.Day() != 1 {
		t.Fatalf("itens inesperados: %+v (páginas %d)", items, pages)
	}
	if _, err := ParseRSAKey("lixo"); err == nil {
		t.Fatal("chave inválida aceita")
	}
}

func TestAWSClientsWithFakeEndpoints(t *testing.T) {
	var targets []string
	ce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") {
			w.WriteHeader(403)
			return
		}
		tg := r.Header.Get("X-Amz-Target")
		targets = append(targets, tg)
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		switch {
		case strings.HasSuffix(tg, "GetCostAndUsage"):
			gb := in["GroupBy"].([]any)
			second := gb[1].(map[string]any)["Key"].(string)
			k2 := "us-east-1"
			if second == "Project" {
				k2 = "Project$portal"
			}
			if in["NextPageToken"] == nil {
				json.NewEncoder(w).Encode(map[string]any{"NextPageToken": "x", "ResultsByTime": []any{map[string]any{"TimePeriod": map[string]string{"Start": "2026-10-01"},
					"Groups": []any{map[string]any{"Keys": []string{"Amazon EC2", k2}, "Metrics": map[string]any{"UnblendedCost": map[string]string{"Amount": "10.5", "Unit": "USD"}}}}}}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ResultsByTime": []any{map[string]any{"TimePeriod": map[string]string{"Start": "2026-10-02"},
				"Groups": []any{map[string]any{"Keys": []string{"Amazon S3", k2}, "Metrics": map[string]any{"UnblendedCost": map[string]string{"Amount": "1.25", "Unit": "USD"}}}}}}})
		case strings.HasSuffix(tg, "GetRightsizingRecommendation"):
			w.Write([]byte(`{"RightsizingRecommendations":[{"RightsizingType":"Modify","CurrentInstance":{"ResourceId":"i-1","InstanceName":"web","ResourceDetails":{"EC2ResourceDetails":{"InstanceType":"m5.2xlarge","Region":"us-east-1"}},"ResourceUtilization":{"EC2ResourceUtilization":{"MaxCpuUtilizationPercentage":"12"}}},"ModifyRecommendationDetail":{"TargetInstances":[{"DefaultTargetInstance":true,"EstimatedMonthlySavings":"140.16","CurrencyCode":"USD","ResourceDetails":{"EC2ResourceDetails":{"InstanceType":"m5.xlarge"}}}]}}]}`))
		case strings.HasSuffix(tg, "GetProducts"):
			w.Write([]byte(`{"PriceList":["{\"terms\":{\"OnDemand\":{\"X\":{\"priceDimensions\":{\"Y\":{\"pricePerUnit\":{\"USD\":\"0.0960000000\"}}}}}}}"]}`))
		default:
			w.WriteHeader(400)
			w.Write([]byte(`{"__type":"com.amazon#ValidationException","message":"alvo desconhecido"}`))
		}
	}))
	defer ce.Close()
	q := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.URL.Query().Get("Action")
		switch a {
		case "DescribeVolumes":
			w.Write([]byte(`<DescribeVolumesResponse><volumeSet><item><volumeId>vol-1</volumeId><size>100</size><volumeType>gp2</volumeType><status>available</status><createTime>2026-01-01T00:00:00.000Z</createTime><attachmentSet/><tagSet><item><key>Name</key><value>orfao</value></item></tagSet></item></volumeSet></DescribeVolumesResponse>`))
		case "DescribeAddresses":
			w.Write([]byte(`<DescribeAddressesResponse><addressesSet><item><publicIp>1.2.3.4</publicIp><allocationId>eipalloc-1</allocationId></item></addressesSet></DescribeAddressesResponse>`))
		case "DescribeSnapshots":
			w.Write([]byte(`<DescribeSnapshotsResponse><snapshotSet><item><snapshotId>snap-1</snapshotId><volumeSize>10</volumeSize><startTime>2025-01-01T00:00:00.000Z</startTime><status>completed</status></item></snapshotSet></DescribeSnapshotsResponse>`))
		case "DescribeInstances":
			w.Write([]byte(`<DescribeInstancesResponse><reservationSet><item><instancesSet><item><instanceId>i-1</instanceId><instanceType>m5.large</instanceType><instanceState><name>running</name></instanceState><launchTime>2026-09-01T00:00:00.000Z</launchTime><blockDeviceMapping><item><ebs><volumeId>vol-2</volumeId></ebs></item></blockDeviceMapping><tagSet><item><key>Environment</key><value>dev</value></item></tagSet></item></instancesSet></item></reservationSet></DescribeInstancesResponse>`))
		case "DescribeDBInstances":
			w.Write([]byte(`<DescribeDBInstancesResponse><DescribeDBInstancesResult><DBInstances><DBInstance><DBInstanceIdentifier>db1</DBInstanceIdentifier><DBInstanceClass>db.m5.xlarge</DBInstanceClass><Engine>postgres</Engine><DBInstanceStatus>available</DBInstanceStatus><MultiAZ>true</MultiAZ><AllocatedStorage>100</AllocatedStorage><StorageType>gp2</StorageType><TagList><Tag><Key>Environment</Key><Value>staging</Value></Tag></TagList></DBInstance></DBInstances></DescribeDBInstancesResult></DescribeDBInstancesResponse>`))
		case "GetMetricStatistics":
			w.Write([]byte(`<GetMetricStatisticsResponse><GetMetricStatisticsResult><Datapoints><member><Average>2</Average><Maximum>8</Maximum></member><member><Average>4</Average><Maximum>12</Maximum></member></Datapoints></GetMetricStatisticsResult></GetMetricStatisticsResponse>`))
		case "StopInstances":
			if r.URL.Query().Get("DryRun") == "true" {
				w.WriteHeader(412)
				w.Write([]byte(`<Response><Errors><Error><Code>DryRunOperation</Code><Message>Request would have succeeded, but DryRun flag is set.</Message></Error></Errors></Response>`))
				return
			}
			w.Write([]byte(`<StopInstancesResponse/>`))
		default:
			w.WriteHeader(400)
			w.Write([]byte(`<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>negado</Message></Error></Errors></Response>`))
		}
	}))
	defer q.Close()
	cfg := SourceConfig{Regions: []string{"us-east-1"}, Usage: false, Rightsizing: true, Metrics: true,
		Endpoints: map[string]string{"ce": ce.URL, "ec2": q.URL, "rds": q.URL, "cloudwatch": q.URL, "pricing": ce.URL}}
	cfg.Normalize("aws")
	cl := NewAWSClient(AWSCreds{AccessKey: "AK", SecretKey: "SK"}, cfg, nil)
	cl.Now = func() time.Time { return testNow }
	res, err := SyncAWS(context.Background(), cl, cfg, 7, "prod", testNow.AddDate(0, 0, -7), Day(testNow), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Costs) != 2 || res.Costs[0].Cost != 10.5 || res.Costs[0].Account != "prod" || res.Costs[1].Service != "Amazon S3" {
		t.Fatalf("custos: %+v", res.Costs)
	}
	var proj []AllocRow
	for _, a := range res.Alloc {
		if a.Dim == "project" {
			proj = append(proj, a)
		}
	}
	if len(proj) != 2 || proj[0].Value != "portal" {
		t.Fatalf("alocação: %+v", res.Alloc)
	}
	if len(res.Rightsizing) != 1 {
		t.Fatalf("rightsizing: %v", res.Rightsizing)
	}
	f, ok := ceRightsizing(res.Rightsizing[0], DefaultSettings(), 7)
	if !ok || f.MonthlySavings == nil || *f.MonthlySavings != 140.16 || !strings.Contains(f.Title, "m5.xlarge") {
		t.Fatalf("finding CE: %+v", f)
	}
	inv, warns, err := ScanAWS(context.Background(), cl, cfg, 7, io.Discard)
	if err != nil || len(warns) != 0 {
		t.Fatal(err, warns)
	}
	types := map[string]Resource{}
	for _, r := range inv {
		types[r.Type] = r
	}
	if len(inv) != 5 || types["volume"].Attached == nil || *types["volume"].Attached || types["public_ip"].Attached == nil || *types["public_ip"].Attached {
		t.Fatalf("inventário: %+v", inv)
	}
	if types["instance"].CPUMax == nil || *types["instance"].CPUMax != 12 || *types["instance"].CPUAvg != 3 {
		t.Fatalf("métricas: %+v", types["instance"])
	}
	if b, _ := types["database"].Raw["multi_az"].(bool); !b || types["database"].SKU != "db.m5.xlarge" {
		t.Fatalf("RDS: %+v", types["database"])
	}
	msg, err := cl.Remediate(context.Background(), "us-east-1", "stop_instance", "i-1", true)
	if err != nil || !strings.Contains(msg, "simulação OK") {
		t.Fatal(msg, err)
	}
	if _, err := cl.Remediate(context.Background(), "us-east-1", "delete_volume", "vol-1", false); err == nil || !strings.Contains(err.Error(), "UnauthorizedOperation") {
		t.Fatal("esperava erro de permissão", err)
	}
	p, err := cl.LookupPrice(context.Background(), "us-east-1", PriceLookup{Kind: "ec2", Value: "m5.large"})
	if err != nil || p.Price != 0.096 || p.SKU != "instance:m5.large" {
		t.Fatal(p, err)
	}
}

func demoData() (Data, DemoData) {
	dd := Demo(1, testNow)
	return Data{Costs: dd.Costs, Alloc: dd.Alloc, Usage: dd.Usage}, dd
}

func TestOverviewAndAllocation(t *testing.T) {
	d, _ := demoData()
	st := DefaultSettings()
	start := Day(testNow).AddDate(0, 0, -30)
	o := BuildOverview(d, Filter{}, st, start, Day(testNow), testNow)
	if !o.HasData || o.Total <= 0 || len(o.Series) != 30 || len(o.SeriesKeys) == 0 {
		t.Fatalf("overview: total=%v séries=%d", o.Total, len(o.Series))
	}
	var svcSum float64
	for _, b := range o.Breakdowns["service"] {
		svcSum += b.Cost
	}
	if diff := svcSum - o.Total; diff > 1 || diff < -1 {
		t.Fatalf("soma por serviço %.2f ≠ total %.2f", svcSum, o.Total)
	}
	var projSum float64
	for _, b := range o.Breakdowns["project"] {
		projSum += b.Cost
	}
	if diff := projSum - o.Total; diff > 2 || diff < -2 {
		t.Fatalf("alocação por projeto %.2f ≠ total %.2f", projSum, o.Total)
	}
	if c := o.TagCoverage["project"]; c < 90 || c > 99 {
		t.Fatalf("cobertura de tags: %v", c)
	}
	// filtro por projeto usa a alocação
	op := BuildOverview(d, Filter{Project: "data"}, st, start, Day(testNow), testNow)
	if op.Total <= 0 || op.Total >= o.Total || len(op.Notes) == 0 {
		t.Fatalf("filtro por projeto: %v", op.Total)
	}
	// conversão de moeda
	st2 := st
	st2.BaseCurrency = "BRL"
	o2 := BuildOverview(d, Filter{}, st2, start, Day(testNow), testNow)
	if len(o2.Unconverted) != 1 || o2.Total != 0 {
		t.Fatalf("sem taxa deveria ignorar USD: %v %v", o2.Unconverted, o2.Total)
	}
	st2.FXRates = map[string]float64{"USD": 5.5}
	o3 := BuildOverview(d, Filter{}, st2, start, Day(testNow), testNow)
	if r := o3.Total / o.Total; r < 5.49 || r > 5.51 {
		t.Fatalf("conversão: %v", r)
	}
	// regras de equipe quando não há linhas de equipe
	var alloc []AllocRow
	for _, a := range d.Alloc {
		if a.Dim != "team" {
			alloc = append(alloc, a)
		}
	}
	st.TeamRules = []TeamRule{{Dim: "project", Pattern: "port*", Team: "Web"}, {Dim: "service", Pattern: "*sagemaker*", Team: "ML"}}
	st.Normalize()
	tr := TeamRows(alloc, st)
	seen := map[string]bool{}
	for _, r := range tr {
		seen[r.Value] = true
	}
	if !seen["Web"] || !seen["ML"] || !seen[""] {
		t.Fatalf("regras de equipe: %v", seen)
	}
}

func TestAnomalies(t *testing.T) {
	d, _ := demoData()
	st := DefaultSettings()
	an := DetectAnomalies(d, st, testNow, 30)
	var nat, sage, total bool
	for _, a := range an {
		if a.Scope == "service" && a.Name == "EC2 - Other" && a.Kind == "spike" {
			nat = true
		}
		if a.Scope == "service" && a.Name == "Amazon SageMaker" && a.Kind == "new" {
			sage = true
		}
	}
	// no total o pico do NAT é ~16%: abaixo do limite padrão de 40%, acima de 10%
	st10 := st
	st10.AnomalyPct = 10
	for _, a := range DetectAnomalies(d, st10, testNow, 30) {
		if a.Scope == "total" && len(a.Drivers) > 0 && a.Drivers[0].Name == "EC2 - Other" {
			total = true
		}
	}
	if !nat || !sage || !total {
		t.Fatalf("anomalias esperadas não encontradas (nat=%v sagemaker=%v total=%v): %d", nat, sage, total, len(an))
	}
	// SageMaker só deve aparecer como "novo" uma vez (sem repetir como pico na rampa)
	n := 0
	for _, a := range an {
		if a.Name == "Amazon SageMaker" && a.Scope == "service" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("SageMaker reportado %d vezes", n)
	}
	// série estável não gera falso positivo
	var rows []CostRow
	for i := 1; i <= 60; i++ {
		rows = append(rows, CostRow{Day: Day(testNow).AddDate(0, 0, -i), Service: "X", Cost: 100 + float64(i%3), Currency: "USD"})
	}
	if a := DetectAnomalies(Data{Costs: rows}, st, testNow, 30); len(a) != 0 {
		t.Fatalf("falso positivo: %+v", a)
	}
	med, _, z := RobustZ([]float64{10, 10, 11, 9, 10, 10, 12}, 30)
	if med != 10 || z < 10 {
		t.Fatal(med, z)
	}
}

func TestForecastAndBudgets(t *testing.T) {
	daily := map[string]float64{}
	for i := 1; i <= 60; i++ {
		daily[dayKey(Day(testNow).AddDate(0, 0, -i))] = 100 + float64(60-i) // crescimento de 1/dia
	}
	f := ForecastDaily(daily, testNow, 0)
	if f.SlopeDay < 0.9 || f.SlopeDay > 1.1 {
		t.Fatalf("inclinação: %v", f.SlopeDay)
	}
	// 5 dias de outubro já ocorridos: 155..159
	if f.MTD != 155+156+157+158+159 {
		t.Fatalf("MTD: %v", f.MTD)
	}
	if f.EOM < f.MTD+26*160 || f.EOM > f.MTD+26*190 || f.EOMLow > f.EOM || f.EOMHigh < f.EOM || f.NextMonth <= 0 {
		t.Fatalf("previsão: %+v", f)
	}
	d, dd := demoData()
	st := DefaultSettings()
	bs := EvaluateBudgets(dd.Budgets, d, st, testNow)
	if len(bs) != 3 || bs[0].Actual <= 0 || bs[0].Forecast <= bs[0].Actual || len(bs[0].Cumulative) != 5 {
		t.Fatalf("orçamentos: %+v", bs[0])
	}
	tiny := []Budget{{Name: "t", ScopeType: "service", ScopeValue: "AWS Lambda", Amount: 1, Currency: "USD", Thresholds: []int{50, 100}}}
	if r := EvaluateBudgets(tiny, d, st, testNow)[0]; r.Status != "exceeded" || len(r.Crossed) != 2 {
		t.Fatalf("orçamento excedido: %+v", r)
	}
	if r := EvaluateBudgets([]Budget{{Name: "b", ScopeType: "all", Amount: 100, Currency: "EUR"}}, d, st, testNow)[0]; r.Status != "no_rate" {
		t.Fatal(r.Status)
	}
}

func TestRulesOnDemoInventory(t *testing.T) {
	_, dd := demoData()
	st := DefaultSettings()
	fs := EvaluateResources(RuleInput{Resources: dd.Resources, Prices: NewPrices(dd.Prices), Settings: st, Now: testNow})
	rules := map[string]Finding{}
	for _, f := range fs {
		if _, ok := rules[f.Rule]; !ok {
			rules[f.Rule] = f
		}
	}
	for _, r := range []string{"unattached_volume", "gp2_to_gp3", "unassociated_ip", "old_snapshot", "stopped_instance", "idle_instance",
		"rightsize_instance", "previous_generation", "off_hours", "spot_candidate", "rightsize_database", "multiaz_nonprod", "db_gp2", "orphan_load_balancer", "missing_tags"} {
		if _, ok := rules[r]; !ok {
			t.Errorf("regra %s não disparou", r)
		}
	}
	// valores
	if f := rules["unattached_volume"]; f.MonthlySavings == nil || f.Action != "delete_volume" {
		t.Errorf("volume órfão: %+v", f)
	}
	for _, f := range fs {
		if f.Rule == "rightsize_instance" && f.ResourceID == "i-0a1b2c3d4e5f60001" {
			// m5.2xlarge (0.384) → m5.xlarge (0.192) = 0.192 × 730
			if f.MonthlySavings == nil || *f.MonthlySavings != 140.16 {
				t.Errorf("rightsizing: %+v", f.MonthlySavings)
			}
		}
		if f.Rule == "unassociated_ip" && (f.MonthlySavings == nil || *f.MonthlySavings != 3.65) {
			t.Errorf("IP: %v", *f.MonthlySavings)
		}
	}
	if Downsize("m5.large") != "" || Downsize("m5.xlarge") != "m5.large" || Downsize("t3.small") != "t3.micro" || Downsize("db.r5.2xlarge") != "db.r5.xlarge" || Downsize("m6g.large") != "m6g.medium" {
		t.Error("Downsize")
	}
	if Modernize("aws", "db.m4.large") != "db.m5.large" || Modernize("aws", "m6i.large") != "" || Modernize("oci", "VM.Standard2.4") == "" {
		t.Error("Modernize")
	}
	// políticas
	vs := EvaluatePolicies(dd.Policies, dd.Resources, Coster{Prices: NewPrices(dd.Prices), Conv: st.Converter()})
	var block, tags bool
	for _, v := range vs {
		if v.Action == "block" && v.ResourceID == "i-0a1b2c3d4e5f60005" {
			t.Errorf("m5.xlarge não deveria violar: %+v", v)
		}
		if strings.Contains(v.Message, "tags ausentes") {
			tags = true
		}
		if v.Action == "block" {
			block = true
		}
	}
	if !tags {
		t.Error("política de tags não disparou")
	}
	_ = block
}

func TestUsageAndCommitments(t *testing.T) {
	d, _ := demoData()
	st := DefaultSettings()
	ua := AnalyzeUsage(d.Usage, st, testNow, 30)
	if !ua.HasData || len(ua.Network) == 0 || len(ua.Storage) == 0 {
		t.Fatalf("uso: %+v", ua)
	}
	got := map[string]bool{}
	for _, f := range ua.Findings {
		got[f.Rule] = true
	}
	if !got["nat_gateway"] || !got["object_lifecycle"] {
		t.Fatalf("recomendações de rede/armazenamento: %v", got)
	}
	for in, want := range map[string]string{"USE1-NatGateway-Bytes": "net_nat", "USE1-USW2-AWS-Out-Bytes": "net_inter_region", "USE1-TimedStorage-ByteHrs": "obj_standard",
		"USE1-TimedStorage-SIA-ByteHrs": "obj_ia", "USE1-EBS:SnapshotUsage": "blk_snapshot", "Outbound Data Transfer Zone 1": "net_egress", "USE1-BoxUsage:m5.large": ""} {
		if c := ClassifyUsage("aws", "", in); c != want {
			t.Errorf("%s → %q, esperado %q", in, c, want)
		}
	}
	ca := AnalyzeCommitments(d, st, testNow)
	if len(ca.Groups) < 2 {
		t.Fatalf("compromissos: %+v", ca)
	}
	g := ca.Groups[0]
	if g.Group != "aws_savings_plans" || g.DailyP10 <= 0 || g.DailyP10 > g.DailyAvg || len(g.Options) != 2 || g.Options[1].MonthlySavings <= g.Options[0].MonthlySavings {
		t.Fatalf("savings plans: %+v", g)
	}
}

func TestK8s(t *testing.T) {
	_, dd := demoData()
	rep, err := AnalyzeK8s(DemoK8sInput(), NewPrices(dd.Prices), DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if rep.PricedNodes != 0 && rep.Monthly <= 0 {
		t.Fatal("custo")
	}
	// m5.xlarge/m5.2xlarge não estão no catálogo de exemplo (exceto m5.xlarge, m5.2xlarge — estão)
	if rep.Monthly <= 0 || rep.Allocated <= 0 || rep.Idle <= 0 || len(rep.Namespaces) == 0 {
		t.Fatalf("relatório: %+v", rep)
	}
	var kinds = map[string]bool{}
	for _, w := range rep.Workloads {
		kinds[w.Kind+"/"+w.Name] = true
	}
	for _, k := range []string{"Deployment/portal-web", "Deployment/orders-api", "StatefulSet/spark-driver", "CronJob/relatorio-noturno", "Pod/cache-sem-dono"} {
		if !kinds[k] {
			t.Errorf("workload %s não identificado: %v", k, kinds)
		}
	}
	rules := map[string]bool{}
	for _, f := range rep.Findings {
		rules[f.Rule] = true
	}
	for _, r := range []string{"k8s_no_requests", "k8s_cpu_overrequest", "k8s_nonprod_replicas", "k8s_spot"} {
		if !rules[r] {
			t.Errorf("recomendação %s ausente: %v", r, rules)
		}
	}
	if ParseCPU("250m") != 0.25 || ParseCPU("2") != 2 || ParseMemGiB("512Mi") != 0.5 || ParseMemGiB("1Gi") != 1 || ParseMemGiB("1073741824") != 1 {
		t.Error("quantidades")
	}
	// custo informado do cluster
	in := DemoK8sInput()
	in.ClusterMonthly = 1000
	rep2, _ := AnalyzeK8s(in, NewPrices(nil), DefaultSettings())
	if rep2.Monthly < 999 || rep2.Monthly > 1001 {
		t.Fatalf("custo informado: %v", rep2.Monthly)
	}
}

const tfMain = `
terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = {
      Project     = "portal"
      Environment = var.environment
    }
  }
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "environment" {
  default = "dev"
}

variable "instance_type" {
  default = "t2.large" # geração anterior
}

locals {
  nodes = 2
}

resource "aws_instance" "web" {
  count         = local.nodes
  ami           = data.aws_ami.x.id
  instance_type = var.instance_type
  root_block_device {
    volume_size = 30
    volume_type = "gp2"
  }
  tags = { Name = "${var.environment}-web", Owner = "web" }
}

resource "aws_ebs_volume" "data" {
  availability_zone = "us-east-1a"
  size              = 100
}

resource "aws_eip" "ip" {}

resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_s3_bucket" "logs" {
  bucket = "logs-${var.environment}"
}

resource "aws_kinesis_stream" "x" {
  name = "x"
}

/* comentário
   resource "aws_instance" "fantasma" {} */
`

func TestIaCEstimate(t *testing.T) {
	_, dd := demoData()
	st := DefaultSettings()
	files := map[string]string{"main.tf": tfMain}
	est := EstimateIaC(files, IaCOptions{Policies: dd.Policies}, NewPrices(dd.Prices), st)
	items := map[string]IaCItem{}
	for _, it := range est.Items {
		items[it.Address] = it
	}
	web := items["aws_instance.web"]
	// 2 × (0.0928×730 + 30×0.10)
	if web.Count != 2 || web.Monthly == nil || *web.Monthly != round2(2*(0.0928*730+3)) || web.Env != "dev" || web.Tags["Project"] != "portal" {
		t.Fatalf("aws_instance: %+v", web)
	}
	if v := items["aws_ebs_volume.data"]; v.Monthly == nil || *v.Monthly != 10 { // gp2 padrão
		t.Fatalf("ebs: %+v", v)
	}
	if v := items["aws_eip.ip"]; v.Monthly == nil || *v.Monthly != 3.65 {
		t.Fatalf("eip: %+v", v)
	}
	if len(est.Free) != 1 || len(est.Unsupported) != 1 || est.Region != "us-east-1" {
		t.Fatalf("classificação: free=%v unsupported=%v região=%s", est.Free, est.Unsupported, est.Region)
	}
	if _, ok := items["aws_instance.fantasma"]; ok {
		t.Fatal("comentário de bloco não foi ignorado")
	}
	if len(est.Fixes) != 2 {
		t.Fatalf("correções: %+v", est.Fixes)
	}
	fixed, n := ApplyIaCFixes(files, est.Fixes)
	if n != 2 || !strings.Contains(fixed["main.tf"], `default = "t3.large"`) || !strings.Contains(fixed["main.tf"], `volume_type = "gp3"`) {
		t.Fatalf("aplicação das correções: %d\n%s", n, fixed["main.tf"])
	}
	est2 := EstimateIaC(fixed, IaCOptions{}, NewPrices(dd.Prices), st)
	if est2.Total >= est.Total {
		t.Fatalf("correções deveriam reduzir o custo: %v → %v", est.Total, est2.Total)
	}
	// verificação de CI
	bigger := strings.Replace(tfMain, "nodes = 2", "nodes = 10", 1)
	ck := CheckIaC(files, map[string]string{"main.tf": bigger}, IaCOptions{Policies: dd.Policies}, NewPrices(dd.Prices), st)
	if ck.Pass || ck.Delta <= 0 || len(ck.Items) != 1 || ck.Items[0].Change != "changed" || !strings.Contains(ck.Markdown(), "bloqueado") {
		t.Fatalf("CI check: %+v", ck)
	}
	ok := CheckIaC(files, files, IaCOptions{}, NewPrices(dd.Prices), st)
	if !ok.Pass || ok.Delta != 0 {
		t.Fatalf("sem mudança deveria passar: %+v", ok)
	}
	// política "block" em dev: tipo grande proibido
	big := strings.Replace(tfMain, `"t2.large"`, `"m5.2xlarge"`, 1)
	ck2 := CheckIaC(files, map[string]string{"main.tf": big}, IaCOptions{Policies: dd.Policies}, NewPrices(dd.Prices), st)
	found := false
	for _, r := range ck2.Reasons {
		if strings.Contains(r, "proibido") {
			found = true
		}
	}
	if ck2.Pass || !found {
		t.Fatalf("política de bloqueio: %+v", ck2.Reasons)
	}
	// variáveis sobrescritas
	est3 := EstimateIaC(files, IaCOptions{Vars: map[string]string{"instance_type": "m5.large", "environment": "prod"}}, NewPrices(dd.Prices), st)
	for _, it := range est3.Items {
		if it.Address == "aws_instance.web" && (!strings.HasPrefix(it.Description, "m5.large") || it.Env != "prod") {
			t.Fatalf("override: %+v", it)
		}
	}
}

func TestIaCOnGeneratedOCI(t *testing.T) {
	src := `
provider "oci" {
  region = var.region
}
variable "region" { default = "sa-saopaulo-1" }
variable "shape" {
  type    = string
  default = "VM.Standard.E5.Flex"
}
variable "freeform_tags" {
  type = map(string)
  default = {
    "Project" = "erp"
  }
}
resource "oci_core_instance" "main" {
  shape         = var.shape
  freeform_tags = var.freeform_tags
  shape_config {
    ocpus         = 2
    memory_in_gbs = 16
  }
  source_details {
    source_type             = "image"
    boot_volume_size_in_gbs = 100
  }
}
resource "oci_core_volume" "data" {
  size_in_gbs = 200
  vpus_per_gb = 20
}
`
	_, dd := demoData()
	est := EstimateIaC(map[string]string{"main.tf": src}, IaCOptions{}, NewPrices(dd.Prices), DefaultSettings())
	if len(est.Items) != 2 || len(est.Unpriced) != 0 {
		t.Fatalf("OCI: %+v", est)
	}
	// instância: 2×0.03×730 + 16×0.002×730 + 100×0.0255 + 1000×0.0017
	want := round2(2*0.03*730 + 16*0.002*730 + 100*0.0255 + 1000*0.0017)
	for _, it := range est.Items {
		if it.Address == "oci_core_instance.main" && (it.Monthly == nil || *it.Monthly != want || it.Tags["Project"] != "erp") {
			t.Fatalf("oci_core_instance: %v (esperado %v) %+v", *it.Monthly, want, it)
		}
	}
}

func TestCSVImports(t *testing.T) {
	csvText := "data;provedor;serviço;conta;região;projeto;ambiente;custo;moeda;tipo_uso\n" +
		"01/10/2026;aws;Amazon EC2;prod;us-east-1;portal;prod;1.234,56;USD;\n" +
		"01/10/2026;aws;Amazon EC2;prod;us-east-1;api;prod;10,5;USD;\n" +
		"02/10/2026;aws;EC2 - Other;prod;us-east-1;;dev;R$ 7,00;BRL;USE1-NatGateway-Bytes\n" +
		"xx;aws;S3;;;;;1;USD;\n"
	imp, err := ParseCostCSV(strings.NewReader(csvText), 3, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(imp.Rows) != 2 || len(imp.Errors) != 1 || imp.Rows[0].Cost != 1245.06 || imp.Currency["BRL"] != 7 {
		t.Fatalf("CSV de custos: %+v", imp)
	}
	if len(imp.Usage) != 1 || imp.Usage[0].Category != "net_nat" {
		t.Fatalf("uso: %+v", imp.Usage)
	}
	var proj float64
	for _, a := range imp.Alloc {
		if a.Dim == "project" && a.Value == "portal" {
			proj += a.Cost
		}
	}
	if proj != 1234.56 {
		t.Fatalf("alocação: %v", proj)
	}
	for in, want := range map[string]float64{"1,234.56": 1234.56, "1.234,56": 1234.56, "1234.5": 1234.5, "US$ 3": 3, "1.000.000": 1000000, "-2,5": -2.5} {
		if v, err := ParseNumber(in); err != nil || v != want {
			t.Errorf("ParseNumber(%q) = %v, %v", in, v, err)
		}
	}
	inv := "provider,region,resource_id,type,sku,size_gb,attached,cpu_avg,tags\n" +
		"aws,us-east-1,vol-9,ebs,gp2,50,não,,Project=x;Owner=y\n" +
		"aws,us-east-1,i-9,vm,m5.large,,,3.5,Environment=dev\n" +
		"aws,us-east-1,z,banana,,,,,\n"
	res, errs, err := ParseInventoryCSV(strings.NewReader(inv), 3, "")
	if err != nil || len(res) != 2 || len(errs) != 1 || res[0].Type != "volume" || *res[0].Attached || res[0].Tags["Owner"] != "y" || *res[1].CPUAvg != 3.5 {
		t.Fatalf("inventário CSV: %+v %v %v", res, errs, err)
	}
	pr, perrs, err := ParsePriceCSV(strings.NewReader("provider,region,sku,unit,price,currency\naws,us-east-1,instance:m5.large,hour,0.096,USD\naws,,x,dia,1,USD\n"))
	if err != nil || len(pr) != 1 || len(perrs) != 1 || pr[0].Region != "us-east-1" {
		t.Fatalf("preços CSV: %+v %v", pr, perrs)
	}
}

func TestScenariosAndReport(t *testing.T) {
	d, dd := demoData()
	st := DefaultSettings()
	r := CompareScenario(dd.Scenarios[0], NewPrices(dd.Prices), st)
	// 7 nós × 0.096 × 730 + 7 × 100 GB × 0.08
	if r.Delta != round2(7*0.096*730+7*100*0.08) || r.A.Missing != 0 {
		t.Fatalf("cenário: %+v", r)
	}
	regs := CompareRegions(dd.Scenarios[0].A, []string{"us-east-1", "eu-west-1"}, NewPrices(dd.Prices), st)
	if len(regs) != 2 {
		t.Fatal(regs)
	}
	w := WhatIfSpend(d, st, testNow, map[string]float64{"Amazon Elastic Compute Cloud - Compute": -30}, 10)
	if w.Base <= 0 || w.Adjusted == w.Base {
		t.Fatalf("what-if: %+v", w)
	}
	bs := EvaluateBudgets(dd.Budgets, d, st, testNow)
	fs := EvaluateResources(RuleInput{Resources: dd.Resources, Prices: NewPrices(dd.Prices), Settings: st, Now: testNow})
	an := DetectAnomalies(d, st, testNow, 30)
	rep, err := BuildReport(ReportRequest{Month: "2026-09", GroupBy: "team", Mode: "chargeback", Distribute: true}, d, st, testNow, bs, fs, an)
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, l := range rep.Lines {
		sum += l.Total
		if l.Key == "(sem equipe)" {
			t.Fatal("custo sem equipe deveria ser rateado")
		}
	}
	if diff := sum - rep.Total; diff > 1 || diff < -1 {
		t.Fatalf("chargeback %.2f ≠ total %.2f", sum, rep.Total)
	}
	if !strings.Contains(rep.Markdown, "Chargeback por equipe") || !strings.Contains(rep.CSV, "custo_direto") || rep.Savings <= 0 {
		t.Fatalf("relatório:\n%s", rep.Markdown)
	}
	if _, err := BuildReport(ReportRequest{GroupBy: "x"}, d, st, testNow, nil, nil, nil); err == nil {
		t.Fatal("agrupamento inválido aceito")
	}
}

func TestHCLParser(t *testing.T) {
	body := parseHCL("x.tf", `
a = "x # não é comentário"
b = [1, 2,
  3]
c = {
  k = "v"
  "k2": 2
}
d = <<-EOT
  texto { com chaves
  EOT
e = var.y ? "sim" : "nao"
blk "l1" "l2" { inner = 5 }
`)
	sc := &hclScope{vars: map[string]hclVal{"y": {Kind: "bool", B: true}}, locals: map[string]string{}}
	if s, _ := sc.eval(body.Attrs["a"].Expr).Str(); s != "x # não é comentário" {
		t.Fatalf("a=%q", s)
	}
	if v := sc.eval(body.Attrs["b"].Expr); v.Kind != "list" || len(v.L) != 3 {
		t.Fatalf("b=%+v", v)
	}
	if m := sc.eval(body.Attrs["c"].Expr).StringMap(); m["k"] != "v" || m["k2"] != "2" {
		t.Fatalf("c=%v", m)
	}
	if s, _ := sc.eval(body.Attrs["e"].Expr).Str(); s != "sim" {
		t.Fatalf("e=%q", s)
	}
	if len(body.Blocks) != 1 || body.Blocks[0].Labels[1] != "l2" || body.Blocks[0].Body.Attrs["inner"].Expr != "5" {
		t.Fatalf("bloco: %+v", body.Blocks)
	}
}

func TestSettingsNormalize(t *testing.T) {
	s := Settings{BaseCurrency: "brl", FXRates: map[string]float64{"USD": 5.4, "X": 1, "EUR": -1}, AnomalyWindow: 3, WebhookURL: "ftp://x",
		TeamRules: []TeamRule{{Dim: "PROJECT", Pattern: "a*", Team: "A"}, {Dim: "foo", Pattern: "x", Team: "y"}}}
	s.Normalize()
	if s.BaseCurrency != "BRL" || len(s.FXRates) != 1 || s.AnomalyWindow != 14 || s.WebhookURL != "" || len(s.TeamRules) != 1 || s.TeamFor("abc", "") != "A" {
		t.Fatalf("%+v", s)
	}
	b, _ := json.Marshal(DefaultSettings())
	var back Settings
	json.Unmarshal(b, &back)
	back.Normalize()
	if back.IaCMaxIncreasePct != 10 || back.SpotDiscount != 0.5 {
		t.Fatal(back)
	}
}
