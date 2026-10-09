package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/doomctl/doomctl/internal/rbac"
	"github.com/doomctl/doomctl/internal/secure"
)

const (
	cookieName  = "doomctl_session"
	idleTimeout = 2 * time.Hour
	preAuthTTL  = 10 * time.Minute
)

type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	FullName    string     `json:"full_name"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Active      bool       `json:"active"`
	MustChange  bool       `json:"must_change_password"`
	MFAEnabled  bool       `json:"mfa_enabled"`
	MFARequired bool       `json:"mfa_required"`
	LastLogin   *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`

	passwordHash string
	mfaSecret    []byte
	mfaPending   []byte
	mfaLastStep  int64
	mfaRecovery  []string
	failed       int
	lockedUntil  *time.Time
}

const userCols = `u.id, u.username, u.full_name, u.email, u.role, u.active, u.must_change_password, u.mfa_enabled,
	u.mfa_required, u.last_login_at, u.created_at, u.password_hash, u.mfa_secret, u.mfa_pending_secret, u.mfa_last_step,
	u.mfa_recovery, u.failed_logins, u.locked_until`

type scanner interface{ Scan(...any) error }

func scanUser(sc scanner, extra ...any) (*User, error) {
	u := &User{}
	var rec []byte
	dest := []any{&u.ID, &u.Username, &u.FullName, &u.Email, &u.Role, &u.Active, &u.MustChange, &u.MFAEnabled,
		&u.MFARequired, &u.LastLogin, &u.CreatedAt, &u.passwordHash, &u.mfaSecret, &u.mfaPending, &u.mfaLastStep,
		&rec, &u.failed, &u.lockedUntil}
	if err := sc.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	json.Unmarshal(rec, &u.mfaRecovery)
	return u, nil
}

func (s *Server) userByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.id=$1`, id))
}

// ---------- sessões ----------

type Session struct {
	ID       string
	Kind     string
	CSRF     string
	LastSeen time.Time
	Created  time.Time
}

func (s *Server) currentSession(r *http.Request) (*Session, *User, error) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, nil, errors.New("sem sessão")
	}
	id := secure.SHA256Hex(c.Value)
	sess := &Session{ID: id}
	var expires time.Time
	u, err := scanUser(s.db.QueryRowContext(r.Context(), `SELECT `+userCols+`, s.kind, s.csrf, s.expires_at, s.last_seen_at, s.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id=$1`, id),
		&sess.Kind, &sess.CSRF, &expires, &sess.LastSeen, &sess.Created)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if now.After(expires) || (sess.Kind == "full" && now.Sub(sess.LastSeen) > idleTimeout) || !u.Active {
		s.db.Exec(`DELETE FROM sessions WHERE id=$1`, id)
		return nil, nil, errors.New("sessão expirada")
	}
	if now.Sub(sess.LastSeen) > time.Minute {
		s.db.Exec(`UPDATE sessions SET last_seen_at=now() WHERE id=$1`, id)
	}
	return sess, u, nil
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, userID int64, kind string) (string, error) {
	token := secure.RandomToken(32)
	csrf := secure.RandomToken(24)
	ttl := s.cfg.SessionTTL
	if kind != "full" {
		ttl = preAuthTTL
	}
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO sessions(id, user_id, kind, csrf, ip, user_agent, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, secure.SHA256Hex(token), userID, kind, csrf, clientIP(r),
		truncate(r.UserAgent(), 300), s.now().Add(ttl))
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: isHTTPS(r), Expires: s.now().Add(ttl),
	})
	// limpeza oportunista
	s.db.Exec(`DELETE FROM sessions WHERE expires_at < now()`)
	return csrf, nil
}

func clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r)})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) checkCSRF(r *http.Request, sess *Session) bool {
	if !sameOrigin(r) {
		return false
	}
	t := r.Header.Get("X-CSRF-Token")
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(sess.CSRF)) == 1
}

// sameOrigin: se o navegador enviou Origin, ele deve bater com o Host.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return o == ""
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------- bootstrap ----------

// EnsureAdmin cria o primeiro administrador quando não há usuários.
func EnsureAdmin(ctx context.Context, db *sql.DB, username, password string) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	generated := false
	if password == "" {
		password, generated = secure.RandomPassword(), true
	} else if err := secure.ValidatePasswordPolicy(password); err != nil {
		return errors.New("DOOMCTL_ADMIN_PASSWORD: " + err.Error())
	}
	h, err := secure.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO users(username, full_name, password_hash, role, must_change_password)
		VALUES ($1, 'Administrador', $2, 'admin', true)`, strings.ToLower(username), h)
	if err != nil {
		return err
	}
	if generated {
		slog.Warn("============================================================")
		slog.Warn("administrador inicial criado", "usuario", username, "senha", password)
		slog.Warn("troque a senha no primeiro acesso (obrigatório)")
		slog.Warn("============================================================")
	} else {
		slog.Info("administrador inicial criado", "usuario", username)
	}
	return nil
}

