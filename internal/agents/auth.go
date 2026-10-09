package agents

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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AuthConfig descreve como autenticar numa API de modelo ou servidor MCP.
type AuthConfig struct {
	Type       string `json:"type"`        // none | bearer | api-key | header | azure_entra | oauth2 | google_sa
	HeaderName string `json:"header_name"` // type=header
	TenantID   string `json:"tenant_id"`   // azure_entra
	ClientID   string `json:"client_id"`   // azure_entra | oauth2
	Scope      string `json:"scope"`       // azure_entra | oauth2 | google_sa
	TokenURL   string `json:"token_url"`   // oauth2
	Project    string `json:"project"`     // google_sa: projeto de cota (x-goog-user-project)
	Secret     string `json:"-"`           // chave, token, client secret ou JSON da service account
}

var AuthTypes = map[string]string{
	"none":        "Sem autenticação",
	"bearer":      "Bearer token (Authorization)",
	"api-key":     "Cabeçalho api-key (Azure)",
	"header":      "Cabeçalho personalizado",
	"azure_entra": "Microsoft Entra ID (service principal)",
	"oauth2":      "OAuth 2.0 client credentials",
	"google_sa":   "Google service account (JSON)",
}

// entraAuthority é o host de login do Microsoft Entra ID (substituível em testes e nuvens soberanas).
var entraAuthority = "https://login.microsoftonline.com/"

// authorizer aplica a autenticação e mantém tokens em cache.
type authorizer struct {
	cfg          AuthConfig
	defaultScope string
	http         *http.Client
	mu           sync.Mutex
	token        string
	exp          time.Time
}

func newAuthorizer(cfg AuthConfig, defaultScope string) *authorizer {
	return &authorizer{cfg: cfg, defaultScope: defaultScope, http: &http.Client{Timeout: 30 * time.Second}}
}

func (a *authorizer) apply(ctx context.Context, req *http.Request) error {
	c := a.cfg
	switch c.Type {
	case "", "none":
		return nil
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	case "api-key":
		req.Header.Set("api-key", c.Secret)
	case "header":
		if c.HeaderName == "" {
			return errors.New("informe o nome do cabeçalho de autenticação")
		}
		req.Header.Set(c.HeaderName, c.Secret)
	case "azure_entra", "oauth2", "google_sa":
		tok, err := a.accessToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if c.Type == "google_sa" && c.Project != "" {
			req.Header.Set("x-goog-user-project", c.Project)
		}
	default:
		return fmt.Errorf("tipo de autenticação desconhecido: %q", c.Type)
	}
	return nil
}

func (a *authorizer) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Until(a.exp) > time.Minute {
		return a.token, nil
	}
	c := a.cfg
	scope := c.Scope
	if scope == "" {
		scope = a.defaultScope
	}
	form := url.Values{}
	var tokenURL string
	switch c.Type {
	case "azure_entra":
		if c.TenantID == "" || c.ClientID == "" || c.Secret == "" {
			return "", errors.New("informe tenant, client ID e client secret do service principal")
		}
		tokenURL = entraAuthority + url.PathEscape(c.TenantID) + "/oauth2/v2.0/token"
		form.Set("grant_type", "client_credentials")
		form.Set("client_id", c.ClientID)
		form.Set("client_secret", c.Secret)
		form.Set("scope", scope)
	case "oauth2":
		if c.TokenURL == "" || c.ClientID == "" {
			return "", errors.New("informe a URL de token e o client ID")
		}
		tokenURL = c.TokenURL
		form.Set("grant_type", "client_credentials")
		form.Set("client_id", c.ClientID)
		form.Set("client_secret", c.Secret)
		if scope != "" {
			form.Set("scope", scope)
		}
	case "google_sa":
		assertion, uri, err := googleAssertion(c.Secret, scope)
		if err != nil {
			return "", err
		}
		tokenURL = uri
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
		form.Set("assertion", assertion)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("obtendo token: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   any    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	json.Unmarshal(b, &tr)
	if resp.StatusCode != 200 || tr.AccessToken == "" {
		msg := tr.Description
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("token recusado (HTTP %d): %s", resp.StatusCode, msg)
	}
	secs := 3600.0
	switch v := tr.ExpiresIn.(type) {
	case float64:
		secs = v
	case string:
		fmt.Sscan(v, &secs)
	}
	a.token, a.exp = tr.AccessToken, time.Now().Add(time.Duration(secs)*time.Second)
	return a.token, nil
}

// googleAssertion monta o JWT assinado (RS256) do fluxo de service account.
func googleAssertion(keyJSON, scope string) (string, string, error) {
	var k struct {
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
		TokenURI     string `json:"token_uri"`
	}
	if err := json.Unmarshal([]byte(keyJSON), &k); err != nil || k.ClientEmail == "" || k.PrivateKey == "" {
		return "", "", errors.New("JSON da service account inválido (client_email/private_key)")
	}
	if k.TokenURI == "" {
		k.TokenURI = "https://oauth2.googleapis.com/token"
	}
	if scope == "" {
		scope = "https://www.googleapis.com/auth/cloud-platform"
	}
	blk, _ := pem.Decode([]byte(k.PrivateKey))
	if blk == nil {
		return "", "", errors.New("chave privada da service account inválida")
	}
	pk, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		if k1, err1 := x509.ParsePKCS1PrivateKey(blk.Bytes); err1 == nil {
			pk = k1
		} else {
			return "", "", errors.New("chave privada da service account inválida: " + err.Error())
		}
	}
	rk, ok := pk.(*rsa.PrivateKey)
	if !ok {
		return "", "", errors.New("a service account precisa de chave RSA")
	}
	now := time.Now().Unix()
	enc := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.PrivateKeyID})
	claims, _ := json.Marshal(map[string]any{"iss": k.ClientEmail, "scope": scope, "aud": k.TokenURI, "iat": now, "exp": now + 3600})
	unsigned := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rk, crypto.SHA256, sum[:])
	if err != nil {
		return "", "", err
	}
	return unsigned + "." + enc.EncodeToString(sig), k.TokenURI, nil
}
