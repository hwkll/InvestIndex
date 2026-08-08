// Package quotes provides market data: real providers with graceful degradation
// to a deterministic random-walk simulator, plus K-line storage.
package quotes

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"investhub/internal/cryptox"
	"investhub/internal/store"
)

// Quote is a live price tick.
type Quote struct {
	AssetID    string  `json:"assetId"`
	Price      float64 `json:"price"`
	PrevClose  float64 `json:"prevClose"`
	ChgPct     float64 `json:"chgPct"`
	Currency   string  `json:"currency"`
	SourceTime int64   `json:"sourceTime"`
	Status     string  `json:"status"` // ok | sim | stale
}

// Candle is one K-line bar.
type Candle struct {
	Ts     int64   `json:"ts"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// Asset is the subset of asset fields the quote layer needs.
type Asset struct {
	ID       string
	Category string
	Symbol   string
	Currency string
	Provider string
}

var simVol = map[string]float64{"crypto": 0.018, "stock": 0.009, "fund": 0.004, "gold": 0.006}

var (
	mu    sync.RWMutex
	cache = map[string]*Quote{}
	sim   = map[string]*simState{}

	// Mode: auto (try real, fall back to sim) | real | sim
	mode = "auto"

	httpc = &http.Client{Timeout: 6 * time.Second}
)

type simState struct {
	price float64
	vol   float64
}

// SetMode switches the data-source strategy.
func SetMode(m string) {
	mu.Lock()
	defer mu.Unlock()
	if m == "auto" || m == "real" || m == "sim" {
		mode = m
	}
}

// Mode returns the current data-source strategy.
func Mode() string {
	mu.RLock()
	defer mu.RUnlock()
	return mode
}

func hashNum(s string) int {
	h := 0
	for _, c := range s {
		h = (h*31 + int(c)) & 0x7fffffff
	}
	return h
}

// DefaultPrice provides a plausible starting price for an asset.
func DefaultPrice(a Asset) float64 {
	sym := strings.ToUpper(a.Symbol)
	known := map[string]float64{
		"BTC": 61000, "ETH": 3050, "SOL": 152, "BNB": 580,
		"XRP": 0.62, "DOGE": 0.16, "ADA": 0.45, "XAU": 560,
	}
	if p, ok := known[sym]; ok {
		return p
	}
	switch a.Category {
	case "crypto":
		return 10 + float64(hashNum(sym)%400)
	case "gold":
		return 540 + float64(hashNum(sym)%40)
	case "fund":
		return 0.8 + float64(hashNum(sym)%350)/100
	}
	if strings.HasPrefix(a.Symbol, "sh.600519") {
		return 1480
	}
	if strings.HasPrefix(a.Symbol, "sh.000001") || strings.HasPrefix(a.Symbol, "sz.000001") {
		return 11.5
	}
	if strings.HasPrefix(a.Symbol, "sz.000858") {
		return 140
	}
	return 8 + float64(hashNum(sym)%1800)
}

func activeAssets() []Asset {
	rows, err := store.Query(`SELECT id, category, symbol, currency, provider FROM assets WHERE status='active' ORDER BY category`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.Category, &a.Symbol, &a.Currency, &a.Provider); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// SeedState primes the in-memory cache for every active asset.
func SeedState() {
	for _, a := range activeAssets() {
		mu.RLock()
		_, exists := cache[a.ID]
		mu.RUnlock()
		if exists {
			continue
		}
		price := store.ScalarFloat(`SELECT price FROM price_snapshots WHERE asset_id=? ORDER BY created_at DESC LIMIT 1`, a.ID)
		if price <= 0 {
			price = DefaultPrice(a)
		}
		vol := simVol[a.Category]
		if vol == 0 {
			vol = 0.01
		}
		initChg := (rand.Float64()*2 - 1) * vol * 8
		mu.Lock()
		// Re-check: another goroutine may have added it between the RUnlock and Lock.
		if _, doubleCheck := cache[a.ID]; doubleCheck {
			mu.Unlock()
			continue
		}
		sim[a.ID] = &simState{price: price, vol: vol}
		cache[a.ID] = &Quote{
			AssetID: a.ID, Price: price, PrevClose: price / (1 + initChg),
			ChgPct: initChg * 100, Currency: a.Currency,
			SourceTime: time.Now().UnixMilli(), Status: "sim",
		}
		mu.Unlock()
	}
}

// AddAsset registers a freshly created asset with the quote layer.
func AddAsset(a Asset) {
	price := DefaultPrice(a)
	vol := simVol[a.Category]
	if vol == 0 {
		vol = 0.01
	}
	initChg := (rand.Float64()*2 - 1) * vol * 8
	mu.Lock()
	sim[a.ID] = &simState{price: price, vol: vol}
	cache[a.ID] = &Quote{
		AssetID: a.ID, Price: price, PrevClose: price / (1 + initChg),
		ChgPct: initChg * 100, Currency: a.Currency,
		SourceTime: time.Now().UnixMilli(), Status: "sim",
	}
	mu.Unlock()
	EnsureKline(a, "1d", 200)
}

// Get returns the cached quote for an asset (nil when unknown).
func Get(assetID string) *Quote {
	mu.RLock()
	defer mu.RUnlock()
	q := cache[assetID]
	if q == nil {
		return nil
	}
	c := *q
	return &c
}

// ---- real providers -----------------------------------------------------

var coingeckoIDs = map[string]string{
	"BTC": "bitcoin", "ETH": "ethereum", "SOL": "solana", "BNB": "binancecoin",
	"XRP": "ripple", "DOGE": "dogecoin", "ADA": "cardano", "AVAX": "avalanche-2",
	"DOT": "polkadot", "LINK": "chainlink", "TON": "the-open-network", "TRX": "tron",
}

// fetchReal tries the configured provider; returns nil on any failure.
func fetchReal(a Asset) *Quote {
	switch {
	case a.Category == "crypto":
		return fetchCoinGecko(a)
	case a.Category == "stock" || a.Category == "fund" || a.Category == "gold":
		return fetchSina(a)
	}
	return nil
}

func fetchCoinGecko(a Asset) *Quote {
	id, ok := coingeckoIDs[strings.ToUpper(a.Symbol)]
	if !ok {
		return nil
	}
	url := fmt.Sprintf("https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=usd&include_24hr_change=true", id)
	resp, err := httpc.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var j map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		return nil
	}
	row, ok := j[id]
	if !ok {
		return nil
	}
	price := row["usd"]
	if price <= 0 || math.IsNaN(price) {
		return nil
	}
	chg := row["usd_24h_change"]
	prev := price
	if chg != 0 {
		prev = price / (1 + chg/100)
	}
	return &Quote{AssetID: a.ID, Price: price, PrevClose: prev, ChgPct: chg,
		Currency: "USD", SourceTime: time.Now().UnixMilli(), Status: "ok"}
}

var sinaRe = regexp.MustCompile(`"([^"]*)"`)

