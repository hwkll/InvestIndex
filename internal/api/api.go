// Package api wires the REST endpoints, SSE stream, static SPA and scheduler
// into a single http.Handler.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"investhub/internal/alerts"
	"investhub/internal/core"
	"investhub/internal/cryptox"
	"investhub/internal/settings"
	"investhub/internal/store"
)

// Version is reported by /api/v1/auth/status.
const Version = "1.1.0"

const (
	sessionDays   = 30
	sessionCookie = "ih_session"
	failWindow    = 5 * time.Minute
	lockDuration  = 5 * time.Minute
	maxBody       = 20 << 20 // 20 MB, big enough for a full backup import
)

// apiError carries a business code to the response envelope.
type apiError struct {
	Code int
	Msg  string
}

func (e *apiError) Error() string { return e.Msg }

func errf(code int, msg string) error { return &apiError{Code: code, Msg: msg} }

// codeOf extracts the business code from any known error type.
func codeOf(err error) (int, string) {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Code, ae.Msg
	}
	var ce *core.APIError
	if errors.As(err, &ce) {
		return ce.Code, ce.Msg
	}
	var le *alerts.APIError
	if errors.As(err, &le) {
		return le.Code, le.Msg
	}
	return 50001, err.Error()
}

type failInfo struct {
	count       int
	windowStart time.Time
	lockUntil   time.Time
}

// Server holds the mutable HTTP-layer state.
type Server struct {
	mu    sync.Mutex
	fails map[string]*failInfo
	stop  chan struct{}
}

// New builds a Server.
func New() *Server {
	return &Server{fails: map[string]*failInfo{}, stop: make(chan struct{})}
}

// ---------------- response helpers ----------------

type envelope struct {
	Code int    `json:"code"`
	Data any    `json:"data"`
	Msg  string `json:"msg"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{Code: 0, Data: data, Msg: "ok"})
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, http.StatusOK, envelope{Code: code, Data: nil, Msg: msg})
}

// h adapts a value-returning handler into an http.HandlerFunc.
func h(fn func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fn(r)
		if err != nil {
			code, msg := codeOf(err)
			fail(w, code, msg)
			return
		}
		ok(w, data)
	}
}

func decode(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return errf(40001, "读取请求体失败")
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return errf(40001, "请求体不是合法 JSON")
	}
	return nil
}

func qstr(r *http.Request, key string) string { return r.URL.Query().Get(key) }

func qint(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func qint64(r *http.Request, key string) int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------- auth ----------------

func (s *Server) pinHash() string { return settings.Get("access_pin_hash") }

func (s *Server) authRequired() bool { return s.pinHash() != "" }

func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	var expires int64
	if err := store.QueryRow(`SELECT expires_at FROM sessions WHERE id = ?`, c.Value).Scan(&expires); err != nil {
		return false
	}
	now := time.Now().UnixMilli()
	if expires < now {
		_, _ = store.Exec(`DELETE FROM sessions WHERE id = ?`, c.Value)
		return false
	}
	// Skip sliding expiry for SSE connections to prevent infinite renewal.
	if !strings.HasPrefix(r.URL.Path, "/api/v1/events") {
		_, _ = store.Exec(`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
			now, now+int64(sessionDays)*86400000, c.Value)
	}
	return true
}

func (s *Server) recordFail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[ip]
	if f == nil {
		f = &failInfo{windowStart: time.Now()}
		s.fails[ip] = f
	}
	if time.Since(f.windowStart) > failWindow {
		f.count = 0
		f.windowStart = time.Now()
	}
	f.count++
	if f.count >= 5 {
		f.lockUntil = time.Now().Add(lockDuration)
	}
}

func (s *Server) failLocked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[ip]
	return f != nil && !f.lockUntil.IsZero() && time.Now().Before(f.lockUntil)
}

func (s *Server) resetFail(ip string) {
	s.mu.Lock()
	delete(s.fails, ip)
	s.mu.Unlock()
}

