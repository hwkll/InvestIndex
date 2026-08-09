// Package core holds the domain model: assets, transactions, the weighted-average
// position/PnL engine, cash accounts, snapshots and trend aggregation.
package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"investhub/internal/cryptox"
	"investhub/internal/quotes"
	"investhub/internal/settings"
	"investhub/internal/store"
)

// APIError carries a PRD business error code.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return e.Msg }

func errf(code int, msg string) error { return &APIError{Code: code, Msg: msg} }

// Categories is the fixed asset taxonomy from the PRD.
var Categories = []string{"crypto", "fund", "gold", "stock"}

var catNames = map[string]string{"crypto": "加密货币", "fund": "基金", "gold": "黄金", "stock": "股票"}

func nowMs() int64         { return time.Now().UnixMilli() }
func today() string        { return time.Now().Format("2006-01-02") }
func f2(v float64) float64 { return math.Round(v*100) / 100 }

// fxRateToCNY returns how many CNY equal 1 unit of the given currency (1 for CNY).
// Rates come from the fx_rates table; unknown currencies fall back to unity so a
// missing row never silently zeroes a conversion.
func fxRateToCNY(cur string) float64 {
	if cur == "" || cur == "CNY" {
		return 1
	}
	v := store.ScalarFloat(`SELECT rate FROM fx_rates WHERE currency = ?`, cur)
	if v <= 0 {
		if cur == "USD" {
			if r, err := strconv.ParseFloat(settings.GetDefault("rate_usd_cny", "7.2"), 64); err == nil && r > 0 {
				return r
			}
		}
		return 1
	}
	return v
}

// Rate returns the USD→CNY rate used for cross-currency aggregation (legacy API).
func Rate() float64 { return fxRateToCNY("USD") }

// MainCurrency is the display currency for aggregated figures.
func MainCurrency() string { return settings.GetDefault("currency", "CNY") }

// Convert translates an amount between any two known currencies via a CNY pivot.
func Convert(amount float64, from, to string) float64 {
	if from == to || from == "" || to == "" {
		return amount
	}
	return amount * fxRateToCNY(from) / fxRateToCNY(to)
}

// ---- Assets -------------------------------------------------------------

// Asset is the API representation of a tracked instrument.
type Asset struct {
	ID        string        `json:"id"`
	Category  string        `json:"category"`
	Name      string        `json:"name"`
	Symbol    string        `json:"symbol"`
	SubType   string        `json:"subType"`
	Currency  string        `json:"currency"`
	Provider  string        `json:"provider"`
	Status    string        `json:"status"`
	Pinned    bool          `json:"pinned"`
	Tags      []string      `json:"tags"`
	Remark    string        `json:"remark"`
	CreatedAt int64         `json:"createdAt"`
	Quote     *quotes.Quote `json:"quote"`
	Health    string        `json:"health"`
}

type assetRow struct {
	ID, Category, Name, Symbol, Currency, Provider, Status string
	SubType, Tags, Remark                                  sql.NullString
	Pinned                                                 int
	CreatedAt                                              int64
}

const assetCols = `id, category, name, symbol, sub_type, currency, provider, status, pinned, tags, remark, created_at`

