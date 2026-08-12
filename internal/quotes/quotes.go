// Package quotes provides market data: real providers with graceful degradation
// to a deterministic random-walk simulator, plus K-line storage.
package quotes

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	Status     string  `json:"status"` // ok | nosource | sim (sim only in explicit sim mode)
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
	SubType  string
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
	rows, err := store.Query(`SELECT id, category, symbol, sub_type, currency, provider FROM assets WHERE status='active' ORDER BY category`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		var a Asset
		// Scan nullable columns into pointers: a NULL sub_type/provider (e.g.
		// crypto/stock seeds) would otherwise make rows.Scan fail and silently
		// drop the asset from polling.
		var subType, currency, provider *string
		if err := rows.Scan(&a.ID, &a.Category, &a.Symbol, &subType, &currency, &provider); err == nil {
			if subType != nil {
				a.SubType = *subType
			}
			if currency != nil {
				a.Currency = *currency
			}
			if provider != nil {
				a.Provider = *provider
			}
			out = append(out, a)
		}
	}
	return out
}

// SeedState primes the in-memory cache for every active asset. The live quote
// is seeded as "nosource" (price 0) rather than a last-snapshot/DefaultPrice
// value: between process start and the first PollAll, a fabricated number
// presented as a live quote is worse than showing nothing. The simulator state
// is still primed from the last snapshot (or DefaultPrice) so K-line backfill
// and explicit sim mode keep a sensible starting point.
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
		mu.Lock()
		// Re-check: another goroutine may have added it between the RUnlock and Lock.
		if _, doubleCheck := cache[a.ID]; doubleCheck {
			mu.Unlock()
			continue
		}
		sim[a.ID] = &simState{price: price, vol: vol}
		cache[a.ID] = &Quote{
			AssetID: a.ID, Price: 0, PrevClose: 0, ChgPct: 0,
			Currency: a.Currency, SourceTime: 0, Status: "nosource",
		}
		mu.Unlock()
	}
}

