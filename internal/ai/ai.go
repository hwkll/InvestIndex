// Package ai builds analysis context, calls DeepSeek when configured and falls
// back to a deterministic heuristic engine so the feature always returns a result.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
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
	Signal     string   `json:"signal"`
	Confidence float64  `json:"confidence"`
	Summary    string   `json:"summary"`
	Reasons    []string `json:"reasons"`
	Risks      []string `json:"risks"`
	Actions    []Action `json:"actions"`
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

// ---- heuristic engine ---------------------------------------------------

var signalCN = map[string]string{"buy": "买入/加仓", "sell": "卖出/减仓", "hold": "持有", "watch": "观望"}

func heuristicAsset(ctx map[string]any) Conclusion {
	ind, _ := ctx["indicators"].(*Indicators)
	pos, _ := ctx["position"].(map[string]any)
	asset, _ := ctx["asset"].(map[string]any)

	score := 0.0
	reasons := []string{}
	risks := []string{}
	actions := []Action{}

	if ind != nil {
		if ind.RSI != nil {
			switch {
			case *ind.RSI > 70:
				score--
				reasons = append(reasons, fmt.Sprintf("RSI=%.1f 处于超买区，短期回调风险较高", *ind.RSI))
			case *ind.RSI < 30:
				score++
				reasons = append(reasons, fmt.Sprintf("RSI=%.1f 处于超卖区，存在反弹机会", *ind.RSI))
			default:
				reasons = append(reasons, fmt.Sprintf("RSI=%.1f 处于中性区间", *ind.RSI))
			}
		}
		if ind.MACDHist != nil {
			if *ind.MACDHist > 0 {
				score++
				reasons = append(reasons, "MACD 柱状图为正，短期动能偏多")
			} else {
				score -= 0.5
				reasons = append(reasons, "MACD 柱状图为负，短期动能偏弱")
			}
		}
		if ind.KdjJ != nil {
			if *ind.KdjJ > 100 {
				reasons = append(reasons, fmt.Sprintf("KDJ J=%.1f 高位，注意高位钝化", *ind.KdjJ))
			} else if *ind.KdjJ < 0 {
				reasons = append(reasons, fmt.Sprintf("KDJ J=%.1f 低位，存在反弹动能", *ind.KdjJ))
			}
		}
		if ind.MA != nil && ind.MA["ma5"] != nil && ind.MA["ma20"] != nil {
			if *ind.MA["ma5"] > *ind.MA["ma20"] {
				score += 0.5
				reasons = append(reasons, "MA5 上穿 MA20，短期均线多头排列")
			} else {
				score -= 0.3
				reasons = append(reasons, "MA5 位于 MA20 下方，短期均线偏空")
			}
		}
	}

	qty := 0.0
	if pos != nil {
		if q, ok := pos["qty"].(float64); ok {
			qty = q
		}
		if fp, ok := pos["floatingPct"].(*float64); ok && fp != nil {
			if *fp > 0.3 {
				score -= 0.3
				reasons = append(reasons, fmt.Sprintf("当前浮盈 %.1f%%，可考虑分批止盈", *fp*100))
			} else if *fp < -0.2 {
				reasons = append(reasons, fmt.Sprintf("当前浮亏 %.1f%%，若基本面未恶化建议持有等待", *fp*100))
			}
		}
	}

	signal := "hold"
	switch {
	case score >= 1.2:
		signal = "buy"
	case score <= -1.2:
		signal = "sell"
	case score < -0.3:
		signal = "watch"
	}
	confidence := math.Min(0.9, 0.5+math.Abs(score)*0.15)

	risks = append(risks, "市场波动与流动性风险；本结论为模型估算，不构成投资建议")
	name := ""
	if asset != nil {
		if n, ok := asset["name"].(string); ok {
			name = n
		}
		if c, ok := asset["category"].(string); ok && c == "crypto" {
			risks = append(risks, "加密货币波动极大，单日涨跌可能超过 20%")
		}
	}
	switch signal {
	case "buy":
		verb := "建仓"
		if qty > 0 {
			verb = "加仓"
		}
		actions = append(actions, Action{Action: verb, Suggestion: "可在当前价附近分批买入，单次不超过总仓位的 10%"})
	case "sell":
		actions = append(actions, Action{Action: "减仓", Suggestion: "可考虑减持，设置止损位保护利润"})
	case "watch":
		actions = append(actions, Action{Action: "观望", Suggestion: "等待更明确的信号再行动"})
	default:
		actions = append(actions, Action{Action: "持有", Suggestion: "维持现有仓位，关注指标变化"})
	}
	return Conclusion{
		Signal: signal, Confidence: math.Round(confidence*100) / 100,
		Summary: fmt.Sprintf("%s 当前信号：%s，综合技术面评分 %.2f。", name, signalCN[signal], score),
		Reasons: reasons, Risks: risks, Actions: actions,
	}
}

