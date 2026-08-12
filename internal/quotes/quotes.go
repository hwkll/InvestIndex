// Package quotes provides real market data with graceful degradation to
// "nosource" when no upstream source is available, plus K-line storage.
package quotes

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
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
	Status     string  `json:"status"` // ok | nosource
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

var (
	mu sync.RWMutex
	cache = map[string]*Quote{}

	// Mode: auto (try real) | real. "sim" was removed — all data is real;
	// when no source is available the quote reports "nosource".
	mode = "auto"

	httpc = &http.Client{Timeout: 6 * time.Second}
)

// SetMode switches the data-source strategy. Only "auto" (try real) and
// "real" are accepted; "sim" is no longer supported — all data must be real.
func SetMode(m string) {
	mu.Lock()
	defer mu.Unlock()
	if m == "auto" || m == "real" {
		mode = m
	}
}

// Mode returns the current data-source strategy.
func Mode() string {
	mu.RLock()
	defer mu.RUnlock()
	return mode
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

// SeedState primes the in-memory cache for every active asset with a neutral
// "nosource" (price 0) entry so the asset is never "unknown" and never flashes
// a fabricated number before its first real fetch. The API handler is
// responsible for pulling a real quote (and backfilling K-line) right after.
func SeedState() {
	for _, a := range activeAssets() {
		mu.RLock()
		_, exists := cache[a.ID]
		mu.RUnlock()
		if exists {
			continue
		}
		mu.Lock()
		// Re-check: another goroutine may have added it between the RUnlock and Lock.
		if _, doubleCheck := cache[a.ID]; doubleCheck {
			mu.Unlock()
			continue
		}
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
// creation.
// Idempotent: re-adding an existing asset is a no-op.
func AddAsset(a Asset) {
	mu.RLock()
	if _, exists := cache[a.ID]; exists {
		mu.RUnlock()
		return
	}
	mu.RUnlock()
	mu.Lock()
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

// ---- simulator (removed) ----------------------------------------------
// The explicit random-walk simulator ("sim" mode) was deleted: all quote data
// must be real. When no upstream source is reachable the quote reports
// "nosource" instead of a fabricated price.

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
	var q *Quote
	if real != nil {
		q = real
	} else {
		// No usable real price (no provider, or the provider was unreachable /
		// returned nothing): report honestly as "nosource" rather than
		// fabricating a simulator price that would pollute PnL/display.
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
			bumpKline(a, q.Price, 200)
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
		if q.Status == "nosource" {
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

// EnsureKline guarantees real daily history exists for an asset+period. If a
// real source is reachable it is fetched and persisted (oldest-first); if not,
// nothing is written — an empty K-line is honest "no real data", never a
// fabricated series.
func EnsureKline(a Asset, period string, limit int) {
	if period == "" {
		period = "1d"
	}
	if store.ScalarInt(`SELECT COUNT(*) FROM kline_cache WHERE asset_id=? AND period=?`, a.ID, period) > 0 {
		return
	}
	candles := fetchKlineReal(a, limit)
	if len(candles) == 0 {
		return // no real source / network unreachable: write nothing
	}
	tx, err := store.DB.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO kline_cache(id, asset_id, period, ts, open, high, low, close, volume) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return
	}
	for _, c := range candles {
		_, _ = stmt.Exec(cryptox.UUID(), a.ID, period, c.Ts, c.Open, c.High, c.Low, c.Close, c.Volume)
	}
	_ = stmt.Close()
	_ = tx.Commit()
}

func bumpKline(a Asset, price float64, limit int) {
	var id string
	var high, low float64
	err := store.QueryRow(`SELECT id, high, low FROM kline_cache WHERE asset_id=? AND period='1d' ORDER BY ts DESC LIMIT 1`, a.ID).
		Scan(&id, &high, &low)
	if err != nil {
		// No existing candle yet: try to backfill real daily history. If no
		// real source is reachable this writes nothing (empty is honest).
		EnsureKline(a, "1d", limit)
		return
	}
	_, _ = store.Exec(`UPDATE kline_cache SET close=?, high=?, low=? WHERE id=?`,
		price, math.Max(high, price), math.Min(low, price), id)
}

// fetchKlineReal pulls real daily candles for an asset by category. Every
// request goes through httpc (6s timeout, ProxyFromEnvironment). On any network
// error or parse failure it returns nil — the caller must NOT fabricate data.
func fetchKlineReal(a Asset, limit int) []Candle {
	if limit <= 0 {
		limit = 180
	}
	if limit > 1000 {
		limit = 1000
	}
	switch a.Category {
	case "crypto":
		return fetchKlineBinance(a, limit)
	case "fund":
		if a.SubType == "etf" {
			return fetchKlineSina(a, limit)
		}
		return fetchKlineFund(a, limit)
	case "stock":
		// Hong Kong stocks: Sina's getKLineData returns null for hk* codes, so
		// route them to Tencent's real HK daily feed. A-share / ETF / US stay on Sina.
		if strings.HasPrefix(sinaCode(a), "hk") {
			return fetchKlineHK(a, limit)
		}
		return fetchKlineSina(a, limit)
	case "gold":
		normGoldSubType(&a)
		if a.SubType == "etf" {
			return fetchKlineSina(a, limit)
		}
		if isXAU(a) {
			return nil // international London gold: no daily series source wired
		}
		return fetchKlineGoldSpot(a, limit)
	}
	return nil
}

// fetchKlineBinance pulls real daily klines from Binance's public REST API.
func fetchKlineBinance(a Asset, limit int) []Candle {
	sym := strings.ToUpper(strings.TrimSpace(a.Symbol))
	if !strings.HasSuffix(sym, "USDT") {
		sym += "USDT"
	}
	url := "https://api.binance.com/api/v3/klines?symbol=" + sym + "&interval=1d&limit=" + strconv.Itoa(limit)
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
	var rows [][]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rows); err != nil {
		return nil
	}
	candles := make([]Candle, 0, len(rows))
	for _, r := range rows {
		// [openTime, open, high, low, close, volume, closeTime, ...]
		if len(r) < 6 {
			continue
		}
		openT, _ := r[0].(float64)
		open, _ := strconv.ParseFloat(fmt.Sprint(r[1]), 64)
		high, _ := strconv.ParseFloat(fmt.Sprint(r[2]), 64)
		low, _ := strconv.ParseFloat(fmt.Sprint(r[3]), 64)
		close, _ := strconv.ParseFloat(fmt.Sprint(r[4]), 64)
		vol, _ := strconv.ParseFloat(fmt.Sprint(r[5]), 64)
		if open <= 0 || high <= 0 || low <= 0 || close <= 0 {
			continue
		}
		candles = append(candles, Candle{
			Ts: int64(openT), Open: open, High: high, Low: low, Close: close, Volume: vol,
		})
	}
	return candles
}

// fetchKlineFund pulls real daily NAV history for an off-exchange (open-end)
// fund from Eastmoney's public F10 history endpoint. Each NAV has no OHLC, so
// open=high=low=close=NAV and volume=0.
func fetchKlineFund(a Asset, limit int) []Candle {
	code := strings.TrimSpace(a.Symbol)
	if code == "" {
		return nil
	}
	url := "https://api.fund.eastmoney.com/f10/lsjz?fundCode=" + code + "&pageIndex=1&pageSize=" + strconv.Itoa(limit)
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var j struct {
		Data struct {
			List []struct {
				FSRQ  string `json:"FSRQ"` // 净值日期
				DWJZ  string `json:"DWJZ"` // 单位净值
				JZZZL string `json:"JZZZL"`
			} `json:"LSJZList"`
		} `json:"Data"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil
	}
	if len(j.Data.List) == 0 {
		return nil
	}
	// Eastmoney returns newest-first; reverse to oldest-first before persisting.
	list := j.Data.List
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	candles := make([]Candle, 0, len(list))
	for _, r := range list {
		ts, derr := time.ParseInLocation("2006-01-02", strings.TrimSpace(r.FSRQ), time.Local)
		if derr != nil {
			continue
		}
		close, cerr := strconv.ParseFloat(strings.TrimSpace(r.DWJZ), 64)
		if cerr != nil || close <= 0 {
			continue
		}
		candles = append(candles, Candle{
			Ts: ts.UnixMilli(), Open: close, High: close, Low: close, Close: close, Volume: 0,
		})
	}
	return candles
}

// fetchKlineSinaSymbol pulls real daily klines from Sina's public K-line API
// for a pre-built Sina symbol (e.g. sh600519 / hk09866 / gdsAUTD).
func fetchKlineSinaSymbol(symbol string, limit int) []Candle {
	if symbol == "" {
		return nil
	}
	url := "https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData?symbol=" + symbol + "&scale=240&ma=no&datalen=" + strconv.Itoa(limit)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Referer", "https://finance.sina.com.cn/")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var rows []struct {
		Day    string `json:"day"`
		Open   string `json:"open"`
		High   string `json:"high"`
		Low    string `json:"low"`
		Close  string `json:"close"`
		Volume string `json:"volume"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil
	}
	candles := make([]Candle, 0, len(rows))
	for _, r := range rows {
		ts, derr := time.ParseInLocation("2006-01-02", strings.TrimSpace(r.Day), time.Local)
		if derr != nil {
			continue
		}
		open, _ := strconv.ParseFloat(strings.TrimSpace(r.Open), 64)
		high, _ := strconv.ParseFloat(strings.TrimSpace(r.High), 64)
		low, _ := strconv.ParseFloat(strings.TrimSpace(r.Low), 64)
		close, _ := strconv.ParseFloat(strings.TrimSpace(r.Close), 64)
		vol, _ := strconv.ParseFloat(strings.TrimSpace(r.Volume), 64)
		if open <= 0 || high <= 0 || low <= 0 || close <= 0 {
			continue
		}
		candles = append(candles, Candle{
			Ts: ts.UnixMilli(), Open: open, High: high, Low: low, Close: close, Volume: vol,
		})
	}
	return candles
}

// fetchKlineSina pulls real daily klines for an A-share / ETF / HK stock via
// Sina, routing through the shared sinaCode construction in fetchReal.
func fetchKlineSina(a Asset, limit int) []Candle {
	return fetchKlineSinaSymbol(sinaCode(a), limit)
}

// fetchKlineHK pulls real daily candles for a Hong Kong stock from Tencent's
// public fqkline feed. Sina's getKLineData returns null for hk* symbols, so HK
// history has to come from here.
//
// Response shape:
//
//	{"code":0,"data":{"hk09866":{"day":[["2026-07-15","39.960","39.580","39.960","38.960","3464432.000"],...]}}}
//
// NOTE the column order is [date, OPEN, CLOSE, HIGH, LOW, volume] — close and
// high/low are NOT in the Sina order. Returns nil on any failure; never fabricates.
func fetchKlineHK(a Asset, limit int) []Candle {
	sym := sinaCode(a) // e.g. hk09866 (already zero-padded to 5 digits)
	if !strings.HasPrefix(sym, "hk") {
		return nil
	}
	url := "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=" +
		sym + ",day,,," + strconv.Itoa(limit) + ",qfq"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://gu.qq.com/")
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	var j struct {
		Code int `json:"code"`
		Data map[string]struct {
			Day    [][]any `json:"day"`
			QfqDay [][]any `json:"qfqday"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &j); err != nil || j.Code != 0 {
		return nil
	}
	node, ok := j.Data[sym]
	if !ok {
		return nil
	}
	rows := node.Day
	if len(rows) == 0 {
		rows = node.QfqDay // some params return the adjusted series under qfqday
	}
	if len(rows) == 0 {
		return nil
	}
	candles := make([]Candle, 0, len(rows))
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		ts, derr := time.ParseInLocation("2006-01-02", strings.TrimSpace(fmt.Sprint(r[0])), time.Local)
		if derr != nil {
			continue
		}
		open, _ := strconv.ParseFloat(fmt.Sprint(r[1]), 64)
		close, _ := strconv.ParseFloat(fmt.Sprint(r[2]), 64)
		high, _ := strconv.ParseFloat(fmt.Sprint(r[3]), 64)
		low, _ := strconv.ParseFloat(fmt.Sprint(r[4]), 64)
		vol, _ := strconv.ParseFloat(fmt.Sprint(r[5]), 64)
		if open <= 0 || high <= 0 || low <= 0 || close <= 0 {
			continue
		}
		candles = append(candles, Candle{
			Ts: ts.UnixMilli(), Open: open, High: high, Low: low, Close: close, Volume: vol,
		})
	}
	return candles
}

// fetchKlineGoldSpot pulls real daily candles for spot / physical gold priced in
// CNY per gram. Sina's gdsAUTD symbol has no daily K-line endpoint (getKLineData
// returns null), so the series comes from the SHFE gold continuous contract
// (Au0) daily K-line — a real, exchange-published CNY/gram series that tracks the
// same underlying metal as Au(T+D) / physical gold, so it is the honest choice
// for a spot-gold trend chart.
//
// Response is JSONP guarded by an XSSI comment:
//
//	/*<script>location.href='//sina.com';</script>*/
//	var _=([{"d":"2008-01-09","o":"230.950","h":"230.990","l":"221.880","c":"223.300","v":"103364",...}]);
//
// Returns nil on any failure — never fabricates a series.
func fetchKlineGoldSpot(a Asset, limit int) []Candle {
	return fetchKlineGoldFutures(limit)
}

// fetchKlineGoldFutures fetches and parses the Au0 (SHFE gold continuous) daily
// K-line. The upstream ignores any length parameter and always returns the full
// history back to 2008 (~500KB), so we slice the newest `limit` rows locally.
func fetchKlineGoldFutures(limit int) []Candle {
	url := "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_=/InnerFuturesNewService.getDailyKLine?symbol=Au0"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://finance.sina.com.cn/futures/quotes/Au0.shtml")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	// Full history is ~500KB today and grows ~250 rows/year; allow 8MB headroom
	// so the payload is never silently truncated into a parse failure.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil
	}
	// Strip the JSONP wrapper: keep everything between the first '[' and last ']'.
	s := string(raw)
	lo, hi := strings.Index(s, "["), strings.LastIndex(s, "]")
	if lo < 0 || hi <= lo {
		return nil
	}
	var rows []struct {
		D string `json:"d"` // date
		O string `json:"o"` // open
		H string `json:"h"` // high
		L string `json:"l"` // low
		C string `json:"c"` // close
		V string `json:"v"` // volume
	}
	if err := json.Unmarshal([]byte(s[lo:hi+1]), &rows); err != nil {
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:] // oldest-first upstream: keep the newest tail
	}
	candles := make([]Candle, 0, len(rows))
	for _, r := range rows {
		ts, derr := time.ParseInLocation("2006-01-02", strings.TrimSpace(r.D), time.Local)
		if derr != nil {
			continue
		}
		open, _ := strconv.ParseFloat(strings.TrimSpace(r.O), 64)
		high, _ := strconv.ParseFloat(strings.TrimSpace(r.H), 64)
		low, _ := strconv.ParseFloat(strings.TrimSpace(r.L), 64)
		close, _ := strconv.ParseFloat(strings.TrimSpace(r.C), 64)
		vol, _ := strconv.ParseFloat(strings.TrimSpace(r.V), 64)
		if open <= 0 || high <= 0 || low <= 0 || close <= 0 {
			continue
		}
		candles = append(candles, Candle{
			Ts: ts.UnixMilli(), Open: open, High: high, Low: low, Close: close, Volume: vol,
		})
	}
	return candles
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
	var subType, currency, provider sql.NullString
	err := store.QueryRow(`SELECT id, category, symbol, sub_type, currency, provider FROM assets WHERE id=?`, assetID).
		Scan(&a.ID, &a.Category, &a.Symbol, &subType, &currency, &provider)
	if err != nil {
		return []Candle{}
	}
	if subType.Valid {
		a.SubType = subType.String
	}
	if currency.Valid {
		a.Currency = currency.String
	}
	if provider.Valid {
		a.Provider = provider.String
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
