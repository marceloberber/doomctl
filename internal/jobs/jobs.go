// Package jobs executa comandos das ferramentas (ansible, tofu, trivy,
// docker, kubectl) de forma assíncrona, com streaming da saída para múltiplos
// assinantes (SSE), limite de concorrência e registro em tool_logs.
package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/doomctl/doomctl/internal/safeenv"
	"github.com/doomctl/doomctl/internal/secure"
)

const maxOutput = 4 << 20 // 4 MiB por job

type Step struct {
	Name  string   // exibido como cabeçalho na saída
	Args  []string // argv (sem shell)
	Stdin []byte
	// AllowFail: continua para o próximo passo mesmo com exit != 0.
	AllowFail bool
	// Func: passo implementado em Go (ex.: chamadas às APIs AWS/OCI). Quando
	// definido, Args é ignorado; a saída escrita em w vai para o log do job.
	Func func(ctx context.Context, w io.Writer) error
}

type Spec struct {
	Module, Action, Target string
	UserID                 int64
	Username               string
	Input                  any
	Steps                  []Step
	Dir                    string
	Env                    []string
	Timeout                time.Duration
	Mask                   []string // valores sensíveis a mascarar na saída
	// Finish roda após o último passo (ex.: parse de XML/JSON). Pode alterar o
	// status e devolver um resultado estruturado e um artefato (caminho).
	Finish  func(ctx context.Context, r *Result)
	Cleanup func()
}

type Result struct {
	Status   string
	ExitCode int
	Output   string
	Data     any
	Artifact string
	Extra    string // texto anexado ao final da saída
}

type Job struct {
	ID       string    `json:"id"`
	LogID    int64     `json:"log_id"`
	Module   string    `json:"module"`
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	UserID   int64     `json:"user_id"`
	Username string    `json:"username"`
	Status   string    `json:"status"`
	Started  time.Time `json:"started_at"`

	mu     sync.Mutex
	buf    bytes.Buffer
	trunc  bool
	subs   map[chan []byte]struct{}
	done   chan struct{}
	ended  bool // saída encerrada (novos assinantes recebem o canal já fechado)
	cancel context.CancelFunc
	mask   []string
}

type Manager struct {
	db   *sql.DB
	sem  chan struct{}
	mu   sync.Mutex
	jobs map[string]*Job
}

func NewManager(db *sql.DB, max int) *Manager {
	return &Manager{db: db, sem: make(chan struct{}, max), jobs: map[string]*Job{}}
}

// MarkInterrupted marca execuções que ficaram "running" após um restart.
func (m *Manager) MarkInterrupted(ctx context.Context) {
	_, err := m.db.ExecContext(ctx, `UPDATE tool_logs SET status='interrupted', finished_at=now(),
		output = output || E'\n[doomctl] execução interrompida (servidor reiniciado)'
		WHERE status IN ('queued','running')`)
	if err != nil {
		slog.Error("marcando jobs interrompidos", "erro", err)
	}
}

func (m *Manager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (m *Manager) Running() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Job{}
	for _, j := range m.jobs {
		j.mu.Lock()
		st := j.Status
		j.mu.Unlock()
		if st == "queued" || st == "running" {
			out = append(out, j)
		}
	}
	return out
}

// Start registra o log, devolve o Job e executa em background.
func (m *Manager) Start(spec Spec) (*Job, error) {
	in, _ := json.Marshal(spec.Input)
	if spec.Input == nil {
		in = []byte("{}")
	}
	var uid any
	if spec.UserID > 0 {
		uid = spec.UserID
	}
	var logID int64
	err := m.db.QueryRow(`INSERT INTO tool_logs(module, action, target, user_id, username, status, input)
		VALUES ($1,$2,$3,$4,$5,'queued',$6) RETURNING id`,
		spec.Module, spec.Action, spec.Target, uid, spec.Username, string(in)).Scan(&logID)
	if err != nil {
		if spec.Cleanup != nil {
			spec.Cleanup()
		}
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	if spec.Timeout <= 0 {
		spec.Timeout = 2 * time.Hour
	}
	j := &Job{
		ID: secure.RandomToken(12), LogID: logID, Module: spec.Module, Action: spec.Action,
		Target: spec.Target, UserID: spec.UserID, Username: spec.Username, Status: "queued",
		Started: time.Now(), subs: map[chan []byte]struct{}{}, done: make(chan struct{}),
		cancel: cancel,
	}
	for _, s := range spec.Mask {
		if len(s) >= 3 {
			j.mask = append(j.mask, s)
		}
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.mu.Unlock()
	go m.run(ctx, j, spec)
	return j, nil
}

func (m *Manager) run(ctx context.Context, j *Job, spec Spec) {
	defer func() {
		if spec.Cleanup != nil {
			spec.Cleanup()
		}
		// mantém o job em memória por 30 min para reconexões SSE
		time.AfterFunc(30*time.Minute, func() {
			m.mu.Lock()
			delete(m.jobs, j.ID)
			m.mu.Unlock()
		})
	}()

	select {
	case m.sem <- struct{}{}:
	default:
		j.write([]byte("[doomctl] aguardando slot de execução...\n"))
		select {
		case m.sem <- struct{}{}:
		case <-ctx.Done():
			m.finish(j, &Result{Status: "canceled", ExitCode: -1})
			return
		}
	}
	defer func() { <-m.sem }()

	j.setStatus("running")
	m.db.Exec(`UPDATE tool_logs SET status='running', started_at=now() WHERE id=$1`, j.LogID)

	ctx, cancelT := context.WithTimeout(ctx, spec.Timeout)
	defer cancelT()

	res := &Result{Status: "success"}
	for i, st := range spec.Steps {
		if len(spec.Steps) > 1 || st.Name != "" {
			name := st.Name
			if name == "" {
				name = strings.Join(st.Args, " ")
			}
			j.write([]byte(fmt.Sprintf("\n━━ [%d/%d] %s\n", i+1, len(spec.Steps), name)))
		}
		code, err := m.exec(ctx, j, spec, st)
		res.ExitCode = code
		if ctx.Err() != nil {
			res.Status = "canceled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				res.Status = "failed"
				j.write([]byte("\n[doomctl] tempo limite excedido\n"))
			}
			break
		}
		if err != nil {
			j.write([]byte("\n[doomctl] erro: " + err.Error() + "\n"))
			res.Status = "failed"
			break
		}
		if code != 0 && !st.AllowFail {
			res.Status = "failed"
			break
		}
	}
	j.mu.Lock()
	res.Output = j.buf.String()
	j.mu.Unlock()
	if spec.Finish != nil && res.Status != "canceled" {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("finish panic", "job", j.ID, "panic", r)
				}
			}()
			spec.Finish(context.Background(), res)
		}()
		if res.Extra != "" {
			j.write([]byte(res.Extra))
		}
	}
	m.finish(j, res)
}