// authGuard blocks unauthenticated API access when a PIN is configured.
func (s *Server) authGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/v1/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.authRequired() && !s.loggedIn(r) {
			fail(w, 40101, "未认证或会话过期")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------- router ----------------

// Handler returns the fully wired http.Handler.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// Reflect the caller's Origin. Never combine "*" with credentials —
			// browsers reject that combination, which broke proxied/cross-origin setups.
			if origin := req.Header.Get("Origin"); origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
			if req.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, req)
		})
	})

	// SSE lives outside the JSON envelope.
	r.Get("/api/v1/events", s.handleSSE)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.authGuard)

		// auth
		r.Get("/auth/status", h(func(req *http.Request) (any, error) {
			demo := false
			if v, ok := store.ScalarStr(`SELECT value FROM meta WHERE key = 'demo_data'`); ok && v == "1" {
				demo = true
			}
			return map[string]any{"authRequired": s.authRequired(), "loggedIn": s.loggedIn(req), "version": Version, "demo": demo}, nil
		}))
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/logout", s.handleLogout)
		r.Put("/auth/pin", h(s.handleSetPin))

		// dashboard
		r.Get("/dashboard/summary", h(func(req *http.Request) (any, error) { return core.GlobalSummaryCached(), nil }))
		r.Get("/pnl/trend", h(func(req *http.Request) (any, error) {
			return core.PnlTrend(pick(qstr(req, "range"), "30d")), nil
		}))

		// assets
		r.Get("/assets", h(handleListAssets))
		r.Post("/assets", h(handleCreateAsset))
		r.Put("/assets/{id}", h(handleUpdateAsset))
		r.Delete("/assets/{id}", h(handleDeleteAsset))
		r.Get("/assets/{id}/quote", h(handleQuote))
		r.Get("/assets/{id}/kline", h(handleKline))
		r.Get("/assets/{id}/indicators", h(handleIndicators))
		r.Get("/assets/{id}/position", h(handlePositionView))
		r.Get("/positions", h(handlePositions))

		// watchlist (自选)
		r.Get("/watchlist", h(func(req *http.Request) (any, error) { return core.ListWatchlist(), nil }))
		r.Post("/watchlist", h(func(req *http.Request) (any, error) {
			var b struct {
				AssetID     string   `json:"assetId"`
				TargetPrice *float64 `json:"targetPrice"`
				Note        string   `json:"note"`
			}
			if err := decode(req, &b); err != nil {
				return nil, err
			}
			if b.AssetID == "" {
				return nil, errf(40001, "assetId 必填")
			}
			return core.AddToWatchlist(b.AssetID, b.TargetPrice, b.Note)
		}))
		r.Put("/watchlist/{id}", h(func(req *http.Request) (any, error) {
			var b struct {
				TargetPrice *float64 `json:"targetPrice"`
				Note        string   `json:"note"`
			}
			if err := decode(req, &b); err != nil {
				return nil, err
			}
			return core.UpdateWatchlist(chi.URLParam(req, "id"), b.TargetPrice, b.Note)
		}))
		r.Delete("/watchlist/{id}", h(func(req *http.Request) (any, error) {
			return core.RemoveFromWatchlist(chi.URLParam(req, "id")), nil
		}))

		// transactions
		r.Get("/transactions", h(handleListTx))
		r.Post("/transactions", h(handleCreateTx))
		r.Put("/transactions/{id}", h(handleUpdateTx))
		r.Delete("/transactions/{id}", h(handleDeleteTx))

		// ai
		r.Post("/ai/analyze", h(handleAnalyze))
		r.Get("/ai/analyses", h(handleListAnalyses))
		r.Get("/ai/analyses/{id}", h(handleGetAnalysis))
		r.Delete("/ai/analyses/{id}", h(handleDeleteAnalysis))

		// alerts
		r.Get("/alerts", h(func(req *http.Request) (any, error) { return alerts.ListRules(), nil }))
		r.Post("/alerts", h(handleCreateAlert))
		r.Put("/alerts/{id}", h(handleUpdateAlert))
		r.Delete("/alerts/{id}", h(func(req *http.Request) (any, error) {
			return alerts.DeleteRule(chi.URLParam(req, "id")), nil
		}))
		r.Get("/alerts/events", h(handleListEvents))
		r.Post("/alerts/events/{id}/read", h(func(req *http.Request) (any, error) {
			return alerts.MarkRead(chi.URLParam(req, "id")), nil
		}))

		// settings
		r.Get("/settings", h(func(req *http.Request) (any, error) { return settings.List(), nil }))
		r.Put("/settings", h(handleUpdateSettings))
		r.Post("/settings/ai-test", h(handleAITest))
		r.Post("/settings/mail-test", h(handleMailTest))
		r.Post("/settings/webhook-test", h(func(req *http.Request) (any, error) { return alerts.TestWebhook(), nil }))

		// fx rates (多币种汇率表)
		r.Get("/settings/fx", h(func(req *http.Request) (any, error) {
			rows, err := store.Query(`SELECT currency, rate FROM fx_rates ORDER BY currency`)
			out := []map[string]any{}
			if err != nil {
				return out, nil
			}
			defer rows.Close()
			for rows.Next() {
				var cur string
				var rate float64
				if rows.Scan(&cur, &rate) == nil {
					out = append(out, map[string]any{"currency": cur, "rate": rate})
				}
			}
			return out, nil
		}))
		r.Put("/settings/fx", h(func(req *http.Request) (any, error) {
			var b map[string]float64
			if err := decode(req, &b); err != nil {
				return nil, err
			}
			ts := time.Now().UnixMilli()
			for cur, rate := range b {
				if rate <= 0 {
					continue
				}
				_, _ = store.Exec(`INSERT INTO fx_rates(currency, rate, updated_at) VALUES(?,?,?)
				    ON CONFLICT(currency) DO UPDATE SET rate=excluded.rate, updated_at=excluded.updated_at`, cur, rate, ts)
			}
			return map[string]any{"ok": true}, nil
		}))

		// cash
		r.Get("/cash-accounts", h(func(req *http.Request) (any, error) { return core.CashAccounts(), nil }))
		r.Post("/cash-accounts", h(handleCreateCash))
		r.Put("/cash-accounts/{id}", h(handleUpdateCash))
		r.Delete("/cash-accounts/{id}", h(func(req *http.Request) (any, error) {
			return core.DeleteCash(chi.URLParam(req, "id"))
		}))

		// data backup
		r.Get("/data/export", handleExportJSON)
		r.Get("/data/export.csv", handleExportCSV)
		r.Post("/data/import", h(handleImportJSON))
		r.Post("/data/import.csv", h(handleImportCSV))
	})

	// unknown API paths still return the JSON envelope
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			fail(w, 40401, "接口不存在: "+req.Method+" "+req.URL.Path)
			return
		}
		serveStatic(w, req)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		fail(w, 40501, "方法不允许: "+req.Method+" "+req.URL.Path)
	})

	return r
}

