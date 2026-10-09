package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/doomctl/doomctl/internal/rbac"
	"github.com/doomctl/doomctl/internal/secure"
	"github.com/doomctl/doomctl/internal/sysinfo"
)

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,31}$`)

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, _ *User) {
	rows, err := s.db.Query(`SELECT ` + userCols + `, (SELECT count(*) FROM sessions s WHERE s.user_id=u.id AND s.kind='full' AND s.expires_at>now())
		FROM users u ORDER BY u.username`)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	type row struct {
		*User
		Sessions int  `json:"sessions"`
		Locked   bool `json:"locked"`
	}
	out := []row{}
	for rows.Next() {
		var n int
		u, err := scanUser(rows, &n)
		if err != nil {
			failErr(w, err)
			return
		}
		out = append(out, row{u, n, u.lockedUntil != nil && u.lockedUntil.After(s.now())})
	}
	writeJSON(w, 200, out)
}

type userInput struct {
	Username    string `json:"username"`
	FullName    string `json:"full_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	Active      *bool  `json:"active"`
	Password    string `json:"password"`
	MFARequired bool   `json:"mfa_required"`
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, admin *User) {
	var in userInput
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	if !usernameRe.MatchString(in.Username) {
		fail(w, 400, "usuário: 3–32 caracteres, começando com letra (a-z, 0-9, . _ -)")
		return
	}
	if !rbac.ValidRole(in.Role) {
		fail(w, 400, "perfil inválido")
		return
	}
	generated := ""
	if in.Password == "" {
		in.Password = secure.RandomPassword()
		generated = in.Password
	} else if err := secure.ValidatePasswordPolicy(in.Password); err != nil {
		fail(w, 400, err.Error())
		return
	}
	h, err := secure.HashPassword(in.Password)
	if err != nil {
		failErr(w, err)
		return
	}
	var id int64
	err = s.db.QueryRow(`INSERT INTO users(username, full_name, email, role, password_hash, must_change_password, mfa_required)
		VALUES ($1,$2,$3,$4,$5,true,$6) RETURNING id`, in.Username, truncate(in.FullName, 120), truncate(in.Email, 200),
		in.Role, h, in.MFARequired).Scan(&id)
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("users", "create", in.Username, admin, "success", fmt.Sprintf("usuário criado com perfil %s (MFA exigido: %v)", in.Role, in.MFARequired), nil)
	writeJSON(w, 201, map[string]any{"id": id, "temporary_password": generated})
}

func (s *Server) countAdmins(ctx context.Context, exclude int64) int {
	var n int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role='admin' AND active AND id<>$1`, exclude).Scan(&n)
	return n
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, admin *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in userInput
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	u, err := s.userByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	if in.Role == "" {
		in.Role = u.Role
	}
	if !rbac.ValidRole(in.Role) {
		fail(w, 400, "perfil inválido")
		return
	}
	active := u.Active
	if in.Active != nil {
		active = *in.Active
	}
	if u.Role == rbac.RoleAdmin && (in.Role != rbac.RoleAdmin || !active) && s.countAdmins(r.Context(), u.ID) == 0 {
		fail(w, 400, "não é possível remover o último administrador ativo")
		return
	}
	_, err = s.db.Exec(`UPDATE users SET full_name=$2, email=$3, role=$4, active=$5, updated_at=now() WHERE id=$1`,
		id, truncate(in.FullName, 120), truncate(in.Email, 200), in.Role, active)
	if err != nil {
		failErr(w, err)
		return
	}
	if !active || in.Role != u.Role {
		s.db.Exec(`DELETE FROM sessions WHERE user_id=$1`, id)
	}
	s.audit("users", "update", u.Username, admin, "success", fmt.Sprintf("perfil=%s ativo=%v", in.Role, active), nil)
	ok(w)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, admin *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if id == admin.ID {
		fail(w, 400, "você não pode excluir o próprio usuário")
		return
	}
	u, err := s.userByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	if u.Role == rbac.RoleAdmin && s.countAdmins(r.Context(), u.ID) == 0 {
		fail(w, 400, "não é possível excluir o último administrador")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM users WHERE id=$1`, id); err != nil {
		failErr(w, err)
		return
	}
	s.audit("users", "delete", u.Username, admin, "success", "usuário excluído", nil)
	ok(w)
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request, admin *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct{ Password string }
	decode(r, &in)
	u, err := s.userByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	generated := ""
	if in.Password == "" {
		in.Password = secure.RandomPassword()
		generated = in.Password
	} else if err := secure.ValidatePasswordPolicy(in.Password); err != nil {
		fail(w, 400, err.Error())
		return
	}
	h, _ := secure.HashPassword(in.Password)
	s.db.Exec(`UPDATE users SET password_hash=$2, must_change_password=true, failed_logins=0, locked_until=NULL, updated_at=now() WHERE id=$1`, id, h)
	s.db.Exec(`DELETE FROM sessions WHERE user_id=$1`, id)
	s.audit("users", "password_reset", u.Username, admin, "success", "senha redefinida pelo administrador", nil)
	writeJSON(w, 200, map[string]any{"temporary_password": generated})
}