// AddAsset registers a freshly created asset with the quote layer. It seeds a
// neutral "nosource" entry (price 0) so the asset is never "unknown" and never
// flashes a fabricated number before its first real fetch; the API handler is
// responsible for pulling a real quote (and backfilling K-line) right after
// creation. The simState is still primed from DefaultPrice so explicit sim mode
// and K-line backfill keep a sensible starting point.
// Idempotent: re-adding an existing asset is a no-op.
func AddAsset(a Asset) {
	mu.RLock()
	if _, exists := cache[a.ID]; exists {
		mu.RUnlock()
		return
	}
	mu.RUnlock()
	price := DefaultPrice(a)
	vol := simVol[a.Category]
	if vol == 0 {
		vol = 0.01
	}
	mu.Lock()
	sim[a.ID] = &simState{price: price, vol: vol}
	cache[a.ID] = &Quote{
		AssetID: a.ID, Price: 0, PrevClose: 0, ChgPct: 0,
		Currency: a.Currency, SourceTime: 0, Status: "nosource",
	}
	mu.Unlock()
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

// fetchReal tries the configured provider; returns nil on any failure.
// srcFailTotal counts failed real-source fetches, surfaced via SourceFailTotal
// so operators can observe upstream data health.
var srcFailTotal int64

// HasProvider reports whether the asset maps to a real upstream data source.
// Crypto (Binance), on-exchange ETFs (Sina), off-exchange funds (Eastmoney net
// value) and SGE spot gold all have a source. When a source is configured but
// temporarily unreachable (e.g. off-exchange fund NAV endpoint down), the
// caller falls back to "nosource" rather than a fabricated simulator price.
func HasProvider(a Asset) bool {
	switch a.Category {
	case "crypto":
		return true // Binance USDT pairs cover the vast majority of tickers
	case "fund", "stock", "gold":
		return true
	}
	return false
}

func fetchReal(a Asset) *Quote {
	switch a.Category {
	case "crypto":
		// Binance is the primary source; CoinGecko is a second real source so a
		// geo-blocked/unreachable Binance still yields a TRUE price instead of
		// degrading to a fabricated one.
		if q := fetchBinance(a); q != nil {
			return q
		}
		return fetchCoinGecko(a)
	case "stock":
		return fetchSina(a)
	case "fund":
		if a.SubType == "etf" {
			return fetchSina(a)
		}
		return fetchTiantian(a)
	case "gold":
		// Normalize legacy subType values (e.g. "实物金"/"纸黄金"/"现货") typed
		// by users before the enum was fixed, so they still resolve to a spot
		// price instead of being treated as an ETF.
		normGoldSubType(&a)
		if a.SubType == "etf" {
			return fetchSina(a) // on-exchange gold ETF, CNY/share
		}
		if isXAU(a) {
			return fetchSinaXAU(a) // international London gold, USD/ounce
		}
		return fetchSinaSpot(a) // physical / paper / SGE Au(T+D), CNY/gram
	}
	return nil
}

// binanceSymbol maps our ticker convention (BTC, DOGE, SOL…) to a Binance USDT
// spot pair. Binance has no USD/CNY pair, so crypto quotes stay in USD.
func binanceSymbol(a Asset) string {
	s := strings.ToUpper(strings.TrimSpace(a.Symbol))
	if !strings.HasSuffix(s, "USDT") {
		s += "USDT"
	}
	return s
}

// fetchBinance pulls the 24h ticker from Binance's public REST API (no key
// required). Returns nil on any failure so the caller can fall back.
func fetchBinance(a Asset) *Quote {
	url := "https://api.binance.com/api/v3/ticker/24hr?symbol=" + binanceSymbol(a)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var j struct {
		LastPrice          string `json:"lastPrice"`
		PrevClosePrice     string `json:"prevClosePrice"`
		PriceChangePercent string `json:"priceChangePercent"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&j); err != nil {
		return nil
	}
	price, err := strconv.ParseFloat(j.LastPrice, 64)
	if err != nil || price <= 0 || math.IsNaN(price) {
		return nil
	}
	prev, _ := strconv.ParseFloat(j.PrevClosePrice, 64)
	if prev <= 0 {
		prev = price
	}
	chg, _ := strconv.ParseFloat(j.PriceChangePercent, 64)
	return &Quote{AssetID: a.ID, Price: price, PrevClose: prev, ChgPct: chg,
		Currency: "USD", SourceTime: time.Now().UnixMilli(), Status: "ok"}
}

// coinGeckoIDs maps our ticker convention to CoinGecko coin ids. Unknown
// symbols are skipped (we never guess an id — a wrong id would return a wrong
// coin's price, which is worse than no price at all).
var coinGeckoIDs = map[string]string{
	"BTC":   "bitcoin",
	"ETH":   "ethereum",
	"DOGE":  "dogecoin",
	"BNB":   "binancecoin",
	"XRP":   "ripple",
	"SOL":   "solana",
	"ADA":   "cardano",
	"DOT":   "polkadot",
	"LTC":   "litecoin",
	"LINK":  "chainlink",
	"MATIC": "matic-network",
	"TRX":   "tron",
	"AVAX":  "avalanche-2",
	"UNI":   "uniswap",
	"ATOM":  "cosmos",
	"XLM":   "stellar",
	"BCH":   "bitcoin-cash",
	"ETC":   "ethereum-classic",
	"NEAR":  "near",
	"APT":   "aptos",
	"ARB":   "arbitrum",
	"OP":    "optimism",
	"TON":   "the-open-network",
	"PEPE":  "pepe",
	"SHIB":  "shiba-inu",
	"WIF":   "dogwifcoin",
}

// coinGeckoID resolves an asset symbol to a CoinGecko coin id ("" when unknown).
func coinGeckoID(a Asset) string {
	s := strings.ToUpper(strings.TrimSpace(a.Symbol))
	s = strings.TrimSuffix(s, "USDT")
	return coinGeckoIDs[s]
}

// fetchCoinGecko pulls a spot USD price from CoinGecko's public simple/price
// endpoint. Returns nil on any failure so the caller can fall through to
// "nosource". CoinGecko's simple endpoint carries no previous close, so
// PrevClose mirrors the price and ChgPct is reported as 0 rather than guessed.
func fetchCoinGecko(a Asset) *Quote {
	id := coinGeckoID(a)
	if id == "" {
		return nil
	}
	url := "https://api.coingecko.com/api/v3/simple/price?ids=" + id + "&vs_currencies=usd"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var j map[string]map[string]float64
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&j); err != nil {
		return nil
	}
	price, ok := j[id]["usd"]
	if !ok || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return nil
	}
	return &Quote{AssetID: a.ID, Price: price, PrevClose: price, ChgPct: 0,
		Currency: "USD", SourceTime: time.Now().UnixMilli(), Status: "ok"}
}

// fetchTiantian pulls the latest unit NAV (单位净值) for an off-exchange
// (open-end) fund from Eastmoney's public F10 history endpoint.
//
// Source note: the older realtime-estimate endpoint
// "fundgz.1234567.com.cn/js/<code>.js" is dead — Eastmoney now serves a generic
// "页面未找到" HTML page for it regardless of scheme, Referer or cache-buster, so
// every off-exchange fund silently reported "nosource". It was replaced with
// api.fund.eastmoney.com/f10/lsjz, which is verified reachable and returns the
// authoritative published NAV. pageSize=2 fetches the latest plus the previous
// session so the daily change is computed exactly instead of being derived from
// a rounded growth rate.
//
// Returns nil (caller maps that to "nosource") on any failure or unknown fund
// code; it never fabricates a price.
func fetchTiantian(a Asset) *Quote {
	code := strings.TrimSpace(a.Symbol)
	if code == "" {
		return nil
	}
	url := "https://api.fund.eastmoney.com/f10/lsjz?fundCode=" + code + "&pageIndex=1&pageSize=2"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://fundf10.eastmoney.com/jjjz_"+code+".html")
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16384))
	if err != nil {
		return nil
	}
	var j struct {
		Data struct {
			List []struct {
				FSRQ  string `json:"FSRQ"`  // 净值日期
				DWJZ  string `json:"DWJZ"`  // 单位净值
				JZZZL string `json:"JZZZL"` // 日增长率 %
			} `json:"LSJZList"`
		} `json:"Data"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil
	}
	list := j.Data.List
	if len(list) == 0 {
		return nil // unknown or delisted fund code: no data, no guessing
	}
	price, err := strconv.ParseFloat(strings.TrimSpace(list[0].DWJZ), 64)
	if err != nil || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return nil
	}
	// Prefer the previous session's published NAV for an exact change; when only
	// one row came back (e.g. a brand-new fund) fall back to the reported growth
	// rate, and report 0 rather than guessing if that is unparseable too.
	prev, chg := price, 0.0
	if len(list) > 1 {
		if p, perr := strconv.ParseFloat(strings.TrimSpace(list[1].DWJZ), 64); perr == nil && p > 0 && !math.IsNaN(p) {
			prev = p
			chg = (price/prev - 1) * 100
		}
	}
	if prev == price {
		if v, verr := strconv.ParseFloat(strings.TrimSpace(list[0].JZZZL), 64); verr == nil && v != 0 {
			chg = v
			prev = price / (1 + v/100)
		}
	}
	// NAV is published once per session, so report the NAV date as the source
	// time instead of "now" — otherwise a stale NAV looks freshly fetched.
	src := time.Now().UnixMilli()
	if d, derr := time.ParseInLocation("2006-01-02", strings.TrimSpace(list[0].FSRQ), time.Local); derr == nil {
		src = d.UnixMilli()
	}
	return &Quote{AssetID: a.ID, Price: price, PrevClose: prev, ChgPct: chg,
		Currency: a.Currency, SourceTime: src, Status: "ok"}
}

