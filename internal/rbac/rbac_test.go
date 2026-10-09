package rbac

import "testing"

func TestNormalize(t *testing.T) {
	if p := Normalize(RoleViewer, "docker", Perm{Read: true, Manage: true}); p.Manage {
		t.Fatal("visualizador nunca pode gerenciar")
	}
	if p := Normalize(RoleOperator, "users", Perm{Read: true, Manage: true}); p.Read || p.Manage {
		t.Fatal("módulo exclusivo de admin não pode ser liberado")
	}
	if p := Normalize(RoleOperator, "docker", Perm{Manage: true}); !p.Read {
		t.Fatal("gerenciar implica ler")
	}
	d := Defaults()
	if d[RoleViewer]["logs"].Read != true || d[RoleViewer]["docker"].Read {
		t.Fatal("visualizador deve ver só relatórios e logs por padrão")
	}
}