// sinaCode maps our symbol convention (sh.600519 / 510300) to Sina's (sh600519).
func sinaCode(a Asset) string {
	s := strings.ToLower(strings.TrimSpace(a.Symbol))
	s = strings.ReplaceAll(s, ".", "")
	if strings.HasPrefix(s, "sh") || strings.HasPrefix(s, "sz") ||
		strings.HasPrefix(s, "hk") || strings.HasPrefix(s, "gb_") {
		return s
	}
	// plain 6-digit A-share / ETF code: infer exchange from the first digit
	if len(s) == 6 && isDigits(s) {
		if s[0] == '6' || s[0] == '5' {
			return "sh" + s
		}
		return "sz" + s
	}
	// US ticker
	if a.SubTypeIsUS() {
		return "gb_" + s
	}
	return ""
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// SubTypeIsUS reports whether the symbol looks like a US ticker.
func (a Asset) SubTypeIsUS() bool {
	s := strings.ToUpper(a.Symbol)
	return a.Category == "stock" && len(s) <= 5 && !isDigits(s) && !strings.Contains(s, ".")
}

func fetchSina(a Asset) *Quote {
	code := sinaCode(a)
	if code == "" {
		return nil
	}
	req, _ := http.NewRequest("GET", "https://hq.sinajs.cn/list="+code, nil)
	req.Header.Set("Referer", "https://finance.sina.com.cn/")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil
	}
	m := sinaRe.FindStringSubmatch(decodeGBK(raw))
	if len(m) < 2 || m[1] == "" {
		return nil
	}
	f := strings.Split(m[1], ",")
	var price, prev float64
	switch {
	case strings.HasPrefix(code, "sh"), strings.HasPrefix(code, "sz"):
		// 0=name 1=open 2=prevClose 3=current
		if len(f) < 4 {
			return nil
		}
		prev, _ = strconv.ParseFloat(f[2], 64)
		price, _ = strconv.ParseFloat(f[3], 64)
	case strings.HasPrefix(code, "hk"):
		if len(f) < 7 {
			return nil
		}
		prev, _ = strconv.ParseFloat(f[3], 64)
		price, _ = strconv.ParseFloat(f[6], 64)
	case strings.HasPrefix(code, "gb_"):
		if len(f) < 27 {
			return nil
		}
		price, _ = strconv.ParseFloat(f[1], 64)
		prev, _ = strconv.ParseFloat(f[26], 64)
	}
	if price <= 0 || math.IsNaN(price) {
		return nil
	}
	if prev <= 0 {
		prev = price
	}
	return &Quote{AssetID: a.ID, Price: price, PrevClose: prev,
		ChgPct: (price/prev - 1) * 100, Currency: a.Currency,
		SourceTime: time.Now().UnixMilli(), Status: "ok"}
}

