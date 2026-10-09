package finops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- AWS Signature Version 4 (somente stdlib) ----------

type AWSCreds struct {
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token,omitempty"`
}

func (c AWSCreds) Valid() bool { return c.AccessKey != "" && c.SecretKey != "" }

func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// awsEscape aplica o URI-encoding do SigV4 (RFC 3986: só A-Z a-z 0-9 - _ . ~ ficam literais).
func awsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func canonicalPath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = awsEscape(s)
	}
	return strings.Join(segs, "/")
}

// SignV4 assina a requisição (cabeçalho Authorization) conforme o SigV4.
func SignV4(req *http.Request, body []byte, c AWSCreds, region, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	date := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{"host": host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			headers[lk] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
		}
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	creq := strings.Join([]string{req.Method, canonicalPath(req.URL.EscapedPath()), canonicalQuery(req.URL.Query()),
		ch.String(), signed, sha256Hex(body)}, "\n")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(creq))
	k := hmacSHA256([]byte("AWS4"+c.SecretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKey, scope, signed, sig))
}

// ---------- cliente AWS ----------

type AWSClient struct {
	Creds AWSCreds
	HTTP  *http.Client
	// Endpoints opcionais (LocalStack, proxies, testes). Vazio = endpoint público da AWS.
	// "{region}" é substituído pela região consultada.
	CEEndpoint         string // https://ce.us-east-1.amazonaws.com
	EC2Endpoint        string // https://ec2.{region}.amazonaws.com
	RDSEndpoint        string // https://rds.{region}.amazonaws.com
	CloudWatchEndpoint string // https://monitoring.{region}.amazonaws.com
	PricingEndpoint    string // https://api.pricing.us-east-1.amazonaws.com
	Now                func() time.Time
}

func (a *AWSClient) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *AWSClient) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

type awsError struct {
	Type    string `json:"__type"`
	Message string `json:"message"`
	Msg2    string `json:"Message"`
}