func (m *Manager) exec(ctx context.Context, j *Job, spec Spec, st Step) (int, error) {
	if st.Func != nil {
		if err := st.Func(ctx, jobWriter{j}); err != nil {
			if ctx.Err() != nil {
				return -1, nil
			}
			j.write([]byte("\n[doomctl] erro: " + err.Error() + "\n"))
			return 1, nil
		}
		return 0, nil
	}
	if len(st.Args) == 0 {
		return 0, nil
	}
	cmd := exec.CommandContext(ctx, st.Args[0], st.Args[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = safeenv.Env(spec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { // mata o grupo de processos inteiro
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	if st.Stdin != nil {
		cmd.Stdin = bytes.NewReader(st.Stdin)
	}
	w := jobWriter{j}
	cmd.Stdout, cmd.Stderr = w, w
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func (m *Manager) finish(j *Job, res *Result) {
	j.mu.Lock()
	out := j.buf.String()
	if j.trunc {
		out += "\n[doomctl] saída truncada em 4 MiB\n"
	}
	j.Status = res.Status
	subs := j.subs
	j.subs = map[chan []byte]struct{}{}
	j.ended = true
	j.mu.Unlock()

	var data any
	if res.Data != nil {
		b, _ := json.Marshal(res.Data)
		data = string(b)
	}
	_, err := m.db.Exec(`UPDATE tool_logs SET status=$2, exit_code=$3, output=$4, result=$5::jsonb, artifact=$6,
		finished_at=now(), duration_ms=(EXTRACT(EPOCH FROM (now()-started_at))*1000)::bigint WHERE id=$1`,
		j.LogID, res.Status, res.ExitCode, strings.ReplaceAll(strings.ToValidUTF8(out, "?"), "\x00", ""), data, res.Artifact)
	if err != nil {
		slog.Error("salvando log do job", "erro", err)
	}
	for ch := range subs {
		close(ch)
	}
	close(j.done)
}

func (m *Manager) Cancel(id string) bool {
	j := m.Get(id)
	if j == nil {
		return false
	}
	j.cancel()
	return true
}

// ---------- Job: buffer + pub/sub ----------

type jobWriter struct{ j *Job }

func (w jobWriter) Write(p []byte) (int, error) { w.j.write(p); return len(p), nil }

func (j *Job) write(p []byte) {
	s := string(p)
	for _, m := range j.mask {
		s = strings.ReplaceAll(s, m, "********")
	}
	p = []byte(s)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.buf.Len()+len(p) > maxOutput {
		j.trunc = true
		return
	}
	j.buf.Write(p)
	for ch := range j.subs {
		select {
		case ch <- p:
		default: // assinante lento: descarta o chunk (o log final é completo)
		}
	}
}

func (j *Job) setStatus(s string) {
	j.mu.Lock()
	j.Status = s
	j.mu.Unlock()
}

func (j *Job) Snapshot() (string, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.String(), j.Status
}

// Subscribe devolve a saída acumulada e um canal com os próximos chunks.
// O canal é fechado quando o job termina.
func (j *Job) Subscribe() (string, chan []byte, func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	ch := make(chan []byte, 256)
	// ended é marcado no início de finish(): quem assina depois disso não pode
	// entrar em j.subs (nunca seria fechado) — recebe o canal fechado e espera Done().
	if j.ended {
		close(ch)
		return j.buf.String(), ch, func() {}
	}
	j.subs[ch] = struct{}{}
	return j.buf.String(), ch, func() {
		j.mu.Lock()
		if _, ok := j.subs[ch]; ok {
			delete(j.subs, ch)
			close(ch)
		}
		j.mu.Unlock()
	}
}

func (j *Job) Done() <-chan struct{} { return j.done }

// Wait bloqueia até o término (usado em testes e chamadas síncronas).
func (j *Job) Wait(ctx context.Context) error {
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ io.Writer = jobWriter{}