// decodeGBK converts the GB18030 bytes Sina returns into UTF-8 well enough to
// extract the numeric payload (names may be lossy, we only need the numbers).
func decodeGBK(b []byte) string {
	out := make([]rune, 0, len(b))
	for _, c := range b {
		if c < 0x80 {
			out = append(out, rune(c))
		} else {
			out = append(out, '?')
		}
	}
	return string(out)
}

// ---- simulator ----------------------------------------------------------

func simStep(assetID string) float64 {
	s := sim[assetID]
	if s == nil {
		return 0
	}
	drift := (rand.Float64()*2 - 1) * s.vol
	s.price = math.Max(0.0001, s.price*(1+drift))
	return s.price
}

// clean rejects obviously bad ticks.
func clean(p float64) bool { return p > 0 && !math.IsNaN(p) && !math.IsInf(p, 0) }

// UpdateOne refreshes a single asset's quote.
func UpdateOne(a Asset) *Quote {
	m := Mode()
	var real *Quote
	if m == "auto" || m == "real" {
		real = fetchReal(a)
		if real != nil && !clean(real.Price) {
			real = nil
		}
	}

	mu.Lock()
	defer mu.Unlock()
	old := cache[a.ID]
	var q *Quote
	if real != nil {
		q = real
	} else {
		if sim[a.ID] == nil {
			vol := simVol[a.Category]
			if vol == 0 {
				vol = 0.01
			}
			start := DefaultPrice(a)
			if old != nil {
				start = old.Price
			}
			sim[a.ID] = &simState{price: start, vol: vol}
		}
		price := simStep(a.ID)
		prev := price
		if old != nil {
			prev = old.PrevClose
		}
		chg := 0.0
		if prev > 0 {
			chg = (price/prev - 1) * 100
		}
		status := "sim"
		if m == "real" {
			status = "stale" // real was requested but unavailable
		}
		q = &Quote{AssetID: a.ID, Price: price, PrevClose: prev, ChgPct: chg,
			Currency: a.Currency, SourceTime: time.Now().UnixMilli(), Status: status}
	}
	cache[a.ID] = q
	c := *q
	return &c
}

// PollAll refreshes every active asset and rolls the latest candle forward.
func PollAll() []Quote {
	assets := activeAssets()
	out := make([]Quote, 0, len(assets))
	for _, a := range assets {
		q := UpdateOne(a)
		if q == nil {
			continue
		}
		out = append(out, *q)
		bumpKline(a, q.Price)
	}
	return out
}

