package api

import (
	"log"
	"os"
	"strconv"
	"time"

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

	pollSec := 30
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 600 {
				n = 600 // cap at 10 minutes so daily snapshots aren't missed
			}
			pollSec = n
		}
	}
	interval := time.Duration(pollSec) * time.Second

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[scheduler] goroutine panic: %v", rec)
			}
		}()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.tick()
			}
		}
	}()
	log.Printf("[scheduler] quote poll every %ds", pollSec)
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
			settings.Set("_last_snapshot", today)
		}
	}

	_, _ = store.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UnixMilli())
}
