// Package alerts implements the alert rule CRUD and the evaluation engine that
// matches live quotes / AI signals against user-defined rules.
package alerts

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"investhub/internal/cryptox"
	"investhub/internal/quotes"
	"investhub/internal/settings"
	"investhub/internal/store"
)

// APIError carries a business error code back to the HTTP layer.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return e.Msg }

func errf(code int, msg string) error { return &APIError{Code: code, Msg: msg} }

func nowMs() int64 { return time.Now().UnixMilli() }

// debounceMs suppresses repeated firing of the same rule.
const debounceMs = 5 * 60 * 1000

// Rule is the API representation of an alert rule.
type Rule struct {
	ID           string   `json:"id"`
	AssetID      string   `json:"assetId,omitempty"`
	AssetName    string   `json:"assetName,omitempty"`
	AssetSymbol  string   `json:"assetSymbol,omitempty"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Direction    string   `json:"direction,omitempty"`
	Threshold    *float64 `json:"threshold"`
	WindowDays   *int     `json:"windowDays"`
	ScheduleCron string   `json:"scheduleCron,omitempty"`
	Channel      string   `json:"channel"`
	Enabled      bool     `json:"enabled"`
	ValidFrom    string   `json:"validFrom,omitempty"`
	ValidTo      string   `json:"validTo,omitempty"`
	Remark       string   `json:"remark,omitempty"`
	CreatedAt    int64    `json:"createdAt"`
	UpdatedAt    int64    `json:"updatedAt"`
}

// Input is the create/update payload.
type Input struct {
	AssetID      string   `json:"assetId"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Direction    string   `json:"direction"`
	Threshold    *float64 `json:"threshold"`
	WindowDays   *int     `json:"windowDays"`
	ScheduleCron string   `json:"scheduleCron"`
	Channel      string   `json:"channel"`
	Enabled      *bool    `json:"enabled"`
	ValidFrom    string   `json:"validFrom"`
	ValidTo      string   `json:"validTo"`
	Remark       string   `json:"remark"`
}

// Event is a fired alert notification.
type Event struct {
	EventID      string   `json:"eventId"`
	RuleID       string   `json:"ruleId"`
	AssetID      string   `json:"assetId,omitempty"`
	Message      string   `json:"message"`
	TriggerValue *float64 `json:"triggerValue"`
	Read         bool     `json:"read"`
	CreatedAt    int64    `json:"createdAt"`
}

// ruleRow mirrors the alert_rules table.
type ruleRow struct {
	ID           string
	AssetID      sql.NullString
	Name         string
	Type         string
	Direction    sql.NullString
	Threshold    sql.NullFloat64
	WindowDays   sql.NullInt64
	ScheduleCron sql.NullString
	Channel      sql.NullString
	Enabled      int
	ValidFrom    sql.NullString
	ValidTo      sql.NullString
	Remark       sql.NullString
	CreatedAt    int64
	UpdatedAt    int64
}

const ruleCols = `id, asset_id, name, type, direction, threshold, window_days, schedule_cron, channel, enabled, valid_from, valid_to, remark, created_at, updated_at`