func heuristicGlobal(ctx map[string]any) Conclusion {
	cats, _ := ctx["categories"].(map[string]any)
	bull, bear := 0, 0
	parts := []string{}
	names := map[string]string{"crypto": "加密货币", "fund": "基金", "gold": "黄金", "stock": "股票"}
	for _, c := range core.Categories {
		m, _ := cats[c].(map[string]any)
		if m == nil {
			continue
		}
		cnt, _ := m["count"].(int)
		if cnt == 0 {
			continue
		}
		fp, _ := m["floatingPct"].(*float64)
		if fp == nil {
			continue
		}
		if *fp > 0.05 {
			bull++
		}
		if *fp < -0.05 {
			bear++
		}
		parts = append(parts, fmt.Sprintf("%s 收益 %.1f%%", names[c], *fp*100))
	}
	signal := "hold"
	if bear > bull && bull == 0 {
		signal = "watch"
	}
	retStr := "暂无收益数据"
	if tr, ok := ctx["totalReturn"].(*float64); ok && tr != nil {
		retStr = fmt.Sprintf("收益率 %.2f%%", *tr*100)
	}
	dayPnl, _ := ctx["dayPnl"].(float64)
	body := "暂无持仓"
	if len(parts) > 0 {
		body = strings.Join(parts, "，")
	}
	summary := fmt.Sprintf("组合整体%s，今日盈亏 %+.2f。各分类：%s。", retStr, dayPnl, body)
	reasons := []string{summary}
	total, _ := ctx["totalAssets"].(float64)
	cash, _ := ctx["cashTotal"].(float64)
	if total > 0 && cash/total > 0.4 {
		reasons = append(reasons, fmt.Sprintf("现金占比偏高（%.0f%%），可关注仓位再平衡机会", cash/total*100))
	}
	return Conclusion{
		Signal: signal, Confidence: 0.55, Summary: summary, Reasons: reasons,
		Risks:   []string{"组合集中风险与单一资产波动；本结论不构成投资建议"},
		Actions: []Action{{Action: "持有", Suggestion: "维持战略配置，定期再平衡"}},
	}
}

// ---- DeepSeek -----------------------------------------------------------

const sysPrompt = `你是一名严谨的投资分析师。基于用户持仓与市场上下文给出决策建议。` +
	`必须严格返回 JSON：{"signal":"buy|sell|hold|watch","confidence":0.0-1.0,"summary":"一句话总结",` +
	`"reasons":["理由"],"risks":["风险"],"actions":[{"action":"加仓/减仓/清仓/持有/止损","suggestion":"具体建议"}]}。` +
	`不要输出 JSON 以外的任何内容。`

// deepseekClient is reused across calls to avoid TCP connection churn.
var deepseekClient = &http.Client{Timeout: 60 * time.Second}

func callDeepSeek(ctx context.Context, ctxData any, model string) (*Conclusion, string, error) {
	key := settings.Get("deepseek_api_key")
	if key == "" {
		return nil, "", nil // not configured => caller uses heuristic
	}
	if model == "" {
		model = settings.GetDefault("deepseek_model", "deepseek-chat")
	}
	ctxJSON, _ := json.Marshal(ctxData)
	body, _ := json.Marshal(map[string]any{
		"model":       model,
		"temperature": 0.3,
		"messages": []map[string]string{
			{"role": "system", "content": "你是投资分析助手，只输出结构化 JSON。"},
			{"role": "user", "content": sysPrompt + "\n上下文：" + string(ctxJSON)},
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

	var conclusion Conclusion
	usedModel := "heuristic"
	degraded := false
	notice := ""

	if c, m, cerr := callDeepSeek(ctx, ctxData, model); cerr != nil {
		conclusion = fallback(scope, ctxData)
		degraded, notice = true, "AI 服务调用失败，已使用本地启发式分析："+cerr.Error()
	} else if c != nil {
		conclusion, usedModel = *c, m
	} else {
		conclusion = fallback(scope, ctxData)
		degraded, notice = true, "未配置 DeepSeek API Key，已使用本地启发式分析"
	}

	id := cryptox.UUID()
	ctxJSON, _ := json.Marshal(ctxData)
	concJSON, _ := json.Marshal(conclusion)
	dur := time.Since(start).Milliseconds()
	createdAt := time.Now().UnixMilli()
	_, _ = store.Exec(`INSERT INTO ai_analyses(id,scope,asset_id,model,status,prompt_tokens,completion_tokens,context_snapshot,conclusion,error_msg,duration_ms,created_at)
	    VALUES(?,?,?,?,'ok',0,0,?,?,?,?,?)`,
		id, scope, nullIf(assetID), usedModel, string(ctxJSON), string(concJSON), nullIf(notice), dur, createdAt)

	return &Result{
		AnalysisID: id, Scope: scope, AssetID: assetID, Signal: conclusion.Signal,
		Model: usedModel, Conclusion: conclusion, Context: ctxData,
		DurationMs: dur, CreatedAt: createdAt, Degraded: degraded, Notice: notice,
	}, nil
}

func fallback(scope string, ctx map[string]any) Conclusion {
	if scope == "asset" {
		return heuristicAsset(ctx)
	}
	return heuristicGlobal(ctx)
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
