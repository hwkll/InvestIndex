package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"investhub/internal/core"
	"investhub/internal/store"
)

// validColNameRe matches safe SQL column names (letters, digits, underscore).
var validColNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// exportTables is the full snapshot set (sessions & meta are intentionally excluded).
var exportTables = []string{
	"assets", "transactions", "cash_accounts", "alert_rules", "ai_analyses",
	"settings", "position_snapshots", "cash_snapshots", "kline_cache", "price_snapshots",
}

// importOrder respects foreign-key dependencies.
var importOrder = []string{
	"settings", "cash_accounts", "assets", "transactions", "alert_rules",
	"kline_cache", "price_snapshots", "position_snapshots", "cash_snapshots", "ai_analyses",
}

type backupFile struct {
	Version    string                      `json:"version"`
	ExportedAt int64                       `json:"exportedAt"`
	Tables     map[string][]map[string]any `json:"tables"`
}

func dumpTable(name string, redact bool) []map[string]any {
	out := []map[string]any{}
	rows, err := store.Query(`SELECT * FROM ` + name)
	if err != nil {
		return out
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return out
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		rec := map[string]any{}
		for i, c := range cols {
			if b, isBytes := vals[i].([]byte); isBytes {
				rec[c] = string(b)
			} else {
				rec[c] = vals[i]
			}
		}
		// When redacting (user-facing export) never include secrets that enable
		// offline brute-force or credential theft — the PIN hash and any *_hash /
		// *_secret settings (security audit F-05).
		if redact && name == "settings" {
			if k, _ := rec["key"].(string); isExportProtectedSetting(k) {
				continue
			}
		}
		out = append(out, rec)
	}
	return out
}

// isExportProtectedSetting lists settings keys that must never leave the machine
// in a user-facing backup. access_pin_hash is the scrypt PIN hash (offline
// brute-force risk); any *_hash / *_secret keys are credentials.
func isExportProtectedSetting(key string) bool {
	if key == "access_pin_hash" {
		return true
	}
	return strings.HasSuffix(key, "_hash") || strings.HasSuffix(key, "_secret")
}

func exportBackup(redact bool) backupFile {
	b := backupFile{Version: Version, ExportedAt: time.Now().UnixMilli(), Tables: map[string][]map[string]any{}}
	for _, t := range exportTables {
		b.Tables[t] = dumpTable(t, redact)
	}
	return b
}