func scanAsset(s interface{ Scan(...any) error }) (*assetRow, error) {
	var a assetRow
	err := s.Scan(&a.ID, &a.Category, &a.Name, &a.Symbol, &a.SubType, &a.Currency,
		&a.Provider, &a.Status, &a.Pinned, &a.Tags, &a.Remark, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func decorate(a *assetRow) Asset {
	q := quotes.Get(a.ID)
	health := "ok"
	switch {
	case a.Status == "disabled":
		health = "disabled"
	case q == nil:
		health = "stale"
	case q.Status == "ok":
		health = "ok"
	case q.Status == "sim":
		health = "sim"
	case q.Status == "nosource":
		health = "nosource"
	default:
		health = "stale"
	}
	var tags []string
	if a.Tags.Valid && a.Tags.String != "" {
		_ = json.Unmarshal([]byte(a.Tags.String), &tags)
	}
	if tags == nil {
		tags = []string{}
	}
	return Asset{
		ID: a.ID, Category: a.Category, Name: a.Name, Symbol: a.Symbol,
		SubType: a.SubType.String, Currency: a.Currency, Provider: a.Provider,
		Status: a.Status, Pinned: a.Pinned == 1, Tags: tags, Remark: a.Remark.String,
		CreatedAt: a.CreatedAt, Quote: q, Health: health,
	}
}

// ListAssets returns all assets, optionally filtered by category.
func ListAssets(category string) []Asset {
	q := `SELECT ` + assetCols + ` FROM assets`
	args := []any{}
	if category != "" {
		q += ` WHERE category = ?`
		args = append(args, category)
	}
	q += ` ORDER BY pinned DESC, name ASC`
	rows, err := store.Query(q, args...)
	if err != nil {
		return []Asset{}
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		if a, err := scanAsset(rows); err == nil {
			out = append(out, decorate(a))
		}
	}
	return out
}

// GetAsset returns one asset or nil.
func GetAsset(id string) *Asset {
	a, err := scanAsset(store.QueryRow(`SELECT `+assetCols+` FROM assets WHERE id = ?`, id))
	if err != nil {
		return nil
	}
	d := decorate(a)
	return &d
}

// AssetInput is the create/update payload.
type AssetInput struct {
	Category string   `json:"category"`
	Name     string   `json:"name"`
	Symbol   string   `json:"symbol"`
	SubType  string   `json:"subType"`
	Currency string   `json:"currency"`
	Provider string   `json:"provider"`
	Remark   string   `json:"remark"`
	Tags     []string `json:"tags"`
	Pinned   *bool    `json:"pinned"`
}

func defaultCurrency(cat string) string {
	if cat == "crypto" {
		return "USD"
	}
	return "CNY"
}

func defaultProvider(cat string) string {
	switch cat {
	case "crypto":
		return "binance"
	case "fund":
		return "fund_eastmoney"
	case "gold":
		return "sge"
	case "stock":
		return "sina"
	}
	return "simulator"
}

// CreateAsset validates and inserts a new instrument.
func CreateAsset(in AssetInput) (*Asset, error) {
	if in.Category == "" || in.Name == "" || in.Symbol == "" {
		return nil, errf(40001, "名称、分类、代码为必填")
	}
	if store.ScalarInt(`SELECT COUNT(*) FROM asset_categories WHERE code = ?`, in.Category) == 0 {
		return nil, errf(40001, "未知分类")
	}
	if store.ScalarInt(`SELECT COUNT(*) FROM assets WHERE category=? AND symbol=?`, in.Category, in.Symbol) > 0 {
		return nil, errf(40901, "该标的已存在 (分类+代码 唯一)")
	}
	id, ts := cryptox.UUID(), nowMs()
	cur := in.Currency
	if cur == "" {
		cur = defaultCurrency(in.Category)
	}
	prov := in.Provider
	if prov == "" {
		prov = defaultProvider(in.Category)
	}
	tagsJSON := "[]"
	if len(in.Tags) > 0 {
		b, _ := json.Marshal(in.Tags)
		tagsJSON = string(b)
	}
	_, err := store.Exec(`INSERT INTO assets(id,category,name,symbol,sub_type,currency,provider,extra,status,pinned,tags,remark,created_at,updated_at)
	    VALUES(?,?,?,?,?,?,?,NULL,'active',0,?,?,?,?)`,
		id, in.Category, in.Name, in.Symbol, nullStr(in.SubType), cur, prov, tagsJSON, nullStr(in.Remark), ts, ts)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	quotes.AddAsset(quotes.Asset{ID: id, Category: in.Category, Symbol: in.Symbol, Currency: cur, Provider: prov})
	return GetAsset(id), nil
}

// UpdateAsset patches an existing instrument.
func UpdateAsset(id string, in AssetInput) (*Asset, error) {
	cur, err := scanAsset(store.QueryRow(`SELECT `+assetCols+` FROM assets WHERE id = ?`, id))
	if err != nil {
		return nil, errf(40401, "标的不存在")
	}
	name := pick(in.Name, cur.Name)
	symbol := pick(in.Symbol, cur.Symbol)
	currency := pick(in.Currency, cur.Currency)
	provider := pick(in.Provider, cur.Provider)
	subType := pick(in.SubType, cur.SubType.String)
	remark := pick(in.Remark, cur.Remark.String)
	if symbol != cur.Symbol &&
		store.ScalarInt(`SELECT COUNT(*) FROM assets WHERE category=? AND symbol=? AND id<>?`, cur.Category, symbol, id) > 0 {
		return nil, errf(40901, "该代码已存在")
	}
	tagsJSON := cur.Tags.String
	if in.Tags != nil {
		b, _ := json.Marshal(in.Tags)
		tagsJSON = string(b)
	}
	pinned := cur.Pinned
	if in.Pinned != nil {
		pinned = 0
		if *in.Pinned {
			pinned = 1
		}
	}
	_, err = store.Exec(`UPDATE assets SET name=?, symbol=?, sub_type=?, currency=?, provider=?, remark=?, tags=?, pinned=?, updated_at=? WHERE id=?`,
		name, symbol, nullStr(subType), currency, provider, nullStr(remark), tagsJSON, pinned, nowMs(), id)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	return GetAsset(id), nil
}

// DeleteAsset removes (hard) or disables (soft) an instrument.
func DeleteAsset(id, mode string) (map[string]any, error) {
	if store.ScalarInt(`SELECT COUNT(*) FROM assets WHERE id = ?`, id) == 0 {
		return nil, errf(40401, "标的不存在")
	}
	txCount := store.ScalarInt(`SELECT COUNT(*) FROM transactions WHERE asset_id = ?`, id)
	if mode == "disable" {
		_, _ = store.Exec(`UPDATE assets SET status='disabled' WHERE id=?`, id)
		return map[string]any{"mode": "disable", "removedTx": 0}, nil
	}
	if _, err := store.Exec(`DELETE FROM assets WHERE id = ?`, id); err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"mode": "hard", "removedTx": txCount}, nil
}

// ---- Position engine ----------------------------------------------------

// Position is the derived state of a holding.
type Position struct {
	Qty         float64
	AvgCost     float64
	CostTotal   float64
	RealizedPnl float64
	FirstBuy    int64
	LastBuy     int64
}

// ComputePosition replays transactions with moving weighted-average cost.
func ComputePosition(assetID string) Position {
	return computePositionExcluding(assetID, "")
}

func computePositionExcluding(assetID, excludeID string) Position {
	q := `SELECT direction, trade_time, quantity, price, fee FROM transactions WHERE asset_id = ?`
	args := []any{assetID}
	if excludeID != "" {
		q += ` AND id <> ?`
		args = append(args, excludeID)
	}
	q += ` ORDER BY trade_time ASC`
	rows, err := store.Query(q, args...)
	if err != nil {
		return Position{}
	}
	defer rows.Close()
	var p Position
	for rows.Next() {
		var dir string
		var tt int64
		var qty, price, fee float64
		if err := rows.Scan(&dir, &tt, &qty, &price, &fee); err != nil {
			continue
		}
		if dir == "buy" {
			p.CostTotal += price*qty + fee
			p.Qty += qty
			if p.Qty > 0 {
				p.AvgCost = p.CostTotal / p.Qty
			}
			if p.FirstBuy == 0 {
				p.FirstBuy = tt
			}
			p.LastBuy = tt
		} else {
			p.RealizedPnl += (price-p.AvgCost)*qty - fee
			p.Qty -= qty
			if p.Qty > 1e-12 {
				p.CostTotal = p.AvgCost * p.Qty
			} else {
				p.Qty, p.AvgCost, p.CostTotal = 0, 0, 0
			}
		}
	}
	return p
}

// PositionView is the API shape for a single holding.
type PositionView struct {
	AssetID        string   `json:"assetId"`
	Category       string   `json:"category"`
	Name           string   `json:"name"`
	Symbol         string   `json:"symbol"`
	Currency       string   `json:"currency"`
	Qty            float64  `json:"qty"`
	AvgCost        float64  `json:"avgCost"`
	CostTotal      float64  `json:"costTotal"`
	Price          float64  `json:"price"`
	ChgPct         float64  `json:"chgPct"`
	MarketValue    float64  `json:"marketValue"`
	FloatingPnl    float64  `json:"floatingPnl"`
	FloatingPct    *float64 `json:"floatingPct"`
	RealizedPnl    float64  `json:"realizedPnl"`
	AccumulatedPnl float64  `json:"accumulatedPnl"`
	DaysHeld       int      `json:"daysHeld"`
	QuoteStatus    string   `json:"quoteStatus"`
}

// GetPositionView builds the derived view for one asset.
func GetPositionView(assetID string) *PositionView {
	var cat, name, symbol, currency string
	err := store.QueryRow(`SELECT category, name, symbol, currency FROM assets WHERE id = ?`, assetID).
		Scan(&cat, &name, &symbol, &currency)
	if err != nil {
		return nil
	}
	p := ComputePosition(assetID)
	q := quotes.Get(assetID)
	price, chg, status := 0.0, 0.0, "stale"
	if q != nil {
		price, chg, status = q.Price, q.ChgPct, q.Status
	}
	mv := price * p.Qty
	fl := mv - p.CostTotal
	var flPct *float64
	if p.CostTotal > 0 {
		v := fl / p.CostTotal
		flPct = &v
	}
	days := 0
	if p.FirstBuy > 0 {
		days = int((nowMs() - p.FirstBuy) / 86400000)
	}
	return &PositionView{
		AssetID: assetID, Category: cat, Name: name, Symbol: symbol, Currency: currency,
		Qty: p.Qty, AvgCost: p.AvgCost, CostTotal: p.CostTotal,
		Price: price, ChgPct: chg, MarketValue: mv,
		FloatingPnl: fl, FloatingPct: flPct, RealizedPnl: p.RealizedPnl,
		AccumulatedPnl: fl + p.RealizedPnl, DaysHeld: days, QuoteStatus: status,
	}
}

// Subtotal aggregates a set of positions inside one currency bucket.
type Subtotal struct {
	Currency    string   `json:"currency"`
	MarketValue float64  `json:"marketValue"`
	CostTotal   float64  `json:"costTotal"`
	FloatingPnl float64  `json:"floatingPnl"`
	RealizedPnl float64  `json:"realizedPnl"`
	FloatingPct *float64 `json:"floatingPct"`
}

// CategoryPositions bundles the holdings of one category with its subtotal.
type CategoryPositions struct {
	Items    []PositionView `json:"items"`
	Subtotal Subtotal       `json:"subtotal"`
}

func aggregate(items []PositionView) Subtotal {
	// Pick the dominant currency among items so a category whose assets mix
	// currencies (e.g. a USD crypto alongside a CNY one) sums correctly. Each
	// item's monetary fields are converted into the dominant currency before
	// accumulation, replacing the old items[0].Currency bucket bias that could
	// silently mis-sum mixed-currency holdings.
	counts := map[string]int{}
	for _, v := range items {
		c := v.Currency
		if c == "" {
			c = "CNY"
		}
		counts[c]++
	}
	dom := "CNY"
	best := -1
	for c, n := range counts {
		if n > best {
			best, dom = n, c
		}
	}
	s := Subtotal{Currency: dom}
	for _, v := range items {
		c := v.Currency
		if c == "" {
			c = "CNY"
		}
		s.MarketValue += Convert(v.MarketValue, c, dom)
		s.CostTotal += Convert(v.CostTotal, c, dom)
		s.FloatingPnl += Convert(v.FloatingPnl, c, dom)
		s.RealizedPnl += Convert(v.RealizedPnl, c, dom)
	}
	if s.CostTotal > 0 {
		p := s.FloatingPnl / s.CostTotal
		s.FloatingPct = &p
	}
	return s
}

// PositionsByCategory returns the holdings of one asset class.
func PositionsByCategory(category string) CategoryPositions {
	rows, err := store.Query(`SELECT id FROM assets WHERE category=? AND status='active'`, category)
	items := []PositionView{}
	if err == nil {
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			if v := GetPositionView(id); v != nil {
				items = append(items, *v)
			}
		}
	}
	return CategoryPositions{Items: items, Subtotal: aggregate(items)}
}

