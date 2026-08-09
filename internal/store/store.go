// Package store owns the SQLite connection, schema (PRD §9 DDL) and seed data.
package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go, cgo-free driver => single static binary
)

var (
	DB      *sql.DB
	DataDir string
	DBPath  string
)

const DDL = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id            TEXT PRIMARY KEY,
    expires_at    INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS cash_accounts (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    currency    TEXT NOT NULL DEFAULT 'CNY',
    balance     REAL NOT NULL CHECK(balance >= 0),
    remark      TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS cash_snapshots (
    id            TEXT PRIMARY KEY,
    account_id    TEXT NOT NULL REFERENCES cash_accounts(id) ON DELETE CASCADE,
    snapshot_date TEXT NOT NULL,
    balance       REAL NOT NULL,
    currency      TEXT NOT NULL DEFAULT 'CNY',
    created_at    INTEGER NOT NULL,
    UNIQUE(account_id, snapshot_date)
);

CREATE TABLE IF NOT EXISTS asset_categories (
    id          TEXT PRIMARY KEY,
    code        TEXT UNIQUE NOT NULL,
    name        TEXT NOT NULL,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    icon        TEXT
);

CREATE TABLE IF NOT EXISTS assets (
    id          TEXT PRIMARY KEY,
    category    TEXT NOT NULL REFERENCES asset_categories(code),
    name        TEXT NOT NULL,
    symbol      TEXT NOT NULL,
    sub_type    TEXT,
    currency    TEXT NOT NULL DEFAULT 'CNY',
    provider    TEXT NOT NULL,
    extra       TEXT,
    status      TEXT NOT NULL DEFAULT 'active',
    pinned      INTEGER NOT NULL DEFAULT 0,
    tags        TEXT,
    remark      TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE(category, symbol)
);
CREATE INDEX IF NOT EXISTS idx_assets_category ON assets(category);

CREATE TABLE IF NOT EXISTS transactions (
    id          TEXT PRIMARY KEY,
    asset_id    TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    direction   TEXT NOT NULL CHECK(direction IN ('buy','sell')),
    trade_time  INTEGER NOT NULL,
    quantity    REAL NOT NULL CHECK(quantity > 0),
    price       REAL NOT NULL CHECK(price >= 0),
    fee         REAL NOT NULL DEFAULT 0 CHECK(fee >= 0),
    remark      TEXT,
    source      TEXT NOT NULL DEFAULT 'manual',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tx_asset ON transactions(asset_id, trade_time DESC);
CREATE INDEX IF NOT EXISTS idx_tx_time  ON transactions(trade_time);

CREATE TABLE IF NOT EXISTS position_snapshots (
    id            TEXT PRIMARY KEY,
    asset_id      TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    snapshot_date TEXT NOT NULL,
    quantity      REAL NOT NULL,
    avg_cost      REAL NOT NULL,
    cost_total    REAL NOT NULL,
    last_price    REAL NOT NULL,
    market_value  REAL NOT NULL,
    currency      TEXT NOT NULL DEFAULT 'CNY',
    created_at    INTEGER NOT NULL,
    UNIQUE(asset_id, snapshot_date)
);
CREATE INDEX IF NOT EXISTS idx_snap_date ON position_snapshots(snapshot_date);

CREATE TABLE IF NOT EXISTS price_snapshots (
    id          TEXT PRIMARY KEY,
    asset_id    TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    price       REAL NOT NULL,
    currency    TEXT NOT NULL,
    source_time INTEGER NOT NULL,
    status      TEXT NOT NULL DEFAULT 'ok',
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_price_asset_time ON price_snapshots(asset_id, created_at DESC);

CREATE TABLE IF NOT EXISTS kline_cache (
    id          TEXT PRIMARY KEY,
    asset_id    TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    period      TEXT NOT NULL DEFAULT '1d',
    ts          INTEGER NOT NULL,
    open REAL NOT NULL, high REAL NOT NULL,
    low  REAL NOT NULL, close REAL NOT NULL,
    volume REAL,
    UNIQUE(asset_id, period, ts)
);
CREATE INDEX IF NOT EXISTS idx_kline_asset ON kline_cache(asset_id, period);

CREATE TABLE IF NOT EXISTS alert_rules (
    id           TEXT PRIMARY KEY,
    asset_id     TEXT REFERENCES assets(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    type         TEXT NOT NULL CHECK(type IN ('price','percent','range_break','ai_signal','schedule')),
    direction    TEXT,
    threshold    REAL,
    window_days  INTEGER,
    schedule_cron TEXT,
    channel      TEXT NOT NULL DEFAULT 'web',
    enabled      INTEGER NOT NULL DEFAULT 1,
    valid_from   INTEGER,
    valid_to     INTEGER,
    remark       TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS alert_events (
    id          TEXT PRIMARY KEY,
    rule_id     TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    asset_id    TEXT REFERENCES assets(id) ON DELETE SET NULL,
    trigger_value REAL,
    message     TEXT NOT NULL,
    read        INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alert_events_time ON alert_events(created_at DESC);

CREATE TABLE IF NOT EXISTS ai_analyses (
    id          TEXT PRIMARY KEY,
    scope       TEXT NOT NULL CHECK(scope IN ('global','asset')),
    asset_id    TEXT REFERENCES assets(id) ON DELETE SET NULL,
    model       TEXT NOT NULL,
    status      TEXT NOT NULL CHECK(status IN ('ok','failed')),
    prompt_tokens INTEGER, completion_tokens INTEGER,
    context_snapshot TEXT NOT NULL,
    conclusion  TEXT,
    error_msg   TEXT,
    duration_ms INTEGER,
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ai_time ON ai_analyses(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ai_asset ON ai_analyses(asset_id, created_at DESC);

CREATE TABLE IF NOT EXISTS fx_rates (
    currency   TEXT PRIMARY KEY,
    rate       REAL NOT NULL,        -- 1 单位该币种 = rate 个 CNY
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS watchlist (
    id           TEXT PRIMARY KEY,
    asset_id     TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    target_price REAL,
    note         TEXT,
    sort_order   INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_watch_asset ON watchlist(asset_id);
`

// Open initialises the data dir, connection, pragmas, schema and seed rows.
func Open() error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	DataDir = filepath.Join(wd, "data")
	if err := os.MkdirAll(DataDir, 0o755); err != nil {
		return err
	}
	DBPath = os.Getenv("INVESTHUB_DB")
	if DBPath == "" {
		DBPath = filepath.Join(DataDir, "investhub.db")
	}

	db, err := sql.Open("sqlite", DBPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return err
	}
	// modernc/sqlite is safe for concurrent use but serialising writes avoids
	// SQLITE_BUSY churn for a single-user self-hosted app.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return err
	}
	DB = db

	if _, err := DB.Exec(DDL); err != nil {
		return err
	}
	migrate()
	seed()
	log.Printf("[store] sqlite ready at %s", DBPath)
	return nil
}

// migrate applies lightweight schema migrations to databases created by older
// builds (which predate the price_snapshots.status column).
func migrate() {
	if hasColumn("price_snapshots", "status") {
		return
	}
	if _, err := DB.Exec(`ALTER TABLE price_snapshots ADD COLUMN status TEXT NOT NULL DEFAULT 'ok'`); err != nil {
		log.Printf("[store] migrate price_snapshots.status failed: %v", err)
	} else {
		log.Printf("[store] migrated price_snapshots: added status column")
	}
}

// hasColumn reports whether a table already carries the named column.
func hasColumn(table, col string) bool {
	rows, err := DB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) == nil && name == col {
			return true
		}
	}
	return false
}

func seed() {
	Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('schema_version','1.1')`)
	Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('onboarded','0')`)
	// Flag freshly-seeded demo data only on a brand-new (empty) database, so the
	// UI can show a "this is sample data" banner. Never re-flag an existing DB.
	if ScalarInt(`SELECT COUNT(*) FROM assets`) == 0 {
		Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('demo_data','1')`)
	}
	cats := [][]any{
		{"crypto", "crypto", "加密货币", 1, "₿"},
		{"fund", "fund", "基金", 2, "📈"},
		{"gold", "gold", "黄金", 3, "🪙"},
		{"stock", "stock", "股票", 4, "📊"},
	}
	for _, c := range cats {
		Exec(`INSERT OR IGNORE INTO asset_categories(id,code,name,sort_order,icon) VALUES(?,?,?,?,?)`, c...)
	}
	// Seed FX rates (1 unit currency = rate CNY). USD/CNY defaults to 7.2; HKD to 0.92.
	// Users can edit these in Settings; nothing here is user data.
	ts := time.Now().UnixMilli()
	for _, fr := range [][]any{
		{"CNY", 1.0}, {"USD", 7.2}, {"HKD", 0.92},
	} {
		Exec(`INSERT OR IGNORE INTO fx_rates(currency, rate, updated_at) VALUES(?,?,?)`, fr[0], fr[1], ts)
	}
}

// MarkOnboarded records that the user has taken a real first action (e.g. created
// their first asset) and clears the seeded-demo marker so the UI stops showing
// the "sample data" banner.
func MarkOnboarded() {
	Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('onboarded','1')`)
	_, _ = DB.Exec(`DELETE FROM meta WHERE key='demo_data'`)
}

// ---- thin helpers -------------------------------------------------------

func Exec(q string, args ...any) (sql.Result, error) { return DB.Exec(q, args...) }
func Query(q string, args ...any) (*sql.Rows, error) { return DB.Query(q, args...) }
func QueryRow(q string, args ...any) *sql.Row        { return DB.QueryRow(q, args...) }

// ScalarInt returns a single integer (0 when no row).
func ScalarInt(q string, args ...any) int64 {
	var n sql.NullInt64
	_ = DB.QueryRow(q, args...).Scan(&n)
	return n.Int64
}

// ScalarFloat returns a single float (0 when no row).
func ScalarFloat(q string, args ...any) float64 {
	var n sql.NullFloat64
	_ = DB.QueryRow(q, args...).Scan(&n)
	return n.Float64
}

// ScalarStr returns a single string plus whether a row existed.
func ScalarStr(q string, args ...any) (string, bool) {
	var s sql.NullString
	if err := DB.QueryRow(q, args...).Scan(&s); err != nil {
		return "", false
	}
	return s.String, s.Valid
}
