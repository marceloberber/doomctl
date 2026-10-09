package safeenv

import (
	"strings"
	"testing"
)

func TestEnvNaoVazaSegredos(t *testing.T) {
	t.Setenv("DOOMCTL_SECRET_KEY", "segredo")
	t.Setenv("DOOMCTL_DATABASE_URL", "postgres://x:y@z/db")
	env := strings.Join(Env("EXTRA=1"), "\n")
	if strings.Contains(env, "segredo") || strings.Contains(env, "postgres://") {
		t.Fatal("variáveis sensíveis vazaram para processos filhos")
	}
	if !strings.Contains(env, "EXTRA=1") || !strings.Contains(env, "PATH=") {
		t.Fatal("ambiente esperado ausente")
	}
}