func scanRule(s interface{ Scan(...any) error }) (*ruleRow, error) {
	r := &ruleRow{}
	err := s.Scan(&r.ID, &r.AssetID, &r.Name, &r.Type, &r.Direction, &r.Threshold, &r.WindowDays,
		&r.ScheduleCron, &r.Channel, &r.Enabled, &r.ValidFrom, &r.ValidTo, &r.Remark, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *ruleRow) toAPI(assetName, assetSymbol string) Rule {
	out := Rule{
		ID: r.ID, AssetID: r.AssetID.String, AssetName: assetName, AssetSymbol: assetSymbol,
		Name: r.Name, Type: r.Type, Direction: r.Direction.String,
		ScheduleCron: r.ScheduleCron.String, Channel: r.Channel.String, Enabled: r.Enabled == 1,
		ValidFrom: r.ValidFrom.String, ValidTo: r.ValidTo.String, Remark: r.Remark.String,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if out.Channel == "" {
		out.Channel = "web"
	}
	if r.Threshold.Valid {
		v := r.Threshold.Float64
		out.Threshold = &v
	}
	if r.WindowDays.Valid {
		v := int(r.WindowDays.Int64)
		out.WindowDays = &v
	}
	return out
}

// CreateRule inserts a new alert rule.
func CreateRule(in Input) (map[string]any, error) {
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Type) == "" {
		return nil, errf(40001, "名称与类型为必填")
	}
	if in.Type != "schedule" && in.AssetID == "" {
		return nil, errf(40001, "非定时提醒需指定标的")
	}
	id := cryptox.UUID()
	ts := nowMs()
	channel := in.Channel
	if channel == "" {
		channel = "web"
	}
	_, err := store.Exec(`INSERT INTO alert_rules(id, asset_id, name, type, direction, threshold, window_days, schedule_cron, channel, enabled, valid_from, valid_to, remark, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,1,?,?,?,?,?)`,
		id, nullStr(in.AssetID), in.Name, in.Type, nullStr(in.Direction), nullFloat(in.Threshold), nullInt(in.WindowDays),
		nullStr(in.ScheduleCron), channel, nullStr(in.ValidFrom), nullStr(in.ValidTo), nullStr(in.Remark), ts, ts)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"id": id}, nil
}

// ListRules returns all rules joined with their asset display names.
func ListRules() []Rule {
	rows, err := store.Query(`SELECT r.` + strings.ReplaceAll(ruleCols, ", ", ", r.") + `, a.name, a.symbol
		FROM alert_rules r LEFT JOIN assets a ON a.id = r.asset_id ORDER BY r.created_at DESC`)
	out := []Rule{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		r := &ruleRow{}
		var an, as sql.NullString
		if err := rows.Scan(&r.ID, &r.AssetID, &r.Name, &r.Type, &r.Direction, &r.Threshold, &r.WindowDays,
			&r.ScheduleCron, &r.Channel, &r.Enabled, &r.ValidFrom, &r.ValidTo, &r.Remark, &r.CreatedAt, &r.UpdatedAt,
			&an, &as); err != nil {
			continue
		}
		out = append(out, r.toAPI(an.String, as.String))
	}
	return out
}

func getRule(id string) *ruleRow {
	r, err := scanRule(store.QueryRow(`SELECT `+ruleCols+` FROM alert_rules WHERE id = ?`, id))
	if err != nil {
		return nil
	}
	return r
}

// UpdateRule patches an existing rule; unspecified fields keep their value.
func UpdateRule(id string, in Input) (map[string]any, error) {
	r := getRule(id)
	if r == nil {
		return nil, errf(40401, "规则不存在")
	}
	name := r.Name
	if in.Name != "" {
		name = in.Name
	}
	direction := r.Direction
	if in.Direction != "" {
		direction = sql.NullString{String: in.Direction, Valid: true}
	}
	threshold := r.Threshold
	if in.Threshold != nil {
		threshold = sql.NullFloat64{Float64: *in.Threshold, Valid: true}
	}
	windowDays := r.WindowDays
	if in.WindowDays != nil {
		windowDays = sql.NullInt64{Int64: int64(*in.WindowDays), Valid: true}
	}
	channel := r.Channel
	if in.Channel != "" {
		channel = sql.NullString{String: in.Channel, Valid: true}
	}
	enabled := r.Enabled
	if in.Enabled != nil {
		if *in.Enabled {
			enabled = 1
		} else {
			enabled = 0
		}
	}
	remark := r.Remark
	if in.Remark != "" {
		remark = sql.NullString{String: in.Remark, Valid: true}
	}
	_, err := store.Exec(`UPDATE alert_rules SET name=?, direction=?, threshold=?, window_days=?, channel=?, enabled=?, remark=?, updated_at=? WHERE id=?`,
		name, direction, threshold, windowDays, channel, enabled, remark, nowMs(), id)
	if err != nil {
		return nil, errf(50001, err.Error())
	}
	return map[string]any{"id": id}, nil
}