// isGoldSpot reports whether a gold asset should be quoted from the SGE/COMEX
// spot market (CNY/gram or USD/ounce) instead of being treated as an on-exchange
// gold ETF (Sina A-share, CNY/share).
//
// Blacklist logic (the previous whitelist was inverted and caused any
// user-typed subType — e.g. "实物金" — to fall through to the Sina A-share
// path and report "nosource"): a gold asset is a spot/physical holding UNLESS
// its subType explicitly marks it as an ETF. This guarantees the common case
// (physical gold, paper gold, SGE Au(T+D), XAU/USD) always resolves to a real
// spot price and never silently degrades to "nosource".
func isGoldSpot(a Asset) bool {
	if a.Category != "gold" {
		return false
	}
	return a.SubType != "etf"
}

// isXAU reports whether a gold asset is the international London-gold (XAU/USD,
// quoted in USD per ounce) rather than the domestic SGE spot (CNY/gram).
func isXAU(a Asset) bool {
	if a.Category != "gold" {
		return false
	}
	if a.SubType == "xau" {
		return true
	}
	s := strings.ToUpper(strings.TrimSpace(a.Symbol))
	return s == "XAU" || s == "XAUUSD" || strings.Contains(s, "XAU")
}

// fetchSinaXAU pulls the international spot gold (London gold / XAU) quote from
// Sina's hf_XAU feed, quoted in USD per ounce.
func fetchSinaXAU(a Asset) *Quote {
	req, _ := http.NewRequest("GET", "https://hq.sinajs.cn/?list=hf_XAU", nil)
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
	if len(f) < 4 {
		return nil
	}
	price, err := strconv.ParseFloat(f[0], 64) // 最新价 (USD/oz)
	if err != nil || price <= 0 || math.IsNaN(price) {
		return nil
	}
	prev, _ := strconv.ParseFloat(f[3], 64) // 昨收
	if prev <= 0 {
		prev = price
	}
	return &Quote{AssetID: a.ID, Price: price, PrevClose: prev,
		ChgPct: (price/prev - 1) * 100, Currency: "USD",
		SourceTime: time.Now().UnixMilli(), Status: "ok"}
}