// ---- Global summary -----------------------------------------------------

// TopMovers holds the best/worst performer of a category.
type TopMovers struct {
	Up   *PositionView `json:"up"`
	Down *PositionView `json:"down"`
}

// CategorySummary is one row of the dashboard category breakdown.
type CategorySummary struct {
	Category    string    `json:"category"`
	Label       string    `json:"label"`
	MarketValue float64   `json:"marketValue"`
	CostTotal   float64   `json:"costTotal"`
	FloatingPnl float64   `json:"floatingPnl"`
	RealizedPnl float64   `json:"realizedPnl"`
	FloatingPct *float64  `json:"floatingPct"`
	Count       int       `json:"count"`
	Top         TopMovers `json:"top"`
}

// DistSegment is one slice of the allocation donut.
type DistSegment struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// Summary is the dashboard payload.
type Summary struct {
	MainCurrency      string                     `json:"mainCurrency"`
	TotalAssets       float64                    `json:"totalAssets"`
	CashTotal         float64                    `json:"cashTotal"`
	CashRatio         float64                    `json:"cashRatio"`
	InvestmentValue   float64                    `json:"investmentValue"`
	TotalCost         float64                    `json:"totalCost"`
	TotalFloatingPnl  float64                    `json:"totalFloatingPnl"`
	TotalRealizedPnl  float64                    `json:"totalRealizedPnl"`
	TotalPnl          float64                    `json:"totalPnl"`
	TotalReturn       *float64                   `json:"totalReturn"`
	DayPnl            float64                    `json:"dayPnl"`
	Categories        map[string]CategorySummary `json:"categories"`
	Distribution      []DistSegment              `json:"distribution"`
	AssetCount        int                        `json:"assetCount"`
	UnreadAlertsCount int                        `json:"unreadAlertsCount"`
	QuoteSimCount     int                        `json:"quoteSimCount"`
	QuoteStaleCount   int                        `json:"quoteStaleCount"`
}

