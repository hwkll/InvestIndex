package ai

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"investhub/internal/core"
	"investhub/internal/quotes"
	"investhub/internal/settings"
	"investhub/internal/store"
)

// IndexSnap is one market index / spot-price snapshot used as analysis context.
type IndexSnap struct {
	Name   string  `json:"name"`
	Price  float64 `json:"price"`
	ChgPct float64 `json:"chgPct"`
}

// BoardSnap is one industry-sector rank entry (top gainers / top losers).
type BoardSnap struct {
	Name   string  `json:"name"`
	ChgPct float64 `json:"chgPct"`
}

// MarketContext is the structured, token-trimmed public-market snapshot merged
// into the analysis prompt. Every field is derived from REAL upstream sources;
// nothing is ever synthesized. Missing data is left empty and flagged in Sources.
type MarketContext struct {
	Indices map[string]IndexSnap `json:"indices"`
	Boards  []BoardSnap          `json:"boards"`
	Macro   map[string]float64   `json:"macro"`
	Sources map[string]string    `json:"sources"`
}

// indexLabels maps the fixed Sina symbols we track to clean Chinese display
// names, so the prompt never depends on the lossy (GBK) upstream names.
var indexLabels = map[string]string{
	"sh000001": "上证指数",
	"sh000300": "沪深300",
	"sz399006": "创业板指",
	"hkHSI":    "恒生指数",
	"gb_ixic":  "纳斯达克",
	"gb_dji":   "道琼斯",
	"gb_spx":   "标普500",
}

// ---------------------------------------------------------------------------
// Upstream fetch — real sources only; a failure yields an empty dimension, never
// a fabricated value.
// ---------------------------------------------------------------------------

// fetchAllIndices pulls every relevant index + gold spot in one Sina batch
// request and reads USD/CNY from the live FX table. Returns false only when NO
// index at all could be resolved.
func fetchAllIndices(mc *MarketContext) bool {
	symbols := []string{"sh000001", "sh000300", "sz399006", "hkHSI", "gb_ixic", "gb_dji", "gb_spx", "hf_XAU"}
	req, err := http.NewRequest("GET", "https://hq.sinajs.cn/?list="+strings.Join(symbols, ","), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Referer", "https://finance.sina.com.cn/")
	resp, err := quotes.HTTPClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return false
	}
	ok := false
	for _, sym := range symbols {
		if snap, got := parseSinaIndex(raw, sym); got {
			if sym == "hf_XAU" {
				mc.Indices["黄金(XAU/USD)"] = snap
			} else {
				mc.Indices[indexLabels[sym]] = snap
			}
			ok = true
		}
	}
	// USD/CNY anchor — already maintained live by the scheduler's FX refresh.
	if rate, ok2 := fetchUSDCNY(); ok2 {
		mc.Indices["美元/人民币"] = IndexSnap{Name: "美元/人民币", Price: rate, ChgPct: 0}
		ok = true
	}
	return ok
}

