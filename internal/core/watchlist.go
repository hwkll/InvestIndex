// Package core — watchlist (自选) module. A watchlist entry references an existing
// asset and optionally a target price; live quotes are merged at read time.
package core

import (
	"database/sql"

	"investhub/internal/cryptox"
	"investhub/internal/quotes"
	"investhub/internal/store"
)

// WatchItem is one自选 row with the live quote merged in.
type WatchItem struct {
	ID          string   `json:"id"`
	AssetID     string   `json:"assetId"`
	Name        string   `json:"name"`
	Symbol      string   `json:"symbol"`
	Category    string   `json:"category"`
	Currency    string   `json:"currency"`
	TargetPrice *float64 `json:"targetPrice"`
	Note        string   `json:"note"`
	Price       float64  `json:"price"`
	ChgPct      float64  `json:"chgPct"`
	QuoteStatus string   `json:"quoteStatus"`
	AboveTarget bool     `json:"aboveTarget"`
}

// ListWatchlist returns all自选 entries with live prices.
func ListWatchlist() []WatchItem {
	rows, err := store.Query(`SELECT w.id, w.asset_id, w.target_price, w.note, a.name, a.symbol, a.category, a.currency
	    FROM watchlist w JOIN assets a ON a.id = w.asset_id ORDER BY w.sort_order, a.name`)
	if err != nil {
		return []WatchItem{}
	}
	defer rows.Close()
	out := []WatchItem{}
	for rows.Next() {
		var w WatchItem
		var tp sql.NullFloat64
		var note sql.NullString
		if err := rows.Scan(&w.ID, &w.AssetID, &tp, &note, &w.Name, &w.Symbol, &w.Category, &w.Currency); err != nil {
			continue
		}
		if tp.Valid {
			v := tp.Float64
			w.TargetPrice = &v
		}
		w.Note = note.String
		if q := quotes.Get(w.AssetID); q != nil {
			w.Price = q.Price
			w.ChgPct = q.ChgPct
			w.QuoteStatus = q.Status
		}
		w.AboveTarget = w.TargetPrice != nil && w.Price >= *w.TargetPrice
		out = append(out, w)
	}
	return out
}

// AddToWatchlist adds an asset to自选, de-duplicating by asset_id.
func AddToWatchlist(assetID string, target *float64, note string) (map[string]any, error) {
	if store.ScalarInt(`SELECT COUNT(*) FROM assets WHERE id = ?`, assetID) == 0 {
		return nil, errf(40401, "标的不存在")
	}
	// Use a transaction so the existence check and insert are atomic.
	tx, err := store.DB.Begin()
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	defer func() { _ = tx.Rollback() }()

	var cnt int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM watchlist WHERE asset_id = ?`, assetID).Scan(&cnt); err != nil {
		return nil, errf(50001, err.Error())
	}
	if cnt > 0 {
		return map[string]any{"id": "", "already": true}, nil
	}
	id := cryptox.UUID()
	ts := nowMs()
	if _, err := tx.Exec(`INSERT INTO watchlist(id, asset_id, target_price, note, sort_order, created_at, updated_at)
	    VALUES(?,?,?,?,0,?,?)`, id, assetID, nullFloat(target), nullStr(note), ts, ts); err != nil {
		return nil, errf(50001, err.Error())
	}
	if err := tx.Commit(); err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"id": id}, nil
}

// UpdateWatchlist patches target price / note of an existing entry.
func UpdateWatchlist(id string, target *float64, note string) (map[string]any, error) {
	if store.ScalarInt(`SELECT COUNT(*) FROM watchlist WHERE id = ?`, id) == 0 {
		return nil, errf(40401, "自选项不存在")
	}
	_, err := store.Exec(`UPDATE watchlist SET target_price=?, note=?, updated_at=? WHERE id=?`,
		nullFloat(target), nullStr(note), nowMs(), id)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"id": id}, nil
}

// RemoveFromWatchlist deletes a自选 entry.
func RemoveFromWatchlist(id string) map[string]any {
	_, _ = store.Exec(`DELETE FROM watchlist WHERE id = ?`, id)
	return map[string]any{"id": id}
}

// IsWatched reports whether an asset is already in自选 (for UI toggles).
func IsWatched(assetID string) bool {
	return store.ScalarInt(`SELECT COUNT(*) FROM watchlist WHERE asset_id = ?`, assetID) > 0
}

// nullFloat adapts an optional float pointer for SQL binding.
func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