func topMovers(items []PositionView) TopMovers {
	held := []PositionView{}
	for _, v := range items {
		if v.Qty > 0 {
			held = append(held, v)
		}
	}
	if len(held) == 0 {
		return TopMovers{}
	}
	up, down := held[0], held[0]
	for _, v := range held {
		if v.ChgPct > up.ChgPct {
			up = v
		}
		if v.ChgPct < down.ChgPct {
			down = v
		}
	}
	return TopMovers{Up: &up, Down: &down}
}

// GlobalSummary aggregates everything for the dashboard.
func GlobalSummary() Summary {
	main := MainCurrency()
	s := Summary{MainCurrency: main, Categories: map[string]CategorySummary{}}
	for _, c := range Categories {
		cp := PositionsByCategory(c)
		sub := cp.Subtotal
		cs := CategorySummary{
			Category:    c,
			Label:       catNames[c],
			MarketValue: Convert(sub.MarketValue, sub.Currency, main),
			CostTotal:   Convert(sub.CostTotal, sub.Currency, main),
			FloatingPnl: Convert(sub.FloatingPnl, sub.Currency, main),
			RealizedPnl: Convert(sub.RealizedPnl, sub.Currency, main),
			FloatingPct: sub.FloatingPct,
			Count:       len(cp.Items),
			Top:         topMovers(cp.Items),
		}
		s.Categories[c] = cs
		s.InvestmentValue += cs.MarketValue
		s.TotalCost += cs.CostTotal
		s.TotalFloatingPnl += cs.FloatingPnl
		s.TotalRealizedPnl += cs.RealizedPnl
	}
	cash := CashAccounts()
	s.CashTotal = cash.TotalBalance
	s.TotalAssets = s.InvestmentValue + s.CashTotal
	if s.TotalAssets > 0 {
		s.CashRatio = s.CashTotal / s.TotalAssets
	}
	s.TotalPnl = s.TotalFloatingPnl + s.TotalRealizedPnl
	if s.TotalCost > 0 {
		r := s.TotalPnl / s.TotalCost
		s.TotalReturn = &r
	}
	s.DayPnl = dayPnl(main)
	s.Distribution = []DistSegment{}
	for _, c := range Categories {
		if v := s.Categories[c].MarketValue; v > 0 {
			s.Distribution = append(s.Distribution, DistSegment{Key: c, Label: catNames[c], Value: v})
		}
	}
	if s.CashTotal > 0 {
		s.Distribution = append(s.Distribution, DistSegment{Key: "cash", Label: "现金", Value: s.CashTotal})
	}
	s.AssetCount = int(store.ScalarInt(`SELECT COUNT(*) FROM assets WHERE status='active'`))
	s.UnreadAlertsCount = int(store.ScalarInt(`SELECT COUNT(*) FROM alert_events WHERE read = 0`))

	// Count how many active assets are showing simulated vs stale (real-requested
	// but unavailable) quotes, so the dashboard can surface a transparency banner.
	simN, staleN := 0, 0
	qrows, _ := store.Query(`SELECT id FROM assets WHERE status='active'`)
	if qrows != nil {
		defer qrows.Close()
		for qrows.Next() {
			var aid string
			if qrows.Scan(&aid) != nil {
				continue
			}
			q := quotes.Get(aid)
			if q == nil || q.Status == "stale" {
				staleN++
			} else if q.Status == "sim" {
				simN++
			}
		}
	}
	s.QuoteSimCount = simN
	s.QuoteStaleCount = staleN
	return s
}