// fetchSinaSpot pulls the Shanghai Gold Exchange Au(T+D) quote from Sina, which
// is quoted directly in CNY per gram (no USD→CNY conversion needed).
func fetchSinaSpot(a Asset) *Quote {
	req, _ := http.NewRequest("GET", "https://hq.sinajs.cn/?list=gds_AUTD", nil)
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
	if len(f) < 9 {
		return nil
	}
	price, err := strconv.ParseFloat(f[8], 64) // 最新价
	if err != nil || price <= 0 || math.IsNaN(price) {
		return nil
	}
	prev, _ := strconv.ParseFloat(f[7], 64) // 昨收
	if prev <= 0 {
		prev = price
	}
	return &Quote{AssetID: a.ID, Price: price, PrevClose: prev,
		ChgPct: (price/prev - 1) * 100, Currency: "CNY",
		SourceTime: time.Now().UnixMilli(), Status: "ok"}
}

// ---- FX rates (live USD/HKD → CNY) --------------------------------------

// RefreshFX fetches live USD→CNY (and HKD→CNY) from Sina and writes them into
// the fx_rates table. Best-effort: network or parse failures leave the existing
// rates untouched. User-locked rates (auto=0, set via Settings) are never
// overwritten.
func RefreshFX() {
	now := time.Now().UnixMilli()
	setFx := func(ccy string, rate float64) {
		if rate <= 0 || math.IsNaN(rate) {
			return
		}
		if _, err := store.Exec(`UPDATE fx_rates SET rate=?, updated_at=? WHERE currency=? AND auto=1`, rate, now, ccy); err != nil {
			log.Printf("[fx] update %s failed: %v", ccy, err)
		} else {
			log.Printf("[fx] %s -> %.4f CNY (live)", ccy, rate)
		}
	}

	// USD → CNY: the anchor rate for any non-CNY asset.
	if usd := fetchSinaFxRate("fx_susdcny", 5, 9); usd > 0 {
		setFx("USD", usd)
		// HKD → CNY: Sina's HKD pair is often empty on this feed, so fall back
		// to the USD peg (HKD is linked to USD at ~7.80; accurate enough for
		// display). Only overwrites the auto-managed HKD row.
		if hkd := fetchSinaFxRate("fx_hkdcny", 0.6, 1.2); hkd > 0 {
			setFx("HKD", hkd)
		} else {
			setFx("HKD", usd/7.80)
		}
	} else {
		log.Printf("[fx] USD rate unavailable, skipping FX refresh")
	}
}

// fetchSinaFxRate fetches a Sina forex symbol and returns the first numeric
// field within [lo, hi] (the sane range for that pair). Returns 0 if none.
func fetchSinaFxRate(symbol string, lo, hi float64) float64 {
	req, _ := http.NewRequest("GET", "https://hq.sinajs.cn/?list="+symbol, nil)
	req.Header.Set("Referer", "https://finance.sina.com.cn/")
	resp, err := httpc.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return 0
	}
	m := sinaRe.FindStringSubmatch(decodeGBK(raw))
	if len(m) < 2 || m[1] == "" {
		return 0
	}
	for _, part := range strings.Split(m[1], ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(v) {
			continue
		}
		if v >= lo && v <= hi {
			return v
		}
	}
	return 0
}

var sinaRe = regexp.MustCompile(`"([^"]*)"`)

