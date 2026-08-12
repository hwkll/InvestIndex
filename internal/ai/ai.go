// Package ai builds analysis context and calls DeepSeek for analysis. A valid
// DeepSeek API Key is required; when the key is missing or the call fails,
// Analyze returns an error (ErrNotConfigured or the call error) so the UI can
// surface the failure instead of producing a degraded / fake analysis.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"investhub/internal/core"
	"investhub/internal/cryptox"
	"investhub/internal/indicators"
	"investhub/internal/quotes"
	"investhub/internal/settings"
	"investhub/internal/store"
)

// limiter caps concurrent analyses (PRD: max 2 in flight).
var limiter = make(chan struct{}, 2)

// ErrNotConfigured is returned by Analyze when no DeepSeek API Key is set, so the
// caller can show a configuration prompt instead of a (fake) analysis.
var ErrNotConfigured = errors.New("未配置 DeepSeek API Key，请在设置中配置后使用 AI 分析")

// Indicators is the flattened technical snapshot handed to the model / UI.
type Indicators struct {
	RSI         *float64            `json:"rsi"`
	MACD        *float64            `json:"macd"`
	MACDSignal  *float64            `json:"macdSignal"`
	MACDHist    *float64            `json:"macdHist"`
	BollUpper   *float64            `json:"bollUpper"`
	BollMid     *float64            `json:"bollMid"`
	BollLower   *float64            `json:"bollLower"`
	KdjK        *float64            `json:"kdjK"`
	KdjD        *float64            `json:"kdjD"`
	KdjJ        *float64            `json:"kdjJ"`
	MA          map[string]*float64 `json:"ma"`
	MaxDrawdown *float64            `json:"maxDrawdown,omitempty"`
	Sharpe      *float64            `json:"sharpe,omitempty"`
	AnnualRet   *float64            `json:"annualReturn,omitempty"`
	Volatility  *float64            `json:"volatility,omitempty"`
}