// summaryCache holds a short-lived snapshot of GlobalSummary to absorb the refresh
// storms triggered by SSE quote ticks (each tick would otherwise force a full
// recompute). A 3s TTL is short enough to stay fresh yet long enough to collapse
// bursts into a single compute.
var summaryCache = struct {
	mu    sync.RWMutex
	at    int64
	ready bool
	value Summary
}{}

// GlobalSummaryCached returns the dashboard summary, serving a cached copy when one
// is younger than 3 seconds.
func GlobalSummaryCached() Summary {
	now := time.Now().UnixMilli()
	summaryCache.mu.RLock()
	if summaryCache.ready && now-summaryCache.at < 3000 {
		v := summaryCache.value
		summaryCache.mu.RUnlock()
		return v
	}
	summaryCache.mu.RUnlock()

	summaryCache.mu.Lock()
	defer summaryCache.mu.Unlock()
	if summaryCache.ready && now-summaryCache.at < 3000 {
		return summaryCache.value
	}
	v := GlobalSummary()
	summaryCache.value = v
	summaryCache.at = now
	summaryCache.ready = true
	return v
}

func dayPnl(main string) float64 {
	rows, err := store.Query(`SELECT id, currency FROM assets WHERE status='active'`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	type row struct{ id, cur string }
	var list []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.cur) == nil {
			list = append(list, r)
		}
	}
	total := 0.0
	for _, r := range list {
		v := GetPositionView(r.id)
		if v == nil || v.Qty <= 0 {
			continue
		}
		prev := v.Price
		if q := quotes.Get(r.id); q != nil && q.PrevClose > 0 {
			prev = q.PrevClose
		}
		total += Convert(v.Qty*(v.Price-prev), r.cur, main)
	}
	return total
}

// ---- Transactions -------------------------------------------------------

// TxInput is the create/update payload for a trade record.
type TxInput struct {
	AssetID   string   `json:"assetId"`
	Direction string   `json:"direction"`
	TradeTime int64    `json:"tradeTime"`
	Quantity  *float64 `json:"quantity"`
	Price     *float64 `json:"price"`
	Fee       *float64 `json:"fee"`
	Remark    string   `json:"remark"`
	Source    string   `json:"source"`
}

// Tx is the API representation of a trade record.
type Tx struct {
	ID          string  `json:"id"`
	AssetID     string  `json:"assetId"`
	AssetName   string  `json:"assetName"`
	AssetSymbol string  `json:"assetSymbol"`
	Category    string  `json:"category"`
	Direction   string  `json:"direction"`
	TradeTime   int64   `json:"tradeTime"`
	Quantity    float64 `json:"quantity"`
	Price       float64 `json:"price"`
	Fee         float64 `json:"fee"`
	Remark      string  `json:"remark"`
	Source      string  `json:"source"`
}

// TxPage is a paginated transaction list.
type TxPage struct {
	Total int  `json:"total"`
	Page  int  `json:"page"`
	Size  int  `json:"size"`
	Items []Tx `json:"items"`
}

