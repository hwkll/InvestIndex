package api

import (
	"log"
	"strconv"
	"time"

	"investhub/internal/ai"
	"investhub/internal/alerts"
	"investhub/internal/core"
	"investhub/internal/quotes"
	"investhub/internal/settings"
	"investhub/internal/store"
)

// SeedDemo creates a small showcase portfolio on a brand-new database.
func SeedDemo() {
	if store.ScalarInt(`SELECT COUNT(*) FROM assets`) > 0 {
		return
	}
	mk := func(in core.AssetInput) string {
		a, err := core.CreateAsset(in)
		if err != nil {
			log.Printf("[seed] %v", err)
			return ""
		}
		return a.ID
	}
	btc := mk(core.AssetInput{Category: "crypto", Name: "比特币", Symbol: "BTC", Currency: "USD"})
	eth := mk(core.AssetInput{Category: "crypto", Name: "以太坊", Symbol: "ETH", Currency: "USD"})
	mt := mk(core.AssetInput{Category: "stock", Name: "贵州茅台", Symbol: "sh.600519", Currency: "CNY"})
	hs300 := mk(core.AssetInput{Category: "fund", Name: "沪深300ETF", Symbol: "510300", SubType: "etf", Currency: "CNY"})
	gold := mk(core.AssetInput{Category: "gold", Name: "黄金ETF", Symbol: "518880", SubType: "etf", Currency: "CNY"})

	now := time.Now().UnixMilli()
	day := int64(86400000)
	tx := func(id string, qty, price, fee float64, daysAgo int64) {
		if id == "" {
			return
		}
		q, p, f := qty, price, fee
		if _, err := core.CreateTransaction(core.TxInput{
			AssetID: id, Direction: "buy", Quantity: &q, Price: &p, Fee: &f, TradeTime: now - daysAgo*day,
		}); err != nil {
			log.Printf("[seed] %v", err)
		}
	}
	tx(btc, 0.5, 58000, 5, 20)
	tx(eth, 4, 2900, 2, 15)
	tx(mt, 100, 1450, 0, 30)
	tx(hs300, 2000, 3.8, 0, 40)
	tx(gold, 500, 5.4, 0, 25)

	b1, b2 := 80000.0, 30000.0
	_, _ = core.CreateCash(core.CashInput{Name: "招商银行卡", Balance: &b1, Currency: "CNY"})
	_, _ = core.CreateCash(core.CashInput{Name: "华泰证券账户", Balance: &b2, Currency: "CNY"})
	log.Println("[seed] demo assets / cash created")
}

// StartScheduler warms up the quote cache and starts the background loop.
func (s *Server) StartScheduler() {
	quotes.SetMode(settings.GetDefault("data_source_mode", "auto"))
	quotes.SeedState()
	SeedDemo()
	quotes.SeedState() // pick up assets that the demo seed just created
	core.SeedHistoricalSnapshots(90)
	core.TakeSnapshots()

	// Refresh FX rates once at startup (best-effort), then on a low-frequency
	// ticker so cross-currency market values track live CNY conversion. The
	// interval is configurable (fx_refresh_interval, seconds) and hot-reloaded
	// via the fxReset channel when the setting changes.
	quotes.RefreshFX()
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[scheduler] fx goroutine panic: %v", rec)
			}
		}()
		t := time.NewTicker(currentFxInterval())
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-s.fxReset:
				d := currentFxInterval()
				t.Reset(d)
				log.Printf("[scheduler] fx refresh interval -> %s", d)
			case <-t.C:
				quotes.RefreshFX()
			}
		}
	}()

	// Quote polling cadence (poll_interval, seconds). Configurable and hot-reloaded
	// via the pollReset channel — no restart needed.
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[scheduler] goroutine panic: %v", rec)
			}
		}()
		t := time.NewTicker(currentPollInterval())
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-s.pollReset:
				d := currentPollInterval()
				t.Reset(d)
				log.Printf("[scheduler] quote poll interval -> %ds", int(d.Seconds()))
			case <-t.C:
				s.tick()
			}
		}
	}()

	// Market-context refresh (background warm-up of the AI analysis market
	// context cache). Best-effort: a failed upstream fetch leaves the cache
	// stale/empty and is logged, but never blocks startup. Interval is the
	// ai_market_context_ttl setting, hot-reloaded via marketCtxReset.
	ai.RefreshMarketContext()
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[scheduler] market-ctx goroutine panic: %v", rec)
			}
		}()
		t := time.NewTicker(currentMarketCtxInterval())
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-s.marketCtxReset:
				d := currentMarketCtxInterval()
				t.Reset(d)
				log.Printf("[scheduler] market-ctx refresh interval -> %s", d)
				ai.RefreshMarketContext()
			case <-t.C:
				ai.RefreshMarketContext()
			}
		}
	}()
	log.Printf("[scheduler] quote poll every %ds, fx refresh every %s, market-ctx refresh every %s",
		int(currentPollInterval().Seconds()), currentFxInterval(), currentMarketCtxInterval())
}