// ---------- handlers públicos ----------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, http.StatusForbidden, "origem inválida")
		return
	}
	var in struct{ Username, Password string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	ip := clientIP(r)
	if !s.rl.allow("ip:"+ip, 30, 5*time.Minute) || !s.rl.allow("user:"+in.Username, 10, 15*time.Minute) {
		fail(w, http.StatusTooManyRequests, "muitas tentativas; aguarde alguns minutos")
		return
	}
	u, err := scanUser(s.db.QueryRowContext(r.Context(), `SELECT `+userCols+` FROM users u WHERE u.username=$1`, in.Username))
	if err != nil {
		secure.DummyCheck(in.Password)
		s.audit("auth", "login", in.Username, nil, "failed", "usuário inexistente ("+ip+")", nil)
		fail(w, http.StatusUnauthorized, "usuário ou senha inválidos")
		return
	}
	if u.lockedUntil != nil && u.lockedUntil.After(s.now()) {
		secure.DummyCheck(in.Password)
		fail(w, http.StatusLocked, "conta bloqueada temporariamente por excesso de tentativas")
		return
	}
	if !secure.CheckPassword(u.passwordHash, in.Password) {
		failed := u.failed + 1
		var lock any
		if failed >= 5 {
			lock, failed = s.now().Add(15*time.Minute), 0
		}
		s.db.Exec(`UPDATE users SET failed_logins=$2, locked_until=$3 WHERE id=$1`, u.ID, failed, lock)
		s.audit("auth", "login", u.Username, u, "failed", "senha incorreta ("+ip+")", nil)
		fail(w, http.StatusUnauthorized, "usuário ou senha inválidos")
		return
	}
	if !u.Active {
		fail(w, http.StatusForbidden, "usuário desativado")
		return
	}
	s.db.Exec(`UPDATE users SET failed_logins=0, locked_until=NULL WHERE id=$1`, u.ID)

	kind, status := "full", "ok"
	switch {
	case u.MFAEnabled:
		kind, status = "mfa", "mfa"
	case u.MFARequired:
		kind, status = "enroll", "mfa_enroll"
	}
	csrf, err := s.createSession(w, r, u.ID, kind)
	if err != nil {
		failErr(w, err)
		return
	}
	if kind == "full" {
		s.db.Exec(`UPDATE users SET last_login_at=now() WHERE id=$1`, u.ID)
		s.audit("auth", "login", u.Username, u, "success", "login ("+ip+")", nil)
	}
	writeJSON(w, 200, map[string]any{"status": status, "csrf": csrf, "must_change_password": u.MustChange})
}