// CreateTransaction records a buy/sell, validating sell quantity against holdings.
func CreateTransaction(in TxInput) (map[string]any, error) {
	if in.AssetID == "" || in.Direction == "" || in.Quantity == nil || in.Price == nil {
		return nil, errf(40001, "缺少必填字段")
	}
	if in.Direction != "buy" && in.Direction != "sell" {
		return nil, errf(40001, "方向只能是 buy/sell")
	}
	if *in.Quantity <= 0 {
		return nil, errf(40001, "数量必须大于 0")
	}
	if *in.Price < 0 {
		return nil, errf(40001, "价格不能为负")
	}
	if store.ScalarInt(`SELECT COUNT(*) FROM assets WHERE id = ?`, in.AssetID) == 0 {
		return nil, errf(40401, "标的不存在")
	}
	if in.Direction == "sell" {
		if p := ComputePosition(in.AssetID); *in.Quantity > p.Qty+1e-9 {
			return nil, errf(40001, "卖出数量超过持仓")
		}
	}
	fee := 0.0
	if in.Fee != nil {
		fee = *in.Fee
	}
	tt := in.TradeTime
	if tt == 0 {
		tt = nowMs()
	}
	src := in.Source
	if src == "" {
		src = "manual"
	}
	id, ts := cryptox.UUID(), nowMs()
	_, err := store.Exec(`INSERT INTO transactions(id,asset_id,direction,trade_time,quantity,price,fee,remark,source,created_at,updated_at)
	    VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, in.AssetID, in.Direction, tt, *in.Quantity, *in.Price, fee, nullStr(in.Remark), src, ts, ts)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	warning := ""
	if q := quotes.Get(in.AssetID); q != nil && q.Status == "ok" && q.Price > 0 {
		if dev := math.Abs(*in.Price-q.Price) / q.Price; dev > 0.5 {
			warning = fmt.Sprintf("成交价 %.4g 与当前行情 %.4g 偏离 %.0f%%，请确认是否为历史交易补录", *in.Price, q.Price, dev*100)
		}
	}
	return map[string]any{"id": id, "warning": warning}, nil
}

// TxQuery filters a transaction listing.
type TxQuery struct {
	AssetID    string
	Category   string
	Direction  string
	From, To   int64
	Page, Size int
}

// ListTransactions returns a filtered, paginated ledger.
func ListTransactions(q TxQuery) TxPage {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Size <= 0 {
		q.Size = 20
	}
	where, args := " WHERE 1=1", []any{}
	if q.AssetID != "" {
		where += " AND t.asset_id = ?"
		args = append(args, q.AssetID)
	}
	if q.Direction != "" {
		where += " AND t.direction = ?"
		args = append(args, q.Direction)
	}
	if q.From > 0 {
		where += " AND t.trade_time >= ?"
		args = append(args, q.From)
	}
	if q.To > 0 {
		where += " AND t.trade_time <= ?"
		args = append(args, q.To)
	}
	if q.Category != "" {
		where += " AND a.category = ?"
		args = append(args, q.Category)
	}
	base := ` FROM transactions t JOIN assets a ON a.id = t.asset_id` + where
	total := int(store.ScalarInt(`SELECT COUNT(*)`+base, args...))
	rows, err := store.Query(`SELECT t.id, t.asset_id, a.name, a.symbol, a.category, t.direction, t.trade_time,
	    t.quantity, t.price, t.fee, COALESCE(t.remark,''), t.source`+base+
		` ORDER BY t.trade_time DESC LIMIT ? OFFSET ?`, append(args, q.Size, (q.Page-1)*q.Size)...)
	items := []Tx{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t Tx
			if rows.Scan(&t.ID, &t.AssetID, &t.AssetName, &t.AssetSymbol, &t.Category, &t.Direction,
				&t.TradeTime, &t.Quantity, &t.Price, &t.Fee, &t.Remark, &t.Source) == nil {
				items = append(items, t)
			}
		}
	}
	return TxPage{Total: total, Page: q.Page, Size: q.Size, Items: items}
}

// UpdateTransaction patches a trade record.
func UpdateTransaction(id string, in TxInput) (map[string]any, error) {
	var assetID, dir, remark string
	var tt int64
	var qty, price, fee float64
	err := store.QueryRow(`SELECT asset_id, direction, trade_time, quantity, price, fee, COALESCE(remark,'') FROM transactions WHERE id=?`, id).
		Scan(&assetID, &dir, &tt, &qty, &price, &fee, &remark)
	if err != nil {
		return nil, errf(40401, "流水不存在")
	}
	if in.Direction != "" {
		dir = in.Direction
	}
	if in.TradeTime > 0 {
		tt = in.TradeTime
	}
	if in.Quantity != nil {
		qty = *in.Quantity
	}
	if in.Price != nil {
		price = *in.Price
	}
	if in.Fee != nil {
		fee = *in.Fee
	}
	if in.Remark != "" {
		remark = in.Remark
	}
	if dir == "sell" {
		if p := computePositionExcluding(assetID, id); qty > p.Qty+1e-9 {
			return nil, errf(40001, "卖出数量超过持仓")
		}
	}
	_, err = store.Exec(`UPDATE transactions SET direction=?, trade_time=?, quantity=?, price=?, fee=?, remark=?, updated_at=? WHERE id=?`,
		dir, tt, qty, price, fee, nullStr(remark), nowMs(), id)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"id": id}, nil
}

// DeleteTransaction removes a trade record.
func DeleteTransaction(id string) (map[string]any, error) {
	if store.ScalarInt(`SELECT COUNT(*) FROM transactions WHERE id=?`, id) == 0 {
		return nil, errf(40401, "流水不存在")
	}
	_, _ = store.Exec(`DELETE FROM transactions WHERE id=?`, id)
	return map[string]any{"id": id}, nil
}

// ---- Cash ---------------------------------------------------------------

// CashAccount is a bank/broker cash balance.
type CashAccount struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Currency    string  `json:"currency"`
	Balance     float64 `json:"balance"`
	BalanceMain float64 `json:"balanceMain"`
	Remark      string  `json:"remark"`
}

// CashList bundles cash accounts and their converted total.
type CashList struct {
	Items        []CashAccount `json:"items"`
	TotalBalance float64       `json:"totalBalance"`
	Currency     string        `json:"currency"`
}

// CashAccounts returns all cash accounts with a main-currency total.
func CashAccounts() CashList {
	main := MainCurrency()
	out := CashList{Items: []CashAccount{}, Currency: main}
	rows, err := store.Query(`SELECT id, name, currency, balance, COALESCE(remark,'') FROM cash_accounts ORDER BY created_at ASC`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c CashAccount
		if rows.Scan(&c.ID, &c.Name, &c.Currency, &c.Balance, &c.Remark) != nil {
			continue
		}
		c.BalanceMain = Convert(c.Balance, c.Currency, main)
		out.TotalBalance += c.BalanceMain
		out.Items = append(out.Items, c)
	}
	return out
}

// CashInput is the create/update payload for a cash account.
type CashInput struct {
	Name     string   `json:"name"`
	Currency string   `json:"currency"`
	Balance  *float64 `json:"balance"`
	Remark   string   `json:"remark"`
}

// CreateCash adds a cash account.
func CreateCash(in CashInput) (map[string]any, error) {
	if in.Name == "" {
		return nil, errf(40001, "账户名称必填")
	}
	if in.Balance == nil || *in.Balance < 0 {
		return nil, errf(40001, "余额不能为负")
	}
	cur := in.Currency
	if cur == "" {
		cur = MainCurrency()
	}
	id, ts := cryptox.UUID(), nowMs()
	_, err := store.Exec(`INSERT INTO cash_accounts(id,name,currency,balance,remark,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		id, in.Name, cur, *in.Balance, nullStr(in.Remark), ts, ts)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"id": id}, nil
}

