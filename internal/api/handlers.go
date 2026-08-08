package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"investhub/internal/ai"
	"investhub/internal/alerts"
	"investhub/internal/core"
	"investhub/internal/quotes"
	"investhub/internal/settings"
	"investhub/internal/store"
)

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
	quotes.AddAsset(quotes.Asset{ID: a.ID, Category: a.Category, Symbol: a.Symbol, Currency: a.Currency, Provider: a.Provider})
	return a, nil
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
		return nil, err
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

func handleUpdateSettings(r *http.Request) (any, error) {
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
	}
	updates := map[string]bool{}
	for k, v := range body {
		if k == "access_pin_hash" {
			continue // only settable through /auth/pin
		}
		if !allowedKeys[k] {
			continue // silently skip unknown keys
		}
		settings.Set(k, toStr(v))
		updates[k] = true
		if k == "data_source_mode" {
			quotes.SetMode(toStr(v))
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