// p converts a float to a pointer, mapping NaN/Inf to nil so JSON stays valid.
func p(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

// Compute derives the indicator snapshot for an asset.
func Compute(assetID, category string) *Indicators {
	kl := quotes.Kline(assetID, "1d", 120)
	if len(kl) < 2 {
		return nil
	}
	closes := make([]float64, len(kl))
	highs := make([]float64, len(kl))
	lows := make([]float64, len(kl))
	for i, c := range kl {
		closes[i], highs[i], lows[i] = c.Close, c.High, c.Low
	}
	m := indicators.MACD(closes, 12, 26, 9)
	b := indicators.Boll(closes, 20, 2)
	k := indicators.KDJ(highs, lows, closes, 9)
	out := &Indicators{
		RSI:        p(indicators.LastValid(indicators.RSI(closes, 14))),
		MACD:       p(indicators.LastValid(m.MACD)),
		MACDSignal: p(indicators.LastValid(m.Signal)),
		MACDHist:   p(indicators.LastValid(m.Hist)),
		BollUpper:  p(indicators.LastValid(b.Upper)),
		BollMid:    p(indicators.LastValid(b.Mid)),
		BollLower:  p(indicators.LastValid(b.Lower)),
		KdjK:       p(indicators.LastValid(k.K)),
		KdjD:       p(indicators.LastValid(k.D)),
		KdjJ:       p(indicators.LastValid(k.J)),
		MA: map[string]*float64{
			"ma5":  p(indicators.LastValid(indicators.SMA(closes, 5))),
			"ma10": p(indicators.LastValid(indicators.SMA(closes, 10))),
			"ma20": p(indicators.LastValid(indicators.SMA(closes, 20))),
			"ma60": p(indicators.LastValid(indicators.SMA(closes, 60))),
		},
	}
	if category == "fund" || category == "gold" {
		rets := indicators.DailyReturns(closes)
		out.MaxDrawdown = p(indicators.MaxDrawdown(closes))
		out.Sharpe = p(indicators.Sharpe(rets, 252))
		out.AnnualRet = p(indicators.AnnualizedReturn(closes))
		out.Volatility = p(indicators.StdDev(rets) * math.Sqrt(252))
	}
	return out
}

// IndicatorsFor is the API entry point for GET /assets/:id/indicators.
func IndicatorsFor(assetID string) *Indicators {
	a := core.GetAsset(assetID)
	if a == nil {
		return nil
	}
	return Compute(assetID, a.Category)
}

// Action is one recommended next step.
type Action struct {
	Action     string `json:"action"`
	Suggestion string `json:"suggestion"`
}

// Conclusion is the structured model output.
type Conclusion struct {
	Signal       string   `json:"signal"`
	Confidence   float64  `json:"confidence"`
	Summary      string   `json:"summary"`
	Reasons      []string `json:"reasons"`
	Risks        []string `json:"risks"`
	Actions      []Action `json:"actions"`
	CurrentPrice *float64 `json:"currentPrice,omitempty"` // filled from our quote data (asset scope only)
	TargetPrice  *float64 `json:"targetPrice,omitempty"`  // AI-provided expected price, given when signal is buy
}

// Result is what the API returns for one analysis run.
type Result struct {
	AnalysisID string     `json:"analysisId"`
	Scope      string     `json:"scope"`
	AssetID    string     `json:"assetId,omitempty"`
	Signal     string     `json:"signal"`
	Model      string     `json:"model"`
	Conclusion Conclusion `json:"conclusion"`
	Context    any        `json:"context"`
	DurationMs int64      `json:"durationMs"`
	CreatedAt  int64      `json:"createdAt"`
	Degraded   bool       `json:"degraded"`
	Notice     string     `json:"notice,omitempty"`
}

func buildAssetContext(assetID string) (map[string]any, error) {
	a := core.GetAsset(assetID)
	if a == nil {
		return nil, fmt.Errorf("标的不存在")
	}
	v := core.GetPositionView(assetID)
	if v == nil {
		return nil, fmt.Errorf("标的不存在")
	}
	kl := quotes.Kline(assetID, "1d", 30)
	closes := make([]float64, 0, len(kl))
	for _, c := range kl {
		closes = append(closes, math.Round(c.Close*10000)/10000)
	}
	return map[string]any{
		"asset": map[string]any{"name": a.Name, "symbol": a.Symbol, "category": a.Category, "currency": a.Currency},
		"position": map[string]any{
			"qty": v.Qty, "avgCost": v.AvgCost, "costTotal": v.CostTotal, "price": v.Price,
			"chgPct": v.ChgPct, "marketValue": v.MarketValue, "floatingPnl": v.FloatingPnl,
			"floatingPct": v.FloatingPct, "realizedPnl": v.RealizedPnl,
			"accumulatedPnl": v.AccumulatedPnl, "daysHeld": v.DaysHeld,
		},
		"quote":        map[string]any{"price": v.Price, "chgPct": v.ChgPct},
		"indicators":   Compute(assetID, a.Category),
		"recentCloses": closes,
	}, nil
}

func buildGlobalContext() map[string]any {
	s := core.GlobalSummary()
	cats := map[string]any{}
	for _, c := range core.Categories {
		cat := s.Categories[c]
		cats[c] = map[string]any{
			"marketValue": cat.MarketValue, "floatingPnl": cat.FloatingPnl,
			"realizedPnl": cat.RealizedPnl, "floatingPct": cat.FloatingPct, "count": cat.Count,
		}
	}
	return map[string]any{
		"asOf": time.Now().Format(time.RFC3339), "mainCurrency": s.MainCurrency,
		"totalAssets": s.TotalAssets, "cashTotal": s.CashTotal, "totalPnl": s.TotalPnl,
		"totalReturn": s.TotalReturn, "dayPnl": s.DayPnl, "categories": cats,
	}
}

// ---- DeepSeek -----------------------------------------------------------

const sysPrompt = `你是一名严谨、专业的投资分析师，服务于个人投资管理工具 InvestHub。` +
	`请基于用户提供的「持仓、行情与技术指标」上下文，给出客观、可执行的投资决策建议。` +
	`` +
	`重要约束：` +
	`1. 当前真实时间（北京时间 UTC+8）为：{{now}}。所有涉及"今天 / 本周 / 近期 / 过去 N 天"的判断，都必须以此时间为准，严禁使用你训练知识里的时间。` +
	`2. 你只能依据上下文中给出的数据进行分析，不得臆造上下文中不存在的指标或数值。` +
	`3. 必须且只能返回如下 JSON，不要输出任何额外文字、Markdown 代码块或解释：` +
	`{"signal":"buy|sell|hold|watch","confidence":0.0-1.0,"summary":"一句话结论","reasons":["支撑理由，尽量结合具体数值"],"risks":["主要风险点"],"actions":[{"action":"加仓|减仓|清仓|持有|止损|观望","suggestion":"具体、可执行的建议"}],"targetPrice":数值或null}` +
	`4. signal 必须从给定枚举中选取；confidence 为 0~1 之间的置信度，数值越高代表判断越明确。` +
	`5. 风险提示中必须包含：本结论由 AI 模型生成，不构成任何投资建议。` +
	`6. 若上下文数据不足（如无可交易持仓、缺少技术指标），请如实说明，不要强行给出买卖信号，可将 signal 设为 "hold" 并在 reasons 中解释原因。` +
	`7. 上下文已提供该标的的当前价（quote.price）。当 signal 为 "buy" 时，必须在 JSON 中给出 targetPrice（建议的目标/预期买入价位，单位与 quote.price 一致，须为大于 0 的数值，并明显区别于当前价）；若 signal 不是 "buy"，targetPrice 可省略或置为 null。`

// deepseekClient is reused across calls to avoid TCP connection churn.
var deepseekClient = &http.Client{Timeout: 60 * time.Second}

func callDeepSeek(ctx context.Context, ctxData any, model string) (*Conclusion, string, error) {
	key := settings.Get("deepseek_api_key")
	if key == "" {
		return nil, "", ErrNotConfigured
	}
	if model == "" {
		model = settings.GetDefault("deepseek_model", "deepseek-v4-flash")
	}
	now := time.Now().Format("2006-01-02 15:04:05 (UTC+8 北京时间)")
	sysContent := strings.ReplaceAll(sysPrompt, "{{now}}", now)
	ctxJSON, _ := json.Marshal(ctxData)
	body, _ := json.Marshal(map[string]any{
		"model":       model,
		"temperature": 0.3,
		"messages": []map[string]string{
			{"role": "system", "content": sysContent},
			{"role": "user", "content": "以下是分析所需的上下文数据（JSON 格式）：\n" + string(ctxJSON)},
		},
		"response_format": map[string]string{"type": "json_object"},
	})
	timeout := 60 * time.Second
	if strings.Contains(model, "reasoner") {
		timeout = 120 * time.Second
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	// Use a client with the appropriate timeout for this specific request.
	cli := &http.Client{Timeout: timeout}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, model, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, model, fmt.Errorf("DeepSeek HTTP %d", resp.StatusCode)
	}
	// Limit response body to 1 MB to prevent memory exhaustion.
	limited := io.LimitReader(resp.Body, 1<<20)
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(limited).Decode(&out); err != nil {
		return nil, model, err
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return nil, model, fmt.Errorf("DeepSeek 返回为空")
	}
	var c Conclusion
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &c); err != nil {
		return nil, model, fmt.Errorf("DeepSeek 返回非 JSON")
	}
	return normalize(&c), model, nil
}

