package api

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"investhub/internal/ai"
	"investhub/internal/alerts"
	"investhub/internal/core"
	"investhub/internal/quotes"
	"investhub/internal/settings"
	"investhub/internal/store"
)

// createFetchTimeout bounds how long handleCreateAsset waits for a real quote
// before accepting a "nosource" placeholder. Tunable via CREATE_FETCH_TIMEOUT
// (milliseconds); defaults to 1500ms.
var createFetchTimeout = func() time.Duration {
	if v := os.Getenv("CREATE_FETCH_TIMEOUT"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 1500 * time.Millisecond
}()

// ---------------- assets ----------------

func handleListAssets(r *http.Request) (any, error) {
	return core.ListAssets(qstr(r, "category")), nil
}

func handleCreateAsset(r *http.Request) (any, error) {
	var in core.AssetInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	a, err := core.CreateAsset(in)
	if err != nil {
		return nil, err
	}
	store.MarkOnboarded() // user's first real asset clears the seeded-demo banner

	// P0: pull a real price immediately after creation so the first screen
	// shows a real quote. A background goroutine fetches the quote; we wait up
	// to createFetchTimeout for it, and if it lands broadcast it over SSE. On
	// timeout we keep the "nosource" placeholder and let the goroutine finish
	// and broadcast the real quote when it arrives.
	asset := quotes.Asset{ID: a.ID, Category: a.Category, Symbol: a.Symbol, SubType: a.SubType, Currency: a.Currency, Provider: a.Provider}
	done := make(chan *quotes.Quote, 1)
	fetch := func() (q *quotes.Quote) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[create] UpdateOne panic for %s: %v", a.ID, rec)
			}
		}()
		return quotes.UpdateOne(asset)
	}
	go func() { done <- fetch() }()
	select {
	case q := <-done:
		if q != nil && q.Status != "nosource" {
			quotes.EnsureKline(asset, "1d", 200)
		}
		if q != nil && q.Status == "ok" {
			Broadcast("quote", q)
		}
	case <-time.After(createFetchTimeout):
		// Timed out: the goroutine keeps running; backfill K-line and broadcast
		// the real quote once the fetch completes in the background.
		go func() {
			res := <-done
			if res == nil {
				return
			}
			if res.Status != "nosource" {
				quotes.EnsureKline(asset, "1d", 200)
			}
			if res.Status == "ok" {
				Broadcast("quote", res)
			}
		}()
	}
	return core.GetAsset(a.ID), nil
}

func handleUpdateAsset(r *http.Request) (any, error) {
	var in core.AssetInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return core.UpdateAsset(chi.URLParam(r, "id"), in)
}

func handleDeleteAsset(r *http.Request) (any, error) {
	return core.DeleteAsset(chi.URLParam(r, "id"), pick(qstr(r, "mode"), "hard"))
}

func handleQuote(r *http.Request) (any, error) {
	q := quotes.Get(chi.URLParam(r, "id"))
	if q == nil {
		return nil, nil
	}
	return q, nil
}

func handleKline(r *http.Request) (any, error) {
	return quotes.Kline(chi.URLParam(r, "id"), pick(qstr(r, "period"), "1d"), qint(r, "limit", 200)), nil
}

func handleIndicators(r *http.Request) (any, error) {
	ind := ai.IndicatorsFor(chi.URLParam(r, "id"))
	if ind == nil {
		return nil, errf(40401, "标的不存在")
	}
	return ind, nil
}

func handlePositionView(r *http.Request) (any, error) {
	v := core.GetPositionView(chi.URLParam(r, "id"))
	if v == nil {
		return nil, errf(40401, "标的不存在")
	}
	return v, nil
}

func handlePositions(r *http.Request) (any, error) {
	if cat := qstr(r, "category"); cat != "" {
		return core.PositionsByCategory(cat), nil
	}
	out := map[string]core.CategoryPositions{}
	for _, c := range core.Categories {
		out[c] = core.PositionsByCategory(c)
	}
	return out, nil
}

// ---------------- transactions ----------------

func handleListTx(r *http.Request) (any, error) {
	return core.ListTransactions(core.TxQuery{
		AssetID:   qstr(r, "asset_id"),
		Category:  qstr(r, "category"),
		Direction: qstr(r, "direction"),
		From:      qint64(r, "from"),
		To:        qint64(r, "to"),
		Page:      qint(r, "page", 1),
		Size:      qint(r, "size", 20),
	}), nil
}

func handleCreateTx(r *http.Request) (any, error) {
	var in core.TxInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return core.CreateTransaction(in)
}

func handleUpdateTx(r *http.Request) (any, error) {
	var in core.TxInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return core.UpdateTransaction(chi.URLParam(r, "id"), in)
}

func handleDeleteTx(r *http.Request) (any, error) {
	return core.DeleteTransaction(chi.URLParam(r, "id"))
}

// ---------------- ai ----------------