// DeleteRule removes a rule.
func DeleteRule(id string) map[string]any {
	_, _ = store.Exec(`DELETE FROM alert_rules WHERE id = ?`, id)
	return map[string]any{"id": id}
}

func lastTrigger(ruleID string) int64 {
	var t sql.NullInt64
	_ = store.QueryRow(`SELECT MAX(created_at) FROM alert_events WHERE rule_id = ?`, ruleID).Scan(&t)
	return t.Int64
}

// EvaluateAll walks every enabled rule and returns newly fired events.
func EvaluateAll() []Event {
	rows, err := store.Query(`SELECT ` + ruleCols + ` FROM alert_rules WHERE enabled = 1`)
	fired := []Event{}
	if err != nil {
		return fired
	}
	rules := []*ruleRow{}
	for rows.Next() {
		if r, e := scanRule(rows); e == nil {
			rules = append(rules, r)
		}
	}
	rows.Close()

	for _, r := range rules {
		if r.Type == "schedule" {
			if e := evalSchedule(r); e != nil {
				fired = append(fired, *e)
			}
			continue
		}
		aName, ok := store.ScalarStr(`SELECT name FROM assets WHERE id = ?`, r.AssetID.String)
		if !ok {
			continue
		}
		q := quotes.Get(r.AssetID.String)
		if q == nil {
			continue
		}
		hit := false
		value := q.Price
		msg := ""
		th := r.Threshold.Float64
		switch r.Type {
		case "price":
			if r.Direction.String == "up" && q.Price >= th {
				hit, msg = true, fmt.Sprintf("%s 现价 %s 达到目标价 %s", aName, fmtNum(q.Price), fmtNum(th))
			} else if r.Direction.String == "down" && q.Price <= th {
				hit, msg = true, fmt.Sprintf("%s 现价 %s 跌破目标价 %s", aName, fmtNum(q.Price), fmtNum(th))
			}
			value = q.Price
		case "percent":
			t := math.Abs(th)
			if r.Direction.String == "up" && q.ChgPct >= t {
				hit, msg = true, fmt.Sprintf("%s 当日涨幅 %.2f%% 达到阈值 %g%%", aName, q.ChgPct, t)
			} else if r.Direction.String == "down" && q.ChgPct <= -t {
				hit, msg = true, fmt.Sprintf("%s 当日跌幅 %.2f%% 达到阈值 %g%%", aName, q.ChgPct, t)
			}
			value = q.ChgPct
		case "range_break":
			win := 90
			if r.WindowDays.Valid && r.WindowDays.Int64 > 0 {
				win = int(r.WindowDays.Int64)
			}
			kl := quotes.Kline(r.AssetID.String, "1d", win+1)
			if len(kl) < 2 {
				continue
			}
			closes := kl[:len(kl)-1]
			hi, lo := closes[0].Close, closes[0].Close
			for _, c := range closes {
				if c.Close > hi {
					hi = c.Close
				}
				if c.Close < lo {
					lo = c.Close
				}
			}
			if r.Direction.String != "down" && q.Price > hi {
				hit, msg = true, fmt.Sprintf("%s 创新高 %s（近%d日高 %s）", aName, fmtNum(q.Price), win, fmtNum(hi))
			} else if r.Direction.String != "up" && q.Price < lo {
				hit, msg = true, fmt.Sprintf("%s 创新低 %s（近%d日低 %s）", aName, fmtNum(q.Price), win, fmtNum(lo))
			}
			value = q.Price
		}
		if hit {
			if e := fire(r, r.AssetID.String, &value, msg); e != nil {
				fired = append(fired, *e)
			}
		}
	}
	return fired
}

