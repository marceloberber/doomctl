// Package server implementa a API HTTP do doomctl e serve o frontend embutido.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doomctl/doomctl/internal/agents"
	"github.com/doomctl/doomctl/internal/config"
	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/rbac"
	"github.com/doomctl/doomctl/internal/secure"
)

type Server struct {
	cfg  *config.Config
	db   *sql.DB
	box  *secure.Box
	jobs *jobs.Manager
	ai   *agents.Assistant
	// avisos do último carregamento da configuração de IA
	aiWarnings atomic.Pointer[[]string]
	aiReload   sync.Mutex // serializa reloadAI
	mux        *http.ServeMux
	web        fs.FS
	perms      *permCache
	rl         *rateLimiter
	now        func() time.Time
}

func New(cfg *config.Config, db *sql.DB, box *secure.Box, web fs.FS) (*Server, error) {
	s := &Server{
		cfg: cfg, db: db, box: box, web: web,
		jobs: jobs.NewManager(db, cfg.MaxJobs),
		ai: agents.NewAssistant(agents.NewOllama(agents.Config{
			Host: cfg.OllamaHost, Model: cfg.OllamaModel, Temperature: cfg.RouterTemperature, Think: cfg.OllamaThink,
		})),
		mux:   http.NewServeMux(),
		perms: &permCache{},
		rl:    newRateLimiter(),
		now:   time.Now,
	}
	if err := s.prepareDirs(); err != nil {
		return nil, err
	}
	if err := s.perms.load(context.Background(), db); err != nil {
		return nil, err
	}
	// configuração dos agentes salva pelo navegador (em erro, segue o padrão do .env)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	s.reloadAI(ctx)
	cancel()
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.securityHeaders(s.mux) }

func (s *Server) Jobs() *jobs.Manager { return s.jobs }

func (s *Server) prepareDirs() error {
	for _, d := range []string{"", "home", "ansible", "ansible/roles", "ansible/tmp", "run", "ssh", "docker",
		"tofu", "tofu/plugin-cache", "trivy-cache", "reports", "tls"} {
		if err := os.MkdirAll(s.cfg.Path(d), 0o700); err != nil {
			return fmt.Errorf("criando %s: %w", s.cfg.Path(d), err)
		}
	}
	cfg := fmt.Sprintf(`# Gerado pelo doomctl — não edite (recriado na inicialização)
[defaults]
roles_path = %s
local_tmp = %s
host_key_checking = False
retry_files_enabled = False
interpreter_python = auto_silent
nocolor = True
forks = 10
timeout = 30

[ssh_connection]
# A primeira conexão grava a chave do host (TOFU); mudanças posteriores são bloqueadas.
ssh_args = -o ControlMaster=auto -o ControlPersist=60s -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s
pipelining = True
`, s.cfg.Path("ansible", "roles"), s.cfg.Path("ansible", "tmp"), s.cfg.Path("ssh", "known_hosts"))
	return os.WriteFile(s.cfg.Path("ansible", "ansible.cfg"), []byte(cfg), 0o600)
}

// toolEnv: variáveis comuns a todas as execuções de ferramentas.
func (s *Server) toolEnv() []string {
	return []string{
		"HOME=" + s.cfg.Path("home"),
		"ANSIBLE_CONFIG=" + s.cfg.Path("ansible", "ansible.cfg"),
		"ANSIBLE_NOCOLOR=1", "ANSIBLE_FORCE_COLOR=0", "PYTHONUNBUFFERED=1",
		"DOCKER_CONFIG=" + s.cfg.Path("docker"),
		"DOCKER_HOST=unix://" + s.cfg.DockerSocket,
		"TRIVY_CACHE_DIR=" + s.cfg.Path("trivy-cache"),
		"TF_PLUGIN_CACHE_DIR=" + s.cfg.Path("tofu", "plugin-cache"),
		"TF_IN_AUTOMATION=1", "TF_INPUT=0", "NO_COLOR=1", "TERM=dumb",
	}
}

// ---------- rotas ----------

type handler func(w http.ResponseWriter, r *http.Request, u *User)

// access descreve a exigência de cada rota.
type access struct {
	module string
	action rbac.Action
	admin  bool
	self   bool // qualquer usuário autenticado (sessão completa)
}

