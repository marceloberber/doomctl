package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/doomctl/doomctl/internal/jobs"
	"github.com/doomctl/doomctl/internal/rbac"
)

type LogEntry struct {
	ID         int64           `json:"id"`
	Module     string          `json:"module"`
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Username   string          `json:"username"`
	Status     string          `json:"status"`
	ExitCode   *int            `json:"exit_code"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     *string         `json:"output,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Artifact   bool            `json:"has_artifact"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	DurationMs *int64          `json:"duration_ms"`
}

type logFilter struct {
	Module, Status, Query string
	Limit, Offset         int
	ExcludeAuth           bool
}

func (s *Server) queryLogs(ctx context.Context, f logFilter) []LogEntry {
	where := []string{"true"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.Module != "" {
		add("module = ?", f.Module)
	}
	if f.ExcludeAuth {
		where = append(where, "module NOT IN ('auth','users')")
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.Query != "" {
		add("(target ILIKE ? OR action ILIKE ? OR username ILIKE ?)", "%"+f.Query+"%")
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	args = append(args, f.Limit, f.Offset)
	q := fmt.Sprintf(`SELECT id, module, action, target, username, status, exit_code, artifact <> '', started_at, finished_at, duration_ms
		FROM tool_logs WHERE %s ORDER BY started_at DESC, id DESC LIMIT $%d OFFSET $%d`, strings.Join(where, " AND "), len(args)-1, len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	out := []LogEntry{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.Module, &e.Action, &e.Target, &e.Username, &e.Status, &e.ExitCode, &e.Artifact,
			&e.StartedAt, &e.FinishedAt, &e.DurationMs); err == nil {
			out = append(out, e)
		}
	}
	return out
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request, u *User) {
	qv := r.URL.Query()
	f := logFilter{Module: qv.Get("module"), Status: qv.Get("status"), Query: strings.TrimSpace(qv.Get("q"))}
	f.Limit, _ = strconv.Atoi(qv.Get("limit"))
	f.Offset, _ = strconv.Atoi(qv.Get("offset"))
	// eventos de autenticação/usuários só para administradores
	if u.Role != rbac.RoleAdmin {
		if f.Module == "auth" || f.Module == "users" {
			writeJSON(w, 200, map[string]any{"items": []LogEntry{}, "total": 0})
			return
		}
		f.ExcludeAuth = true
	}
	items := s.queryLogs(r.Context(), f)
	var total int64
	cnt := `SELECT count(*) FROM tool_logs WHERE ($1 = '' OR module=$1) AND ($2 = '' OR status=$2)
		AND ($3 = '' OR target ILIKE '%'||$3||'%' OR action ILIKE '%'||$3||'%' OR username ILIKE '%'||$3||'%')`
	if f.ExcludeAuth {
		cnt += ` AND module NOT IN ('auth','users')`
	}
	s.db.QueryRowContext(r.Context(), cnt, f.Module, f.Status, f.Query).Scan(&total)
	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}

func (s *Server) getLog(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var e LogEntry
	var out string
	var res []byte
	err = s.db.QueryRowContext(r.Context(), `SELECT id, module, action, target, username, status, exit_code, input, output, result,
		artifact <> '', started_at, finished_at, duration_ms FROM tool_logs WHERE id=$1`, id).
		Scan(&e.ID, &e.Module, &e.Action, &e.Target, &e.Username, &e.Status, &e.ExitCode, &e.Input, &out, &res,
			&e.Artifact, &e.StartedAt, &e.FinishedAt, &e.DurationMs)
	if err != nil {
		failErr(w, err)
		return
	}
	if (e.Module == "auth" || e.Module == "users") && u.Role != rbac.RoleAdmin {
		fail(w, 403, "somente administradores")
		return
	}
	e.Output = &out
	if len(res) > 0 {
		e.Result = res
	}
	writeJSON(w, 200, e)
}

func (s *Server) logArtifact(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var art string
	if err := s.db.QueryRow(`SELECT artifact FROM tool_logs WHERE id=$1`, id).Scan(&art); err != nil {
		failErr(w, err)
		return
	}
	if art == "" {
		fail(w, 404, "execução sem artefato")
		return
	}
	// o artefato precisa estar dentro de reports/
	p := filepath.Clean(art)
	if !strings.HasPrefix(p, s.cfg.Path("reports")+string(os.PathSeparator)) {
		fail(w, 403, "caminho de artefato inválido")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(p)))
	http.ServeFile(w, r, p)
}

func (s *Server) purgeLogs(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct{ Days int }
	if err := decode(r, &in); err != nil || in.Days < 1 {
		fail(w, 400, "informe days >= 1")
		return
	}
	rows, _ := s.db.Query(`SELECT artifact FROM tool_logs WHERE started_at < now() - make_interval(days => $1) AND artifact <> ''`, in.Days)
	if rows != nil {
		for rows.Next() {
			var a string
			if rows.Scan(&a) == nil && strings.HasPrefix(filepath.Clean(a), s.cfg.Path("reports")) {
				os.Remove(a)
			}
		}
		rows.Close()
	}
	res, err := s.db.Exec(`DELETE FROM tool_logs WHERE started_at < now() - make_interval(days => $1) AND status NOT IN ('queued','running')`, in.Days)
	if err != nil {
		failErr(w, err)
		return
	}
	n, _ := res.RowsAffected()
	s.audit("logs", "purge", fmt.Sprintf("> %d dias", in.Days), u, "info", fmt.Sprintf("%d registros removidos", n), nil)
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// ---------- jobs ----------

func (s *Server) canSeeJob(u *User, j *jobs.Job) bool {
	return j.UserID == u.ID || s.can(u, j.Module, rbac.Read) || (j.Module == "kubernetes" && s.can(u, "kubernetes", rbac.Read))
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, u *User) {
	out := []*jobs.Job{}
	for _, j := range s.jobs.Running() {
		if s.canSeeJob(u, j) {
			out = append(out, j)
		}
	}
	writeJSON(w, 200, out)
}

// streamJob envia a saída via Server-Sent Events.
func (s *Server) streamJob(w http.ResponseWriter, r *http.Request, u *User) {
	j := s.jobs.Get(r.PathValue("id"))
	if j == nil {
		fail(w, 404, "execução não encontrada (consulte os Logs)")
		return
	}
	if !s.canSeeJob(u, j) {
		fail(w, 403, "sem permissão")
		return
	}
	fl, okf := w.(http.Flusher)
	if !okf {
		fail(w, 500, "streaming não suportado")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(ev string, data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, b)
		fl.Flush()
	}
	backlog, ch, unsub := j.Subscribe()
	defer unsub()
	send("meta", map[string]any{"id": j.ID, "log_id": j.LogID, "module": j.Module, "action": j.Action, "target": j.Target})
	if backlog != "" {
		send("chunk", backlog)
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case b, open := <-ch:
			if !open {
				<-j.Done()
				var status string
				var code sql.NullInt64
				var result []byte
				s.db.QueryRow(`SELECT status, exit_code, result FROM tool_logs WHERE id=$1`, j.LogID).Scan(&status, &code, &result)
				var res any
				if len(result) > 0 {
					json.Unmarshal(result, &res)
				}
				// envia o que faltou (assinante lento pode ter perdido chunks)
				full, _ := j.Snapshot()
				send("end", map[string]any{"status": status, "exit_code": code.Int64, "log_id": j.LogID, "result": res, "length": len(full)})
				return
			}
			send("chunk", string(b))
		}
	}
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, u *User) {
	j := s.jobs.Get(r.PathValue("id"))
	if j == nil {
		fail(w, 404, "execução não encontrada")
		return
	}
	if j.UserID != u.ID && !s.can(u, j.Module, rbac.Manage) {
		fail(w, 403, "sem permissão")
		return
	}
	s.jobs.Cancel(j.ID)
	ok(w)
}

// startJob preenche usuário/ambiente comuns e responde {job_id, log_id}.
func (s *Server) startJob(w http.ResponseWriter, u *User, spec jobs.Spec) {
	spec.UserID, spec.Username = u.ID, u.Username
	spec.Env = append(s.toolEnv(), spec.Env...)
	j, err := s.jobs.Start(spec)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"job_id": j.ID, "log_id": j.LogID})
}

// runDir cria um diretório temporário privado para uma execução.
func (s *Server) runDir() (string, func(), error) {
	d, err := os.MkdirTemp(s.cfg.Path("run"), "job-")
	if err != nil {
		return "", nil, err
	}
	os.Chmod(d, 0o700)
	return d, func() { os.RemoveAll(d) }, nil
}
