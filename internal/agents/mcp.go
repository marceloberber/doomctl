package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Cliente MCP (Model Context Protocol) no transporte "Streamable HTTP":
// initialize → notifications/initialized → tools/list → tools/call.

const mcpProtocolVersion = "2025-06-18"

type MCPConfig struct {
	ID      int64
	Name    string
	URL     string
	Auth    AuthConfig
	Headers map[string]string
	Timeout time.Duration
}

// MCPTool é uma ferramenta anunciada pelo servidor.
type MCPTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	ReadOnly    bool           `json:"read_only"`
	Destructive bool           `json:"destructive"`
}

type MCPServerInfo struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocol_version"`
	Instructions    string `json:"instructions,omitempty"`
}

// MCPClient: o estado da sessão é protegido por mu; as chamadas HTTP acontecem fora do
// lock (várias ferramentas podem rodar em paralelo no mesmo servidor).
type MCPClient struct {
	cfg      MCPConfig
	auth     *authorizer
	http     *http.Client
	mu       sync.Mutex
	session  string
	protocol string
	ready    bool
	nextID   atomic.Int64
	info     MCPServerInfo
}

type mcpSess struct{ id, protocol string }

func NewMCPClient(cfg MCPConfig) *MCPClient {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &MCPClient{cfg: cfg, auth: newAuthorizer(cfg.Auth, ""), http: &http.Client{Timeout: cfg.Timeout}}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     any             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	Method string          `json:"method"`
}

func (c *MCPClient) post(ctx context.Context, payload map[string]any, sess mcpSess) (*http.Response, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
	if sess.id != "" {
		req.Header.Set("Mcp-Session-Id", sess.id)
	}
	if sess.protocol != "" {
		req.Header.Set("MCP-Protocol-Version", sess.protocol)
	}
	if err := c.auth.apply(ctx, req); err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// call envia uma requisição JSON-RPC com a sessão informada e devolve o "result" e o
// Mcp-Session-Id devolvido pelo servidor (aceita resposta JSON ou SSE).
func (c *MCPClient) call(ctx context.Context, method string, params any, sess mcpSess) (json.RawMessage, string, error) {
	id := c.nextID.Add(1)
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	resp, err := c.post(ctx, payload, sess)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	newSess := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode == http.StatusNotFound && sess.id != "" && method != "initialize" {
		return nil, newSess, errSessionExpired
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newSess, readErr(resp, "MCP")
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	match := func(r rpcResponse) bool { return r.Method == "" && fmt.Sprint(r.ID) == fmt.Sprint(id) }
	if strings.Contains(ct, "text/event-stream") {
		var out *rpcResponse
		err := sseLines(resp.Body, func(_, data string) (bool, error) {
			var r rpcResponse
			if json.Unmarshal([]byte(data), &r) != nil {
				return false, nil
			}
			if match(r) {
				out = &r
				return true, nil
			}
			return false, nil // notificações/requisições do servidor são ignoradas
		})
		if err != nil {
			return nil, newSess, err
		}
		if out == nil {
			return nil, newSess, errors.New("MCP: resposta não recebida no stream")
		}
		if out.Error != nil {
			return nil, newSess, fmt.Errorf("MCP %s: %s (%d)", method, out.Error.Message, out.Error.Code)
		}
		return out.Result, newSess, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, newSess, err
	}
	var r rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(b), &r); err != nil {
		var batch []rpcResponse
		if json.Unmarshal(b, &batch) != nil {
			return nil, newSess, fmt.Errorf("MCP: resposta inválida: %s", truncateStr(string(b), 200))
		}
		for _, x := range batch {
			if match(x) {
				r = x
			}
		}
	}
	if r.Error != nil {
		return nil, newSess, fmt.Errorf("MCP %s: %s (%d)", method, r.Error.Message, r.Error.Code)
	}
	return r.Result, newSess, nil
}

var errSessionExpired = errors.New("sessão MCP expirada")

func (c *MCPClient) notify(ctx context.Context, method string, sess mcpSess) {
	resp, err := c.post(ctx, map[string]any{"jsonrpc": "2.0", "method": method}, sess)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// initLocked abre uma sessão nova (chamado com mu travado).
func (c *MCPClient) initLocked(ctx context.Context) error {
	c.session, c.protocol, c.ready = "", "", false
	res, sid, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "doomctl", "version": "1"},
	}, mcpSess{})
	if err != nil {
		return err
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	json.Unmarshal(res, &r)
	c.session = sid
	c.protocol = firstNonEmpty(r.ProtocolVersion, mcpProtocolVersion)
	c.info = MCPServerInfo{Name: r.ServerInfo.Name, Version: r.ServerInfo.Version, ProtocolVersion: c.protocol, Instructions: truncateStr(r.Instructions, 2000)}
	c.notify(ctx, "notifications/initialized", mcpSess{c.session, c.protocol})
	c.ready = true
	return nil
}