func (s *Server) handle(pattern string, a access, h handler) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		sess, u, err := s.currentSession(r)
		if err != nil || sess.Kind != "full" {
			fail(w, http.StatusUnauthorized, "não autenticado")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.checkCSRF(r, sess) {
			fail(w, http.StatusForbidden, "token CSRF inválido")
			return
		}
		if u.MustChange && !a.self {
			fail(w, http.StatusForbidden, "troque sua senha antes de continuar")
			return
		}
		switch {
		case a.admin:
			if u.Role != rbac.RoleAdmin {
				fail(w, http.StatusForbidden, "somente administradores")
				return
			}
		case a.module != "":
			if !s.can(u, a.module, a.action) {
				fail(w, http.StatusForbidden, "sem permissão para "+string(a.action)+" em "+a.module)
				return
			}
		}
		h(w, r, u)
	})
}

func (s *Server) can(u *User, module string, act rbac.Action) bool {
	if u.Role == rbac.RoleAdmin {
		return true
	}
	p := s.perms.get(u.Role, module)
	if act == rbac.Manage {
		return p.Manage
	}
	return p.Read
}

func (s *Server) routes() {
	R := func(m string) access { return access{module: m, action: rbac.Read} }
	M := func(m string) access { return access{module: m, action: rbac.Manage} }
	self := access{self: true}
	admin := access{admin: true}

	// autenticação (rotas públicas tratam a sessão por conta própria)
	s.mux.HandleFunc("POST /api/auth/login", s.login)
	s.mux.HandleFunc("POST /api/auth/mfa", s.mfaVerify)
	s.mux.HandleFunc("GET /api/auth/mfa/setup", s.mfaSetup)
	s.mux.HandleFunc("POST /api/auth/mfa/enable", s.mfaEnable)
	s.mux.HandleFunc("POST /api/auth/logout", s.logout)
	s.mux.HandleFunc("GET /api/auth/me", s.me)
	s.mux.HandleFunc("GET /api/health", s.health)
	s.handle("POST /api/auth/password", self, s.changePassword)

	// perfil
	s.handle("PUT /api/me/profile", self, s.updateProfile)
	s.handle("POST /api/me/mfa/disable", self, s.mfaDisableSelf)
	s.handle("POST /api/me/mfa/recovery", self, s.mfaRegenRecovery)
	s.handle("GET /api/me/sessions", self, s.mySessions)
	s.handle("POST /api/me/sessions/revoke-others", self, s.revokeOtherSessions)

	// administração
	s.handle("GET /api/users", admin, s.listUsers)
	s.handle("POST /api/users", admin, s.createUser)
	s.handle("PUT /api/users/{id}", admin, s.updateUser)
	s.handle("DELETE /api/users/{id}", admin, s.deleteUser)
	s.handle("POST /api/users/{id}/password", admin, s.resetUserPassword)
	s.handle("POST /api/users/{id}/mfa", admin, s.adminMFA)
	s.handle("POST /api/users/{id}/revoke-sessions", admin, s.adminRevokeSessions)
	s.handle("GET /api/permissions", self, s.getPermissions)
	s.handle("PUT /api/permissions", admin, s.putPermissions)
	s.handle("GET /api/system", admin, s.systemInfo)

	// visão geral, logs, jobs
	s.handle("GET /api/dashboard", R("dashboard"), s.dashboard)
	s.handle("GET /api/logs", R("logs"), s.listLogs)
	s.handle("GET /api/logs/{id}", R("logs"), s.getLog)
	s.handle("GET /api/logs/{id}/artifact", R("logs"), s.logArtifact)
	s.handle("POST /api/logs/purge", admin, s.purgeLogs)
	s.handle("GET /api/jobs", self, s.listJobs)
	s.handle("GET /api/jobs/{id}/stream", self, s.streamJob)
	s.handle("POST /api/jobs/{id}/cancel", self, s.cancelJob)

	// Ansible
	s.handle("GET /api/ansible/hosts", R("ansible"), s.listHosts)
	s.handle("POST /api/ansible/hosts", M("ansible"), s.saveHost)
	s.handle("PUT /api/ansible/hosts/{id}", M("ansible"), s.saveHost)
	s.handle("DELETE /api/ansible/hosts/{id}", M("ansible"), s.deleteHost)
	s.handle("POST /api/ansible/hosts/import", M("ansible"), s.importHosts)
	s.handle("GET /api/ansible/inventory", R("ansible"), s.exportInventory)
	s.handle("GET /api/ansible/vaults", R("ansible"), s.listVaults)
	s.handle("POST /api/ansible/vaults", M("ansible"), s.createVault)
	s.handle("POST /api/ansible/vaults/{id}/view", M("ansible"), s.viewVault)
	s.handle("PUT /api/ansible/vaults/{id}", M("ansible"), s.updateVault)
	s.handle("DELETE /api/ansible/vaults/{id}", M("ansible"), s.deleteVault)
	s.handle("GET /api/ansible/task-types", R("ansible"), s.taskTypes)
	s.handle("POST /api/ansible/generate/playbook", R("ansible"), s.generatePlaybook)
	s.handle("GET /api/ansible/playbooks", R("ansible"), s.listPlaybooks)
	s.handle("GET /api/ansible/playbooks/{id}", R("ansible"), s.getPlaybook)
	s.handle("POST /api/ansible/playbooks", M("ansible"), s.savePlaybook)
	s.handle("PUT /api/ansible/playbooks/{id}", M("ansible"), s.savePlaybook)
	s.handle("DELETE /api/ansible/playbooks/{id}", M("ansible"), s.deletePlaybook)
	s.handle("POST /api/ansible/playbooks/{id}/check", M("ansible"), s.checkPlaybook)
	s.handle("POST /api/ansible/playbooks/{id}/run", M("ansible"), s.runPlaybook)
	s.handle("GET /api/ansible/roles", R("ansible"), s.listRoles)
	s.handle("GET /api/ansible/roles/skeleton", R("ansible"), s.roleSkeleton)
	s.handle("GET /api/ansible/roles/{id}", R("ansible"), s.getRole)
	s.handle("GET /api/ansible/roles/{id}/download", R("ansible"), s.downloadRole)
	s.handle("POST /api/ansible/roles", M("ansible"), s.saveRole)
	s.handle("PUT /api/ansible/roles/{id}", M("ansible"), s.saveRole)
	s.handle("DELETE /api/ansible/roles/{id}", M("ansible"), s.deleteRole)
	s.handle("POST /api/ansible/adhoc", M("ansible"), s.adhoc)

	// Docker
	s.handle("GET /api/docker/presets", R("docker"), s.dockerPresets)
	s.handle("POST /api/docker/generate/dockerfile", R("docker"), s.generateDockerfile)
	s.handle("POST /api/docker/generate/compose", R("docker"), s.generateCompose)
	s.handle("GET /api/docker/files", R("docker"), s.listDockerFiles)
	s.handle("GET /api/docker/files/{id}", R("docker"), s.getDockerFile)
	s.handle("POST /api/docker/files", M("docker"), s.saveDockerFile)
	s.handle("PUT /api/docker/files/{id}", M("docker"), s.saveDockerFile)
	s.handle("DELETE /api/docker/files/{id}", M("docker"), s.deleteDockerFile)
	s.handle("POST /api/docker/files/{id}/validate", M("docker"), s.validateDockerFile)
	s.handle("GET /api/docker/registries", R("docker"), s.listRegistries)
	s.handle("POST /api/docker/registries", M("docker"), s.saveRegistry)
	s.handle("DELETE /api/docker/registries/{id}", M("docker"), s.deleteRegistry)
	s.handle("POST /api/docker/registries/{id}/login", M("docker"), s.registryLogin)
	s.handle("POST /api/docker/registries/{id}/logout", M("docker"), s.registryLogout)

	// OpenTofu
	s.handle("POST /api/tofu/generate", R("opentofu"), s.generateTofu)
	s.handle("GET /api/tofu/projects", R("opentofu"), s.listTofu)
	s.handle("GET /api/tofu/projects/{id}", R("opentofu"), s.getTofu)
	s.handle("GET /api/tofu/projects/{id}/download", R("opentofu"), s.downloadTofu)
	s.handle("POST /api/tofu/projects", M("opentofu"), s.saveTofu)
	s.handle("PUT /api/tofu/projects/{id}", M("opentofu"), s.saveTofu)
	s.handle("DELETE /api/tofu/projects/{id}", M("opentofu"), s.deleteTofu)
	s.handle("PUT /api/tofu/projects/{id}/credentials", M("opentofu"), s.setTofuCreds)
	s.handle("POST /api/tofu/projects/{id}/action/{action}", M("opentofu"), s.tofuAction)

	// Kubernetes (beta)
	s.handle("GET /api/k8s/templates", R("kubernetes"), s.k8sTemplates)
	s.handle("POST /api/k8s/render", R("kubernetes"), s.k8sRender)
	s.handle("POST /api/k8s/bundle", R("kubernetes"), s.k8sBundle)
	s.handle("GET /api/k8s/manifests", R("kubernetes"), s.listManifests)
	s.handle("GET /api/k8s/manifests/{id}", R("kubernetes"), s.getManifest)
	s.handle("POST /api/k8s/manifests", M("kubernetes"), s.saveManifest)
	s.handle("PUT /api/k8s/manifests/{id}", M("kubernetes"), s.saveManifest)
	s.handle("DELETE /api/k8s/manifests/{id}", M("kubernetes"), s.deleteManifest)
	s.handle("POST /api/k8s/validate", R("kubernetes"), s.validateManifest)
	s.handle("GET /api/k8s/clusters", R("kubernetes"), s.listClusters)
	s.handle("POST /api/k8s/clusters", M("kubernetes"), s.saveCluster)
	s.handle("DELETE /api/k8s/clusters/{id}", M("kubernetes"), s.deleteCluster)
	s.handle("POST /api/k8s/clusters/{id}/kubectl", M("kubernetes"), s.kubectl)
	s.handle("POST /api/k8s/clusters/{id}/apply", M("kubernetes"), s.k8sApply)
	s.handle("POST /api/k8s/clusters/{id}/scan", M("kubernetes"), s.k8sScan)

	// Redes & Segurança
	s.handle("POST /api/net/calc", R("netcalc"), s.netCalc)
	s.handle("POST /api/net/split", R("netcalc"), s.netSplit)
	s.handle("POST /api/net/vlsm", R("netcalc"), s.netVLSM)
	s.handle("GET /api/net/masks", R("netcalc"), s.netMasks)
	s.handle("GET /api/trivy/images", R("trivy"), s.trivyImages)
	s.handle("POST /api/trivy/scan", M("trivy"), s.trivyScan)
	s.handle("POST /api/trivy/db-update", M("trivy"), s.trivyDBUpdate)

	// Plug-ins e IA
	// FinOps
	s.handle("GET /api/finops/overview", R("finops"), s.finopsOverview)
	s.handle("GET /api/finops/dimensions", R("finops"), s.finopsDimensions)
	s.handle("GET /api/finops/anomalies", R("finops"), s.finopsAnomalies)
	s.handle("GET /api/finops/forecast", R("finops"), s.finopsForecast)
	s.handle("GET /api/finops/budgets", R("finops"), s.finopsBudgetList)
	s.handle("POST /api/finops/budgets", M("finops"), s.finopsSaveBudget)
	s.handle("PUT /api/finops/budgets/{id}", M("finops"), s.finopsSaveBudget)
	s.handle("DELETE /api/finops/budgets/{id}", M("finops"), s.finopsDeleteRow("finops_budgets", "budget_delete"))
	s.handle("GET /api/finops/findings", R("finops"), s.finopsFindings)
	s.handle("POST /api/finops/findings/dismiss", M("finops"), s.finopsDismiss)
	s.handle("POST /api/finops/remediation/script", R("finops"), s.finopsRemediationScript)
	s.handle("POST /api/finops/remediation/execute", M("finops"), s.finopsRemediate)
	s.handle("GET /api/finops/resources", R("finops"), s.finopsResourceList)
	s.handle("GET /api/finops/usage", R("finops"), s.finopsUsage)
	s.handle("GET /api/finops/commitments", R("finops"), s.finopsCommitments)
	s.handle("POST /api/finops/report", R("finops"), s.finopsReport)
	s.handle("POST /api/finops/k8s/analyze", R("finops"), s.finopsK8s)
	s.handle("POST /api/finops/iac/estimate", R("finops"), s.finopsIaCEstimate)
	s.handle("POST /api/finops/iac/check", R("finops"), s.finopsIaCCheck)
	s.handle("POST /api/finops/iac/fix", M("finops"), s.finopsIaCFix)
	s.handle("GET /api/finops/scenarios", R("finops"), s.finopsScenarioList)
	s.handle("POST /api/finops/scenarios/price", R("finops"), s.finopsScenarioPrice)
	s.handle("POST /api/finops/scenarios", M("finops"), s.finopsSaveScenario)
	s.handle("PUT /api/finops/scenarios/{id}", M("finops"), s.finopsSaveScenario)
	s.handle("DELETE /api/finops/scenarios/{id}", M("finops"), s.finopsDeleteRow("finops_scenarios", "scenario_delete"))
	s.handle("POST /api/finops/whatif", R("finops"), s.finopsWhatIf)
	s.handle("GET /api/finops/policies", R("finops"), s.finopsPolicyList)
	s.handle("POST /api/finops/policies", M("finops"), s.finopsSavePolicy)
	s.handle("PUT /api/finops/policies/{id}", M("finops"), s.finopsSavePolicy)
	s.handle("DELETE /api/finops/policies/{id}", M("finops"), s.finopsDeleteRow("finops_policies", "policy_delete"))
	s.handle("GET /api/finops/settings", R("finops"), s.finopsGetSettings)
	s.handle("PUT /api/finops/settings", M("finops"), s.finopsPutSettings)
	s.handle("GET /api/finops/sources", R("finops"), s.finopsSources)
	s.handle("POST /api/finops/sources", M("finops"), s.finopsSaveSource)
	s.handle("PUT /api/finops/sources/{id}", M("finops"), s.finopsSaveSource)
	s.handle("DELETE /api/finops/sources/{id}", M("finops"), s.finopsDeleteSource)
	s.handle("PUT /api/finops/sources/{id}/credentials", M("finops"), s.finopsSetCreds)
	s.handle("POST /api/finops/sources/{id}/test", M("finops"), s.finopsTestSource)
	s.handle("POST /api/finops/sources/{id}/sync", M("finops"), s.finopsSync)
	s.handle("POST /api/finops/sources/{id}/scan", M("finops"), s.finopsScan)
	s.handle("POST /api/finops/sources/{id}/import-costs", M("finops"), s.finopsImportCosts)
	s.handle("POST /api/finops/sources/{id}/import-resources", M("finops"), s.finopsImportResources)
	s.handle("POST /api/finops/demo", M("finops"), s.finopsLoadDemo)
	s.handle("DELETE /api/finops/demo", M("finops"), s.finopsRemoveDemo)
	s.handle("GET /api/finops/prices", R("finops"), s.finopsPricesList)
	s.handle("GET /api/finops/prices/export", R("finops"), s.finopsExportPrices)
	s.handle("POST /api/finops/prices", M("finops"), s.finopsSavePrice)
	s.handle("DELETE /api/finops/prices/{id}", M("finops"), s.finopsDeleteRow("finops_prices", "price_delete"))
	s.handle("POST /api/finops/prices/import", M("finops"), s.finopsImportPrices)
	s.handle("POST /api/finops/prices/aws-lookup", M("finops"), s.finopsAWSPrice)
	s.handle("POST /api/finops/alerts/test", M("finops"), s.finopsTestAlert)
	s.handle("POST /api/finops/alerts/evaluate", M("finops"), s.finopsEvaluateNow)

	s.handle("GET /api/plugins", R("plugins"), s.listPlugins)
	s.handle("POST /api/plugins/{id}/{action}", M("plugins"), s.pluginAction)
	s.handle("GET /api/ai/status", R("ai"), s.aiStatus)
	s.handle("POST /api/ai/chat", M("ai"), s.aiChat)
	s.handle("POST /api/ai/reset", R("ai"), s.aiReset)

	// configuração dos agentes de IA (somente administradores)
	s.handle("GET /api/ai/config", admin, s.aiGetConfig)
	s.handle("PUT /api/ai/config", admin, s.aiPutConfig)
	s.handle("POST /api/ai/config/reset", admin, s.aiResetConfig)
	s.handle("GET /api/ai/config/history", admin, s.aiConfigHistory)
	s.handle("POST /api/ai/config/history/{id}/restore", admin, s.aiRestoreConfig)
	s.handle("GET /api/ai/config/export", admin, s.aiExportConfig)
	s.handle("POST /api/ai/config/import", admin, s.aiImportConfig)
	s.handle("POST /api/ai/config/preview", admin, s.aiPreview)
	s.handle("POST /api/ai/playground", admin, s.aiPlayground)
	s.handle("GET /api/ai/connections", admin, s.aiListConns)
	s.handle("POST /api/ai/connections", admin, s.aiSaveConn)
	s.handle("PUT /api/ai/connections/{id}", admin, s.aiSaveConn)
	s.handle("DELETE /api/ai/connections/{id}", admin, s.aiDeleteConn)
	s.handle("POST /api/ai/connections/{id}/test", admin, s.aiTestConn)
	s.handle("GET /api/ai/connections/{id}/models", admin, s.aiConnModels)
	s.handle("GET /api/ai/mcp", admin, s.aiListMCP)
	s.handle("POST /api/ai/mcp", admin, s.aiSaveMCP)
	s.handle("PUT /api/ai/mcp/{id}", admin, s.aiSaveMCP)
	s.handle("DELETE /api/ai/mcp/{id}", admin, s.aiDeleteMCP)
	s.handle("POST /api/ai/mcp/{id}/refresh", admin, s.aiRefreshMCP)
	s.handle("PUT /api/ai/mcp/{id}/tools", admin, s.aiMCPTools)
	s.handle("POST /api/ai/mcp/{id}/call", admin, s.aiMCPCall)
	s.handle("GET /api/ai/ollama", admin, s.aiOllamaInfo)
	s.handle("POST /api/ai/ollama/pull", admin, s.aiOllamaPull)
	s.handle("POST /api/ai/ollama/delete", admin, s.aiOllamaDelete)
	s.handle("POST /api/ai/ollama/load", admin, s.aiOllamaLoad)
	s.handle("POST /api/ai/ollama/restart", admin, s.aiOllamaRestart)
	s.handle("GET /api/ai/usage", admin, s.aiUsage)

	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "rota não encontrada")
	})
	s.mux.Handle("/", s.static())
}