func normalize(c *Conclusion) *Conclusion {
	switch c.Signal {
	case "buy", "sell", "hold", "watch":
	default:
		c.Signal = "hold"
	}
	c.Confidence = math.Max(0, math.Min(1, c.Confidence))
	if c.TargetPrice != nil {
		if math.IsNaN(*c.TargetPrice) || *c.TargetPrice <= 0 {
			c.TargetPrice = nil
		}
	}
	if c.Reasons == nil {
		c.Reasons = []string{}
	}
	if c.Risks == nil {
		c.Risks = []string{}
	}
	if c.Actions == nil {
		c.Actions = []Action{}
	}
	return c
}

// Analyze runs one analysis (global or per-asset) and persists it.
// ctx allows the HTTP handler to cancel the request when the client disconnects.
func Analyze(ctx context.Context, scope, assetID, model string) (*Result, error) {
	if settings.Get("deepseek_api_key") == "" {
		return nil, ErrNotConfigured
	}
	select {
	case limiter <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-limiter }()

	start := time.Now()
	var ctxData map[string]any
	var err error
	if scope == "asset" {
		ctxData, err = buildAssetContext(assetID)
		if err != nil {
			return nil, err
		}
	} else {
		scope = "global"
		ctxData = buildGlobalContext()
	}

	c, m, cerr := callDeepSeek(ctx, ctxData, model)
	if cerr != nil {
		return nil, fmt.Errorf("AI 分析调用失败：%w", cerr)
	}
	if c == nil {
		return nil, ErrNotConfigured
	}
	conclusion := *c
	usedModel := m

	// Surface the current price from our own quote data (reliable, asset scope only)
	// so the analysis output always shows a price even if the model omits it.
	if scope == "asset" {
		if q, ok := ctxData["quote"].(map[string]any); ok {
			if pr, ok := q["price"].(float64); ok && pr > 0 {
				conclusion.CurrentPrice = &pr
			}
		}
	}

	id := cryptox.UUID()
	ctxJSON, _ := json.Marshal(ctxData)
	concJSON, _ := json.Marshal(conclusion)
	dur := time.Since(start).Milliseconds()
	createdAt := time.Now().UnixMilli()
	_, _ = store.Exec(`INSERT INTO ai_analyses(id,scope,asset_id,model,status,prompt_tokens,completion_tokens,context_snapshot,conclusion,error_msg,duration_ms,created_at)
	    VALUES(?,?,?,?,'ok',0,0,?,?,?,?,?)`,
		id, scope, nullIf(assetID), usedModel, string(ctxJSON), string(concJSON), nil, dur, createdAt)

	return &Result{
		AnalysisID: id, Scope: scope, AssetID: assetID, Signal: conclusion.Signal,
		Model: usedModel, Conclusion: conclusion, Context: ctxData,
		DurationMs: dur, CreatedAt: createdAt, Degraded: false, Notice: "",
	}, nil
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- history ------------------------------------------------------------

// HistoryItem is one row of the analysis history list.
type HistoryItem struct {
	ID         string      `json:"id"`
	Scope      string      `json:"scope"`
	AssetID    string      `json:"assetId"`
	AssetName  string      `json:"assetName"`
	Model      string      `json:"model"`
	Status     string      `json:"status"`
	Conclusion *Conclusion `json:"conclusion"`
	DurationMs int64       `json:"durationMs"`
	CreatedAt  int64       `json:"createdAt"`
}

// HistoryPage is a paginated analysis history.
type HistoryPage struct {
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
	Items []HistoryItem `json:"items"`
}

// List returns stored analyses, newest first.
func List(scope, assetID string, page, size int) HistoryPage {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	where, args := " WHERE 1=1", []any{}
	if scope != "" {
		where += " AND a.scope = ?"
		args = append(args, scope)
	}
	if assetID != "" {
		where += " AND a.asset_id = ?"
		args = append(args, assetID)
	}
	total := int(store.ScalarInt(`SELECT COUNT(*) FROM ai_analyses a`+where, args...))
	rows, err := store.Query(`SELECT a.id, a.scope, COALESCE(a.asset_id,''), COALESCE(s.name,''), a.model, a.status,
	    COALESCE(a.conclusion,''), COALESCE(a.duration_ms,0), a.created_at
	    FROM ai_analyses a LEFT JOIN assets s ON s.id = a.asset_id`+where+
		` ORDER BY a.created_at DESC LIMIT ? OFFSET ?`, append(args, size, (page-1)*size)...)
	items := []HistoryItem{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var it HistoryItem
			var conc string
			if rows.Scan(&it.ID, &it.Scope, &it.AssetID, &it.AssetName, &it.Model, &it.Status,
				&conc, &it.DurationMs, &it.CreatedAt) != nil {
				continue
			}
			if conc != "" {
				var c Conclusion
				if json.Unmarshal([]byte(conc), &c) == nil {
					it.Conclusion = &c
				}
			}
			items = append(items, it)
		}
	}
	return HistoryPage{Total: total, Page: page, Size: size, Items: items}
}

// Detail returns a single analysis with its full context snapshot.
func Detail(id string) map[string]any {
	var scope, assetID, model, status, ctxRaw, concRaw string
	var dur, createdAt int64
	err := store.QueryRow(`SELECT scope, COALESCE(asset_id,''), model, status, context_snapshot, COALESCE(conclusion,''), COALESCE(duration_ms,0), created_at
	    FROM ai_analyses WHERE id=?`, id).
		Scan(&scope, &assetID, &model, &status, &ctxRaw, &concRaw, &dur, &createdAt)
	if err != nil {
		return nil
	}
	var ctx any
	_ = json.Unmarshal([]byte(ctxRaw), &ctx)
	var conc Conclusion
	_ = json.Unmarshal([]byte(concRaw), &conc)
	return map[string]any{
		"id": id, "scope": scope, "assetId": assetID, "model": model, "status": status,
		"context": ctx, "conclusion": conc, "durationMs": dur, "createdAt": createdAt,
	}
}

// Delete removes one stored analysis.
func Delete(id string) map[string]any {
	_, _ = store.Exec(`DELETE FROM ai_analyses WHERE id=?`, id)
	return map[string]any{"id": id}
}