// session garante uma sessão aberta e devolve uma cópia. Se stale for a sessão que expirou,
// abre outra (a menos que outra goroutine já tenha feito isso).
func (c *MCPClient) sessionFor(ctx context.Context, stale *mcpSess) (mcpSess, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready || (stale != nil && c.session == stale.id) {
		if err := c.initLocked(ctx); err != nil {
			return mcpSess{}, err
		}
	}
	return mcpSess{c.session, c.protocol}, nil
}

// rpc chama o método fora do lock e repete uma vez se a sessão expirar.
func (c *MCPClient) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	sess, err := c.sessionFor(ctx, nil)
	if err != nil {
		return nil, err
	}
	res, _, err := c.call(ctx, method, params, sess)
	if errors.Is(err, errSessionExpired) {
		if sess, err = c.sessionFor(ctx, &sess); err != nil {
			return nil, err
		}
		res, _, err = c.call(ctx, method, params, sess)
	}
	return res, err
}

// Initialize conecta e devolve as informações do servidor.
func (c *MCPClient) Initialize(ctx context.Context) (MCPServerInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.initLocked(ctx); err != nil {
		return MCPServerInfo{}, err
	}
	return c.info, nil
}

// ListTools lista as ferramentas (com paginação).
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	var out []MCPTool
	cursor := ""
	for i := 0; i < 50; i++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		res, err := c.rpc(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var r struct {
			Tools []struct {
				Name        string         `json:"name"`
				Title       string         `json:"title"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
				Annotations struct {
					ReadOnlyHint    *bool `json:"readOnlyHint"`
					DestructiveHint *bool `json:"destructiveHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &r); err != nil {
			return nil, err
		}
		for _, t := range r.Tools {
			mt := MCPTool{Name: t.Name, Title: t.Title, Description: truncateStr(t.Description, 1024), InputSchema: t.InputSchema}
			if t.Annotations.ReadOnlyHint != nil {
				mt.ReadOnly = *t.Annotations.ReadOnlyHint
			}
			// pela especificação, destructiveHint vale true por padrão quando a ferramenta não é somente leitura
			mt.Destructive = !mt.ReadOnly && (t.Annotations.DestructiveHint == nil || *t.Annotations.DestructiveHint)
			out = append(out, mt)
		}
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
	}
	if out == nil {
		out = []MCPTool{}
	}
	return out, nil
}

// CallTool executa a ferramenta e devolve o texto do resultado.
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) (string, bool, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", true, err
	}
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
			URI string `json:"uri"`
		} `json:"content"`
		StructuredContent any  `json:"structuredContent"`
		IsError           bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", true, err
	}
	var b strings.Builder
	for _, ct := range r.Content {
		switch ct.Type {
		case "text":
			b.WriteString(ct.Text)
		case "resource":
			b.WriteString(firstNonEmpty(ct.Resource.Text, ct.Resource.URI))
		case "resource_link":
			b.WriteString(ct.URI)
		default:
			b.WriteString("[conteúdo " + ct.Type + " " + ct.MimeType + " omitido]")
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 && r.StructuredContent != nil {
		j, _ := json.Marshal(r.StructuredContent)
		b.Write(j)
	}
	return strings.TrimSpace(b.String()), r.IsError, nil
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