// ---------- estáticos ----------

func (s *Server) static() http.Handler {
	fsrv := http.FileServerFS(s.web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(s.web, p); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			p = "index.html"
			r = r2
		}
		if p == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		fsrv.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasSuffix(r.URL.Path, "/stream") {
			slog.Debug("http", "metodo", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		fail(w, http.StatusServiceUnavailable, "banco indisponível")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "version": s.cfg.Version})
}

// ---------- helpers ----------

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) { writeJSON(w, code, apiError{msg}) }

func failErr(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, "registro não encontrado")
		return
	}
	if isUniqueViolation(err) {
		fail(w, http.StatusConflict, "já existe um registro com esse nome")
		return
	}
	slog.Error("erro interno", "erro", err)
	fail(w, http.StatusInternalServerError, "erro interno")
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate key value")
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("JSON inválido: %w", err)
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("id inválido")
	}
	return id, nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func ok(w http.ResponseWriter) { writeJSON(w, 200, map[string]bool{"ok": true}) }

// audit registra eventos administrativos/autenticação em tool_logs.
func (s *Server) audit(module, action, target string, u *User, status, msg string, input any) {
	var uid any
	uname := ""
	if u != nil {
		uid, uname = u.ID, u.Username
	}
	in := []byte("{}")
	if input != nil {
		in, _ = json.Marshal(input)
	}
	_, err := s.db.Exec(`INSERT INTO tool_logs(module, action, target, user_id, username, status, input, output, finished_at, duration_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now(), 0)`, module, action, target, uid, uname, status, string(in), msg)
	if err != nil {
		slog.Error("audit", "erro", err)
	}
}