func handleExportJSON(w http.ResponseWriter, r *http.Request) {
	// User-facing export redacts secret settings (PIN hash, *_hash/_secret).
	body, err := json.MarshalIndent(exportBackup(true), "", "  ")
	if err != nil {
		fail(w, 50001, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="investhub-backup.json"`)
	_, _ = w.Write(body)
}

// protectedSettingsKeys are never touched by an import. Importing a settings
// table must never downgrade auth (clear the PIN hash) or clobber the live
// credential, so these rows are skipped on insert and preserved on delete.
func isProtectedSetting(key string) bool {
	return key == "access_pin_hash"
}

// writePreImportBackup snapshots the current database to a timestamped JSON file
// so a destructive import can be rolled back. Best-effort: a failure is logged
// but does not abort the import.
func writePreImportBackup() (string, error) {
	ts := time.Now().Format("20060102-150405")
	path := filepath.Join(store.DataDir, "investhub.db.bak-"+ts+".json")
	body, err := json.MarshalIndent(exportBackup(false), "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func handleImportJSON(r *http.Request) (any, error) {
	var b backupFile
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	if len(b.Tables) == 0 {
		return nil, errf(40001, "无效的备份文件")
	}

	// Take an automatic rollback snapshot before mutating anything.
	if bakPath, err := writePreImportBackup(); err == nil {
		log.Printf("[backup] 导入前自动备份已保存: %s", bakPath)
	} else {
		log.Printf("[backup] 导入前自动备份失败（仍继续导入）: %v", err)
	}

	tx, err := store.DB.Begin()
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	defer func() { _ = tx.Rollback() }()

	total := 0
	for _, t := range importOrder {
		rowsIn, has := b.Tables[t]
		if !has {
			continue
		}
		if len(rowsIn) == 0 {
			// An explicitly empty table in the backup means "leave as-is".
			// Never wipe existing data, which previously let a malicious or
			// accidental `{"settings":[]}` silently clear the PIN and config.
			continue
		}
		if t == "settings" {
			// Preserve the live PIN hash; only non-protected settings are wiped.
			if _, err := tx.Exec(`DELETE FROM settings WHERE key != 'access_pin_hash'`); err != nil {
				return nil, errf(50001, t+": "+err.Error())
			}
		} else {
			if _, err := tx.Exec(`DELETE FROM ` + t); err != nil {
				return nil, errf(50001, t+": "+err.Error())
			}
		}
		for _, row := range rowsIn {
			if t == "settings" {
				if k, _ := row["key"].(string); isProtectedSetting(k) {
					continue // never import a PIN hash
				}
			}
			cols := make([]string, 0, len(row))
			args := make([]any, 0, len(row))
			for k, v := range row {
				if !validColNameRe.MatchString(k) {
					return nil, errf(40001, "备份文件包含非法列名: "+k)
				}
				cols = append(cols, k)
				args = append(args, normalizeJSONValue(v))
			}
			if len(cols) == 0 {
				continue
			}
			ph := strings.TrimRight(strings.Repeat("?,", len(cols)), ",")
			q := fmt.Sprintf(`INSERT OR REPLACE INTO %s(%s) VALUES(%s)`, t, strings.Join(cols, ","), ph)
			if _, err := tx.Exec(q, args...); err != nil {
				return nil, errf(50001, t+": "+err.Error())
			}
			total++
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"imported": true, "rows": total}, nil
}

// normalizeJSONValue converts float64 that are really integers back to int64 so
// SQLite columns keep their original affinity.
func normalizeJSONValue(v any) any {
	f, isFloat := v.(float64)
	if !isFloat {
		return v
	}
	if f == float64(int64(f)) {
		return int64(f)
	}
	return f
}

// ---------------- CSV ----------------

func handleExportCSV(w http.ResponseWriter, r *http.Request) {
	scope := pick(qstr(r, "scope"), "transactions")
	var sb strings.Builder
	sb.WriteString("\ufeff") // BOM so Excel picks up UTF-8
	cw := csv.NewWriter(&sb)

	if scope == "positions" {
		_ = cw.Write([]string{"分类", "名称", "代码", "数量", "持仓均价", "现价", "市值", "浮动盈亏", "浮动收益率", "已实现盈亏"})
		for _, c := range core.Categories {
			for _, it := range core.PositionsByCategory(c).Items {
				if it.Qty <= 0 {
					continue
				}
				pct := "--"
				if it.FloatingPct != nil {
					pct = strconv.FormatFloat(*it.FloatingPct*100, 'f', 2, 64) + "%"
				}
				_ = cw.Write([]string{c, it.Name, it.Symbol,
					num(it.Qty), num(it.AvgCost), num(it.Price), num(it.MarketValue), num(it.FloatingPnl), pct, num(it.RealizedPnl)})
			}
		}
	} else {
		_ = cw.Write([]string{"日期", "标的", "代码", "方向", "数量", "单价", "手续费", "备注", "来源"})
		for _, t := range core.ListTransactions(core.TxQuery{Page: 1, Size: 100000}).Items {
			_ = cw.Write([]string{
				time.UnixMilli(t.TradeTime).Format("2006-01-02"), t.AssetName, t.AssetSymbol, t.Direction,
				num(t.Quantity), num(t.Price), num(t.Fee), t.Remark, t.Source,
			})
		}
	}
	cw.Flush()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="investhub-export.csv"`)
	_, _ = w.Write([]byte(sb.String()))
}

func handleImportCSV(r *http.Request) (any, error) {
	var body struct {
		CSV  string `json:"csv"`
		Text string `json:"text"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	text := body.CSV
	if text == "" {
		text = body.Text
	}
	text = strings.TrimPrefix(text, "\ufeff")
	if strings.TrimSpace(text) == "" {
		return nil, errf(40001, "CSV 内容为空")
	}
	rd := csv.NewReader(strings.NewReader(text))
	rd.FieldsPerRecord = -1
	records, err := rd.ReadAll()
	if err != nil {
		return nil, errf(40001, "CSV 解析失败: "+err.Error())
	}
	if len(records) < 2 {
		return nil, errf(40001, "CSV 内容为空")
	}
	header := records[0]
	idx := func(names ...string) int {
		for _, n := range names {
			for i, hcol := range header {
				if strings.TrimSpace(hcol) == n {
					return i
				}
			}
		}
		return -1
	}
	at := func(cols []string, i int) string {
		if i < 0 || i >= len(cols) {
			return ""
		}
		return strings.TrimSpace(cols[i])
	}
	iSym := idx("代码", "symbol")
	iName := idx("标的", "名称", "name")
	iDir := idx("方向", "direction")
	iQty := idx("数量", "quantity")
	iPrice := idx("单价", "price")
	iFee := idx("手续费", "fee")
	iDate := idx("日期", "date")

	count, skipped := 0, 0
	for _, cols := range records[1:] {
		sym := at(cols, iSym)
		name := at(cols, iName)
		if name == "" {
			name = sym
		}
		dir := strings.ToLower(pick(at(cols, iDir), "buy"))
		if dir == "买入" {
			dir = "buy"
		} else if dir == "卖出" {
			dir = "sell"
		}
		qty, e1 := strconv.ParseFloat(at(cols, iQty), 64)
		price, e2 := strconv.ParseFloat(at(cols, iPrice), 64)
		if sym == "" && name == "" || e1 != nil || e2 != nil || qty == 0 {
			skipped++
			continue
		}
		fee, _ := strconv.ParseFloat(pick(at(cols, iFee), "0"), 64)

		assetID, found := store.ScalarStr(`SELECT id FROM assets WHERE symbol = ?`, sym)
		if !found {
			assetID, found = store.ScalarStr(`SELECT id FROM assets WHERE name = ?`, name)
		}
		if !found {
			skipped++
			continue
		}
		tradeTime := time.Now().UnixMilli()
		if d := at(cols, iDate); d != "" {
			for _, layout := range []string{"2006-01-02", "2006/01/02", time.RFC3339, "2006-01-02 15:04:05"} {
				if tv, e := time.ParseInLocation(layout, d, time.Local); e == nil {
					tradeTime = tv.UnixMilli()
					break
				}
			}
		}
		if _, err := core.CreateTransaction(core.TxInput{
			AssetID: assetID, Direction: dir, TradeTime: tradeTime,
			Quantity: &qty, Price: &price, Fee: &fee, Source: "csv",
		}); err != nil {
			skipped++
			continue
		}
		count++
	}
	return map[string]any{"imported": count, "skipped": skipped}, nil
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// toStr renders a JSON scalar as the string form stored in settings.
func toStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
