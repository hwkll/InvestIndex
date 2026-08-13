// Package settings is a key/value store where sensitive keys are encrypted at rest.
package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"investhub/internal/cryptox"
	"investhub/internal/store"
)

var encrypted = map[string]bool{
	"deepseek_api_key": true,
	"smtp_pass":        true,
	"smtp_user":        true,
	"webhook_url":      true, // P2: at-rest 加密 + List 掩码
}

// Get returns a setting value (decrypted when needed); "" when missing.
func Get(key string) string {
	v, ok := store.ScalarStr(`SELECT value FROM settings WHERE key = ?`, key)
	if !ok {
		return ""
	}
	if encrypted[key] {
		return cryptox.Decrypt(v)
	}
	return v
}

// GetDefault returns Get(key) or def when empty.
func GetDefault(key, def string) string {
	if v := Get(key); v != "" {
		return v
	}
	return def
}

// Set upserts a value, encrypting sensitive keys. It returns an error when
// encryption fails so the caller can abort instead of persisting plaintext
// at rest (security audit F-16).
func Set(key, value string) error {
	stored := value
	if encrypted[key] && value != "" {
		enc, err := cryptox.Encrypt(value)
		if err != nil {
			return fmt.Errorf("加密设置 %s 失败: %w", key, err)
		}
		stored = enc
	}
	_, err := store.Exec(`INSERT INTO settings(key,value,updated_at) VALUES(?,?,?)
	    ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, stored, time.Now().UnixMilli())
	return err
}

// Delete removes a setting.
func Delete(key string) { _, _ = store.Exec(`DELETE FROM settings WHERE key = ?`, key) }

// List returns all settings, masking sensitive values as {has_value:bool}.
func List() map[string]any {
	out := map[string]any{}
	rows, err := store.Query(`SELECT key, value, updated_at FROM settings`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		var ts int64
		if err := rows.Scan(&k, &v, &ts); err != nil {
			continue
		}
		if k == "access_pin_hash" {
			continue // never expose
		}
		if encrypted[k] {
			out[k] = map[string]any{"has_value": v != "", "updated_at": ts}
		} else {
			out[k] = map[string]any{"value": v, "updated_at": ts}
		}
	}
	ensure := func(k, def string) {
		if _, ok := out[k]; !ok {
			out[k] = map[string]any{"value": def}
		}
	}
	ensure("currency", "CNY")
	ensure("deepseek_model", "deepseek-v4-flash")
	ensure("rate_usd_cny", "7.2")
	ensure("data_source_mode", "auto")
	// 刷新频率（秒）：行情轮询与多币种汇率刷新，均可在设置页调整并即时生效。
	ensure("poll_interval", "30")
	ensure("fx_refresh_interval", "1800")
	// AI 市场环境增强：服务端预抓取真实大盘指数 / 行业板块 / 宏观作为分析背景。
	ensure("ai_market_context", "true")
	ensure("ai_market_context_ttl", "900")
	if _, ok := out["deepseek_api_key"]; !ok {
		out["deepseek_api_key"] = map[string]any{"has_value": false}
	}
	if _, ok := out["webhook_url"]; !ok {
		out["webhook_url"] = map[string]any{"has_value": false}
	}
	return out
}

// TestAI pings the DeepSeek endpoint with the given (or stored) credentials.
func TestAI(apiKey, model string) map[string]any {
	if apiKey == "" {
		apiKey = Get("deepseek_api_key")
	}
	if model == "" {
		model = GetDefault("deepseek_model", "deepseek-v4-flash")
	}
	if apiKey == "" {
		return map[string]any{"ok": false, "error": "未配置 API Key"}
	}
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 5,
	})
	req, _ := http.NewRequest("POST", "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return map[string]any{"ok": false, "error": fmt.Sprintf("HTTP %d %s", resp.StatusCode, string(b))}
	}
	return map[string]any{"ok": true, "model": model}
}

// Migrate re-encrypts any plaintext rows for encrypted keys so that secrets at
// rest are always ciphertext. Safe to call on every startup (idempotent).
func Migrate() {
	for k := range encrypted {
		v, ok := store.ScalarStr(`SELECT value FROM settings WHERE key = ?`, k)
		if !ok || v == "" {
			continue
		}
		if !strings.HasPrefix(v, "enc:") {
			if err := Set(k, v); err != nil { // Set encrypts when the key is in the encrypted set
				fmt.Printf("[settings] 启动重加密 %s 失败: %v\n", k, err)
			}
		}
	}
}