// adminMFA: o administrador pode exigir, liberar ou desativar (resetar) o MFA de outro usuário.
func (s *Server) adminMFA(w http.ResponseWriter, r *http.Request, admin *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	var in struct{ Action string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	u, err := s.userByID(r.Context(), id)
	if err != nil {
		failErr(w, err)
		return
	}
	var msg string
	switch in.Action {
	case "require": // ativar/exigir: sem MFA cadastrado, o próximo login força o cadastro
		s.db.Exec(`UPDATE users SET mfa_required=true, updated_at=now() WHERE id=$1`, id)
		if !u.MFAEnabled {
			s.db.Exec(`DELETE FROM sessions WHERE user_id=$1`, id)
		}
		msg = "MFA passou a ser obrigatório"
	case "release": // deixa de exigir (o usuário decide se mantém)
		s.db.Exec(`UPDATE users SET mfa_required=false, updated_at=now() WHERE id=$1`, id)
		msg = "MFA deixou de ser obrigatório"
	case "disable": // desativa e apaga o segredo (ex.: celular perdido)
		s.db.Exec(`UPDATE users SET mfa_enabled=false, mfa_required=false, mfa_secret=NULL, mfa_pending_secret=NULL,
			mfa_recovery='[]', mfa_last_step=0, updated_at=now() WHERE id=$1`, id)
		msg = "MFA desativado e segredo removido"
	case "reset": // apaga o segredo mas mantém a exigência (recadastro no próximo login)
		s.db.Exec(`UPDATE users SET mfa_enabled=false, mfa_required=true, mfa_secret=NULL, mfa_pending_secret=NULL,
			mfa_recovery='[]', mfa_last_step=0, updated_at=now() WHERE id=$1`, id)
		s.db.Exec(`DELETE FROM sessions WHERE user_id=$1`, id)
		msg = "MFA resetado; novo cadastro exigido no próximo login"
	default:
		fail(w, 400, "ação inválida (require, release, disable, reset)")
		return
	}
	s.audit("users", "mfa_"+in.Action, u.Username, admin, "success", msg, nil)
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

func (s *Server) adminRevokeSessions(w http.ResponseWriter, r *http.Request, admin *User) {
	id, err := pathID(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.db.Exec(`DELETE FROM sessions WHERE user_id=$1`, id)
	s.audit("users", "revoke_sessions", strconv.FormatInt(id, 10), admin, "success", "sessões encerradas", nil)
	ok(w)
}

// ---------- permissões ----------

func (s *Server) getPermissions(w http.ResponseWriter, r *http.Request, _ *User) {
	writeJSON(w, 200, map[string]any{"modules": rbac.Modules, "matrix": s.perms.all()})
}

func (s *Server) putPermissions(w http.ResponseWriter, r *http.Request, admin *User) {
	var in map[string]map[string]rbac.Perm
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		failErr(w, err)
		return
	}
	defer tx.Rollback()
	for role, mods := range in {
		if role != rbac.RoleOperator && role != rbac.RoleViewer {
			continue
		}
		for mod, p := range mods {
			if !rbac.ModuleExists(mod) {
				continue
			}
			p = rbac.Normalize(role, mod, p)
			if _, err := tx.Exec(`INSERT INTO role_permissions(role, module, can_read, can_manage) VALUES ($1,$2,$3,$4)
				ON CONFLICT (role, module) DO UPDATE SET can_read=EXCLUDED.can_read, can_manage=EXCLUDED.can_manage`,
				role, mod, p.Read, p.Manage); err != nil {
				failErr(w, err)
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		failErr(w, err)
		return
	}
	s.perms.load(r.Context(), s.db)
	s.audit("users", "permissions", "matriz", admin, "success", "matriz de permissões atualizada", in)
	writeJSON(w, 200, s.perms.all())
}

// ---------- sistema ----------

func (s *Server) systemInfo(w http.ResponseWriter, r *http.Request, _ *User) {
	if r.URL.Query().Get("refresh") == "1" {
		sysinfo.InvalidateCache()
	}
	var dbVer string
	s.db.QueryRow(`SHOW server_version`).Scan(&dbVer)
	_, sockErr := os.Stat(s.cfg.DockerSocket)
	var dataSize int64
	filepath.Walk(s.cfg.Path("reports"), func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			dataSize += fi.Size()
		}
		return nil
	})
	writeJSON(w, 200, map[string]any{
		"version": s.cfg.Version, "go": runtime.Version(), "os": runtime.GOOS + "/" + runtime.GOARCH,
		"postgres": dbVer, "tools": sysinfo.Tools(r.Context()), "ollama": s.ai.LLM().Status(r.Context()),
		"docker_socket": map[string]any{"path": s.cfg.DockerSocket, "present": sockErr == nil},
		"config": map[string]any{
			"listen": s.cfg.Listen, "tls_mode": s.cfg.TLSMode, "data_dir": s.cfg.DataDir, "session_ttl": s.cfg.SessionTTL.String(),
			"max_jobs": s.cfg.MaxJobs, "ollama_host": s.cfg.OllamaHost,
			"ollama_model": s.cfg.OllamaModel, "router_temperature": s.cfg.RouterTemperature,
			"portainer_image": s.cfg.PortainerImage, "portainer_port": s.cfg.PortainerPort,
		},
		"reports_bytes": dataSize, "running_jobs": len(s.jobs.Running()), "time": s.now(),
	})
}

// ---------- dashboard ----------

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, u *User) {
	ctx := r.Context()
	period := r.URL.Query().Get("period")
	interval := "7 days"
	switch period {
	case "day":
		interval = "1 day"
	case "month":
		interval = "30 days"
	}
	q := func(query string, args ...any) int64 {
		var n sql.NullInt64
		s.db.QueryRowContext(ctx, query, args...).Scan(&n)
		return n.Int64
	}
	excl := `module NOT IN ('auth','users')`
	total := q(`SELECT count(*) FROM tool_logs WHERE `+excl+` AND status IN ('success','failed','canceled','interrupted') AND started_at > now() - $1::interval`, interval)
	success := q(`SELECT count(*) FROM tool_logs WHERE `+excl+` AND status='success' AND started_at > now() - $1::interval`, interval)
	failed := q(`SELECT count(*) FROM tool_logs WHERE `+excl+` AND status IN ('failed','interrupted') AND started_at > now() - $1::interval`, interval)
	prevTotal := q(`SELECT count(*) FROM tool_logs WHERE `+excl+` AND status IN ('success','failed','canceled','interrupted')
		AND started_at BETWEEN now() - 2*$1::interval AND now() - $1::interval`, interval)
	aiQ := q(`SELECT count(*) FROM tool_logs WHERE module='ai' AND started_at > now() - $1::interval`, interval)
	var critical int64
	var lastTrivy sql.NullString
	s.db.QueryRowContext(ctx, `SELECT COALESCE((result->'summary'->>'CRITICAL')::bigint,0), target FROM tool_logs
		WHERE module='trivy' AND status='success' AND result IS NOT NULL ORDER BY started_at DESC LIMIT 1`).Scan(&critical, &lastTrivy)

	byModule := map[string]int64{}
	rows, err := s.db.QueryContext(ctx, `SELECT module, count(*) FROM tool_logs WHERE `+excl+` AND started_at > now() - $1::interval GROUP BY module`, interval)
	if err == nil {
		for rows.Next() {
			var m string
			var n int64
			rows.Scan(&m, &n)
			byModule[m] = n
		}
		rows.Close()
	}
	// série diária (execuções por dia, sucesso x falha)
	type day struct {
		Day     string `json:"day"`
		Success int64  `json:"success"`
		Failed  int64  `json:"failed"`
	}
	days := 7
	if period == "month" {
		days = 30
	} else if period == "day" {
		days = 1
	}
	series := []day{}
	rows, err = s.db.QueryContext(ctx, `SELECT to_char(d, 'YYYY-MM-DD'),
		(SELECT count(*) FROM tool_logs t WHERE `+excl+` AND t.status='success' AND t.started_at::date = d::date),
		(SELECT count(*) FROM tool_logs t WHERE `+excl+` AND t.status IN ('failed','interrupted') AND t.started_at::date = d::date)
		FROM generate_series(current_date - ($1::int - 1), current_date, '1 day') d ORDER BY d`, days)
	if err == nil {
		for rows.Next() {
			var d day
			rows.Scan(&d.Day, &d.Success, &d.Failed)
			series = append(series, d)
		}
		rows.Close()
	}
	counts := map[string]int64{
		"hosts":      q(`SELECT count(*) FROM ansible_hosts`),
		"playbooks":  q(`SELECT count(*) FROM ansible_playbooks`),
		"vaults":     q(`SELECT count(*) FROM ansible_vaults`),
		"roles":      q(`SELECT count(*) FROM ansible_roles`),
		"docker":     q(`SELECT count(*) FROM docker_files`),
		"tofu":       q(`SELECT count(*) FROM tofu_projects`),
		"k8s":        q(`SELECT count(*) FROM k8s_manifests`),
		"clusters":   q(`SELECT count(*) FROM k8s_clusters`),
		"users":      q(`SELECT count(*) FROM users WHERE active`),
		"mfa_users":  q(`SELECT count(*) FROM users WHERE active AND mfa_enabled`),
		"registries": q(`SELECT count(*) FROM docker_registries`),
	}
	recent := s.queryLogs(ctx, logFilter{Limit: 8, ExcludeAuth: true})
	rate := 100.0
	if total > 0 {
		rate = float64(success) * 100 / float64(total)
	}
	writeJSON(w, 200, map[string]any{
		"period": interval, "executions": total, "previous_executions": prevTotal, "success": success, "failed": failed,
		"success_rate": rate, "ai_questions": aiQ, "trivy_critical": critical, "trivy_target": lastTrivy.String,
		"by_module": byModule, "series": series,
		"counts": counts, "recent": recent, "running": len(s.jobs.Running()), "time": time.Now(),
	})
}