// parseSinaIndex extracts and parses one `var hq_str_<sym>="..."` line. The
// field layout differs per venue:
//   - A-share (sh/sz): f[2]=prevClose, f[3]=current  → chgPct computed.
//   - US (gb_):        f[1]=current,  f[2]=chgPct(%)  (given directly).
//   - HK (hk):         f[6]=current,  f[8]=chgPct(%)  (given directly).
//   - Gold (hf_):      f[0]=price,    f[3]=prevClose   → chgPct computed.
func parseSinaIndex(raw []byte, sym string) (IndexSnap, bool) {
	marker := []byte("hq_str_" + sym + "=\"")
	idx := bytes.Index(raw, marker)
	if idx < 0 {
		return IndexSnap{}, false
	}
	start := idx + len(marker)
	end := bytes.IndexByte(raw[start:], '"')
	if end < 0 {
		return IndexSnap{}, false
	}
	f := strings.Split(string(raw[start:start+end]), ",")
	snap := IndexSnap{Name: indexLabels[sym]}
	switch {
	case strings.HasPrefix(sym, "sh") || strings.HasPrefix(sym, "sz"):
		price, ok1 := atof(f, 3)
		prev, ok2 := atof(f, 2)
		if !ok1 || !ok2 || price <= 0 {
			return IndexSnap{}, false
		}
		snap.Price = price
		snap.ChgPct = (price/prev - 1) * 100
	case strings.HasPrefix(sym, "gb_"):
		price, ok1 := atof(f, 1)
		if !ok1 || price <= 0 {
			return IndexSnap{}, false
		}
		snap.Price = price
		if v, ok2 := atof(f, 2); ok2 {
			snap.ChgPct = v
		}
	case strings.HasPrefix(sym, "hk"):
		price, ok1 := atof(f, 6)
		if !ok1 || price <= 0 {
			return IndexSnap{}, false
		}
		snap.Price = price
		if v, ok2 := atof(f, 8); ok2 {
			snap.ChgPct = v
		}
	case strings.HasPrefix(sym, "hf_"):
		price, ok1 := atof(f, 0)
		prev, ok2 := atof(f, 3)
		if !ok1 || price <= 0 {
			return IndexSnap{}, false
		}
		snap.Price = price
		if ok2 && prev > 0 {
			snap.ChgPct = (price/prev - 1) * 100
		}
	default:
		return IndexSnap{}, false
	}
	return snap, true
}