// ---------------- auth handlers ----------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.failLocked(ip) {
		fail(w, 42901, "尝试过于频繁，请 5 分钟后再试")
		return
	}
	var body struct {
		Pin string `json:"pin"`
	}
	if err := decode(r, &body); err != nil {
		code, msg := codeOf(err)
		fail(w, code, msg)
		return
	}
	hash := s.pinHash()
	if hash == "" {
		ok(w, map[string]any{"ok": true, "noPin": true})
		return
	}
	if body.Pin == "" || !cryptox.VerifyPassword(body.Pin, hash) {
		s.recordFail(ip)
		fail(w, 40101, "口令错误")
		return
	}
	s.resetFail(ip)
	token := cryptox.RandomToken(32)
	now := time.Now().UnixMilli()
	_, _ = store.Exec(`INSERT INTO sessions(id, expires_at, created_at, last_seen_at) VALUES(?,?,?,?)`,
		token, now+int64(sessionDays)*86400000, now, now)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: sessionDays * 86400,
	})
	ok(w, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_, _ = store.Exec(`DELETE FROM sessions WHERE id = ?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	ok(w, map[string]any{"ok": true})
}

func (s *Server) handleSetPin(r *http.Request) (any, error) {
	var body struct {
		Pin    *string `json:"pin"`
		OldPin string  `json:"oldPin"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	cur := s.pinHash()
	if cur != "" {
		if body.OldPin == "" || !cryptox.VerifyPassword(body.OldPin, cur) {
			return nil, errf(40001, "旧口令验证失败")
		}
	}
	if body.Pin == nil || *body.Pin == "" {
		settings.Delete("access_pin_hash")
		// invalidate all sessions when protection is removed
		_, _ = store.Exec(`DELETE FROM sessions`)
		return map[string]any{"cleared": true}, nil
	}
	if len(*body.Pin) < 6 {
		return nil, errf(40001, "口令长度至少 6 位")
	}
	settings.Set("access_pin_hash", cryptox.HashPassword(*body.Pin))
	return map[string]any{"set": true}, nil
}

func pick(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