func handleAnalyze(r *http.Request) (any, error) {
	var body struct {
		Scope   string `json:"scope"`
		AssetID string `json:"assetId"`
		Model   string `json:"model"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	res, err := ai.Analyze(r.Context(), pick(body.Scope, "asset"), body.AssetID, body.Model)
	if err != nil {
		if errors.Is(err, ai.ErrNotConfigured) {
			return nil, errf(40301, "请在「设置 → AI 分析」中配置 DeepSeek API Key 后再使用 AI 分析")
		}
		return nil, errf(500, err.Error())
	}
	Broadcast("ai_done", map[string]any{
		"analysisId": res.AnalysisID, "scope": res.Scope, "assetId": res.AssetID,
		"signal": res.Signal, "model": res.Model,
	})
	if res.Scope == "asset" && (res.Signal == "buy" || res.Signal == "sell") {
		for _, ev := range alerts.CheckAISignal(res.AssetID, res.Signal) {
			Broadcast("alert", ev)
		}
	}
	return res, nil
}

func handleListAnalyses(r *http.Request) (any, error) {
	return ai.List(qstr(r, "scope"), qstr(r, "asset_id"), qint(r, "page", 1), qint(r, "size", 20)), nil
}

func handleGetAnalysis(r *http.Request) (any, error) {
	v := ai.Detail(chi.URLParam(r, "id"))
	if v == nil {
		return nil, errf(40401, "分析记录不存在")
	}
	return v, nil
}

func handleDeleteAnalysis(r *http.Request) (any, error) {
	return ai.Delete(chi.URLParam(r, "id")), nil
}

// ---------------- alerts ----------------

func handleCreateAlert(r *http.Request) (any, error) {
	var in alerts.Input
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return alerts.CreateRule(in)
}

func handleUpdateAlert(r *http.Request) (any, error) {
	var in alerts.Input
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return alerts.UpdateRule(chi.URLParam(r, "id"), in)
}

func handleListEvents(r *http.Request) (any, error) {
	var read *bool
	switch qstr(r, "read") {
	case "true":
		t := true
		read = &t
	case "false":
		f := false
		read = &f
	}
	return map[string]any{"items": alerts.ListEvents(read), "unread": alerts.UnreadCount()}, nil
}

// ---------------- settings ----------------

func (s *Server) handleUpdateSettings(r *http.Request) (any, error) {
	var body map[string]any
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	// Only allow known setting keys to prevent polluting internal state.
	allowedKeys := map[string]bool{
		"currency": true, "deepseek_model": true, "rate_usd_cny": true,
		"data_source_mode": true, "smtp_host": true, "smtp_port": true,
		"smtp_user": true, "smtp_pass": true, "smtp_from": true,
		"smtp_tls": true, "smtp_to": true, "webhook_url": true,
		"benchmark": true, "deepseek_api_key": true,
		"poll_interval": true, "fx_refresh_interval": true,
	}
	updates := map[string]bool{}
	for k, v := range body {
		if k == "access_pin_hash" {
			continue // only settable through /auth/pin
		}
		if !allowedKeys[k] {
			continue // silently skip unknown keys
		}
		storeVal := toStr(v)
		// Clamp refresh intervals so the persisted value always matches the
		// effective timer used by the scheduler.
		if k == "poll_interval" || k == "fx_refresh_interval" {
			if n, err := strconv.Atoi(storeVal); err != nil || n <= 0 {
				if k == "poll_interval" {
					storeVal = "30"
				} else {
					storeVal = "1800"
				}
			} else if k == "poll_interval" {
				if n < 5 {
					n = 5
				}
				if n > 600 {
					n = 600
				}
				storeVal = strconv.Itoa(n)
			} else {
				if n < 60 {
					n = 60
				}
				if n > 86400 {
					n = 86400
				}
				storeVal = strconv.Itoa(n)
			}
		}
		if err := settings.Set(k, storeVal); err != nil {
			return nil, err
		}
		updates[k] = true
		// Hot-reload effects: a few keys change runtime behavior immediately.
		switch k {
		case "data_source_mode":
			quotes.SetMode(storeVal)
		case "poll_interval":
			s.TriggerPollReset()
		case "fx_refresh_interval":
			s.TriggerFxReset()
		}
	}
	return map[string]any{"updates": updates}, nil
}

func handleAITest(r *http.Request) (any, error) {
	var body struct {
		APIKey string `json:"apiKey"`
		Model  string `json:"model"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	return settings.TestAI(body.APIKey, body.Model), nil
}

func handleMailTest(r *http.Request) (any, error) {
	return alerts.SendTestMail(), nil
}

// ---------------- cash ----------------

func handleCreateCash(r *http.Request) (any, error) {
	var in core.CashInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return core.CreateCash(in)
}

func handleUpdateCash(r *http.Request) (any, error) {
	var in core.CashInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return core.UpdateCash(chi.URLParam(r, "id"), in)
}