func (s *Server) preAuth(w http.ResponseWriter, r *http.Request, kinds ...string) (*Session, *User, bool) {
	sess, u, err := s.currentSession(r)
	if err != nil {
		fail(w, http.StatusUnauthorized, "sessão expirada; faça login novamente")
		return nil, nil, false
	}
	allowed := false
	for _, k := range kinds {
		if sess.Kind == k {
			allowed = true
		}
	}
	if !allowed {
		fail(w, http.StatusForbidden, "etapa de autenticação inválida")
		return nil, nil, false
	}
	if r.Method != http.MethodGet && !s.checkCSRF(r, sess) {
		fail(w, http.StatusForbidden, "token CSRF inválido")
		return nil, nil, false
	}
	return sess, u, true
}

// upgrade troca a sessão pré-auth por uma sessão completa (rotação de token).
func (s *Server) upgrade(w http.ResponseWriter, r *http.Request, sess *Session, u *User) (string, error) {
	s.db.Exec(`DELETE FROM sessions WHERE id=$1`, sess.ID)
	csrf, err := s.createSession(w, r, u.ID, "full")
	if err != nil {
		return "", err
	}
	s.db.Exec(`UPDATE users SET last_login_at=now() WHERE id=$1`, u.ID)
	s.audit("auth", "login", u.Username, u, "success", "login com MFA ("+clientIP(r)+")", nil)
	return csrf, nil
}

func (s *Server) mfaVerify(w http.ResponseWriter, r *http.Request) {
	sess, u, okk := s.preAuth(w, r, "mfa")
	if !okk {
		return
	}
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !s.rl.allow("mfa:"+sess.ID, 5, preAuthTTL) {
		s.db.Exec(`DELETE FROM sessions WHERE id=$1`, sess.ID)
		clearCookie(w, r)
		fail(w, http.StatusTooManyRequests, "muitas tentativas de MFA; faça login novamente")
		return
	}
	if !s.checkSecondFactor(r.Context(), u, in.Code) {
		s.audit("auth", "mfa", u.Username, u, "failed", "código MFA inválido", nil)
		fail(w, http.StatusUnauthorized, "código inválido")
		return
	}
	csrf, err := s.upgrade(w, r, sess, u)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "csrf": csrf, "must_change_password": u.MustChange})
}