// ---------- rate limit (login) ----------

type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{hits: map[string][]time.Time{}} }

// allow: no máximo n eventos por janela para a chave.
func (l *rateLimiter) allow(key string, n int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	h := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < window {
			h = append(h, t)
		}
	}
	if len(h) >= n {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	if len(l.hits) > 10000 { // limpeza simples
		for k, v := range l.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > window {
				delete(l.hits, k)
			}
		}
	}
	return true
}

// ---------- cache de permissões ----------

type permCache struct {
	mu sync.RWMutex
	m  map[string]map[string]rbac.Perm
}

func (p *permCache) load(ctx context.Context, db *sql.DB) error {
	m := rbac.Defaults()
	rows, err := db.QueryContext(ctx, `SELECT role, module, can_read, can_manage FROM role_permissions`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var role, mod string
		var pr rbac.Perm
		if err := rows.Scan(&role, &mod, &pr.Read, &pr.Manage); err != nil {
			return err
		}
		if _, ok := m[role]; ok && rbac.ModuleExists(mod) {
			m[role][mod] = rbac.Normalize(role, mod, pr)
		}
	}
	p.mu.Lock()
	p.m = m
	p.mu.Unlock()
	return rows.Err()
}

func (p *permCache) get(role, module string) rbac.Perm {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.m[role][module]
}

func (p *permCache) all() map[string]map[string]rbac.Perm {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := map[string]map[string]rbac.Perm{}
	for r, mm := range p.m {
		out[r] = map[string]rbac.Perm{}
		for k, v := range mm {
			out[r][k] = v
		}
	}
	return out
}
