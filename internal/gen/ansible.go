package gen

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ---------- Playbook ----------

type AnsibleTask struct {
	Type   string            `json:"type"`
	Name   string            `json:"name"`
	Params map[string]string `json:"params"`
	Notify string            `json:"notify"`
	When   string            `json:"when"`
	Tags   string            `json:"tags"`
}

type AnsibleHandler struct {
	Name    string `json:"name"`
	Service string `json:"service"`
}

type PlaybookSpec struct {
	Name        string           `json:"name"`
	Hosts       string           `json:"hosts"`
	Become      bool             `json:"become"`
	GatherFacts bool             `json:"gather_facts"`
	Vars        string           `json:"vars"` // linhas chave=valor
	Roles       []string         `json:"roles"`
	Tasks       []AnsibleTask    `json:"tasks"`
	Handlers    []AnsibleHandler `json:"handlers"`
}

// TaskField descreve os parâmetros de cada tipo de tarefa para o formulário.
type TaskField struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text | select | bool | textarea
	Options  []string `json:"options,omitempty"`
	Default  string   `json:"default,omitempty"`
	Required bool     `json:"required,omitempty"`
}

type TaskType struct {
	ID     string      `json:"id"`
	Module string      `json:"module"`
	Label  string      `json:"label"`
	Fields []TaskField `json:"fields"`
}