// UpdateCash patches a cash account.
func UpdateCash(id string, in CashInput) (map[string]any, error) {
	var name, cur, remark string
	var bal float64
	err := store.QueryRow(`SELECT name, currency, balance, COALESCE(remark,'') FROM cash_accounts WHERE id=?`, id).
		Scan(&name, &cur, &bal, &remark)
	if err != nil {
		return nil, errf(40401, "账户不存在")
	}
	if in.Balance != nil {
		if *in.Balance < 0 {
			return nil, errf(40001, "余额不能为负")
		}
		bal = *in.Balance
	}
	name = pick(in.Name, name)
	cur = pick(in.Currency, cur)
	remark = pick(in.Remark, remark)
	_, err = store.Exec(`UPDATE cash_accounts SET name=?, currency=?, balance=?, remark=?, updated_at=? WHERE id=?`,
		name, cur, bal, nullStr(remark), nowMs(), id)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"id": id}, nil
}

// DeleteCash removes a cash account.
func DeleteCash(id string) (map[string]any, error) {
	_, _ = store.Exec(`DELETE FROM cash_accounts WHERE id=?`, id)
	return map[string]any{"id": id}, nil
}

// ---- Snapshots & trend --------------------------------------------------

// TakeSnapshots writes today's position and cash snapshot rows.
func TakeSnapshots() map[string]any {
	date := today()
	rows, err := store.Query(`SELECT id, currency FROM assets WHERE status='active'`)
	n := 0
	if err == nil {
		defer rows.Close()
		type row struct{ id, cur string }
		var list []row
		for rows.Next() {
			var r row
			if rows.Scan(&r.id, &r.cur) == nil {
				list = append(list, r)
			}
		}
		for _, r := range list {
			v := GetPositionView(r.id)
			if v == nil {
				continue
			}
			_, _ = store.Exec(`INSERT INTO position_snapshots(id,asset_id,snapshot_date,quantity,avg_cost,cost_total,last_price,market_value,currency,created_at)
			    VALUES(?,?,?,?,?,?,?,?,?,?)
			    ON CONFLICT(asset_id, snapshot_date) DO UPDATE SET
			      quantity=excluded.quantity, avg_cost=excluded.avg_cost, cost_total=excluded.cost_total,
			      last_price=excluded.last_price, market_value=excluded.market_value`,
				cryptox.UUID(), r.id, date, v.Qty, v.AvgCost, v.CostTotal, v.Price, v.MarketValue, r.cur, nowMs())
			n++
		}
	}
	crows, err := store.Query(`SELECT id, balance, currency FROM cash_accounts`)
	if err == nil {
		defer crows.Close()
		type crow struct {
			id, cur string
			bal     float64
		}
		var list []crow
		for crows.Next() {
			var c crow
			if crows.Scan(&c.id, &c.bal, &c.cur) == nil {
				list = append(list, c)
			}
		}
		for _, c := range list {
			_, _ = store.Exec(`INSERT OR IGNORE INTO cash_snapshots(id,account_id,snapshot_date,balance,currency,created_at) VALUES(?,?,?,?,?,?)`,
				cryptox.UUID(), c.id, date, c.bal, c.cur, nowMs())
		}
	}
	return map[string]any{"date": date, "assets": n}
}

// SeedHistoricalSnapshots backfills synthetic history so trend charts aren't empty.
func SeedHistoricalSnapshots(days int) map[string]any {
	if store.ScalarInt(`SELECT COUNT(*) FROM position_snapshots`) > 0 {
		return map[string]any{"skipped": true}
	}
	rows, err := store.Query(`SELECT id, currency, category FROM assets WHERE status='active'`)
	if err != nil {
		return map[string]any{"seeded": 0}
	}
	type row struct{ id, cur, cat string }
	var list []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.cur, &r.cat) == nil {
			list = append(list, r)
		}
	}
	rows.Close()

	vols := map[string]float64{"crypto": 0.02, "stock": 0.012, "fund": 0.006, "gold": 0.008}
	now := time.Now()
	for _, r := range list {
		v := GetPositionView(r.id)
		if v == nil || v.Qty <= 0 {
			continue
		}
		vol := vols[r.cat]
		if vol == 0 {
			vol = 0.01
		}
		prices := make([]float64, days)
		p := v.Price * (1 + (rand.Float64()*2-1)*vol*30)
		prices[0] = p
		for i := 1; i < days; i++ {
			p = math.Max(1e-6, p*(1+(rand.Float64()*2-1)*vol*4))
			prices[i] = p
		}
		prices[days-1] = v.Price
		for i := 0; i < days; i++ {
			d := now.AddDate(0, 0, -(days - 1 - i)).Format("2006-01-02")
			lp := prices[i]
			_, _ = store.Exec(`INSERT OR IGNORE INTO position_snapshots(id,asset_id,snapshot_date,quantity,avg_cost,cost_total,last_price,market_value,currency,created_at)
			    VALUES(?,?,?,?,?,?,?,?,?,?)`,
				cryptox.UUID(), r.id, d, v.Qty, v.AvgCost, v.CostTotal, lp, v.Qty*lp, r.cur, nowMs())
		}
	}
	crows, err := store.Query(`SELECT id, balance, currency FROM cash_accounts`)
	if err == nil {
		type crow struct {
			id, cur string
			bal     float64
		}
		var clist []crow
		for crows.Next() {
			var c crow
			if crows.Scan(&c.id, &c.bal, &c.cur) == nil {
				clist = append(clist, c)
			}
		}
		crows.Close()
		for _, c := range clist {
			for i := 0; i < days; i++ {
				d := now.AddDate(0, 0, -(days - 1 - i)).Format("2006-01-02")
				_, _ = store.Exec(`INSERT OR IGNORE INTO cash_snapshots(id,account_id,snapshot_date,balance,currency,created_at) VALUES(?,?,?,?,?,?)`,
					cryptox.UUID(), c.id, d, c.bal, c.cur, nowMs())
			}
		}
	}
	return map[string]any{"seeded": days}
}