// checkSecondFactor aceita código TOTP (com anti-replay) ou código de recuperação (uso único).
func (s *Server) checkSecondFactor(ctx context.Context, u *User, code string) bool {
	code = strings.TrimSpace(strings.ToLower(code))
	if len(u.mfaSecret) == 0 {
		return false
	}
	if strings.Contains(code, "-") {
		h := hashRecovery(code)
		for i, c := range u.mfaRecovery {
			if subtle.ConstantTimeCompare([]byte(c), []byte(h)) == 1 {
				rest := append(append([]string{}, u.mfaRecovery[:i]...), u.mfaRecovery[i+1:]...)
				b, _ := json.Marshal(rest)
				s.db.ExecContext(ctx, `UPDATE users SET mfa_recovery=$2 WHERE id=$1`, u.ID, string(b))
				s.audit("auth", "mfa_recovery", u.Username, u, "info", "código de recuperação utilizado", nil)
				return true
			}
		}
		return false
	}
	secret, err := s.box.Open(u.mfaSecret)
	if err != nil {
		return false
	}
	step, ok := secure.VerifyTOTP(string(secret), code, s.now(), u.mfaLastStep)
	if !ok {
		return false
	}
	// UPDATE condicional evita corrida de replay entre requisições simultâneas
	res, err := s.db.ExecContext(ctx, `UPDATE users SET mfa_last_step=$2 WHERE id=$1 AND mfa_last_step < $2`, u.ID, step)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func hashRecovery(c string) string {
	h := sha256.Sum256([]byte("doomctl-recovery:" + strings.ToLower(strings.TrimSpace(c))))
	return hex.EncodeToString(h[:])
}

func (s *Server) mfaSetup(w http.ResponseWriter, r *http.Request) {
	_, u, okk := s.preAuth(w, r, "enroll", "full")
	if !okk {
		return
	}
	if u.MFAEnabled {
		fail(w, http.StatusConflict, "MFA já está ativo")
		return
	}
	secret := secure.NewTOTPSecret()
	enc, err := s.box.Seal([]byte(secret))
	if err != nil {
		failErr(w, err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE users SET mfa_pending_secret=$2 WHERE id=$1`, u.ID, enc); err != nil {
		failErr(w, err)
		return
	}
	uri := secure.TOTPURI("DOOMCTL", u.Username, secret)
	png, err := qrcode.Encode(uri, qrcode.Medium, 280)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"secret": secret, "uri": uri, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		"issuer": "DOOMCTL", "account": u.Username, "algorithm": "SHA1", "digits": 6, "period": 30,
	})
}

func (s *Server) mfaEnable(w http.ResponseWriter, r *http.Request) {
	sess, u, okk := s.preAuth(w, r, "enroll", "full")
	if !okk {
		return
	}
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(u.mfaPending) == 0 {
		fail(w, 400, "gere o QR code antes de confirmar")
		return
	}
	if !s.rl.allow("mfaen:"+sess.ID, 10, preAuthTTL) {
		fail(w, http.StatusTooManyRequests, "muitas tentativas")
		return
	}
	secret, err := s.box.Open(u.mfaPending)
	if err != nil {
		failErr(w, err)
		return
	}
	step, valid := secure.VerifyTOTP(string(secret), in.Code, s.now(), 0)
	if !valid {
		fail(w, http.StatusUnauthorized, "código inválido — confira o horário do celular e tente novamente")
		return
	}
	codes := secure.RecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = hashRecovery(c)
	}
	hb, _ := json.Marshal(hashes)
	_, err = s.db.ExecContext(r.Context(), `UPDATE users SET mfa_secret=mfa_pending_secret, mfa_pending_secret=NULL,
		mfa_enabled=true, mfa_last_step=$2, mfa_recovery=$3, updated_at=now() WHERE id=$1`, u.ID, step, string(hb))
	if err != nil {
		failErr(w, err)
		return
	}
	s.audit("auth", "mfa_enable", u.Username, u, "success", "MFA (TOTP) ativado", nil)
	resp := map[string]any{"status": "ok", "recovery_codes": codes}
	if sess.Kind == "enroll" {
		csrf, err := s.upgrade(w, r, sess, u)
		if err != nil {
			failErr(w, err)
			return
		}
		resp["csrf"] = csrf
		resp["must_change_password"] = u.MustChange
	}
	writeJSON(w, 200, resp)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sess, u, err := s.currentSession(r); err == nil {
		if !s.checkCSRF(r, sess) {
			fail(w, http.StatusForbidden, "token CSRF inválido")
			return
		}
		s.db.Exec(`DELETE FROM sessions WHERE id=$1`, sess.ID)
		if sess.Kind == "full" {
			s.audit("auth", "logout", u.Username, u, "info", "logout", nil)
		}
	}
	clearCookie(w, r)
	ok(w)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	sess, u, err := s.currentSession(r)
	if err != nil {
		writeJSON(w, 200, map[string]any{"authenticated": false})
		return
	}
	perms := map[string]map[string]bool{}
	for _, m := range rbac.Modules {
		perms[m.ID] = map[string]bool{"read": s.can(u, m.ID, rbac.Read), "manage": s.can(u, m.ID, rbac.Manage)}
	}
	writeJSON(w, 200, map[string]any{
		"authenticated": sess.Kind == "full", "stage": sess.Kind, "csrf": sess.CSRF, "user": u,
		"permissions": perms, "modules": rbac.Modules, "version": s.cfg.Version,
	})
}

// ---------- perfil (sessão completa) ----------

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct{ Current, New string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !s.rl.allow("pw:"+u.Username, 10, 15*time.Minute) {
		fail(w, http.StatusTooManyRequests, "muitas tentativas")
		return
	}
	if !secure.CheckPassword(u.passwordHash, in.Current) {
		fail(w, http.StatusUnauthorized, "senha atual incorreta")
		return
	}
	if err := secure.ValidatePasswordPolicy(in.New); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if in.New == in.Current {
		fail(w, 400, "a nova senha deve ser diferente da atual")
		return
	}
	h, err := secure.HashPassword(in.New)
	if err != nil {
		failErr(w, err)
		return
	}
	sess, _, _ := s.currentSession(r)
	if _, err := s.db.Exec(`UPDATE users SET password_hash=$2, must_change_password=false, updated_at=now() WHERE id=$1`, u.ID, h); err != nil {
		failErr(w, err)
		return
	}
	if sess != nil {
		s.db.Exec(`DELETE FROM sessions WHERE user_id=$1 AND id<>$2`, u.ID, sess.ID)
	}
	s.audit("auth", "password_change", u.Username, u, "success", "senha alterada; outras sessões encerradas", nil)
	ok(w)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		FullName string `json:"full_name"`
		Email    string `json:"email"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET full_name=$2, email=$3, updated_at=now() WHERE id=$1`,
		u.ID, truncate(strings.TrimSpace(in.FullName), 120), truncate(strings.TrimSpace(in.Email), 200)); err != nil {
		failErr(w, err)
		return
	}
	ok(w)
}

func (s *Server) mfaDisableSelf(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct{ Password, Code string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if u.MFARequired {
		fail(w, http.StatusForbidden, "o MFA é exigido pelo administrador e não pode ser desativado")
		return
	}
	if !u.MFAEnabled {
		fail(w, 400, "MFA não está ativo")
		return
	}
	if !secure.CheckPassword(u.passwordHash, in.Password) || !s.checkSecondFactor(r.Context(), u, in.Code) {
		fail(w, http.StatusUnauthorized, "senha ou código inválidos")
		return
	}
	s.db.Exec(`UPDATE users SET mfa_enabled=false, mfa_secret=NULL, mfa_pending_secret=NULL, mfa_recovery='[]', updated_at=now() WHERE id=$1`, u.ID)
	s.audit("auth", "mfa_disable", u.Username, u, "info", "MFA desativado pelo próprio usuário", nil)
	ok(w)
}

func (s *Server) mfaRegenRecovery(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct{ Password, Code string }
	if err := decode(r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !u.MFAEnabled {
		fail(w, 400, "MFA não está ativo")
		return
	}
	if !secure.CheckPassword(u.passwordHash, in.Password) || !s.checkSecondFactor(r.Context(), u, in.Code) {
		fail(w, http.StatusUnauthorized, "senha ou código inválidos")
		return
	}
	codes := secure.RecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = hashRecovery(c)
	}
	hb, _ := json.Marshal(hashes)
	s.db.Exec(`UPDATE users SET mfa_recovery=$2 WHERE id=$1`, u.ID, string(hb))
	s.audit("auth", "mfa_recovery_regen", u.Username, u, "info", "códigos de recuperação regenerados", nil)
	writeJSON(w, 200, map[string]any{"recovery_codes": codes})
}

func (s *Server) mySessions(w http.ResponseWriter, r *http.Request, u *User) {
	cur, _, _ := s.currentSession(r)
	rows, err := s.db.Query(`SELECT id, ip, user_agent, created_at, last_seen_at FROM sessions
		WHERE user_id=$1 AND kind='full' AND expires_at > now() ORDER BY last_seen_at DESC`, u.ID)
	if err != nil {
		failErr(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, ip, ua string
		var c, l time.Time
		if err := rows.Scan(&id, &ip, &ua, &c, &l); err != nil {
			failErr(w, err)
			return
		}
		out = append(out, map[string]any{"id": id[:12], "ip": ip, "user_agent": ua, "created_at": c, "last_seen_at": l,
			"current": cur != nil && cur.ID == id})
	}
	writeJSON(w, 200, out)
}

func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request, u *User) {
	cur, _, _ := s.currentSession(r)
	if cur == nil {
		fail(w, 401, "sessão inválida")
		return
	}
	s.db.Exec(`DELETE FROM sessions WHERE user_id=$1 AND id<>$2`, u.ID, cur.ID)
	ok(w)
}