// TaskTypes: somente módulos ansible.builtin (idempotentes sempre que possível).
var TaskTypes = []TaskType{
	{"package", "ansible.builtin.package", "Pacote (genérico)", []TaskField{
		{Name: "name", Label: "Pacotes (separados por vírgula)", Type: "text", Required: true},
		{Name: "state", Label: "Estado", Type: "select", Options: []string{"present", "latest", "absent"}, Default: "present"}}},
	{"apt", "ansible.builtin.apt", "APT (Debian/Ubuntu)", []TaskField{
		{Name: "name", Label: "Pacotes (separados por vírgula)", Type: "text", Required: true},
		{Name: "state", Label: "Estado", Type: "select", Options: []string{"present", "latest", "absent"}, Default: "present"},
		{Name: "update_cache", Label: "Atualizar cache", Type: "bool", Default: "true"},
		{Name: "cache_valid_time", Label: "Validade do cache (s)", Type: "text", Default: "3600"}}},
	{"dnf", "ansible.builtin.dnf", "DNF (RHEL/Rocky/Alma)", []TaskField{
		{Name: "name", Label: "Pacotes (separados por vírgula)", Type: "text", Required: true},
		{Name: "state", Label: "Estado", Type: "select", Options: []string{"present", "latest", "absent"}, Default: "present"}}},
	{"service", "ansible.builtin.service", "Serviço", []TaskField{
		{Name: "name", Label: "Serviço", Type: "text", Required: true},
		{Name: "state", Label: "Estado", Type: "select", Options: []string{"started", "stopped", "restarted", "reloaded"}, Default: "started"},
		{Name: "enabled", Label: "Habilitar no boot", Type: "bool", Default: "true"}}},
	{"copy", "ansible.builtin.copy", "Copiar arquivo", []TaskField{
		{Name: "src", Label: "Origem (no controller)", Type: "text"},
		{Name: "content", Label: "Conteúdo (alternativa à origem)", Type: "textarea"},
		{Name: "dest", Label: "Destino", Type: "text", Required: true},
		{Name: "owner", Label: "Dono", Type: "text"}, {Name: "group", Label: "Grupo", Type: "text"},
		{Name: "mode", Label: "Permissão", Type: "text", Default: "0644"},
		{Name: "backup", Label: "Backup antes de sobrescrever", Type: "bool", Default: "true"}}},
	{"template", "ansible.builtin.template", "Template Jinja2", []TaskField{
		{Name: "src", Label: "Template (.j2)", Type: "text", Required: true},
		{Name: "dest", Label: "Destino", Type: "text", Required: true},
		{Name: "mode", Label: "Permissão", Type: "text", Default: "0644"},
		{Name: "backup", Label: "Backup", Type: "bool", Default: "true"}}},
	{"file", "ansible.builtin.file", "Arquivo/diretório", []TaskField{
		{Name: "path", Label: "Caminho", Type: "text", Required: true},
		{Name: "state", Label: "Estado", Type: "select", Options: []string{"directory", "file", "touch", "link", "absent"}, Default: "directory"},
		{Name: "src", Label: "Origem (para link)", Type: "text"},
		{Name: "owner", Label: "Dono", Type: "text"}, {Name: "group", Label: "Grupo", Type: "text"},
		{Name: "mode", Label: "Permissão", Type: "text", Default: "0755"}}},
	{"user", "ansible.builtin.user", "Usuário", []TaskField{
		{Name: "name", Label: "Usuário", Type: "text", Required: true},
		{Name: "groups", Label: "Grupos (vírgula)", Type: "text"},
		{Name: "append", Label: "Manter grupos atuais", Type: "bool", Default: "true"},
		{Name: "shell", Label: "Shell", Type: "text", Default: "/bin/bash"},
		{Name: "state", Label: "Estado", Type: "select", Options: []string{"present", "absent"}, Default: "present"}}},
	{"authorized_key", "ansible.posix.authorized_key", "Chave SSH autorizada (ansible.posix)", []TaskField{
		{Name: "user", Label: "Usuário", Type: "text", Required: true},
		{Name: "key", Label: "Chave pública", Type: "textarea", Required: true},
		{Name: "state", Label: "Estado", Type: "select", Options: []string{"present", "absent"}, Default: "present"}}},
	{"lineinfile", "ansible.builtin.lineinfile", "Linha em arquivo", []TaskField{
		{Name: "path", Label: "Arquivo", Type: "text", Required: true},
		{Name: "regexp", Label: "Regex (linha a substituir)", Type: "text"},
		{Name: "line", Label: "Linha", Type: "text", Required: true},
		{Name: "backup", Label: "Backup", Type: "bool", Default: "true"}}},
	{"get_url", "ansible.builtin.get_url", "Download (get_url)", []TaskField{
		{Name: "url", Label: "URL", Type: "text", Required: true},
		{Name: "dest", Label: "Destino", Type: "text", Required: true},
		{Name: "checksum", Label: "Checksum (sha256:...)", Type: "text"},
		{Name: "mode", Label: "Permissão", Type: "text", Default: "0644"}}},
	{"git", "ansible.builtin.git", "Git clone/pull", []TaskField{
		{Name: "repo", Label: "Repositório", Type: "text", Required: true},
		{Name: "dest", Label: "Destino", Type: "text", Required: true},
		{Name: "version", Label: "Versão/branch/tag", Type: "text", Default: "main"}}},
	{"command", "ansible.builtin.command", "Comando (sem shell)", []TaskField{
		{Name: "cmd", Label: "Comando", Type: "text", Required: true},
		{Name: "creates", Label: "creates (idempotência)", Type: "text"}}},
	{"shell", "ansible.builtin.shell", "Shell (último recurso)", []TaskField{
		{Name: "cmd", Label: "Comando", Type: "textarea", Required: true},
		{Name: "creates", Label: "creates (idempotência)", Type: "text"}}},
	{"reboot", "ansible.builtin.reboot", "Reboot", []TaskField{
		{Name: "reboot_timeout", Label: "Timeout (s)", Type: "text", Default: "600"}}},
	{"debug", "ansible.builtin.debug", "Debug", []TaskField{
		{Name: "msg", Label: "Mensagem", Type: "text", Required: true}}},
	{"include_role", "ansible.builtin.include_role", "Incluir role", []TaskField{
		{Name: "name", Label: "Role", Type: "text", Required: true}}},
}

func taskType(id string) *TaskType {
	for i := range TaskTypes {
		if TaskTypes[i].ID == id {
			return &TaskTypes[i]
		}
	}
	return nil
}

func paramValue(tt *TaskType, f TaskField, v string) any {
	switch f.Type {
	case "bool":
		b, _ := strconv.ParseBool(v)
		return b
	case "textarea":
		if strings.Contains(v, "\n") {
			return Literal(v)
		}
	}
	if f.Name == "mode" {
		return Raw(DQ(v)) // "0644" como string (evita interpretação octal)
	}
	if (f.Name == "reboot_timeout" || f.Name == "cache_valid_time") && isInt(v) {
		n, _ := strconv.Atoi(v)
		return n
	}
	if f.Name == "name" && (tt.ID == "package" || tt.ID == "apt" || tt.ID == "dnf") && strings.Contains(v, ",") {
		l := List{}
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				l = append(l, p)
			}
		}
		return l
	}
	return v
}

func isInt(s string) bool { _, err := strconv.Atoi(s); return err == nil }

var hostPatternRe = regexp.MustCompile(`^[A-Za-z0-9_.:*!&,\[\]-]+$`)