// PersistPrices writes the current cache into price_snapshots.
func PersistPrices() {
	mu.RLock()
	defer mu.RUnlock()
	now := time.Now().UnixMilli()
	for id, q := range cache {
		_, _ = store.Exec(`INSERT INTO price_snapshots(id, asset_id, price, currency, source_time, created_at) VALUES(?,?,?,?,?,?)`,
			cryptox.UUID(), id, q.Price, q.Currency, q.SourceTime, now)
	}
	// keep the table bounded
	_, _ = store.Exec(`DELETE FROM price_snapshots WHERE created_at < ?`, now-30*86400000)
}

// ---- K-line -------------------------------------------------------------

// EnsureKline backfills a synthetic daily history when none exists yet.
func EnsureKline(a Asset, period string, limit int) {
	if period == "" {
		period = "1d"
	}
	if store.ScalarInt(`SELECT COUNT(*) FROM kline_cache WHERE asset_id=? AND period=?`, a.ID, period) > 0 {
		return
	}
	cur := DefaultPrice(a)
	if q := Get(a.ID); q != nil && q.Price > 0 {
		cur = q.Price
	}
	days := limit
	if days > 180 || days <= 0 {
		days = 180
	}
	vol := simVol[a.Category]
	if vol == 0 {
		vol = 0.01
	}
	closes := make([]float64, days)
	p := cur / (1 + (rand.Float64()*2-1)*vol*20)
	closes[0] = p
	for i := 1; i < days; i++ {
		p = math.Max(0.0001, p*(1+(rand.Float64()*2-1)*vol*3))
		closes[i] = p
	}
	closes[days-1] = cur

	tx, err := store.DB.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO kline_cache(id, asset_id, period, ts, open, high, low, close, volume) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return
	}
	now := time.Now().UnixMilli()
	const day = int64(86400000)
	for i := 0; i < days; i++ {
		c := closes[i]
		o := c * (1 - vol)
		if i > 0 {
			o = closes[i-1]
		}
		h := math.Max(o, c) * (1 + rand.Float64()*vol)
		l := math.Min(o, c) * (1 - rand.Float64()*vol)
		ts := now - int64(days-1-i)*day
		_, _ = stmt.Exec(cryptox.UUID(), a.ID, period, ts, o, h, l, c, rand.Float64()*1000+100)
	}
	_ = stmt.Close()
	_ = tx.Commit()
}

func bumpKline(a Asset, price float64) {
	var id string
	var high, low float64
	err := store.QueryRow(`SELECT id, high, low FROM kline_cache WHERE asset_id=? AND period='1d' ORDER BY ts DESC LIMIT 1`, a.ID).
		Scan(&id, &high, &low)
	if err != nil {
		EnsureKline(a, "1d", 200)
		return
	}
	_, _ = store.Exec(`UPDATE kline_cache SET close=?, high=?, low=? WHERE id=?`,
		price, math.Max(high, price), math.Min(low, price), id)
}

// Kline returns the most recent `limit` candles for an asset.
func Kline(assetID, period string, limit int) []Candle {
	if period == "" {
		period = "1d"
	}
	if limit <= 0 {
		limit = 200
	}
	var a Asset
	err := store.QueryRow(`SELECT id, category, symbol, currency, provider FROM assets WHERE id=?`, assetID).
		Scan(&a.ID, &a.Category, &a.Symbol, &a.Currency, &a.Provider)
	if err != nil {
		return []Candle{}
	}
	EnsureKline(a, period, limit)
	rows, err := store.Query(`SELECT ts, open, high, low, close, COALESCE(volume,0) FROM kline_cache
	    WHERE asset_id=? AND period=? ORDER BY ts DESC LIMIT ?`, assetID, period, limit)
	if err != nil {
		return []Candle{}
	}
	defer rows.Close()
	var rev []Candle
	for rows.Next() {
		var c Candle
		if err := rows.Scan(&c.Ts, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err == nil {
			rev = append(rev, c)
		}
	}
	// rows came back newest-first; flip to oldest-first
	out := make([]Candle, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out
}