// atof safely parses a numeric CSV field; non-numeric / NaN / Inf → false.
func atof(f []string, i int) (float64, bool) {
	if i < 0 || i >= len(f) {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// fetchUSDCNY reads the live USD→CNY rate from fx_rates (maintained by the
// scheduler). Returns 0,false when unavailable so it is simply omitted.
func fetchUSDCNY() (float64, bool) {
	rate := store.ScalarFloat(`SELECT rate FROM fx_rates WHERE currency='USD' AND auto=1`)
	if rate <= 0 {
		return 0, false
	}
	return rate, true
}

// fetchAllBoards pulls the Eastmoney industry-sector rank (top 5 gainers + top 5
// losers) — a global ranking, not mapped per-asset. f3 = 涨跌幅%, f14 = 板块名.
func fetchAllBoards(mc *MarketContext) bool {
	top := fetchEMBoards("1")    // po=1 → 涨幅榜 (top gainers)
	bottom := fetchEMBoards("0") // po=0 → 跌幅榜 (top losers)
	for _, b := range append(top, bottom...) {
		mc.Boards = append(mc.Boards, b)
	}
	return len(mc.Boards) > 0
}

func fetchEMBoards(po string) []BoardSnap {
	url := "https://push2.eastmoney.com/api/qt/clist/get?pn=1&pz=5&po=" + po +
		"&np=1&fltt=2&invt=2&fid=f3&fs=m:90+t:2+f:!50"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://quote.eastmoney.com/")
	resp, err := quotes.HTTPClient().Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil
	}
	var j struct {
		Data struct {
			Diff []struct {
				F3  float64 `json:"f3"`
				F14 string  `json:"f14"`
			} `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil
	}
	out := make([]BoardSnap, 0, len(j.Data.Diff))
	for _, d := range j.Data.Diff {
		if d.F14 == "" {
			continue
		}
		out = append(out, BoardSnap{Name: d.F14, ChgPct: d.F3})
	}
	return out
}

// fetchAllMacro pulls CPI (当月同比) and 制造业PMI from Eastmoney's public
// datacenter. Each indicator is read independently; one failing does not void
// the other, and an empty result is reported honestly (macro → "nosource").
func fetchAllMacro(mc *MarketContext) bool {
	mc.Macro = map[string]float64{}
	if v, ok := fetchEMMacro("RPT_ECONOMY_CPI", "NATIONAL_SAME"); ok {
		mc.Macro["CPI同比"] = v
	}
	if v, ok := fetchEMMacro("RPT_ECONOMY_PMI", "MAKE_INDEX"); ok {
		mc.Macro["制造业PMI"] = v
	}
	return len(mc.Macro) > 0
}

func fetchEMMacro(report, field string) (float64, bool) {
	url := "https://datacenter-web.eastmoney.com/api/data/v1/get?reportName=" + report +
		"&columns=ALL&pageSize=12"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://data.eastmoney.com/")
	resp, err := quotes.HTTPClient().Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return 0, false
	}
	var j struct {
		Result struct {
			Data []map[string]any `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return 0, false
	}
	if len(j.Result.Data) == 0 {
		return 0, false
	}
	// The datacenter sort param is unreliable; pick the newest record locally by
	// the REPORT_DATE string (YYYY-MM-DD … sorts correctly lexicographically).
	newest := j.Result.Data[0]
	newestDate, _ := newest["REPORT_DATE"].(string)
	for _, row := range j.Result.Data[1:] {
		if d, ok := row["REPORT_DATE"].(string); ok && d > newestDate {
			newest = row
			newestDate = d
		}
	}
	v, ok := newest[field].(float64)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// ---------------------------------------------------------------------------
// In-memory cache (mutex-protected) + scope selection.
// ---------------------------------------------------------------------------

type mcCacheEntry struct {
	mc     *MarketContext
	status map[string]string
	at     int64
}

var (
	mcMu    sync.RWMutex
	mcCache *mcCacheEntry
)

// marketTTL reads the configured cache lifetime (seconds) from settings.
func marketTTL() int64 {
	if v := settings.GetDefault("ai_market_context_ttl", "900"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return int64(n)
		}
	}
	return 900
}

// refreshMarketContext fetches the FULL (global) market context across all
// dimensions in parallel and returns it with a per-dimension status map.
func refreshMarketContext() (*MarketContext, map[string]string) {
	mc := &MarketContext{
		Indices: map[string]IndexSnap{},
		Boards:  []BoardSnap{},
		Macro:   map[string]float64{},
		Sources: map[string]string{},
	}
	status := map[string]string{}
	var wg sync.WaitGroup
	wg.Add(3)
	var idxOK, boardOK, macroOK bool
	go func() { defer wg.Done(); idxOK = fetchAllIndices(mc) }()
	go func() { defer wg.Done(); boardOK = fetchAllBoards(mc) }()
	go func() { defer wg.Done(); macroOK = fetchAllMacro(mc) }()
	wg.Wait()
	status["indices"] = boolStatus(idxOK)
	status["boards"] = boolStatus(boardOK)
	status["macro"] = boolStatus(macroOK)
	mc.Sources = status
	return mc, status
}

func boolStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "nosource"
}

// loadFresh refreshes and stores the cache, returning the fresh copy.
func loadFresh() (*MarketContext, map[string]string) {
	mc, status := refreshMarketContext()
	mcMu.Lock()
	mcCache = &mcCacheEntry{mc: mc, status: status, at: time.Now().UnixMilli()}
	mcMu.Unlock()
	return mc, status
}

// RefreshMarketContext forces a cache refresh. Used by the scheduler warm-up and
// on settings changes; safe to call any time.
func RefreshMarketContext() {
	loadFresh()
}

// FetchMarketContext returns the scope-appropriate, token-trimmed market context
// for the analysis prompt plus the per-dimension source status.
//
// This is a pure DATA-PREP step that runs BEFORE the DeepSeek call. Any upstream
// failure yields an empty dimension + a "nosource" status and NEVER returns an
// error, so it can never trigger the AI hard rules (no-key → 40301 / call fail
// → 500). When the feature is disabled in settings it returns (nil, nil) and the
// caller leaves ctxData untouched — matching the pre-feature behavior.
func FetchMarketContext(scope string, asset core.Asset) (map[string]any, map[string]string) {
	if settings.GetDefault("ai_market_context", "true") != "true" {
		return nil, nil
	}
	mcMu.RLock()
	cached := mcCache
	mcMu.RUnlock()
	var full *MarketContext
	var status map[string]string
	if cached != nil && cached.mc != nil && time.Now().UnixMilli()-cached.at < marketTTL()*1000 {
		full = cached.mc
		status = cached.status
	} else {
		full, status = loadFresh()
	}
	sel := selectScope(full, scope, asset)
	b, err := json.Marshal(sel)
	if err != nil {
		return nil, status
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, status
	}
	return m, status
}

// MarketContextStatus returns the cached per-dimension statuses (+ a light
// summary) for the settings UI / ops visibility. It never triggers a network
// fetch: an unprimed cache simply reports empty.
func MarketContextStatus() map[string]any {
	mcMu.RLock()
	cached := mcCache
	mcMu.RUnlock()
	if cached == nil || cached.mc == nil {
		return map[string]any{
			"status":    map[string]string{},
			"indices":   0,
			"boards":    0,
			"macro":     0,
			"fetchedAt": int64(0),
		}
	}
	return map[string]any{
		"status":    cached.status,
		"indices":   len(cached.mc.Indices),
		"boards":    len(cached.mc.Boards),
		"macro":     len(cached.mc.Macro),
		"fetchedAt": cached.at,
	}
}

// selectScope trims the full market context to the scope-appropriate subset so
// the prompt stays bounded. Boards are only meaningful for fund/stock/global;
// crypto/gold mark them "n/a" (not applicable, not a failure).
func selectScope(full *MarketContext, scope string, asset core.Asset) *MarketContext {
	out := &MarketContext{
		Indices: map[string]IndexSnap{},
		Boards:  []BoardSnap{},
		Macro:   map[string]float64{},
		Sources: map[string]string{},
	}
	// Macro is shared by every scope.
	for k, v := range full.Macro {
		out.Macro[k] = v
	}
	if len(out.Macro) > 0 {
		out.Sources["macro"] = "ok"
	} else {
		out.Sources["macro"] = "nosource"
	}

	idxOK := false
	switch {
	case scope == "global":
		for k, v := range full.Indices {
			out.Indices[k] = v
			idxOK = true
		}
	case asset.Category == "stock":
		switch stockMarket(asset.Symbol) {
		case "hk":
			if v, ok := full.Indices["恒生指数"]; ok {
				out.Indices["恒生指数"] = v
				idxOK = true
			}
		case "us":
			for _, lbl := range []string{"纳斯达克", "道琼斯", "标普500"} {
				if v, ok := full.Indices[lbl]; ok {
					out.Indices[lbl] = v
					idxOK = true
				}
			}
		default: // A-share
			for _, lbl := range []string{"上证指数", "沪深300", "创业板指"} {
				if v, ok := full.Indices[lbl]; ok {
					out.Indices[lbl] = v
					idxOK = true
				}
			}
		}
	case asset.Category == "fund":
		for _, lbl := range []string{"沪深300", "上证指数"} {
			if v, ok := full.Indices[lbl]; ok {
				out.Indices[lbl] = v
				idxOK = true
			}
		}
	case asset.Category == "crypto", asset.Category == "gold":
		for _, lbl := range []string{"纳斯达克", "道琼斯"} {
			if v, ok := full.Indices[lbl]; ok {
				out.Indices[lbl] = v
				idxOK = true
			}
		}
		if asset.Category == "crypto" {
			if v, ok := full.Indices["黄金(XAU/USD)"]; ok {
				out.Indices["黄金(XAU/USD)"] = v
				idxOK = true
			}
		}
		if asset.Category == "gold" {
			if v, ok := full.Indices["美元/人民币"]; ok {
				out.Indices["美元/人民币"] = v
			}
		}
	}
	if idxOK {
		out.Sources["indices"] = "ok"
	} else {
		out.Sources["indices"] = "nosource"
	}

	// Boards: only for fund / stock / global.
	if scope == "global" || asset.Category == "fund" || asset.Category == "stock" {
		out.Boards = full.Boards
		if len(out.Boards) > 0 {
			out.Sources["boards"] = "ok"
		} else {
			out.Sources["boards"] = "nosource"
		}
	} else {
		out.Sources["boards"] = "n/a"
	}
	return out
}

// stockMarket infers the trading venue from the asset symbol.
func stockMarket(symbol string) string {
	s := strings.ToLower(strings.TrimSpace(symbol))
	if strings.Contains(s, "hk") || strings.HasSuffix(s, ".hk") {
		return "hk"
	}
	if isUSStock(s) {
		return "us"
	}
	return "a"
}

func isUSStock(s string) bool {
	if len(s) == 0 || len(s) > 5 {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
