package gen

import (
	"os/exec"
	"strings"
	"testing"
)

// yamlOK valida o YAML com PyYAML quando disponível (mesmo parser usado pelo Ansible).
func yamlOK(t *testing.T, name, content string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		return
	}
	if exec.Command("python3", "-c", "import yaml").Run() != nil {
		return
	}
	cmd := exec.Command("python3", "-c", "import sys,yaml; list(yaml.safe_load_all(sys.stdin))")
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%s: YAML inválido: %s\n%s", name, out, content)
	}
}

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"nginx":       "nginx",
		"8080:80":     `"8080:80"`,
		"yes":         `"yes"`,
		"0644":        `"0644"`,
		"a: b":        `"a: b"`,
		"@timestamp":  `"@timestamp"`,
		"":            `""`,
		"nginx:1.29":  "nginx:1.29",
		"- item":      `"- item"`,
		"valor # com": `"valor # com"`,
	}
	for in, want := range cases {
		if got := Q(in); got != want {
			t.Errorf("Q(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	got, err := SplitArgs(`ansible web -m shell -a "uptime && df -h" -e 'x=1 y=2'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ansible", "web", "-m", "shell", "-a", "uptime && df -h", "-e", "x=1 y=2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if _, err := SplitArgs(`echo "aberto`); err == nil {
		t.Fatal("aspas abertas deveriam falhar")
	}
}

func TestDockerfileAndCompose(t *testing.T) {
	for name, p := range DockerfilePresets() {
		df, ign, err := Dockerfile(p)
		if err != nil || !strings.Contains(df, "FROM ") || ign == "" {
			t.Fatalf("preset %s: %v\n%s", name, err, df)
		}
	}
	df, _, _ := Dockerfile(DockerfilePresets()["python"])
	if !strings.Contains(df, "USER 10001:10001") || !strings.Contains(df, `CMD ["python","-m","gunicorn"`) {
		t.Fatalf("dockerfile python inesperado:\n%s", df)
	}
	y, err := Compose(ComposeSpec{
		Name: "stack",
		Services: []ComposeService{
			{Name: "db", Image: "postgres:18-trixie", Environment: "POSTGRES_PASSWORD=${DB_PASSWORD}",
				Volumes: "pgdata:/var/lib/postgresql", Networks: []string{"back"},
				Healthcheck: ComposeHealth{Test: "pg_isready -U postgres"}},
			{Name: "web", Image: "app:1.0", Registry: "registry.local:5000", Ports: "8080:80", DependsOn: []string{"db"},
				Networks: []string{"back", "lan"}, CapDropAll: true, NoNewPrivs: true, ReadOnly: true, MemLimit: "512m", CPUs: "1.5",
				Command: `sh -c "exec app --port 80"`},
		},
		Networks: []ComposeNetwork{{Name: "back", Driver: "bridge", Internal: true},
			{Name: "lan", Driver: "macvlan", Parent: "eth0", Subnet: "192.168.1.0/24", Gateway: "192.168.1.1"}},
		Volumes: []ComposeVolume{{Name: "pgdata"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`- "8080:80"`, "image: registry.local:5000/app:1.0", "condition: service_healthy", "parent: eth0", "cpus: \"1.5\""} {
		if !strings.Contains(y, s) {
			t.Errorf("compose sem %q:\n%s", s, y)
		}
	}
	yamlOK(t, "compose", y)
}

func TestPlaybook(t *testing.T) {
	y, err := Playbook(PlaybookSpec{Name: "Base", Hosts: "web", Become: true, Vars: "app_port=8080",
		Tasks: []AnsibleTask{
			{Type: "apt", Params: map[string]string{"name": "nginx, curl", "update_cache": "true"}},
			{Type: "copy", Params: map[string]string{"content": "linha1\nlinha2", "dest": "/etc/motd", "mode": "0644"}, Notify: "Restart nginx"},
			{Type: "service", Params: map[string]string{"name": "nginx", "enabled": "true"}},
		},
		Handlers: []AnsibleHandler{{Name: "Restart nginx", Service: "nginx"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"ansible.builtin.apt:", "- nginx", `mode: "0644"`, "content: |", "notify: Restart nginx", "handlers:"} {
		if !strings.Contains(y, s) {
			t.Errorf("playbook sem %q:\n%s", s, y)
		}
	}
	yamlOK(t, "playbook", y)
	if _, err := Playbook(PlaybookSpec{Tasks: []AnsibleTask{{Type: "apt"}}}); err == nil {
		t.Fatal("campo obrigatório deveria falhar")
	}
	for part, c := range RoleSkeleton("nginx", "Servidor web", "ops") {
		yamlOK(t, "role/"+part, c)
	}
}

func TestTofu(t *testing.T) {
	f, err := Tofu(TofuSpec{Provider: "aws", Name: "lab", Region: "sa-east-1", Network: true, NetCIDR: "10.20.0.0/16",
		SubnetCIDR: "10.20.1.0/24", SSHCIDR: "203.0.113.10/32", Compute: true, UseDebianAMI: true, Bucket: true,
		BucketName: "lab-bucket-123", Versioning: true, Tags: "Env=lab"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`http_tokens   = "required"`, "aws_s3_bucket_public_access_block", "data.aws_ami.debian.id", `"${var.name}-vpc"`} {
		if !strings.Contains(f["main.tf"], s) {
			t.Errorf("main.tf sem %q", s)
		}
	}
	if _, err := Tofu(TofuSpec{Provider: "aws", Name: "lab", Region: "x", Network: true, NetCIDR: "10.0.0.0/16",
		SubnetCIDR: "10.0.1.0/24", SSHCIDR: "0.0.0.0/0"}); err == nil {
		t.Fatal("SSH aberto ao mundo deveria ser recusado")
	}
	o, err := Tofu(TofuSpec{Provider: "oci", Name: "lab", Region: "sa-saopaulo-1", Network: true, NetCIDR: "10.30.0.0/16",
		SubnetCIDR: "10.30.1.0/24", SSHCIDR: "198.51.100.0/24", Compute: true, Shape: "VM.Standard.E5.Flex", BlockVolume: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(o["main.tf"], "oci_core_volume_attachment") || !strings.Contains(o["main.tf"], "shape_config") {
		t.Fatal("OCI sem attachment/shape_config")
	}
	if hcl("${x}") != `"$${x}"` {
		t.Fatal("hcl não escapou interpolação")
	}
}

func TestK8sTemplates(t *testing.T) {
	for _, tp := range K8sTemplates() {
		files, err := RenderK8s(tp.ID, Values{})
		if err != nil {
			t.Errorf("%s: %v", tp.ID, err)
			continue
		}
		for name, c := range files {
			if strings.HasSuffix(name, ".yaml") && !strings.Contains(name, "templates/") {
				yamlOK(t, tp.ID+"/"+name, c)
			}
		}
	}
	if _, err := RenderK8s("rbac", Values{"verbs": "*"}); err == nil {
		t.Fatal("verbo * deveria ser recusado")
	}
	f, _ := RenderK8s("deployment", Values{})
	d := f["web-deployment.yaml"]
	for _, s := range []string{"readOnlyRootFilesystem: true", "- ALL", "readinessProbe:", "maxUnavailable: 0"} {
		if !strings.Contains(d, s) {
			t.Errorf("deployment sem %q:\n%s", s, d)
		}
	}
}
