package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func fixed(r Route) Classifier {
	return func(context.Context, string) (Route, error) { return r, nil }
}

func failing(context.Context, string) (Route, error) { return "", errors.New("offline") }

func TestDecide(t *testing.T) {
	cases := []struct {
		name    string
		q       string
		cls     Classifier
		want    Route
		wantLLM bool
	}{
		{"terraform ec2", "Como crio um módulo Terraform para EC2 com IMDSv2?", failing, RouteCloud, false},
		{"subnet cidr", "Divida 10.0.0.0/16 em 6 sub-redes /19 e me dê os ranges", failing, RouteNetSec, false},
		{"mascara sozinha", "como calculo uma /27?", failing, RouteNetSec, false},
		{"crashloop", "Meu pod está em CrashLoopBackOff", failing, RouteCloud, false},
		{"scan imagem", "Como uso o Trivy no pipeline e bloqueio CVE crítica?", failing, RouteNetSec, false},
		{"finops nat", "Reduzir custo de NAT Gateway na AWS com FinOps", failing, RouteCloud, false},
		{"oci block", "Como expandir um Block Volume na OCI sem parar a VM?", failing, RouteCloud, false},
		{"futebol via llm", "Qual o melhor time do Brasileirão?", fixed(RouteOffTopic), RouteOffTopic, true},
		{"futebol llm offline falha fechada", "Qual o melhor time do Brasileirão?", failing, RouteOffTopic, true},
		{"poema kubernetes", "Me escreva um poema sobre Kubernetes", fixed(RouteOffTopic), RouteOffTopic, true},
		{"poema llm offline", "Me escreva um poema sobre Kubernetes", failing, RouteOffTopic, true},
		{"sem keyword mas tecnico", "Como faço rollback de uma release?", fixed(RouteCloud), RouteCloud, true},
		{"vazio", "   ", failing, RouteOffTopic, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(context.Background(), c.q, c.cls)
			if d.Route != c.want || d.UsedLLM != c.wantLLM {
				t.Fatalf("got route=%s llm=%v (%+v), want route=%s llm=%v", d.Route, d.UsedLLM, d.Scores, c.want, c.wantLLM)
			}
		})
	}
}

func TestTemperatureRange(t *testing.T) {
	for _, v := range []float64{0.2, 0.25, 0.3} {
		if err := ValidateTemperature(v); err != nil {
			t.Errorf("%.2f deveria ser aceita: %v", v, err)
		}
	}
	for _, v := range []float64{0.0, 0.19, 0.31, 0.7} {
		if err := ValidateTemperature(v); err == nil {
			t.Errorf("%.2f deveria ser rejeitada", v)
		}
	}
}

func TestParseClassifierOutput(t *testing.T) {
	if r, err := parseClassifierOutput(`{"rota":"redes_seguranca"}`); err != nil || r != RouteNetSec {
		t.Fatalf("got %s %v", r, err)
	}
	if _, err := parseClassifierOutput(`{"rota":"culinaria"}`); err == nil {
		t.Fatal("rota desconhecida deveria falhar")
	}
	if _, err := parseClassifierOutput(`não é json`); err == nil {
		t.Fatal("texto livre deveria falhar")
	}
}

// Servidor Ollama falso: valida temperaturas e conta chamadas.
func fakeOllama(t *testing.T, classifierRoute Route, calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("body invalido: %v", err)
			return
		}
		temp, _ := req.Options["temperature"].(float64)
		if !req.Stream { // classificador
			if temp != 0 {
				t.Errorf("classificador deveria usar temperatura 0, usou %v", temp)
			}
			json.NewEncoder(w).Encode(chatChunk{Message: Message{Role: "assistant", Content: `{"rota":"` + string(classifierRoute) + `"}`}, Done: true})
			return
		}
		if temp < 0.2 || temp > 0.3 {
			t.Errorf("persona com temperatura fora da faixa: %v", temp)
		}
		if !strings.Contains(req.Messages[0].Content, "Não posso responder a este tipo de pergunta") {
			t.Error("system prompt sem a regra de recusa")
		}
		enc := json.NewEncoder(w)
		enc.Encode(chatChunk{Message: Message{Role: "assistant", Content: "**Resumo:** "}})
		enc.Encode(chatChunk{Message: Message{Role: "assistant", Content: "ok"}, Done: true})
	}))
}

func newTestAssistant(url string) *Assistant {
	cfg := Config{Host: url, Model: "qwen3.8", Temperature: 0.25}
	return NewAssistant(NewOllama(cfg))
}

func cliHeader(w *bytes.Buffer) func(Decision) {
	return func(d Decision) {
		if d.Route != RouteOffTopic {
			w.WriteString("[" + PersonaName(d.Route) + "]\n")
		}
	}
}

func TestSessionOffTopicDoesNotCallPersona(t *testing.T) {
	var calls int32
	srv := fakeOllama(t, RouteOffTopic, &calls)
	defer srv.Close()

	var out bytes.Buffer
	if _, _, err := newTestAssistant(srv.URL).Ask(context.Background(), "u1", "Qual a capital da França?", &out, cliHeader(&out)); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != RefusalMsg {
		t.Fatalf("saida = %q, want %q", got, RefusalMsg)
	}
	if calls != 1 { // só o classificador
		t.Fatalf("chamadas ao ollama = %d, want 1", calls)
	}
}

func TestSessionPersonaStreaming(t *testing.T) {
	var calls int32
	srv := fakeOllama(t, RouteCloud, &calls)
	defer srv.Close()

	a := newTestAssistant(srv.URL)
	var out bytes.Buffer
	if _, _, err := a.Ask(context.Background(), "u1", "Como versiono o state do Terraform no S3?", &out, cliHeader(&out)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "[Atlas]") || !strings.Contains(out.String(), "**Resumo:** ok") {
		t.Fatalf("saida inesperada: %q", out.String())
	}
	if calls != 1 { // heurística decidiu; só a persona foi chamada
		t.Fatalf("chamadas = %d, want 1", calls)
	}
	if len(a.history["u1"][RouteCloud]) != 2 {
		t.Fatalf("historico = %d, want 2", len(a.history["u1"][RouteCloud]))
	}
	if len(a.history["u2"]) != 0 {
		t.Fatal("historico vazou entre usuarios")
	}
}