// jsonCall faz chamadas no protocolo JSON 1.1 (Cost Explorer, Pricing).
func (a *AWSClient) jsonCall(ctx context.Context, endpoint, region, service, target string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	SignV4(req, body, a.Creds, region, service, a.now())
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != 200 {
		var e awsError
		json.Unmarshal(b, &e)
		msg := e.Message
		if msg == "" {
			msg = e.Msg2
		}
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		t := e.Type
		if i := strings.LastIndex(t, "#"); i >= 0 {
			t = t[i+1:]
		}
		return fmt.Errorf("AWS %s HTTP %d %s: %s", service, resp.StatusCode, t, truncate(msg, 300))
	}
	return json.Unmarshal(b, out)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// GetCostAndUsage consulta o Cost Explorer (diário, UnblendedCost) agrupando por duas chaves.
// groupBy: ex. [{"DIMENSION","SERVICE"},{"DIMENSION","REGION"}] ou [{"TAG","Project"}].
// GetCostAndUsage consulta o custo diário (UnblendedCost) agrupado por até duas
// dimensões/tags. filter é opcional (expressão do Cost Explorer). Cada página custa
// US$ 0,01 na AWS.
func (a *AWSClient) GetCostAndUsage(ctx context.Context, start, end time.Time, groupBy [][2]string, filter map[string]any) ([]AWSCostGroup, error) {
	ep := a.CEEndpoint
	if ep == "" {
		ep = "https://ce.us-east-1.amazonaws.com"
	}
	var gb []map[string]string
	for _, g := range groupBy {
		gb = append(gb, map[string]string{"Type": g[0], "Key": g[1]})
	}
	var out []AWSCostGroup
	token := ""
	for page := 0; page < 200; page++ {
		in := map[string]any{
			"TimePeriod":  map[string]string{"Start": start.Format("2006-01-02"), "End": end.Format("2006-01-02")},
			"Granularity": "DAILY",
			"Metrics":     []string{"UnblendedCost"},
			"GroupBy":     gb,
		}
		if token != "" {
			in["NextPageToken"] = token
		}
		if filter != nil {
			in["Filter"] = filter
		}
		var resp struct {
			ResultsByTime []struct {
				TimePeriod struct{ Start string }
				Groups     []struct {
					Keys    []string
					Metrics map[string]struct{ Amount, Unit string }
				}
			}
			NextPageToken string
		}
		if err := a.jsonCall(ctx, ep, "us-east-1", "ce", "AWSInsightsIndexService.GetCostAndUsage", in, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.ResultsByTime {
			day, err := time.Parse("2006-01-02", r.TimePeriod.Start)
			if err != nil {
				continue
			}
			for _, g := range r.Groups {
				m := g.Metrics["UnblendedCost"]
				amt, _ := strconv.ParseFloat(m.Amount, 64)
				out = append(out, AWSCostGroup{Day: day, Keys: g.Keys, Amount: amt, Unit: m.Unit})
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return out, nil
}

type AWSCostGroup struct {
	Day    time.Time
	Keys   []string
	Amount float64
	Unit   string
}

// TagValue extrai o valor de uma chave do Cost Explorer no formato "Project$valor".
func TagValue(k string) string {
	if i := strings.Index(k, "$"); i >= 0 {
		return k[i+1:]
	}
	return k
}

// GetRightsizingRecommendation devolve as recomendações de rightsizing de EC2 (resposta bruta).
func (a *AWSClient) GetRightsizingRecommendation(ctx context.Context) ([]map[string]any, error) {
	ep := a.CEEndpoint
	if ep == "" {
		ep = "https://ce.us-east-1.amazonaws.com"
	}
	var out []map[string]any
	token := ""
	for page := 0; page < 50; page++ {
		in := map[string]any{"Service": "AmazonEC2",
			"Configuration": map[string]any{"RecommendationTarget": "SAME_INSTANCE_FAMILY", "BenefitsConsidered": true}}
		if token != "" {
			in["NextPageToken"] = token
		}
		var resp struct {
			RightsizingRecommendations []map[string]any
			NextPageToken              string
		}
		if err := a.jsonCall(ctx, ep, "us-east-1", "ce", "AWSInsightsIndexService.GetRightsizingRecommendation", in, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.RightsizingRecommendations...)
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return out, nil
}

// ---------- EC2 (API Query/XML) ----------

func (a *AWSClient) ec2Call(ctx context.Context, region, action string, params url.Values, out any) error {
	ep := a.EC2Endpoint
	if ep == "" {
		ep = "https://ec2.{region}.amazonaws.com"
	}
	return a.queryCall(ctx, "ec2", ep, region, "2016-11-15", action, params, out)
}

// QueryError é um erro devolvido pelas APIs Query (EC2, RDS, CloudWatch).
type QueryError struct {
	Service, Action, Region, Code, Message string
	Status                                 int
}

func (e *QueryError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("%s %s (%s): HTTP %d", strings.ToUpper(e.Service), e.Action, e.Region, e.Status)
	}
	return fmt.Sprintf("%s %s (%s): %s: %s", strings.ToUpper(e.Service), e.Action, e.Region, e.Code, e.Message)
}

// queryCall chama uma API do protocolo Query da AWS (GET assinado, resposta XML).
func (a *AWSClient) queryCall(ctx context.Context, service, epTemplate, region, version, action string, params url.Values, out any) error {
	ep := strings.ReplaceAll(epTemplate, "{region}", region)
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", version)
	u := strings.TrimRight(ep, "/") + "/?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	SignV4(req, nil, a.Creds, region, service, a.now())
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Errors []struct{ Code, Message string } `xml:"Errors>Error"` // EC2
			Error  struct{ Code, Message string }   `xml:"Error"`        // RDS / CloudWatch
		}
		xml.Unmarshal(b, &e)
		qe := &QueryError{Service: service, Action: action, Region: region, Status: resp.StatusCode}
		if len(e.Errors) > 0 {
			qe.Code, qe.Message = e.Errors[0].Code, e.Errors[0].Message
		} else {
			qe.Code, qe.Message = e.Error.Code, e.Error.Message
		}
		return qe
	}
	if out == nil {
		return nil
	}
	return xml.Unmarshal(b, out)
}

type ec2Tag struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}

func tagsMap(ts []ec2Tag) map[string]string {
	m := map[string]string{}
	for _, t := range ts {
		m[t.Key] = t.Value
	}
	return m
}

func ageDays(ts string, now time.Time) int {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	return int(now.Sub(t).Hours() / 24)
}

// ScanEC2 coleta volumes, IPs elásticos, snapshots próprios e instâncias da região.
func (a *AWSClient) ScanEC2(ctx context.Context, region string) ([]Resource, error) {
	now := a.now()
	var out []Resource
	// volumes
	token := ""
	for page := 0; page < 100; page++ {
		p := url.Values{"MaxResults": {"500"}}
		if token != "" {
			p.Set("NextToken", token)
		}
		var r struct {
			Items []struct {
				ID          string   `xml:"volumeId"`
				Size        int      `xml:"size"`
				Type        string   `xml:"volumeType"`
				Status      string   `xml:"status"`
				CreateTime  string   `xml:"createTime"`
				Attachments []string `xml:"attachmentSet>item>instanceId"`
				Tags        []ec2Tag `xml:"tagSet>item"`
			} `xml:"volumeSet>item"`
			NextToken string `xml:"nextToken"`
		}
		if err := a.ec2Call(ctx, region, "DescribeVolumes", p, &r); err != nil {
			return nil, err
		}
		for _, v := range r.Items {
			attached := len(v.Attachments) > 0
			out = append(out, Resource{Provider: "aws", Region: region, ResourceID: v.ID, Type: "volume", SKU: v.Type,
				SizeGB: float64(v.Size), State: v.Status, Attached: &attached, AgeDays: ageDays(v.CreateTime, now), Tags: tagsMap(v.Tags),
				Name: tagsMap(v.Tags)["Name"]})
		}
		if r.NextToken == "" {
			break
		}
		token = r.NextToken
	}
	// IPs elásticos
	var ad struct {
		Items []struct {
			PublicIP      string   `xml:"publicIp"`
			AllocationID  string   `xml:"allocationId"`
			AssociationID string   `xml:"associationId"`
			InstanceID    string   `xml:"instanceId"`
			Tags          []ec2Tag `xml:"tagSet>item"`
		} `xml:"addressesSet>item"`
	}
	if err := a.ec2Call(ctx, region, "DescribeAddresses", nil, &ad); err != nil {
		return nil, err
	}
	for _, e := range ad.Items {
		attached := e.AssociationID != "" || e.InstanceID != ""
		id := e.AllocationID
		if id == "" {
			id = e.PublicIP
		}
		out = append(out, Resource{Provider: "aws", Region: region, ResourceID: id, Type: "public_ip", Name: e.PublicIP,
			Attached: &attached, State: map[bool]string{true: "associated", false: "unassociated"}[attached], Tags: tagsMap(e.Tags)})
	}
	// snapshots próprios
	token = ""
	for page := 0; page < 100; page++ {
		p := url.Values{"Owner.1": {"self"}, "MaxResults": {"1000"}}
		if token != "" {
			p.Set("NextToken", token)
		}
		var r struct {
			Items []struct {
				ID          string   `xml:"snapshotId"`
				VolumeID    string   `xml:"volumeId"`
				Size        int      `xml:"volumeSize"`
				StartTime   string   `xml:"startTime"`
				Description string   `xml:"description"`
				State       string   `xml:"status"`
				Tags        []ec2Tag `xml:"tagSet>item"`
			} `xml:"snapshotSet>item"`
			NextToken string `xml:"nextToken"`
		}
		if err := a.ec2Call(ctx, region, "DescribeSnapshots", p, &r); err != nil {
			return nil, err
		}
		for _, s := range r.Items {
			out = append(out, Resource{Provider: "aws", Region: region, ResourceID: s.ID, Type: "snapshot", SKU: "snapshot",
				SizeGB: float64(s.Size), State: s.State, AgeDays: ageDays(s.StartTime, now), Tags: tagsMap(s.Tags),
				Name: firstNonEmpty(tagsMap(s.Tags)["Name"], truncate(s.Description, 60))})
		}
		if r.NextToken == "" {
			break
		}
		token = r.NextToken
	}
	// instâncias
	token = ""
	for page := 0; page < 100; page++ {
		p := url.Values{"MaxResults": {"1000"}}
		if token != "" {
			p.Set("NextToken", token)
		}
		var r struct {
			Reservations []struct {
				Instances []struct {
					ID         string   `xml:"instanceId"`
					Type       string   `xml:"instanceType"`
					State      string   `xml:"instanceState>name"`
					LaunchTime string   `xml:"launchTime"`
					Lifecycle  string   `xml:"instanceLifecycle"`
					Volumes    []string `xml:"blockDeviceMapping>item>ebs>volumeId"`
					Tags       []ec2Tag `xml:"tagSet>item"`
				} `xml:"instancesSet>item"`
			} `xml:"reservationSet>item"`
			NextToken string `xml:"nextToken"`
		}
		if err := a.ec2Call(ctx, region, "DescribeInstances", p, &r); err != nil {
			return nil, err
		}
		for _, rs := range r.Reservations {
			for _, i := range rs.Instances {
				if i.State == "terminated" {
					continue
				}
				tg := tagsMap(i.Tags)
				raw := map[string]any{"volumes": i.Volumes}
				if i.Lifecycle != "" {
					raw["lifecycle"] = i.Lifecycle
				}
				out = append(out, Resource{Provider: "aws", Region: region, ResourceID: i.ID, Type: "instance", SKU: i.Type,
					State: i.State, AgeDays: ageDays(i.LaunchTime, now), Tags: tg, Name: tg["Name"], Raw: raw})
			}
		}
		if r.NextToken == "" {
			break
		}
		token = r.NextToken
	}
	return out, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// ---------- Pricing API (preços públicos sob demanda) ----------

// PriceLookup descreve um preço a buscar no AWS Price List.
type PriceLookup struct {
	Kind  string `json:"kind"`  // ec2 | ebs
	Value string `json:"value"` // tipo de instância (t3.micro) ou tipo de volume (gp3)
}

// LookupPrice consulta o AWS Price List (GetProducts) e devolve preço USD e SKU interno.
func (a *AWSClient) LookupPrice(ctx context.Context, region string, l PriceLookup) (Price, error) {
	ep := a.PricingEndpoint
	if ep == "" {
		ep = "https://api.pricing.us-east-1.amazonaws.com"
	}
	f := func(field, value string) map[string]string {
		return map[string]string{"Type": "TERM_MATCH", "Field": field, "Value": value}
	}
	filters := []map[string]string{f("regionCode", region)}
	var sku, unit string
	switch l.Kind {
	case "ec2":
		filters = append(filters, f("instanceType", l.Value), f("operatingSystem", "Linux"), f("tenancy", "Shared"),
			f("preInstalledSw", "NA"), f("capacitystatus", "Used"))
		sku, unit = "instance:"+l.Value, "hour"
	case "ebs":
		filters = append(filters, f("productFamily", "Storage"), f("volumeApiName", l.Value))
		sku, unit = "volume:"+l.Value, "gb-month"
	default:
		return Price{}, fmt.Errorf("tipo de preço não suportado: %q", l.Kind)
	}
	in := map[string]any{"ServiceCode": "AmazonEC2", "Filters": filters, "FormatVersion": "aws_v1", "MaxResults": 20}
	var resp struct{ PriceList []string }
	if err := a.jsonCall(ctx, ep, "us-east-1", "pricing", "AWSPriceListService.GetProducts", in, &resp); err != nil {
		return Price{}, err
	}
	for _, raw := range resp.PriceList {
		if p, ok := parseOnDemandUSD(raw); ok {
			return Price{Provider: "aws", Region: region, SKU: sku, Unit: unit, Price: p, Currency: "USD", Source: "aws-pricing"}, nil
		}
	}
	return Price{}, errors.New("preço não encontrado no AWS Price List para " + l.Kind + " " + l.Value + " em " + region)
}

// parseOnDemandUSD extrai o primeiro pricePerUnit.USD > 0 dos termos OnDemand.
func parseOnDemandUSD(raw string) (float64, bool) {
	var doc struct {
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return 0, false
	}
	for _, t := range doc.Terms.OnDemand {
		for _, d := range t.PriceDimensions {
			if v, err := strconv.ParseFloat(d.PricePerUnit["USD"], 64); err == nil && v > 0 {
				return v, true
			}
		}
	}
	return 0, false
}