// sinaCode maps our symbol convention (sh.600519 / 510300 / 9866.HK) to Sina's
// (sh600519 / hk09866).
func sinaCode(a Asset) string {
	s := strings.ToLower(strings.TrimSpace(a.Symbol))
	// Hong Kong: accept prefix "hk09866" OR suffix "9866.hk". Sina wants a
	// 5-digit zero-padded code, e.g. hk09866.
	if strings.HasPrefix(s, "hk") && isDigits(s[2:]) {
		return normalizeHK(s[2:])
	}
	if dot := strings.LastIndex(s, "."); dot >= 0 {
		if s[dot+1:] == "hk" && isDigits(s[:dot]) {
			return normalizeHK(s[:dot])
		}
	}
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

// normalizeHK zero-pads a raw HK numeric code to Sina's 5-digit form.
func normalizeHK(code string) string {
	for len(code) < 5 {
		code = "0" + code
	}
	return "hk" + code
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// normGoldSubType maps free-form / legacy gold subType values onto the fixed
// enum {physical, sge, xau, etf}. Anything unrecognized (including empty and
// user-typed "实物金"/"纸黄金"/"现货") collapses to "physical" so the asset
// still resolves to a spot price. Only an explicit "etf" is treated as an
// on-exchange gold ETF.
func normGoldSubType(a *Asset) {
	switch strings.ToLower(strings.TrimSpace(a.SubType)) {
	case "etf", "基金", "etf基金", "黄金etf":
		a.SubType = "etf"
	case "xau", "伦敦金", "国际金", "xauusd":
		a.SubType = "xau"
	case "sge", "autd", "au(t+d)", "au9999", "au99.99", "金交所", "递延":
		a.SubType = "sge"
	case "physical", "实物金", "纸黄金", "现货", "积存金", "金条", "金豆":
		a.SubType = "physical"
	default:
		// empty or any unrecognized value → safe default: physical spot
		if a.SubType == "" || (a.Category == "gold" && a.SubType != "etf") {
			a.SubType = "physical"
		}
	}
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
	if (m == "auto" || m == "real") && HasProvider(a) {
		real = fetchReal(a)
		if real != nil && !clean(real.Price) {
			real = nil
		}
		if real == nil {
			log.Printf("[quotes] real fetch unavailable for %s/%s (mode=%s)", a.Category, a.Symbol, m)
			atomic.AddInt64(&srcFailTotal, 1)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	old := cache[a.ID]
	var q *Quote
	if real != nil {
		q = real
	} else if !HasProvider(a) || (a.Category == "fund" && a.SubType != "etf") {
		// Structurally no upstream source, or an off-exchange fund whose net
		// value source is unavailable: report honestly as "nosource" rather
		// than fabricating a simulator price that would pollute PnL.
		q = &Quote{AssetID: a.ID, Price: 0, PrevClose: 0, ChgPct: 0,
			Currency: a.Currency, SourceTime: 0, Status: "nosource"}
	} else if m == "sim" {
		// Explicit simulator mode: generate a synthetic price (offline/demo use).
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
		q = &Quote{AssetID: a.ID, Price: price, PrevClose: prev, ChgPct: chg,
			Currency: a.Currency, SourceTime: time.Now().UnixMilli(), Status: "sim"}
	} else {
		// Auto/real mode but no usable real price: report honestly as "nosource"
		// instead of fabricating a simulator price that would pollute PnL/display.
		q = &Quote{AssetID: a.ID, Price: 0, PrevClose: 0, ChgPct: 0,
			Currency: a.Currency, SourceTime: 0, Status: "nosource"}
	}
	cache[a.ID] = q
	c := *q
	return &c
}

// PollAll refreshes every active asset concurrently (bounded) and rolls the
// latest candle forward. Network fetches run in parallel; cache writes stay
// serialized inside UpdateOne; K-line backfills run sequentially afterward.
func PollAll() []Quote {
	assets := activeAssets()
	if len(assets) == 0 {
		return nil
	}
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	results := make([]*Quote, len(assets))
	var wg sync.WaitGroup
	for i, a := range assets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, a Asset) {
			defer wg.Done()
			defer func() { <-sem }()
			q := UpdateOne(a)
			if q != nil {
				c := *q
				results[i] = &c
			}
		}(i, a)
	}
	wg.Wait()
	out := make([]Quote, 0, len(assets))
	for i, a := range assets {
		q := results[i]
		if q == nil {
			continue
		}
		out = append(out, *q)
		if q.Status != "nosource" {
			bumpKline(a, q.Price)
		}
	}
	return out
}

// PersistPrices writes the current cache into price_snapshots.
func PersistPrices() {
	mu.RLock()
	defer mu.RUnlock()
	now := time.Now().UnixMilli()
	for id, q := range cache {
		if q.Status == "sim" || q.Status == "nosource" {
			// Never persist fabricated prices, and never persist price=0
			// placeholders (a 0 row is not a price and would only pollute
			// snapshot history / seeding).
			continue
		}
		_, _ = store.Exec(`INSERT INTO price_snapshots(id, asset_id, price, currency, source_time, status, created_at) VALUES(?,?,?,?,?,?,?)`,
			cryptox.UUID(), id, q.Price, q.Currency, q.SourceTime, q.Status, now)
	}
	// keep the table bounded
	_, _ = store.Exec(`DELETE FROM price_snapshots WHERE created_at < ?`, now-30*86400000)
}

// SourceFailTotal returns the cumulative count of failed real-source fetches.
func SourceFailTotal() int64 { return atomic.LoadInt64(&srcFailTotal) }

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