// evalSchedule handles "HH:MM" daily reminders (default 09:00).
func evalSchedule(r *ruleRow) *Event {
	hh, mm := 9, 0
	if s := r.ScheduleCron.String; s != "" {
		parts := strings.SplitN(s, ":", 2)
		if len(parts) == 2 {
			if a, e1 := strconv.Atoi(strings.TrimSpace(parts[0])); e1 == nil {
				if b, e2 := strconv.Atoi(strings.TrimSpace(parts[1])); e2 == nil {
					hh, mm = a, b
				}
			}
		}
	}
	d := time.Now()
	if d.Hour() != hh || d.Minute() != mm {
		return nil
	}
	if lastTrigger(r.ID) > nowMs()-24*3600*1000 {
		return nil // already fired today
	}
	return fire(r, r.AssetID.String, nil, "定时提醒："+r.Name)
}

// fire persists an alert event honouring the debounce window.
func fire(r *ruleRow, assetID string, value *float64, msg string) *Event {
	if nowMs()-lastTrigger(r.ID) < debounceMs {
		return nil
	}
	id := cryptox.UUID()
	ts := nowMs()
	_, err := store.Exec(`INSERT INTO alert_events(id, rule_id, asset_id, trigger_value, message, read, created_at) VALUES(?,?,?,?,?,0,?)`,
		id, r.ID, nullStr(assetID), nullFloat(value), msg, ts)
	if err != nil {
		return nil
	}
	if strings.Contains(r.Channel.String, "mail") {
		to := settings.Get("smtp_to")
		if to == "" {
			to = settings.Get("smtp_user")
		}
		if to == "" {
			fmt.Printf("[mail] no recipient configured (set smtp_to / smtp_user)\n")
		} else if err := sendMailReal(to, "InvestHub 提醒", msg); err != nil {
			fmt.Printf("[mail] send failed: %v\n", err)
		}
	}
	if strings.Contains(r.Channel.String, "webhook") {
		if err := fireWebhook(msg); err != nil {
			fmt.Printf("[webhook] send failed: %v\n", err)
		}
	}
	return &Event{EventID: id, RuleID: r.ID, AssetID: assetID, Message: msg, TriggerValue: value, CreatedAt: ts}
}

// CheckAISignal fires ai_signal rules bound to the asset when the model says buy/sell.
func CheckAISignal(assetID, signal string) []Event {
	fired := []Event{}
	if signal != "buy" && signal != "sell" {
		return fired
	}
	rows, err := store.Query(`SELECT `+ruleCols+` FROM alert_rules WHERE enabled = 1 AND type = 'ai_signal' AND asset_id = ?`, assetID)
	if err != nil {
		return fired
	}
	rules := []*ruleRow{}
	for rows.Next() {
		if r, e := scanRule(rows); e == nil {
			rules = append(rules, r)
		}
	}
	rows.Close()

	name, _ := store.ScalarStr(`SELECT name FROM assets WHERE id = ?`, assetID)
	if name == "" {
		name = "标的"
	}
	verb := "买入"
	if signal == "sell" {
		verb = "卖出"
	}
	for _, r := range rules {
		if e := fire(r, assetID, nil, fmt.Sprintf("%s AI 给出 %s 信号", name, verb)); e != nil {
			fired = append(fired, *e)
		}
	}
	return fired
}

