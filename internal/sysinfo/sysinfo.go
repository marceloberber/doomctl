// Package sysinfo executa comandos curtos e síncronos e detecta as ferramentas
// instaladas no servidor (ansible, tofu, trivy, docker, kubectl...).
package sysinfo

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/doomctl/doomctl/internal/safeenv"
)

// Run executa argv sem shell e devolve stdout, stderr e exit code.
func Run(ctx context.Context, timeout time.Duration, stdin []byte, env []string, args ...string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = safeenv.Env(env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), errb.String(), ee.ExitCode(), nil
	}
	if err != nil {
		return out.String(), errb.String(), -1, err
	}
	return out.String(), errb.String(), 0, nil
}

type Tool struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Path      string `json:"path"`
}

var tools = []struct {
	name string
	args []string
}{
	{"ansible", []string{"ansible", "--version"}},
	{"ansible-lint", []string{"ansible-lint", "--version"}},
	{"tofu", []string{"tofu", "version"}},
	{"trivy", []string{"trivy", "--version"}},
	{"docker", []string{"docker", "--version"}},
	{"docker-compose", []string{"docker", "compose", "version", "--short"}},
	{"kubectl", []string{"kubectl", "version", "--client"}},
	{"python3", []string{"python3", "--version"}},
	{"sshpass", []string{"sshpass", "-V"}},
}

var verRe = regexp.MustCompile(`v?(\d+\.\d+(\.\d+)?)`)

var (
	cacheMu sync.Mutex
	cache   []Tool
	cacheAt time.Time
)

// Tools detecta as ferramentas (cache de 5 min).
func Tools(ctx context.Context) []Tool {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cache != nil && time.Since(cacheAt) < 5*time.Minute {
		return cache
	}
	out := make([]Tool, len(tools))
	var wg sync.WaitGroup
	for i, t := range tools {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tl := Tool{Name: t.name}
			p, err := exec.LookPath(t.args[0])
			if err == nil {
				tl.Path = p
				so, se, code, err := Run(ctx, 8*time.Second, nil, nil, t.args...)
				if err == nil && code == 0 {
					tl.Installed = true
					first := strings.SplitN(strings.TrimSpace(so+se), "\n", 2)[0]
					if m := verRe.FindStringSubmatch(first); m != nil {
						tl.Version = m[1]
					} else {
						tl.Version = first
					}
				}
			}
			out[i] = tl
		}()
	}
	wg.Wait()
	cache, cacheAt = out, time.Now()
	return out
}

func Has(ctx context.Context, name string) bool {
	for _, t := range Tools(ctx) {
		if t.Name == name {
			return t.Installed
		}
	}
	return false
}

// InvalidateCache força nova detecção (ex.: após instalar algo).
func InvalidateCache() {
	cacheMu.Lock()
	cache = nil
	cacheMu.Unlock()
}
