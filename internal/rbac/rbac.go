// Package rbac define os perfis (admin, operator, viewer), os módulos do doomctl e
// a matriz de permissões padrão (leitura x gerenciamento) por módulo.
package rbac

const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

type Action string

const (
	Read   Action = "read"   // ver recursos, relatórios e saídas
	Manage Action = "manage" // criar, editar, excluir e executar
)

type Module struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Section   string `json:"section"`
	Status    string `json:"status"` // stable | beta | soon
	AdminOnly bool   `json:"admin_only"`
}

// Modules é a lista canônica de módulos (ferramentas) do doomctl.
var Modules = []Module{
	{"dashboard", "Visão Geral", "geral", "stable", false},
	{"ansible", "Ansible", "devops", "soon", false}, // interface web em breve; API disponível
	{"docker", "Docker", "devops", "stable", false},
	{"kubernetes", "Kubernetes", "devops", "beta", false},
	{"opentofu", "OpenTofu", "devops", "stable", false},
	{"aws", "AWS EC2 & S3 (IAM)", "devops", "soon", false},
	{"finops", "FinOps", "devops", "stable", false},
	{"netcalc", "Sub-redes & Máscaras", "netsec", "stable", false},
	{"trivy", "Trivy", "netsec", "stable", false},
	{"falco", "Falco", "netsec", "soon", false},
	{"ai", "Assistente IA", "operacao", "stable", false},
	{"logs", "Logs", "operacao", "stable", false},
	{"plugins", "Plug-ins", "operacao", "stable", false},
	{"users", "Usuários", "admin", "stable", true},
	{"settings", "Configurações", "admin", "stable", true},
}

func ModuleExists(id string) bool {
	for _, m := range Modules {
		if m.ID == id {
			return true
		}
	}
	return false
}

func IsAdminOnly(id string) bool {
	for _, m := range Modules {
		if m.ID == id {
			return m.AdminOnly
		}
	}
	return true
}

type Perm struct {
	Read   bool `json:"read"`
	Manage bool `json:"manage"`
}

// Defaults: matriz inicial. Admin tem tudo (fixo, não editável).
// Visualizador só enxerga relatórios (dashboard, FinOps) e logs de execução.
func Defaults() map[string]map[string]Perm {
	op := map[string]Perm{}
	vw := map[string]Perm{}
	for _, m := range Modules {
		switch {
		case m.AdminOnly:
			op[m.ID], vw[m.ID] = Perm{}, Perm{}
		case m.ID == "dashboard" || m.ID == "logs":
			op[m.ID], vw[m.ID] = Perm{Read: true}, Perm{Read: true}
		case m.ID == "finops":
			// relatórios de custo são úteis ao visualizador; gerenciar inclui credenciais e remediação
			op[m.ID], vw[m.ID] = Perm{Read: true, Manage: true}, Perm{Read: true}
		case m.ID == "plugins":
			op[m.ID], vw[m.ID] = Perm{Read: true}, Perm{}
		default:
			op[m.ID], vw[m.ID] = Perm{Read: true, Manage: true}, Perm{}
		}
	}
	return map[string]map[string]Perm{RoleOperator: op, RoleViewer: vw}
}

// Normalize aplica as regras invioláveis da matriz.
func Normalize(role, module string, p Perm) Perm {
	if IsAdminOnly(module) {
		return Perm{}
	}
	if role == RoleViewer {
		p.Manage = false // visualizador nunca gerencia
	}
	if p.Manage {
		p.Read = true
	}
	return p
}

func ValidRole(r string) bool { return r == RoleAdmin || r == RoleOperator || r == RoleViewer }