func Playbook(s PlaybookSpec) (string, error) {
	if s.Hosts == "" {
		s.Hosts = "all"
	}
	if !hostPatternRe.MatchString(s.Hosts) {
		return "", fmt.Errorf("padrão de hosts inválido")
	}
	play := Map{}.Set("name", def(s.Name, "Playbook gerado pelo doomctl")).Set("hosts", s.Hosts)
	play = play.Set("become", s.Become).Set("gather_facts", s.GatherFacts)
	vars := Map{}
	for _, kv := range KVLines(s.Vars) {
		if !IdentRe.MatchString(kv[0]) {
			return "", fmt.Errorf("variável inválida: %q", kv[0])
		}
		vars = vars.Set(kv[0], kv[1])
	}
	play = play.SetIf("vars", vars)
	if len(s.Roles) > 0 {
		l := List{}
		for _, r := range s.Roles {
			if r = strings.TrimSpace(r); r != "" {
				l = append(l, Map{}.Set("role", r))
			}
		}
		play = play.SetIf("roles", l)
	}
	tasks := List{}
	for i, t := range s.Tasks {
		tt := taskType(t.Type)
		if tt == nil {
			return "", fmt.Errorf("tarefa %d: tipo desconhecido %q", i+1, t.Type)
		}
		params := Map{}
		for _, f := range tt.Fields {
			v := strings.TrimSpace(t.Params[f.Name])
			if f.Type == "textarea" {
				v = strings.TrimRight(t.Params[f.Name], " \n")
			}
			if v == "" {
				if f.Required {
					return "", fmt.Errorf("tarefa %d (%s): campo %q obrigatório", i+1, tt.Label, f.Label)
				}
				continue
			}
			if f.Name == "groups" {
				params = params.Set("groups", v)
				continue
			}
			params = params.Set(f.Name, paramValue(tt, f, v))
		}
		tm := Map{}.Set("name", def(t.Name, tt.Label))
		tm = tm.Set(tt.Module, params)
		tm = tm.SetIf("when", t.When)
		if t.Notify != "" {
			tm = tm.Set("notify", t.Notify)
		}
		if tags := strings.Fields(strings.ReplaceAll(t.Tags, ",", " ")); len(tags) > 0 {
			l := List{}
			for _, tg := range tags {
				l = append(l, tg)
			}
			tm = tm.Set("tags", l)
		}
		tasks = append(tasks, tm)
	}
	play = play.SetIf("tasks", tasks)
	hs := List{}
	for _, h := range s.Handlers {
		if h.Name == "" || h.Service == "" {
			continue
		}
		hs = append(hs, Map{}.Set("name", h.Name).Set("ansible.builtin.service",
			Map{}.Set("name", h.Service).Set("state", "restarted")))
	}
	play = play.SetIf("handlers", hs)
	return "---\n# Gerado pelo doomctl. Teste antes com: ansible-playbook --check --diff\n" + YAML(List{play}), nil
}

// ---------- Role ----------

var RoleNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// RoleSkeleton devolve o conteúdo inicial de tasks/handlers/meta/defaults/vars.
func RoleSkeleton(name, description, author string) map[string]string {
	return map[string]string{
		"tasks": fmt.Sprintf(`---
# tasks/main.yml — role %s
- name: Instalar pacotes da role
  ansible.builtin.package:
    name: "{{ %s_packages }}"
    state: present

- name: Garantir serviço em execução
  ansible.builtin.service:
    name: "{{ %s_service }}"
    state: started
    enabled: true
  when: %s_service | length > 0
`, name, name, name, name),
		"handlers": fmt.Sprintf(`---
# handlers/main.yml — role %s
- name: Restart %s
  ansible.builtin.service:
    name: "{{ %s_service }}"
    state: restarted
`, name, name, name),
		"meta": fmt.Sprintf(`---
galaxy_info:
  role_name: %s
  author: %s
  description: %s
  license: MIT
  min_ansible_version: "2.15"
  platforms:
    - name: Debian
      versions:
        - trixie
        - bookworm
    - name: EL
      versions:
        - "9"
dependencies: []
`, name, Q(def(author, "doomctl")), Q(def(description, "Role gerada pelo doomctl"))),
		"defaults": fmt.Sprintf(`---
# defaults/main.yml — valores sobrescrevíveis
%s_packages: []
%s_service: ""
`, name, name),
		"vars": "---\n# vars/main.yml — valores internos da role (alta precedência)\n",
	}
}
