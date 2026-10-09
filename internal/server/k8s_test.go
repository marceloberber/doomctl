package server

import "testing"

func TestParseKubectl(t *testing.T) {
	ok := []string{"get pods -A", "kubectl describe pod web-1 -n prod", "logs deploy/web --tail=50", "rollout undo deploy/web", "auth can-i --list"}
	for _, c := range ok {
		if _, err := parseKubectl(c); err != nil {
			t.Errorf("%q deveria passar: %v", c, err)
		}
	}
	bad := []string{"delete pod x", "exec -it web -- sh", "get pods --kubeconfig=/etc/x", "apply -f x.yaml", "get pods --token abc", "rollout bogus", "auth whoami"}
	for _, c := range bad {
		if _, err := parseKubectl(c); err == nil {
			t.Errorf("%q deveria ser bloqueado", c)
		}
	}
}
