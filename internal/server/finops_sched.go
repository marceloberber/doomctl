package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/doomctl/doomctl/internal/finops"
)

// StartFinOps inicia o agendador do FinOps: sincronização automática das fontes
// e avaliação de alertas (anomalias e orçamentos) a cada 10 minutos.
func (s *Server) StartFinOps(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(90 * time.Second):
		}
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			s.finopsTick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (s *Server) finopsTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("finops agendador", "panic", r)
		}
	}()
	st := s.finopsSettings(ctx)
	if st.AutoSyncHours > 0 {
		srcs, err := s.finopsSourceList(ctx)
		if err == nil {
			for _, src := range srcs {
				if !src.AutoSync || !src.HasCredentials || (src.Provider != "aws" && src.Provider != "oci") {
					continue
				}
				if src.LastSyncAt != nil && time.Since(*src.LastSyncAt) < time.Duration(st.AutoSyncHours)*time.Hour {
					continue
				}
				if _, busy := finopsSyncBusy.Load(src.ID); busy {
					continue
				}
				spec, err := s.finopsSyncSpec(src, 0)
				if err != nil {
					continue
				}
				spec.Username = "agendador"
				spec.Env = s.toolEnv()
				if _, err := s.jobs.Start(spec); err != nil {
					slog.Warn("finops: sincronização agendada", "fonte", src.Name, "erro", err)
				}
			}
		}
	}
	if _, err := s.finopsEvaluateAlerts(ctx); err != nil {
		slog.Warn("finops: alertas", "erro", err)
	}
}

// finopsEvaluateAlerts gera alertas novos (deduplicados) de anomalias e orçamentos.
func (s *Server) finopsEvaluateAlerts(ctx context.Context) (int, error) {
	now := s.now()
	st := s.finopsSettings(ctx)
	d, err := s.finopsData(ctx, finops.Day(now).AddDate(0, 0, -70), finops.Day(now).AddDate(0, 0, 1))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range finops.DetectAnomalies(d, st, now, 3) {
		if a.Severity == "low" || a.Scope == "region" || a.Scope == "account" {
			continue
		}
		if s.finopsAlert(ctx, st, a.Key, "anomaly", a.Severity, "🔺 Anomalia de custo — "+a.Message()) {
			n++
		}
	}
	bs, err := s.finopsBudgets(ctx)
	if err != nil {
		return n, err
	}
	var notify []finops.Budget
	for _, b := range bs {
		if b.Notify {
			notify = append(notify, b)
		}
	}
	month := now.UTC().Format("2006-01")
	for _, b := range finops.EvaluateBudgets(notify, d, st, now) {
		if b.Status == "no_rate" {
			continue
		}
		for _, t := range b.Crossed {
			sev := "medium"
			if t >= 100 {
				sev = "high"
			}
			msg := fmt.Sprintf("💰 Orçamento \"%s\" atingiu %d%%: gasto %s %.2f de %s %.2f no mês (previsão %s %.2f)", b.Name, t,
				st.BaseCurrency, b.Actual, st.BaseCurrency, b.AmountBase, st.BaseCurrency, b.Forecast)
			if s.finopsAlert(ctx, st, fmt.Sprintf("budget|%d|%s|%d", b.ID, month, t), "budget", sev, msg) {
				n++
			}
		}
		if b.ForecastCrossed && b.PctActual < 100 {
			msg := fmt.Sprintf("📈 Previsão acima do orçamento \"%s\": fechamento estimado em %s %.2f (%.0f%% de %s %.2f)", b.Name,
				st.BaseCurrency, b.Forecast, b.PctForecast, st.BaseCurrency, b.AmountBase)
			if s.finopsAlert(ctx, st, fmt.Sprintf("budget|%d|%s|forecast", b.ID, month), "forecast", "medium", msg) {
				n++
			}
		}
	}
	return n, nil
}

// finopsAlert registra o alerta (uma única vez por chave) e o envia ao webhook.
func (s *Server) finopsAlert(ctx context.Context, st finops.Settings, key, kind, sev, msg string) bool {
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO finops_alerts(key, kind, severity, message) VALUES ($1,$2,$3,$4)
		ON CONFLICT (key) DO NOTHING RETURNING id`, key, kind, sev, msg).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return false
	}
	if st.WebhookURL != "" {
		if err := s.sendWebhook(ctx, st.WebhookURL, msg); err != nil {
			s.db.ExecContext(ctx, `UPDATE finops_alerts SET error=$2 WHERE id=$1`, id, truncate(err.Error(), 300))
		} else {
			s.db.ExecContext(ctx, `UPDATE finops_alerts SET delivered=true WHERE id=$1`, id)
		}
	}
	return true
}

// sendWebhook envia {"text": ..., "content": ...} (Slack, Mattermost, Google Chat, Teams, Discord).
func (s *Server) sendWebhook(ctx context.Context, url, msg string) error {
	body, _ := json.Marshal(map[string]string{"text": msg, "content": msg})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "doomctl-finops")
	cl := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(b), 200))
	}
	return nil
}