// ListEvents returns the latest 50 events, optionally filtered by read state.
func ListEvents(read *bool) []Event {
	q := `SELECT id, rule_id, asset_id, trigger_value, message, read, created_at FROM alert_events`
	args := []any{}
	if read != nil {
		q += ` WHERE read = ?`
		if *read {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	q += ` ORDER BY created_at DESC LIMIT 50`
	out := []Event{}
	rows, err := store.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		var assetID sql.NullString
		var tv sql.NullFloat64
		var rd int
		if err := rows.Scan(&e.EventID, &e.RuleID, &assetID, &tv, &e.Message, &rd, &e.CreatedAt); err != nil {
			continue
		}
		e.AssetID = assetID.String
		if tv.Valid {
			v := tv.Float64
			e.TriggerValue = &v
		}
		e.Read = rd == 1
		out = append(out, e)
	}
	return out
}

// MarkRead flags one event as read.
func MarkRead(id string) map[string]any {
	_, _ = store.Exec(`UPDATE alert_events SET read = 1 WHERE id = ?`, id)
	return map[string]any{"id": id}
}

// UnreadCount returns the badge number.
func UnreadCount() int64 {
	return store.ScalarInt(`SELECT COUNT(*) FROM alert_events WHERE read = 0`)
}

// sendMailReal delivers body to the recipient via the configured SMTP server.
// It supports implicit TLS (port 465), STARTTLS (587/25) and plaintext, and
// authenticates with SMTP PLAIN when credentials are present.
func sendMailReal(to, subject, body string) error {
	host := settings.Get("smtp_host")
	if host == "" {
		return fmt.Errorf("未配置 SMTP 服务器（smtp_host）")
	}
	port := settings.GetDefault("smtp_port", "587")
	user := settings.Get("smtp_user")
	pass := settings.Get("smtp_pass")
	from := settings.GetDefault("smtp_from", user)
	if from == "" {
		from = "investhub@localhost"
	}
	implicit := settings.GetDefault("smtp_tls", "0") == "1"
	addr := net.JoinHostPort(host, port)

	var c *smtp.Client
	var err error
	if implicit {
		conn, e := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if e != nil {
			return e
		}
		c, err = smtp.NewClient(conn, host)
	} else {
		c, err = smtp.Dial(addr)
	}
	if err != nil {
		return err
	}
	defer c.Quit()

	if ok, _ := c.Extension("STARTTLS"); ok && !implicit {
		if e := c.StartTLS(&tls.Config{ServerName: host}); e != nil {
			return e
		}
	}
	if user != "" {
		if e := c.Auth(smtp.PlainAuth("", user, pass, host)); e != nil {
			return e
		}
	}
	if e := c.Mail(from); e != nil {
		return e
	}
	if e := c.Rcpt(to); e != nil {
		return e
	}
	w, e := c.Data()
	if e != nil {
		return e
	}
	if _, e = w.Write([]byte(buildMail(from, to, subject, body))); e != nil {
		return e
	}
	return w.Close()
}

// fireWebhook POSTs the alert message as JSON to the configured webhook URL.
func fireWebhook(message string) error {
	url := settings.Get("webhook_url")
	if url == "" {
		return fmt.Errorf("未配置 webhook 地址（webhook_url）")
	}
	payload, err := json.Marshal(map[string]any{
		"source":  "InvestHub",
		"message": message,
		"time":    time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook 返回 HTTP %d", resp.StatusCode)
	}
	return nil
}

var webhookClient = &http.Client{Timeout: 10 * time.Second}

// TestWebhook sends a sample message to verify the configured webhook_url.
func TestWebhook() map[string]any {
	if err := fireWebhook("InvestHub 测试消息：如果你的 Webhook 接收端收到这条消息，说明配置成功。"); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func buildMail(from, to, subject, body string) string {
	return "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body + "\r\n"
}

// SendTestMail sends a probe email using the current SMTP settings.
func SendTestMail() map[string]any {
	to := settings.Get("smtp_to")
	if to == "" {
		to = settings.Get("smtp_user")
	}
	if to == "" {
		return map[string]any{"ok": false, "error": "未配置收件人（smtp_to 或 smtp_user）"}
	}
	if settings.Get("smtp_host") == "" {
		return map[string]any{"ok": false, "error": "未配置 SMTP 服务器（smtp_host）"}
	}
	if err := sendMailReal(to, "InvestHub 测试邮件", "这是一封来自 InvestHub 的测试邮件，若你收到说明 SMTP 配置正确。"); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func fmtNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*10000)/10000, 'f', -1, 64)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
