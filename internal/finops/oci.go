package finops

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OCICreds são as credenciais de API key da OCI.
type OCICreds struct {
	Tenancy     string `json:"tenancy_ocid"`
	User        string `json:"user_ocid"`
	Fingerprint string `json:"fingerprint"`
	PrivateKey  string `json:"private_key"`
	Region      string `json:"region"` // home region (endpoint da Usage API)
}

func (c OCICreds) Valid() bool {
	return c.Tenancy != "" && c.User != "" && c.Fingerprint != "" && c.PrivateKey != "" && c.Region != ""
}

// ParseRSAKey lê chave privada PEM (PKCS#1 ou PKCS#8, sem senha).
func ParseRSAKey(pemText string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(strings.TrimSpace(pemText)))
	if blk == nil {
		return nil, errors.New("chave privada inválida (PEM esperado)")
	}
	if strings.Contains(blk.Type, "ENCRYPTED") || blk.Headers["Proc-Type"] != "" {
		return nil, errors.New("chave privada protegida por senha não é suportada; gere uma API key sem passphrase")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, errors.New("chave privada inválida: " + err.Error())
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("a OCI exige chave RSA")
	}
	return rk, nil
}

// OCISigningString monta a string de assinatura (draft-cavage-http-signatures, perfil OCI).
func OCISigningString(req *http.Request, headers []string) string {
	var lines []string
	for _, h := range headers {
		if h == "(request-target)" {
			target := req.URL.EscapedPath()
			if req.URL.RawQuery != "" {
				target += "?" + req.URL.RawQuery
			}
			lines = append(lines, "(request-target): "+strings.ToLower(req.Method)+" "+target)
			continue
		}
		v := req.Header.Get(h)
		if h == "host" {
			v = req.Host
			if v == "" {
				v = req.URL.Host
			}
		}
		lines = append(lines, h+": "+v)
	}
	return strings.Join(lines, "\n")
}

// SignOCI assina a requisição conforme a documentação "Request Signatures" da OCI.
func SignOCI(req *http.Request, body []byte, c OCICreds, key *rsa.PrivateKey, now time.Time) error {
	req.Header.Set("Date", now.UTC().Format(http.TimeFormat))
	headers := []string{"(request-target)", "date", "host"}
	if req.Method == http.MethodPost || req.Method == http.MethodPut || req.Method == http.MethodPatch {
		sum := sha256.Sum256(body)
		req.Header.Set("X-Content-Sha256", base64.StdEncoding.EncodeToString(sum[:]))
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Content-Length", strconv.Itoa(len(body)))
		req.ContentLength = int64(len(body))
		headers = append(headers, "x-content-sha256", "content-type", "content-length")
	}
	ss := OCISigningString(req, headers)
	h := sha256.Sum256([]byte(ss))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf(`Signature version="1",keyId="%s/%s/%s",algorithm="rsa-sha256",headers="%s",signature="%s"`,
		c.Tenancy, c.User, c.Fingerprint, strings.Join(headers, " "), base64.StdEncoding.EncodeToString(sig)))
	return nil
}

type OCIClient struct {
	Creds    OCICreds
	HTTP     *http.Client
	Endpoint string // vazio = https://usageapi.{region}.oci.oraclecloud.com
	Now      func() time.Time
	key      *rsa.PrivateKey
}

func (o *OCIClient) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// OCIUsageItem é um item da resposta de /usage.
type OCIUsageItem struct {
	Day         time.Time
	Service     string
	Region      string
	Compartment string
	SKUName     string
	Amount      float64
	Currency    string
	Quantity    float64
	Unit        string
	Tags        map[string]string // "namespace.key" → valor
}

// RequestSummarizedUsages consulta a Usage API (custo diário) com os agrupamentos informados.
func (o *OCIClient) RequestSummarizedUsages(ctx context.Context, start, end time.Time, groupBy []string, groupByTag []map[string]string) ([]OCIUsageItem, error) {
	if o.key == nil {
		k, err := ParseRSAKey(o.Creds.PrivateKey)
		if err != nil {
			return nil, err
		}
		o.key = k
	}
	ep := o.Endpoint
	if ep == "" {
		ep = "https://usageapi." + o.Creds.Region + ".oci.oraclecloud.com"
	}
	cl := o.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 90 * time.Second}
	}
	in := map[string]any{
		"tenantId":         o.Creds.Tenancy,
		"timeUsageStarted": start.UTC().Format("2006-01-02") + "T00:00:00Z",
		"timeUsageEnded":   end.UTC().Format("2006-01-02") + "T00:00:00Z",
		"granularity":      "DAILY",
		"queryType":        "COST",
		"groupBy":          groupBy,
	}
	if len(groupByTag) > 0 {
		in["groupByTag"] = groupByTag
	}
	body, _ := json.Marshal(in)
	var out []OCIUsageItem
	page := ""
	for i := 0; i < 200; i++ {
		u := strings.TrimRight(ep, "/") + "/20200107/usage"
		if page != "" {
			u += "?page=" + url.QueryEscape(page)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if err := SignOCI(req, body, o.Creds, o.key, o.now()); err != nil {
			return nil, err
		}
		resp, err := cl.Do(req)
		if err != nil {
			return nil, err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			var e struct{ Code, Message string }
			json.Unmarshal(b, &e)
			if e.Message == "" {
				e.Message = truncate(strings.TrimSpace(string(b)), 300)
			}
			return nil, fmt.Errorf("OCI Usage API HTTP %d %s: %s", resp.StatusCode, e.Code, e.Message)
		}
		var r struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		for _, it := range r.Items {
			out = append(out, parseOCIItem(it))
		}
		page = resp.Header.Get("opc-next-page")
		if page == "" {
			break
		}
	}
	return out, nil
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func num(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

func parseOCIItem(it map[string]any) OCIUsageItem {
	x := OCIUsageItem{Service: str(it, "service"), Region: str(it, "region"), Compartment: str(it, "compartmentName"), SKUName: str(it, "skuName"),
		Amount: num(it, "computedAmount"), Currency: str(it, "currency"), Quantity: num(it, "computedQuantity"), Unit: str(it, "unit"),
		Tags: map[string]string{}}
	ts := str(it, "timeUsageStarted")
	if len(ts) >= 10 {
		x.Day, _ = time.Parse("2006-01-02", ts[:10])
	}
	if tags, ok := it["tags"].([]any); ok {
		for _, t := range tags {
			if tm, ok := t.(map[string]any); ok {
				k := str(tm, "key")
				if ns := str(tm, "namespace"); ns != "" {
					k = ns + "." + k
				}
				x.Tags[k] = str(tm, "value")
			}
		}
	}
	if k := str(it, "tagKey"); k != "" {
		if ns := str(it, "tagNamespace"); ns != "" {
			k = ns + "." + k
		}
		x.Tags[k] = str(it, "tagValue")
	}
	return x
}