// TrendPoint is one day of the portfolio trend.
type TrendPoint struct {
	Date        string  `json:"date"`
	TotalAssets float64 `json:"totalAssets"`
	Cost        float64 `json:"cost"`
	Pnl         float64 `json:"pnl"`
}

// BenchPoint is one day of the benchmark index (normalised to 100 at window start).
type BenchPoint struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

// Trend is the PnL trend response.
type Trend struct {
	Range         string       `json:"range"`
	MainCurrency  string       `json:"mainCurrency"`
	Series        []TrendPoint `json:"series"`
	Benchmark      []BenchPoint `json:"benchmark"`
	BenchmarkLabel string      `json:"benchmarkLabel"`
	ExcessReturn   *float64    `json:"excessReturn"`
}

// PnlTrend aggregates daily snapshots into a portfolio trend series.
func PnlTrend(rng string) Trend {
	days := map[string]int{"7d": 7, "30d": 30, "90d": 90, "all": 100000}[rng]
	if days == 0 {
		days = 30
	}
	main := MainCurrency()

	type bucket struct{ mv, cost map[string]float64 }
	byDate := map[string]*bucket{}
	order := []string{}

	rows, err := store.Query(`SELECT snapshot_date, currency, market_value, cost_total FROM position_snapshots ORDER BY snapshot_date ASC`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d, cur string
			var mv, cost float64
			if rows.Scan(&d, &cur, &mv, &cost) != nil {
				continue
			}
			b := byDate[d]
			if b == nil {
				b = &bucket{mv: map[string]float64{}, cost: map[string]float64{}}
				byDate[d] = b
				order = append(order, d)
			}
			b.mv[cur] += mv
			b.cost[cur] += cost
		}
	}
	cashByDate := map[string]map[string]float64{}
	crows, err := store.Query(`SELECT snapshot_date, currency, balance FROM cash_snapshots`)
	if err == nil {
		defer crows.Close()
		for crows.Next() {
			var d, cur string
			var bal float64
			if crows.Scan(&d, &cur, &bal) != nil {
				continue
			}
			if cashByDate[d] == nil {
				cashByDate[d] = map[string]float64{}
			}
			cashByDate[d][cur] += bal
		}
	}
	if len(order) > days {
		order = order[len(order)-days:]
	}
	series := make([]TrendPoint, 0, len(order))
	for _, d := range order {
		b := byDate[d]
		mv, cost := 0.0, 0.0
		for cur, v := range b.mv {
			mv += Convert(v, cur, main)
		}
		for cur, v := range b.cost {
			cost += Convert(v, cur, main)
		}
		p := TrendPoint{Date: d, Cost: f2(cost), Pnl: f2(mv - cost)}
		for cur, v := range cashByDate[d] {
			mv += Convert(v, cur, main)
		}
		p.TotalAssets = f2(mv)
		series = append(series, p)
	}

	t := Trend{Range: rng, MainCurrency: main, Series: series}
	// Later-②: optional benchmark overlay. The benchmark is one of the user's own
	// assets (selected by symbol); its close series is normalised to 100 at the
	// window start so it can be compared against the (also 100-based) portfolio line.
	if benchSym := settings.Get("benchmark"); benchSym != "" && len(series) >= 2 {
		if id, ok := store.ScalarStr(`SELECT id FROM assets WHERE symbol = ? AND status='active' LIMIT 1`, benchSym); ok {
			raw := quotes.Kline(id, "1d", len(series))
			if len(raw) >= 2 {
				// quotes.Kline returns candles DESC (newest first); reverse to ASC so
				// each candle lines up index-for-index with the ascending portfolio
				// series. Skipping this step previously inverted the overlay and gave
				// excessReturn the wrong sign.
				kl := make([]quotes.Candle, len(raw))
				for i, c := range raw {
					kl[len(raw)-1-i] = c
				}
				n := len(series)
				if len(kl) < n {
					n = len(kl)
				}
				portTail := series[len(series)-n:]
				klTail := kl[len(kl)-n:]
				baseP, baseB := portTail[0].TotalAssets, klTail[0].Close
				if baseP > 0 && baseB > 0 {
					bench := make([]BenchPoint, 0, n)
					for i := 0; i < n; i++ {
						bench = append(bench, BenchPoint{Date: portTail[i].Date, Value: klTail[i].Close / baseB * 100})
					}
					t.Benchmark = bench
					t.BenchmarkLabel = benchSym
					pr := (portTail[n-1].TotalAssets - baseP) / baseP
					br := (klTail[n-1].Close - baseB) / baseB
					er := pr - br
					t.ExcessReturn = &er
				}
			}
		}
	}
	return t
}

// ---- helpers ------------------------------------------------------------

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func pick(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