// currentPollInterval reads the quote polling cadence (seconds) from settings,
// clamped to a sane range so the server can't be told to spin or to stall.
func currentPollInterval() time.Duration {
	sec := 30
	if v := settings.GetDefault("poll_interval", "30"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n < 5 {
				n = 5
			}
			if n > 600 {
				n = 600 // cap at 10 minutes so daily snapshots aren't missed
			}
			sec = n
		}
	}
	return time.Duration(sec) * time.Second
}

// currentFxInterval reads the FX refresh cadence (seconds) from settings,
// clamped so the remote FX source is neither hammered nor starved.
func currentFxInterval() time.Duration {
	sec := 1800
	if v := settings.GetDefault("fx_refresh_interval", "1800"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n < 60 {
				n = 60
			}
			if n > 86400 {
				n = 86400 // at most once a day
			}
			sec = n
		}
	}
	return time.Duration(sec) * time.Second
}

// TriggerPollReset asks the scheduler to reload the quote-poll interval.
// Non-blocking: at most one pending signal is queued; the goroutine reads the
// latest configured value when it wakes, so a dropped signal is harmless.
func (s *Server) TriggerPollReset() {
	select {
	case s.pollReset <- struct{}{}:
	default:
	}
}

// TriggerFxReset asks the scheduler to reload the FX-refresh interval.
func (s *Server) TriggerFxReset() {
	select {
	case s.fxReset <- struct{}{}:
	default:
	}
}

// currentMarketCtxInterval reads the market-context cache lifetime (the
// ai_market_context_ttl setting, seconds) and clamps it to a sane range so the
// upstream warm-up is neither hammered nor starved.
func currentMarketCtxInterval() time.Duration {
	sec := 900
	if v := settings.GetDefault("ai_market_context_ttl", "900"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n < 60 {
				n = 60
			}
			if n > 86400 {
				n = 86400
			}
			sec = n
		}
	}
	return time.Duration(sec) * time.Second
}

// TriggerMarketCtxReset asks the scheduler to reload the market-context refresh
// interval (and refresh once immediately). Non-blocking.
func (s *Server) TriggerMarketCtxReset() {
	select {
	case s.marketCtxReset <- struct{}{}:
	default:
	}
}

// Stop halts the scheduler goroutine. Safe to call multiple times.
func (s *Server) Stop() {
	select {
	case <-s.stop:
		// already closed
	default:
		close(s.stop)
	}
}

func (s *Server) tick() {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[scheduler] recovered: %v", rec)
		}
	}()

	for _, q := range quotes.PollAll() {
		Broadcast("quote", q)
	}
	quotes.PersistPrices()

	for _, ev := range alerts.EvaluateAll() {
		Broadcast("alert", ev)
	}

	// daily position snapshot around 23:55
	now := time.Now()
	if now.Hour() == 23 && now.Minute() >= 55 {
		today := now.Format("2006-01-02")
		if settings.Get("_last_snapshot") != today {
			core.TakeSnapshots()
			if err := settings.Set("_last_snapshot", today); err != nil {
				log.Printf("[scheduler] 记录 _last_snapshot 失败: %v", err)
			}
		}
	}

	_, _ = store.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UnixMilli())
}
