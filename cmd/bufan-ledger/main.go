package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

//go:embed migrations/001_init.sql
var migrationSQL string

const appName = "不凡收款管理端"

type Config struct {
	Addr                string
	DSN                 string
	TablePrefix         string
	PublicBaseURL       string
	EPayBaseURL         string
	EPayInternalSecret  string
	AdminToken          string
	AutoMigrate         bool
	AutoRegisterDevice  bool
	DefaultDeviceSecret string
	ADBPath             string
	AccountPickStrategy string
}

type App struct {
	cfg Config
	db  *sql.DB
}

type Device struct {
	ID                     int64
	DeviceNo               string
	DeviceName             sql.NullString
	Secret                 string
	Status                 int
	AppVersion             sql.NullString
	LastHeartbeatAt        sql.NullTime
	LastIP                 sql.NullString
	BatteryLevel           sql.NullInt64
	NotificationPermission int
}

type ADBDevice struct {
	Serial  string
	State   string
	Details map[string]string
}

type Account struct {
	ID                int64
	Channel           string
	AccountAlias      string
	AccountIdentifier sql.NullString
	DeviceNo          string
	QRCodeURL         sql.NullString
	Status            int
}

type CollectSession struct {
	ID             int64
	SessionNo      string
	EPayTradeNo    string
	EPayOutTradeNo string
	UID            int64
	Channel        string
	AccountID      sql.NullInt64
	Amount         float64
	Status         string
	ExpireAt       time.Time
	PaidAt         sql.NullTime
	CreatedAt      time.Time
	AccountAlias   sql.NullString
	AccountQRCode  sql.NullString
	NotificationID sql.NullInt64
}

type NotificationEvent struct {
	ID             int64
	EventID        string
	DeviceNo       string
	AccountID      sql.NullInt64
	Channel        string
	PackageName    sql.NullString
	RawTitle       sql.NullString
	RawText        sql.NullString
	ParsedAmount   sql.NullFloat64
	ParsedPayer    sql.NullString
	NotifyTime     sql.NullTime
	ReceivedAt     time.Time
	MatchStatus    string
	MatchedTradeNo sql.NullString
}

type SummaryPoint struct {
	Label    string  `json:"label"`
	SubLabel string  `json:"sub"`
	Amount   float64 `json:"amount"`
	Count    int64   `json:"count"`
	Avg      float64 `json:"avg"`
	Percent  int     `json:"percent"`
}

type heartbeatReq struct {
	DeviceNo               string `json:"device_no"`
	DeviceName             string `json:"device_name"`
	AppVersion             string `json:"app_version"`
	BatteryLevel           int    `json:"battery_level"`
	NotificationPermission bool   `json:"notification_permission"`
}

type notificationReq struct {
	EventID     string `json:"event_id"`
	DeviceNo    string `json:"device_no"`
	PackageName string `json:"package_name"`
	Channel     string `json:"channel"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	NotifyTime  int64  `json:"notify_time"`
	LocalTime   int64  `json:"local_time"`
	From        string `json:"from"`
	Content     string `json:"content"`
	Type        string `json:"type"`
	Time        int64  `json:"time"`
}

type createSessionReq struct {
	TradeNo    string  `json:"trade_no"`
	OutTradeNo string  `json:"out_trade_no"`
	UID        int64   `json:"uid"`
	Channel    string  `json:"channel"`
	Amount     float64 `json:"amount"`
	ExpireAt   string  `json:"expire_at"`
	ReturnURL  string  `json:"return_url"`
	NotifyURL  string  `json:"notify_url"`
}

type createSessionResp struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	SessionNo string `json:"session_no,omitempty"`
	PayURL    string `json:"pay_url,omitempty"`
	AccountID int64  `json:"account_id,omitempty"`
}

type paidCallbackReq struct {
	TradeNo   string  `json:"trade_no"`
	EventID   string  `json:"event_id"`
	Channel   string  `json:"channel"`
	Amount    float64 `json:"amount"`
	PaidAt    string  `json:"paid_at"`
	BuyerHint string  `json:"buyer_hint"`
}

type internalStatsReq struct {
	UID    int64 `json:"uid"`
	Days   int   `json:"days"`
	Months int   `json:"months"`
	Limit  int   `json:"limit"`
}

type internalLimitReq struct {
	Limit int `json:"limit"`
}

type internalSessionsQueryReq struct {
	TradeNo    string `json:"trade_no"`
	OutTradeNo string `json:"out_trade_no"`
	SessionNo  string `json:"session_no"`
	UID        int64  `json:"uid"`
	Status     string `json:"status"`
	Limit      int    `json:"limit"`
}

type internalCancelSessionReq struct {
	TradeNo   string `json:"trade_no"`
	SessionNo string `json:"session_no"`
	Reason    string `json:"reason"`
}

type internalEventsRecentReq struct {
	UID         int64  `json:"uid"`
	MatchStatus string `json:"match_status"`
	Limit       int    `json:"limit"`
}

type internalSessionItem struct {
	ID             int64   `json:"id"`
	SessionNo      string  `json:"session_no"`
	EPayTradeNo    string  `json:"epay_trade_no"`
	EPayOutTradeNo string  `json:"epay_out_trade_no"`
	UID            int64   `json:"uid"`
	Channel        string  `json:"channel"`
	AccountID      int64   `json:"account_id,omitempty"`
	AccountAlias   string  `json:"account_alias"`
	AccountQRCode  string  `json:"account_qrcode"`
	Amount         float64 `json:"amount"`
	Status         string  `json:"status"`
	ExpireAt       string  `json:"expire_at"`
	PaidAt         string  `json:"paid_at"`
	CreatedAt      string  `json:"created_at"`
	NotificationID int64   `json:"notification_event_id,omitempty"`
}

type internalEventItem struct {
	ID             int64   `json:"id"`
	EventID        string  `json:"event_id"`
	DeviceNo       string  `json:"device_no"`
	AccountID      int64   `json:"account_id,omitempty"`
	Channel        string  `json:"channel"`
	PackageName    string  `json:"package_name"`
	RawTitle       string  `json:"raw_title"`
	RawText        string  `json:"raw_text"`
	ParsedAmount   float64 `json:"parsed_amount"`
	ParsedPayer    string  `json:"parsed_payer"`
	NotifyTime     string  `json:"notify_time"`
	ReceivedAt     string  `json:"received_at"`
	MatchStatus    string  `json:"match_status"`
	MatchedTradeNo string  `json:"matched_trade_no"`
}

type internalAccountItem struct {
	ID                  int64   `json:"id"`
	Channel             string  `json:"channel"`
	AccountAlias        string  `json:"account_alias"`
	AccountIdentifier   string  `json:"account_identifier"`
	DeviceNo            string  `json:"device_no"`
	QRCodeURL           string  `json:"qrcode_url"`
	Status              int     `json:"status"`
	DailyLimitAmount    float64 `json:"daily_limit_amount"`
	DailyReceivedAmount float64 `json:"daily_received_amount"`
	LastAssignedAt      string  `json:"last_assigned_at"`
	LastNotifyAt        string  `json:"last_notify_at"`
}

type internalAccountChannelSummary struct {
	Channel       string  `json:"channel"`
	Total         int64   `json:"total"`
	Active        int64   `json:"active"`
	DailyReceived float64 `json:"daily_received_amount"`
}

func main() {
	loadDotEnv(".env")
	cfg := loadConfig()
	if cfg.DSN == "" {
		log.Fatal("NL_DSN 未配置")
	}
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}
	app := &App{cfg: cfg, db: db}
	if cfg.AutoMigrate {
		if err := app.migrate(ctx); err != nil {
			log.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", app.healthz)
	mux.HandleFunc("GET /admin", app.admin)
	mux.HandleFunc("POST /admin/devices", app.adminCreateDevice)
	mux.HandleFunc("POST /admin/devices/scan-adb", app.adminScanADBDevices)
	mux.HandleFunc("POST /admin/devices/status", app.adminSetDeviceStatus)
	mux.HandleFunc("POST /admin/accounts", app.adminCreateAccount)
	mux.HandleFunc("POST /admin/accounts/status", app.adminSetAccountStatus)
	mux.HandleFunc("POST /admin/settings/pick-strategy", app.adminUpdatePickStrategy)
	mux.HandleFunc("POST /admin/events/manual-match", app.adminManualMatchEvent)
	mux.HandleFunc("POST /admin/events/ignore", app.adminIgnoreEvent)
	mux.HandleFunc("POST /admin/sessions/manual-paid", app.adminManualPaidSession)
	mux.HandleFunc("GET /pay/", app.payPage)
	mux.HandleFunc("POST /api/device/heartbeat", app.deviceHeartbeat)
	mux.HandleFunc("POST /api/device/notifications", app.deviceNotification)
	mux.HandleFunc("POST /internal/collect-sessions", app.createCollectSession)
	mux.HandleFunc("POST /internal/status", app.internalStatus)
	mux.HandleFunc("POST /internal/stats", app.internalStats)
	mux.HandleFunc("POST /internal/accounts/summary", app.internalAccountsSummary)
	mux.HandleFunc("POST /internal/sessions/query", app.internalSessionsQuery)
	mux.HandleFunc("POST /internal/sessions/cancel", app.internalCancelSession)
	mux.HandleFunc("POST /internal/events/recent", app.internalEventsRecent)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusFound)
	})
	s := &http.Server{Addr: cfg.Addr, Handler: logMiddleware(mux), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("%s listening on %s", appName, cfg.Addr)
	log.Fatal(s.ListenAndServe())
}

func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		if len(value) >= 2 {
			if (value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"') {
				value = value[1 : len(value)-1]
			}
		}
		_ = os.Setenv(key, value)
	}
}

func loadConfig() Config {
	return Config{
		Addr:                env("NL_ADDR", ":8098"),
		DSN:                 env("NL_DSN", ""),
		TablePrefix:         sanitizePrefix(env("NL_TABLE_PREFIX", "pre")),
		PublicBaseURL:       strings.TrimRight(env("NL_PUBLIC_BASE_URL", "http://127.0.0.1:8098"), "/"),
		EPayBaseURL:         strings.TrimRight(env("NL_EPAY_BASE_URL", "http://127.0.0.1"), "/"),
		EPayInternalSecret:  env("NL_EPAY_INTERNAL_SECRET", ""),
		AdminToken:          env("NL_ADMIN_TOKEN", ""),
		AutoMigrate:         envBool("NL_AUTO_MIGRATE", true),
		AutoRegisterDevice:  envBool("NL_AUTO_REGISTER_DEVICE", false),
		DefaultDeviceSecret: env("NL_DEFAULT_DEVICE_SECRET", ""),
		ADBPath:             env("NL_ADB_PATH", "adb"),
		AccountPickStrategy: sanitizePickStrategy(env("NL_ACCOUNT_PICK_STRATEGY", "least_amount")),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func envBool(k string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
func sanitizePrefix(s string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	s = re.ReplaceAllString(s, "")
	if s == "" {
		return "pre"
	}
	return s
}
func sanitizePickStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "random":
		return "random"
	case "round_robin", "roundrobin", "rotate":
		return "round_robin"
	case "least_orders", "order_lowest", "orders":
		return "least_orders"
	case "least_amount", "amount_lowest", "balance", "":
		return "least_amount"
	default:
		return "least_amount"
	}
}
func pickStrategyLabel(s string) string {
	switch sanitizePickStrategy(s) {
	case "random":
		return "随机分配"
	case "round_robin":
		return "轮询分配"
	case "least_orders":
		return "订单数最少优先"
	default:
		return "金额最少优先"
	}
}
func (a *App) table(name string) string { return "`" + a.cfg.TablePrefix + "_" + name + "`" }

func (a *App) migrate(ctx context.Context) error {
	sqlText := strings.ReplaceAll(migrationSQL, "`pre_", "`"+a.cfg.TablePrefix+"_")
	var cleaned []string
	for _, line := range strings.Split(sqlText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	for _, stmt := range strings.Split(strings.Join(cleaned, "\n"), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := a.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w; sql=%s", err, stmt)
		}
	}
	if err := a.ensureColumn(ctx, "nl_account", "last_assigned_at", "datetime DEFAULT NULL"); err != nil {
		return err
	}
	if err := a.ensureLedgerSetting(ctx, "account_pick_strategy", a.cfg.AccountPickStrategy); err != nil {
		return err
	}
	return nil
}

func (a *App) ensureColumn(ctx context.Context, tableName, columnName, definition string) error {
	table := a.cfg.TablePrefix + "_" + tableName
	var n int
	err := a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=?", table, columnName).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = a.db.ExecContext(ctx, "ALTER TABLE "+a.table(tableName)+" ADD COLUMN `"+columnName+"` "+definition)
	if err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, columnName, err)
	}
	return nil
}

func (a *App) ensureLedgerSetting(ctx context.Context, key, value string) error {
	_, err := a.db.ExecContext(ctx, "INSERT IGNORE INTO "+a.table("nl_config")+" (k,v,updated_at) VALUES (?,?,NOW())", key, value)
	return err
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Truncate(time.Millisecond))
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func bad(w http.ResponseWriter, msg string, code int) {
	writeJSON(w, code, map[string]any{"code": -1, "msg": msg})
}

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"code": 0, "app": appName, "time": time.Now().Format(time.RFC3339)})
}

func (a *App) checkAdmin(r *http.Request) bool {
	if a.cfg.AdminToken == "" {
		return true
	}
	return r.Header.Get("X-Admin-Token") == a.cfg.AdminToken || r.URL.Query().Get("token") == a.cfg.AdminToken || r.FormValue("token") == a.cfg.AdminToken
}

func normalizeAdminView(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "overview", "sessions", "events", "devices", "accounts", "payment":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "overview"
	}
}

func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	view := normalizeAdminView(r.URL.Query().Get("view"))
	devices, _ := a.listDevices(ctx, 20)
	accounts, _ := a.listAccounts(ctx, 20)
	sessions, _ := a.listSessions(ctx, 18)
	events, _ := a.listEvents(ctx, 18)
	stats := a.stats(ctx)
	strategy := a.effectivePickStrategy(ctx)
	dailySummary := a.summaryByDay(ctx, 14)
	monthlySummary := a.summaryByMonth(ctx, 12)
	data := map[string]any{"View": view, "Stats": stats, "DailySummary": dailySummary, "MonthlySummary": monthlySummary, "Devices": devices, "Accounts": accounts, "Sessions": sessions, "Events": events, "Token": r.URL.Query().Get("token"), "ScanMsg": r.URL.Query().Get("scan_msg"), "PickStrategy": strategy, "PickStrategyLabel": pickStrategyLabel(strategy), "PublicBaseURL": a.cfg.PublicBaseURL, "Title": appName}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := adminTpl.Execute(w, data); err != nil {
		log.Println(err)
	}
}

func (a *App) stats(ctx context.Context) map[string]string {
	q := func(sqlText string) string {
		var s sql.NullString
		_ = a.db.QueryRowContext(ctx, sqlText).Scan(&s)
		if s.Valid {
			return s.String
		}
		return "0"
	}
	table := a.table("nl_collect_session")
	eventTable := a.table("nl_notification_event")
	return map[string]string{
		"devices":          q("SELECT COUNT(*) FROM " + a.table("nl_device")),
		"accounts":         q("SELECT COUNT(*) FROM " + a.table("nl_account")),
		"waiting":          q("SELECT COUNT(*) FROM " + table + " WHERE status='waiting'"),
		"waiting_amount":   q("SELECT COALESCE(SUM(amount),0) FROM " + table + " WHERE status='waiting'"),
		"paid":             q("SELECT COUNT(*) FROM " + table + " WHERE status='paid'"),
		"events":           q("SELECT COUNT(*) FROM " + eventTable + " WHERE DATE(received_at)=CURDATE()"),
		"event_waiting":    q("SELECT COUNT(*) FROM " + eventTable + " WHERE match_status='pending'"),
		"event_ambiguous":  q("SELECT COUNT(*) FROM " + eventTable + " WHERE match_status='ambiguous'"),
		"today_match_rate": q("SELECT COALESCE(CONCAT(ROUND(SUM(CASE WHEN match_status='matched' THEN 1 ELSE 0 END)/NULLIF(COUNT(*),0)*100,1),'%'),'0%') FROM " + eventTable + " WHERE DATE(received_at)=CURDATE()"),
		"amount":           q("SELECT COALESCE(SUM(amount),0) FROM " + table + " WHERE status='paid' AND DATE(paid_at)=CURDATE()"),
		"today_orders":     q("SELECT COUNT(*) FROM " + table + " WHERE status='paid' AND DATE(paid_at)=CURDATE()"),
		"yesterday_amount": q("SELECT COALESCE(SUM(amount),0) FROM " + table + " WHERE status='paid' AND DATE(paid_at)=DATE_SUB(CURDATE(), INTERVAL 1 DAY)"),
		"yesterday_orders": q("SELECT COUNT(*) FROM " + table + " WHERE status='paid' AND DATE(paid_at)=DATE_SUB(CURDATE(), INTERVAL 1 DAY)"),
		"week_amount":      q("SELECT COALESCE(SUM(amount),0) FROM " + table + " WHERE status='paid' AND paid_at >= DATE_SUB(CURDATE(), INTERVAL 6 DAY)"),
		"week_orders":      q("SELECT COUNT(*) FROM " + table + " WHERE status='paid' AND paid_at >= DATE_SUB(CURDATE(), INTERVAL 6 DAY)"),
		"last30_amount":    q("SELECT COALESCE(SUM(amount),0) FROM " + table + " WHERE status='paid' AND paid_at >= DATE_SUB(CURDATE(), INTERVAL 29 DAY)"),
		"last30_orders":    q("SELECT COUNT(*) FROM " + table + " WHERE status='paid' AND paid_at >= DATE_SUB(CURDATE(), INTERVAL 29 DAY)"),
		"month_amount":     q("SELECT COALESCE(SUM(amount),0) FROM " + table + " WHERE status='paid' AND DATE_FORMAT(paid_at,'%Y-%m')=DATE_FORMAT(CURDATE(),'%Y-%m')"),
		"month_orders":     q("SELECT COUNT(*) FROM " + table + " WHERE status='paid' AND DATE_FORMAT(paid_at,'%Y-%m')=DATE_FORMAT(CURDATE(),'%Y-%m')"),
		"avg_amount":       q("SELECT COALESCE(ROUND(AVG(amount),2),0) FROM " + table + " WHERE status='paid' AND DATE_FORMAT(paid_at,'%Y-%m')=DATE_FORMAT(CURDATE(),'%Y-%m')"),
		"year_amount":      q("SELECT COALESCE(SUM(amount),0) FROM " + table + " WHERE status='paid' AND YEAR(paid_at)=YEAR(CURDATE())"),
		"year_orders":      q("SELECT COUNT(*) FROM " + table + " WHERE status='paid' AND YEAR(paid_at)=YEAR(CURDATE())"),
		"total_amount":     q("SELECT COALESCE(SUM(amount),0) FROM " + table + " WHERE status='paid'"),
		"total_orders":     q("SELECT COUNT(*) FROM " + table + " WHERE status='paid'"),
		"last_paid_at":     q("SELECT COALESCE(DATE_FORMAT(MAX(paid_at),'%m-%d %H:%i'),'-') FROM " + table + " WHERE status='paid'"),
	}
}

func (a *App) statsForUID(ctx context.Context, uid int64) map[string]string {
	if uid <= 0 {
		return a.stats(ctx)
	}
	q := func(sqlText string, args ...any) string {
		var s sql.NullString
		_ = a.db.QueryRowContext(ctx, sqlText, args...).Scan(&s)
		if s.Valid {
			return s.String
		}
		return "0"
	}
	table := a.table("nl_collect_session")
	eventTable := a.table("nl_notification_event")
	return map[string]string{
		"devices":          "0",
		"accounts":         "0",
		"waiting":          q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='waiting'", uid),
		"waiting_amount":   q("SELECT COALESCE(SUM(amount),0) FROM "+table+" WHERE uid=? AND status='waiting'", uid),
		"paid":             q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='paid'", uid),
		"events":           q("SELECT COUNT(*) FROM "+eventTable+" e JOIN "+table+" s ON e.matched_trade_no=s.epay_trade_no WHERE s.uid=? AND DATE(e.received_at)=CURDATE()", uid),
		"event_waiting":    "0",
		"event_ambiguous":  "0",
		"today_match_rate": "0%",
		"amount":           q("SELECT COALESCE(SUM(amount),0) FROM "+table+" WHERE uid=? AND status='paid' AND DATE(paid_at)=CURDATE()", uid),
		"today_orders":     q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='paid' AND DATE(paid_at)=CURDATE()", uid),
		"yesterday_amount": q("SELECT COALESCE(SUM(amount),0) FROM "+table+" WHERE uid=? AND status='paid' AND DATE(paid_at)=DATE_SUB(CURDATE(), INTERVAL 1 DAY)", uid),
		"yesterday_orders": q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='paid' AND DATE(paid_at)=DATE_SUB(CURDATE(), INTERVAL 1 DAY)", uid),
		"week_amount":      q("SELECT COALESCE(SUM(amount),0) FROM "+table+" WHERE uid=? AND status='paid' AND paid_at >= DATE_SUB(CURDATE(), INTERVAL 6 DAY)", uid),
		"week_orders":      q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='paid' AND paid_at >= DATE_SUB(CURDATE(), INTERVAL 6 DAY)", uid),
		"last30_amount":    q("SELECT COALESCE(SUM(amount),0) FROM "+table+" WHERE uid=? AND status='paid' AND paid_at >= DATE_SUB(CURDATE(), INTERVAL 29 DAY)", uid),
		"last30_orders":    q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='paid' AND paid_at >= DATE_SUB(CURDATE(), INTERVAL 29 DAY)", uid),
		"month_amount":     q("SELECT COALESCE(SUM(amount),0) FROM "+table+" WHERE uid=? AND status='paid' AND DATE_FORMAT(paid_at,'%Y-%m')=DATE_FORMAT(CURDATE(),'%Y-%m')", uid),
		"month_orders":     q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='paid' AND DATE_FORMAT(paid_at,'%Y-%m')=DATE_FORMAT(CURDATE(),'%Y-%m')", uid),
		"avg_amount":       q("SELECT COALESCE(ROUND(AVG(amount),2),0) FROM "+table+" WHERE uid=? AND status='paid' AND DATE_FORMAT(paid_at,'%Y-%m')=DATE_FORMAT(CURDATE(),'%Y-%m')", uid),
		"year_amount":      q("SELECT COALESCE(SUM(amount),0) FROM "+table+" WHERE uid=? AND status='paid' AND YEAR(paid_at)=YEAR(CURDATE())", uid),
		"year_orders":      q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='paid' AND YEAR(paid_at)=YEAR(CURDATE())", uid),
		"total_amount":     q("SELECT COALESCE(SUM(amount),0) FROM "+table+" WHERE uid=? AND status='paid'", uid),
		"total_orders":     q("SELECT COUNT(*) FROM "+table+" WHERE uid=? AND status='paid'", uid),
		"last_paid_at":     q("SELECT COALESCE(DATE_FORMAT(MAX(paid_at),'%m-%d %H:%i'),'-') FROM "+table+" WHERE uid=? AND status='paid'", uid),
	}
}

func (a *App) ledgerSetting(ctx context.Context, key, def string) string {
	var v sql.NullString
	err := a.db.QueryRowContext(ctx, "SELECT v FROM "+a.table("nl_config")+" WHERE k=? LIMIT 1", key).Scan(&v)
	if err == nil && v.Valid && strings.TrimSpace(v.String) != "" {
		return strings.TrimSpace(v.String)
	}
	return def
}

func (a *App) setLedgerSetting(ctx context.Context, key, value string) error {
	_, err := a.db.ExecContext(ctx, "INSERT INTO "+a.table("nl_config")+" (k,v,updated_at) VALUES (?,?,NOW()) ON DUPLICATE KEY UPDATE v=VALUES(v),updated_at=NOW()", key, value)
	return err
}

func (a *App) effectivePickStrategy(ctx context.Context) string {
	return sanitizePickStrategy(a.ledgerSetting(ctx, "account_pick_strategy", a.cfg.AccountPickStrategy))
}

func (a *App) summaryByDay(ctx context.Context, days int) []SummaryPoint {
	if days <= 0 {
		days = 14
	}
	today := time.Now()
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location()).AddDate(0, 0, -days+1)
	items := make([]SummaryPoint, 0, days)
	index := make(map[string]int, days)
	weekdays := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		index[key] = i
		items = append(items, SummaryPoint{Label: d.Format("01/02"), SubLabel: weekdays[int(d.Weekday())], Percent: 0})
	}
	rows, err := a.db.QueryContext(ctx, "SELECT DATE_FORMAT(paid_at,'%Y-%m-%d') d, COUNT(*) c, COALESCE(SUM(amount),0) a FROM "+a.table("nl_collect_session")+" WHERE status='paid' AND paid_at>=? AND paid_at<DATE_ADD(CURDATE(), INTERVAL 1 DAY) GROUP BY DATE_FORMAT(paid_at,'%Y-%m-%d')", start.Format("2006-01-02 15:04:05"))
	if err != nil {
		log.Println(err)
		return items
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int64
		var amount float64
		if err := rows.Scan(&key, &count, &amount); err == nil {
			if pos, ok := index[key]; ok {
				items[pos].Count = count
				items[pos].Amount = round2(amount)
			}
		}
	}
	applySummaryPercent(items)
	return items
}

func (a *App) summaryByMonth(ctx context.Context, months int) []SummaryPoint {
	if months <= 0 {
		months = 12
	}
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -months+1, 0)
	items := make([]SummaryPoint, 0, months)
	index := make(map[string]int, months)
	for i := 0; i < months; i++ {
		d := monthStart.AddDate(0, i, 0)
		key := d.Format("2006-01")
		index[key] = i
		items = append(items, SummaryPoint{Label: d.Format("01月"), SubLabel: d.Format("2006"), Percent: 0})
	}
	rows, err := a.db.QueryContext(ctx, "SELECT DATE_FORMAT(paid_at,'%Y-%m') m, COUNT(*) c, COALESCE(SUM(amount),0) a FROM "+a.table("nl_collect_session")+" WHERE status='paid' AND paid_at>=? GROUP BY DATE_FORMAT(paid_at,'%Y-%m')", monthStart.Format("2006-01-02 15:04:05"))
	if err != nil {
		log.Println(err)
		return items
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int64
		var amount float64
		if err := rows.Scan(&key, &count, &amount); err == nil {
			if pos, ok := index[key]; ok {
				items[pos].Count = count
				items[pos].Amount = round2(amount)
			}
		}
	}
	applySummaryPercent(items)
	return items
}

func (a *App) summaryByDayForUID(ctx context.Context, days int, uid int64) []SummaryPoint {
	if uid <= 0 {
		return a.summaryByDay(ctx, days)
	}
	if days <= 0 {
		days = 14
	}
	today := time.Now()
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location()).AddDate(0, 0, -days+1)
	items := make([]SummaryPoint, 0, days)
	index := make(map[string]int, days)
	weekdays := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	for i := 0; i < days; i++ {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		index[key] = i
		items = append(items, SummaryPoint{Label: d.Format("01/02"), SubLabel: weekdays[int(d.Weekday())], Percent: 0})
	}
	rows, err := a.db.QueryContext(ctx, "SELECT DATE_FORMAT(paid_at,'%Y-%m-%d') d, COUNT(*) c, COALESCE(SUM(amount),0) a FROM "+a.table("nl_collect_session")+" WHERE uid=? AND status='paid' AND paid_at>=? AND paid_at<DATE_ADD(CURDATE(), INTERVAL 1 DAY) GROUP BY DATE_FORMAT(paid_at,'%Y-%m-%d')", uid, start.Format("2006-01-02 15:04:05"))
	if err != nil {
		log.Println(err)
		return items
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int64
		var amount float64
		if err := rows.Scan(&key, &count, &amount); err == nil {
			if pos, ok := index[key]; ok {
				items[pos].Count = count
				items[pos].Amount = round2(amount)
			}
		}
	}
	applySummaryPercent(items)
	return items
}

func (a *App) summaryByMonthForUID(ctx context.Context, months int, uid int64) []SummaryPoint {
	if uid <= 0 {
		return a.summaryByMonth(ctx, months)
	}
	if months <= 0 {
		months = 12
	}
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -months+1, 0)
	items := make([]SummaryPoint, 0, months)
	index := make(map[string]int, months)
	for i := 0; i < months; i++ {
		d := monthStart.AddDate(0, i, 0)
		key := d.Format("2006-01")
		index[key] = i
		items = append(items, SummaryPoint{Label: d.Format("01月"), SubLabel: d.Format("2006"), Percent: 0})
	}
	rows, err := a.db.QueryContext(ctx, "SELECT DATE_FORMAT(paid_at,'%Y-%m') m, COUNT(*) c, COALESCE(SUM(amount),0) a FROM "+a.table("nl_collect_session")+" WHERE uid=? AND status='paid' AND paid_at>=? GROUP BY DATE_FORMAT(paid_at,'%Y-%m')", uid, monthStart.Format("2006-01-02 15:04:05"))
	if err != nil {
		log.Println(err)
		return items
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int64
		var amount float64
		if err := rows.Scan(&key, &count, &amount); err == nil {
			if pos, ok := index[key]; ok {
				items[pos].Count = count
				items[pos].Amount = round2(amount)
			}
		}
	}
	applySummaryPercent(items)
	return items
}

func applySummaryPercent(items []SummaryPoint) {
	maxAmount := 0.0
	for _, item := range items {
		if item.Amount > maxAmount {
			maxAmount = item.Amount
		}
	}
	for i := range items {
		if items[i].Count > 0 {
			items[i].Avg = round2(items[i].Amount / float64(items[i].Count))
		}
		if maxAmount <= 0 || items[i].Amount <= 0 {
			items[i].Percent = 0
			continue
		}
		items[i].Percent = int(math.Round(items[i].Amount / maxAmount * 100))
		if items[i].Percent < 8 {
			items[i].Percent = 8
		}
	}
}

func (a *App) listDevices(ctx context.Context, limit int) ([]Device, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT id,device_no,device_name,secret,status,app_version,last_heartbeat_at,last_ip,battery_level,notification_permission FROM "+a.table("nl_device")+" ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		_ = rows.Scan(&d.ID, &d.DeviceNo, &d.DeviceName, &d.Secret, &d.Status, &d.AppVersion, &d.LastHeartbeatAt, &d.LastIP, &d.BatteryLevel, &d.NotificationPermission)
		out = append(out, d)
	}
	return out, rows.Err()
}
func (a *App) listAccounts(ctx context.Context, limit int) ([]Account, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT id,channel,account_alias,account_identifier,device_no,qrcode_url,status FROM "+a.table("nl_account")+" ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var x Account
		_ = rows.Scan(&x.ID, &x.Channel, &x.AccountAlias, &x.AccountIdentifier, &x.DeviceNo, &x.QRCodeURL, &x.Status)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (a *App) listSessions(ctx context.Context, limit int) ([]CollectSession, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT s.id,s.session_no,s.epay_trade_no,s.epay_out_trade_no,s.uid,s.channel,s.account_id,s.amount,s.status,s.expire_at,s.paid_at,s.created_at,a.account_alias,a.qrcode_url,s.notification_event_id FROM "+a.table("nl_collect_session")+" s LEFT JOIN "+a.table("nl_account")+" a ON s.account_id=a.id ORDER BY s.id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CollectSession
	for rows.Next() {
		var x CollectSession
		_ = rows.Scan(&x.ID, &x.SessionNo, &x.EPayTradeNo, &x.EPayOutTradeNo, &x.UID, &x.Channel, &x.AccountID, &x.Amount, &x.Status, &x.ExpireAt, &x.PaidAt, &x.CreatedAt, &x.AccountAlias, &x.AccountQRCode, &x.NotificationID)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (a *App) listEvents(ctx context.Context, limit int) ([]NotificationEvent, error) {
	rows, err := a.db.QueryContext(ctx, "SELECT id,event_id,device_no,account_id,channel,package_name,raw_title,raw_text,parsed_amount,parsed_payer,notify_time,received_at,match_status,matched_trade_no FROM "+a.table("nl_notification_event")+" ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationEvent
	for rows.Next() {
		var x NotificationEvent
		_ = rows.Scan(&x.ID, &x.EventID, &x.DeviceNo, &x.AccountID, &x.Channel, &x.PackageName, &x.RawTitle, &x.RawText, &x.ParsedAmount, &x.ParsedPayer, &x.NotifyTime, &x.ReceivedAt, &x.MatchStatus, &x.MatchedTradeNo)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (a *App) querySessions(ctx context.Context, req internalSessionsQueryReq) ([]internalSessionItem, error) {
	limit := clampInt(req.Limit, 1, 200, 50)
	where := []string{"1=1"}
	args := make([]any, 0, 8)
	if v := strings.TrimSpace(req.TradeNo); v != "" {
		where = append(where, "s.epay_trade_no=?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(req.OutTradeNo); v != "" {
		where = append(where, "s.epay_out_trade_no=?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(req.SessionNo); v != "" {
		where = append(where, "s.session_no=?")
		args = append(args, v)
	}
	if req.UID > 0 {
		where = append(where, "s.uid=?")
		args = append(args, req.UID)
	}
	if st := sanitizeSessionStatus(req.Status); st != "" {
		where = append(where, "s.status=?")
		args = append(args, st)
	}
	args = append(args, limit)
	rows, err := a.db.QueryContext(ctx, "SELECT s.id,s.session_no,s.epay_trade_no,s.epay_out_trade_no,s.uid,s.channel,s.account_id,s.amount,s.status,s.expire_at,s.paid_at,s.created_at,a.account_alias,a.qrcode_url,s.notification_event_id FROM "+a.table("nl_collect_session")+" s LEFT JOIN "+a.table("nl_account")+" a ON s.account_id=a.id WHERE "+strings.Join(where, " AND ")+" ORDER BY s.id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []internalSessionItem
	for rows.Next() {
		var s CollectSession
		if err := rows.Scan(&s.ID, &s.SessionNo, &s.EPayTradeNo, &s.EPayOutTradeNo, &s.UID, &s.Channel, &s.AccountID, &s.Amount, &s.Status, &s.ExpireAt, &s.PaidAt, &s.CreatedAt, &s.AccountAlias, &s.AccountQRCode, &s.NotificationID); err != nil {
			return nil, err
		}
		out = append(out, sessionItem(s))
	}
	return out, rows.Err()
}

func (a *App) queryEvents(ctx context.Context, req internalEventsRecentReq) ([]internalEventItem, error) {
	limit := clampInt(req.Limit, 1, 200, 50)
	from := a.table("nl_notification_event") + " e"
	where := []string{"1=1"}
	args := make([]any, 0, 4)
	if req.UID > 0 {
		from += " JOIN " + a.table("nl_collect_session") + " s ON e.matched_trade_no=s.epay_trade_no"
		where = append(where, "s.uid=?")
		args = append(args, req.UID)
	}
	if st := sanitizeMatchStatus(req.MatchStatus); st != "" {
		where = append(where, "e.match_status=?")
		args = append(args, st)
	}
	args = append(args, limit)
	rows, err := a.db.QueryContext(ctx, "SELECT e.id,e.event_id,e.device_no,e.account_id,e.channel,e.package_name,e.raw_title,e.raw_text,e.parsed_amount,e.parsed_payer,e.notify_time,e.received_at,e.match_status,e.matched_trade_no FROM "+from+" WHERE "+strings.Join(where, " AND ")+" ORDER BY e.id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []internalEventItem
	for rows.Next() {
		var e NotificationEvent
		if err := rows.Scan(&e.ID, &e.EventID, &e.DeviceNo, &e.AccountID, &e.Channel, &e.PackageName, &e.RawTitle, &e.RawText, &e.ParsedAmount, &e.ParsedPayer, &e.NotifyTime, &e.ReceivedAt, &e.MatchStatus, &e.MatchedTradeNo); err != nil {
			return nil, err
		}
		out = append(out, eventItem(e))
	}
	return out, rows.Err()
}

func (a *App) accountSummary(ctx context.Context, limit int) (map[string]any, error) {
	limit = clampInt(limit, 1, 200, 50)
	rows, err := a.db.QueryContext(ctx, "SELECT channel,COUNT(*) total,SUM(CASE WHEN status=1 THEN 1 ELSE 0 END) active,COALESCE(SUM(daily_received_amount),0) received FROM "+a.table("nl_account")+" GROUP BY channel ORDER BY channel ASC")
	if err != nil {
		return nil, err
	}
	var channels []internalAccountChannelSummary
	for rows.Next() {
		var x internalAccountChannelSummary
		if err := rows.Scan(&x.Channel, &x.Total, &x.Active, &x.DailyReceived); err != nil {
			rows.Close()
			return nil, err
		}
		x.DailyReceived = round2(x.DailyReceived)
		channels = append(channels, x)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	accountRows, err := a.db.QueryContext(ctx, "SELECT id,channel,account_alias,account_identifier,device_no,qrcode_url,status,daily_limit_amount,daily_received_amount,last_assigned_at,last_notify_at FROM "+a.table("nl_account")+" ORDER BY status DESC,id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer accountRows.Close()
	var accounts []internalAccountItem
	for accountRows.Next() {
		var x internalAccountItem
		var identifier, qrcode sql.NullString
		var lastAssigned, lastNotify sql.NullTime
		if err := accountRows.Scan(&x.ID, &x.Channel, &x.AccountAlias, &identifier, &x.DeviceNo, &qrcode, &x.Status, &x.DailyLimitAmount, &x.DailyReceivedAmount, &lastAssigned, &lastNotify); err != nil {
			return nil, err
		}
		x.AccountIdentifier = nullStringValue(identifier)
		x.QRCodeURL = nullStringValue(qrcode)
		x.DailyLimitAmount = round2(x.DailyLimitAmount)
		x.DailyReceivedAmount = round2(x.DailyReceivedAmount)
		x.LastAssignedAt = nullTimeValue(lastAssigned)
		x.LastNotifyAt = nullTimeValue(lastNotify)
		accounts = append(accounts, x)
	}
	return map[string]any{"channels": channels, "accounts": accounts}, accountRows.Err()
}

func (a *App) adminCreateDevice(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	deviceNo := strings.TrimSpace(r.FormValue("device_no"))
	secret := strings.TrimSpace(r.FormValue("secret"))
	name := strings.TrimSpace(r.FormValue("device_name"))
	if deviceNo == "" || secret == "" {
		http.Error(w, "device_no 和 secret 必填", 400)
		return
	}
	_, err := a.db.ExecContext(r.Context(), "INSERT INTO "+a.table("nl_device")+" (device_no,device_name,secret,status,created_at,updated_at) VALUES (?,?,?,?,NOW(),NOW()) ON DUPLICATE KEY UPDATE device_name=VALUES(device_name),secret=VALUES(secret),status=1,updated_at=NOW()", deviceNo, name, secret, 1)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.redirectAdmin(w, r)
}

func (a *App) adminScanADBDevices(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	devices, err := a.scanADBDevices(r.Context())
	if err != nil {
		a.redirectAdminWithScanMsg(w, r, "ADB 扫描失败："+err.Error())
		return
	}
	if len(devices) == 0 {
		a.redirectAdminWithScanMsg(w, r, "未发现 ADB 设备。请确认手机已开启 USB 调试，并且 Go 服务所在机器能执行 adb devices -l。")
		return
	}
	sharedSecret := strings.TrimSpace(r.FormValue("scan_secret"))
	if sharedSecret == "" {
		sharedSecret = a.cfg.DefaultDeviceSecret
	}
	var usable, created, updated, skipped int
	randomSecretUsed := false
	for _, d := range devices {
		if d.State != "device" {
			skipped++
			continue
		}
		usable++
		secret := sharedSecret
		if secret == "" {
			secret = "BF" + randHex(12)
			randomSecretUsed = true
		}
		isNew, err := a.upsertADBDevice(r.Context(), d, secret)
		if err != nil {
			log.Printf("scan adb upsert device %s: %v", d.Serial, err)
			skipped++
			continue
		}
		if isNew {
			created++
		} else {
			updated++
		}
	}
	msg := fmt.Sprintf("ADB 扫描完成：发现 %d 台，可用 %d 台，新增 %d 台，更新 %d 台，跳过 %d 台。", len(devices), usable, created, updated, skipped)
	if randomSecretUsed {
		msg += " 未填写批量密钥，新设备已生成随机密钥，请在设备列表复制到安卓端。"
	} else if sharedSecret != "" && created > 0 {
		msg += " 新增设备使用本次填写的批量密钥。"
	}
	a.redirectAdminWithScanMsg(w, r, msg)
}

func (a *App) scanADBDevices(ctx context.Context) ([]ADBDevice, error) {
	adbPath := strings.TrimSpace(a.cfg.ADBPath)
	if adbPath == "" {
		adbPath = "adb"
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, adbPath, "devices", "-l").CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, errors.New("执行 adb devices -l 超时")
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("未找到 adb，请安装 Android SDK platform-tools，或配置 NL_ADB_PATH")
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return parseADBDevices(string(out)), nil
}

func (a *App) upsertADBDevice(ctx context.Context, d ADBDevice, secret string) (bool, error) {
	serial := strings.TrimSpace(d.Serial)
	if serial == "" {
		return false, errors.New("设备序列号为空")
	}
	var exists int
	if err := a.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+a.table("nl_device")+" WHERE device_no=?", serial).Scan(&exists); err != nil {
		return false, err
	}
	name := d.DisplayName()
	_, err := a.db.ExecContext(ctx, "INSERT INTO "+a.table("nl_device")+" (device_no,device_name,secret,status,created_at,updated_at) VALUES (?,?,?,?,NOW(),NOW()) ON DUPLICATE KEY UPDATE device_name=VALUES(device_name),status=1,updated_at=NOW()", serial, name, secret, 1)
	if err != nil {
		return false, err
	}
	return exists == 0, nil
}

func (a *App) redirectAdminWithScanMsg(w http.ResponseWriter, r *http.Request, msg string) {
	q := url.Values{}
	q.Set("view", "devices")
	token := r.FormValue("token")
	if token != "" {
		q.Set("token", token)
	}
	q.Set("scan_msg", msg)
	http.Redirect(w, r, "/admin?"+q.Encode(), http.StatusFound)
}

func (a *App) adminCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	channel := normalizeChannel(r.FormValue("channel"))
	alias := strings.TrimSpace(r.FormValue("account_alias"))
	deviceNo := strings.TrimSpace(r.FormValue("device_no"))
	qr := strings.TrimSpace(r.FormValue("qrcode_url"))
	if channel == "" || alias == "" || deviceNo == "" {
		http.Error(w, "channel / account_alias / device_no 必填", 400)
		return
	}
	_, err := a.db.ExecContext(r.Context(), "INSERT INTO "+a.table("nl_account")+" (channel,account_alias,account_identifier,device_no,qrcode_url,status,created_at,updated_at) VALUES (?,?,?,?,?,1,NOW(),NOW())", channel, alias, r.FormValue("account_identifier"), deviceNo, qr)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.redirectAdmin(w, r)
}

func (a *App) adminSetDeviceStatus(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	status := 0
	if r.FormValue("status") == "1" {
		status = 1
	}
	if id > 0 {
		_, _ = a.db.ExecContext(r.Context(), "UPDATE "+a.table("nl_device")+" SET status=?,updated_at=NOW() WHERE id=?", status, id)
	}
	a.redirectAdmin(w, r)
}

func (a *App) adminSetAccountStatus(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	status := 0
	if r.FormValue("status") == "1" {
		status = 1
	}
	if id > 0 {
		_, _ = a.db.ExecContext(r.Context(), "UPDATE "+a.table("nl_account")+" SET status=?,updated_at=NOW() WHERE id=?", status, id)
	}
	a.redirectAdmin(w, r)
}

func (a *App) adminUpdatePickStrategy(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	strategy := sanitizePickStrategy(r.FormValue("account_pick_strategy"))
	if err := a.setLedgerSetting(r.Context(), "account_pick_strategy", strategy); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.redirectAdmin(w, r)
}

func (a *App) adminIgnoreEvent(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if id > 0 {
		_, _ = a.db.ExecContext(r.Context(), "UPDATE "+a.table("nl_notification_event")+" SET match_status='ignored' WHERE id=? AND match_status<>'matched'", id)
	}
	a.redirectAdmin(w, r)
}

func (a *App) adminManualMatchEvent(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	eventPK, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	tradeNo := strings.TrimSpace(r.FormValue("trade_no"))
	force := r.FormValue("force") == "1"
	if eventPK <= 0 || tradeNo == "" {
		http.Error(w, "事件ID和订单号必填", http.StatusBadRequest)
		return
	}
	evt, err := a.getEventByID(r.Context(), eventPK)
	if err != nil {
		http.Error(w, "通知事件不存在", http.StatusNotFound)
		return
	}
	s, err := a.getSessionByTradeNo(r.Context(), tradeNo)
	if err != nil {
		http.Error(w, "收款会话不存在", http.StatusNotFound)
		return
	}
	if evt.ParsedAmount.Valid && round2(evt.ParsedAmount.Float64) != round2(s.Amount) && !force {
		http.Error(w, "通知金额与订单金额不一致；如确需补单请勾选强制", http.StatusConflict)
		return
	}
	paidAt := time.Now().Format("2006-01-02 15:04:05")
	if evt.NotifyTime.Valid {
		paidAt = evt.NotifyTime.Time.Format("2006-01-02 15:04:05")
	}
	if err := a.markPaid(r.Context(), s, evt.EventID, evt.Channel, s.Amount, paidAt, eventPK, 80); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	go a.notifyEPay(s.EPayTradeNo, evt.EventID, evt.Channel, s.Amount, paidAt)
	a.redirectAdmin(w, r)
}

func (a *App) adminManualPaidSession(w http.ResponseWriter, r *http.Request) {
	if !a.checkAdmin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	tradeNo := strings.TrimSpace(r.FormValue("trade_no"))
	buyerHint := strings.TrimSpace(r.FormValue("buyer_hint"))
	if tradeNo == "" {
		http.Error(w, "订单号必填", http.StatusBadRequest)
		return
	}
	s, err := a.getSessionByTradeNo(r.Context(), tradeNo)
	if err != nil {
		http.Error(w, "收款会话不存在", http.StatusNotFound)
		return
	}
	paidAt := time.Now().Format("2006-01-02 15:04:05")
	eventID := "MANUAL" + s.SessionNo
	if buyerHint != "" {
		eventID = eventID + sha256Hex([]byte(buyerHint))[:8]
	}
	if err := a.markPaid(r.Context(), s, eventID, s.Channel, s.Amount, paidAt, 0, 60); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	go a.notifyEPay(s.EPayTradeNo, eventID, s.Channel, s.Amount, paidAt)
	a.redirectAdmin(w, r)
}

func (a *App) redirectAdmin(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	view := r.FormValue("view")
	if view == "" {
		view = r.URL.Query().Get("view")
	}
	path := "/admin?view=" + url.QueryEscape(normalizeAdminView(view))
	if token != "" {
		path += "&token=" + url.QueryEscape(token)
	}
	http.Redirect(w, r, path, http.StatusFound)
}

func (a *App) getEventByID(ctx context.Context, id int64) (*NotificationEvent, error) {
	var x NotificationEvent
	err := a.db.QueryRowContext(ctx, "SELECT id,event_id,device_no,account_id,channel,package_name,raw_title,raw_text,parsed_amount,parsed_payer,notify_time,received_at,match_status,matched_trade_no FROM "+a.table("nl_notification_event")+" WHERE id=? LIMIT 1", id).Scan(&x.ID, &x.EventID, &x.DeviceNo, &x.AccountID, &x.Channel, &x.PackageName, &x.RawTitle, &x.RawText, &x.ParsedAmount, &x.ParsedPayer, &x.NotifyTime, &x.ReceivedAt, &x.MatchStatus, &x.MatchedTradeNo)
	return &x, err
}

func (a *App) getSessionByTradeNo(ctx context.Context, tradeNo string) (*CollectSession, error) {
	var s CollectSession
	err := a.db.QueryRowContext(ctx, "SELECT s.id,s.session_no,s.epay_trade_no,s.epay_out_trade_no,s.uid,s.channel,s.account_id,s.amount,s.status,s.expire_at,s.paid_at,s.created_at,a.account_alias,a.qrcode_url,s.notification_event_id FROM "+a.table("nl_collect_session")+" s LEFT JOIN "+a.table("nl_account")+" a ON s.account_id=a.id WHERE s.epay_trade_no=? LIMIT 1", tradeNo).Scan(&s.ID, &s.SessionNo, &s.EPayTradeNo, &s.EPayOutTradeNo, &s.UID, &s.Channel, &s.AccountID, &s.Amount, &s.Status, &s.ExpireAt, &s.PaidAt, &s.CreatedAt, &s.AccountAlias, &s.AccountQRCode, &s.NotificationID)
	return &s, err
}

func (a *App) markPaid(ctx context.Context, s *CollectSession, eventID, channel string, amount float64, paidAt string, eventPK int64, score int) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "UPDATE "+a.table("nl_collect_session")+" SET status='paid',paid_at=?,notification_event_id=?,match_score=?,updated_at=NOW() WHERE id=? AND status<>'paid'", paidAt, nullEventArg(eventPK), score, s.ID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		_ = tx.Rollback()
		return errors.New("会话已处理或状态不允许补单")
	}
	if eventPK > 0 {
		_, err = tx.ExecContext(ctx, "UPDATE "+a.table("nl_notification_event")+" SET match_status='matched',matched_trade_no=? WHERE id=?", s.EPayTradeNo, eventPK)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if s.AccountID.Valid {
		_, _ = tx.ExecContext(ctx, "UPDATE "+a.table("nl_account")+" SET daily_received_amount=daily_received_amount+?,last_notify_at=NOW() WHERE id=?", round2(amount), s.AccountID.Int64)
	}
	_, _ = tx.ExecContext(ctx, "INSERT INTO "+a.table("nl_audit_log")+" (actor,action,target_type,target_id,detail,created_at) VALUES ('admin','manual_paid','collect_session',?,?,NOW())", s.EPayTradeNo, eventID)
	return tx.Commit()
}

func nullEventArg(id int64) any {
	if id > 0 {
		return id
	}
	return nil
}

func (a *App) deviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		bad(w, "读取请求失败", 400)
		return
	}
	var req heartbeatReq
	if err := json.Unmarshal(body, &req); err != nil {
		bad(w, "JSON格式错误", 400)
		return
	}
	deviceNo := firstNonEmpty(req.DeviceNo, r.Header.Get("device"), r.Header.Get("X-Device-No"))
	dev, err := a.authDevice(r.Context(), r, body, deviceNo)
	if err != nil {
		bad(w, err.Error(), 401)
		return
	}
	perm := 0
	if req.NotificationPermission {
		perm = 1
	}
	_, _ = a.db.ExecContext(r.Context(), "UPDATE "+a.table("nl_device")+" SET device_name=IF(?='',device_name,?),app_version=?,last_heartbeat_at=NOW(),last_ip=?,battery_level=?,notification_permission=?,updated_at=NOW() WHERE id=?", req.DeviceName, req.DeviceName, req.AppVersion, clientIP(r), req.BatteryLevel, perm, dev.ID)
	writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok"})
}

func (a *App) deviceNotification(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		bad(w, "读取请求失败", 400)
		return
	}
	var req notificationReq
	if err := json.Unmarshal(body, &req); err != nil {
		bad(w, "JSON格式错误", 400)
		return
	}
	if req.Title == "" {
		req.Title = req.From
	}
	if req.Text == "" {
		req.Text = req.Content
	}
	if req.NotifyTime == 0 {
		req.NotifyTime = req.Time
	}
	if req.Channel == "" {
		req.Channel = req.Type
	}
	deviceNo := firstNonEmpty(req.DeviceNo, r.Header.Get("device"), r.Header.Get("X-Device-No"))
	if req.EventID == "" {
		req.EventID = eventID(deviceNo, req.PackageName, req.Title, req.Text, req.NotifyTime)
	}
	_, err = a.authDevice(r.Context(), r, body, deviceNo)
	if err != nil {
		bad(w, err.Error(), 401)
		return
	}
	ch := normalizeChannel(firstNonEmpty(req.Channel, channelFromPackage(req.PackageName)))
	if ch == "" {
		bad(w, "未知渠道", 400)
		return
	}
	amount := parseAmount(req.Title + " " + req.Text)
	var notifyTime any = nil
	if req.NotifyTime > 0 {
		notifyTime = normalizeMillis(req.NotifyTime).Format("2006-01-02 15:04:05")
	}
	payloadHash := sha256Hex(body)
	var accountID sql.NullInt64
	_ = a.db.QueryRowContext(r.Context(), "SELECT id FROM "+a.table("nl_account")+" WHERE device_no=? AND channel=? AND status=1 ORDER BY id ASC LIMIT 1", deviceNo, ch).Scan(&accountID)
	res, err := a.db.ExecContext(r.Context(), "INSERT INTO "+a.table("nl_notification_event")+" (event_id,device_no,account_id,channel,package_name,raw_title,raw_text,parsed_amount,notify_time,received_at,match_status,raw_payload_hash) VALUES (?,?,?,?,?,?,?,?,?,NOW(),'pending',?) ON DUPLICATE KEY UPDATE received_at=received_at", req.EventID, deviceNo, nullInt64Arg(accountID), ch, req.PackageName, req.Title, req.Text, nullFloatArg(amount), notifyTime, payloadHash)
	if err != nil {
		bad(w, err.Error(), 500)
		return
	}
	inserted, _ := res.RowsAffected()
	if inserted == 0 {
		writeJSON(w, 200, map[string]any{"code": 0, "msg": "duplicate"})
		return
	}
	var eventPK int64
	_ = a.db.QueryRowContext(r.Context(), "SELECT id FROM "+a.table("nl_notification_event")+" WHERE event_id=?", req.EventID).Scan(&eventPK)
	matchStatus, tradeNo := a.matchEvent(r.Context(), eventPK, req.EventID, ch, accountID, amount)
	writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok", "match_status": matchStatus, "trade_no": tradeNo})
}

func (a *App) authDevice(ctx context.Context, r *http.Request, body []byte, deviceNo string) (*Device, error) {
	if deviceNo == "" {
		return nil, errors.New("缺少 device_no")
	}
	dev, err := a.getDevice(ctx, deviceNo)
	if errors.Is(err, sql.ErrNoRows) && a.cfg.AutoRegisterDevice && a.cfg.DefaultDeviceSecret != "" {
		_, err = a.db.ExecContext(ctx, "INSERT INTO "+a.table("nl_device")+" (device_no,device_name,secret,status,created_at,updated_at) VALUES (?,?,?,1,NOW(),NOW())", deviceNo, deviceNo, a.cfg.DefaultDeviceSecret)
		if err != nil {
			return nil, err
		}
		dev, err = a.getDevice(ctx, deviceNo)
	}
	if err != nil {
		return nil, errors.New("设备不存在")
	}
	if dev.Status != 1 {
		return nil, errors.New("设备已停用")
	}
	legacySecret := r.Header.Get("secret")
	if legacySecret != "" {
		if subtleEqual(legacySecret, dev.Secret) {
			return dev, nil
		}
		return nil, errors.New("设备密钥错误")
	}
	ts := r.Header.Get("X-Timestamp")
	nonce := r.Header.Get("X-Nonce")
	sig := r.Header.Get("X-Signature")
	if ts == "" || nonce == "" || sig == "" {
		return nil, errors.New("缺少签名头")
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil, errors.New("时间戳错误")
	}
	if math.Abs(float64(time.Now().Unix()-n)) > 300 {
		return nil, errors.New("请求已过期")
	}
	if !a.saveNonce(ctx, "device:"+deviceNo, nonce) {
		return nil, errors.New("重复 nonce")
	}
	mac := hmac.New(sha256.New, []byte(dev.Secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("\n"))
	mac.Write([]byte(nonce))
	mac.Write([]byte("\n"))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !subtleEqual(sig, want) {
		return nil, errors.New("签名错误")
	}
	return dev, nil
}

func (a *App) getDevice(ctx context.Context, deviceNo string) (*Device, error) {
	var d Device
	err := a.db.QueryRowContext(ctx, "SELECT id,device_no,device_name,secret,status,app_version,last_heartbeat_at,last_ip,battery_level,notification_permission FROM "+a.table("nl_device")+" WHERE device_no=? LIMIT 1", deviceNo).Scan(&d.ID, &d.DeviceNo, &d.DeviceName, &d.Secret, &d.Status, &d.AppVersion, &d.LastHeartbeatAt, &d.LastIP, &d.BatteryLevel, &d.NotificationPermission)
	return &d, err
}
func (a *App) saveNonce(ctx context.Context, scope, nonce string) bool {
	_, _ = a.db.ExecContext(ctx, "DELETE FROM "+a.table("nl_nonce")+" WHERE created_at < DATE_SUB(NOW(), INTERVAL 10 MINUTE)")
	_, err := a.db.ExecContext(ctx, "INSERT INTO "+a.table("nl_nonce")+" (scope,nonce,created_at) VALUES (?,?,NOW())", scope, nonce)
	return err == nil
}

func (a *App) createCollectSession(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		bad(w, "读取请求失败", 400)
		return
	}
	if err := a.authInternal(r.Context(), r, body, "epay"); err != nil {
		bad(w, err.Error(), 401)
		return
	}
	var req createSessionReq
	if err := json.Unmarshal(body, &req); err != nil {
		bad(w, "JSON格式错误", 400)
		return
	}
	if req.TradeNo == "" || req.OutTradeNo == "" || req.UID <= 0 || req.Amount <= 0 {
		bad(w, "参数不完整", 400)
		return
	}
	ch := normalizeChannel(req.Channel)
	if ch == "" {
		bad(w, "支付渠道不支持", 400)
		return
	}
	expireAt := time.Now().Add(15 * time.Minute)
	if req.ExpireAt != "" {
		if t, err := parseTime(req.ExpireAt); err == nil {
			expireAt = t
		}
	}
	account, err := a.pickAccount(r.Context(), ch, req.Amount)
	if err != nil {
		bad(w, "暂无可用收款账号", 409)
		return
	}
	sessionNo := "BF" + time.Now().Format("20060102150405") + randHex(4)
	_, err = a.db.ExecContext(r.Context(), "INSERT INTO "+a.table("nl_collect_session")+" (session_no,epay_trade_no,epay_out_trade_no,uid,channel,account_id,amount,status,expire_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'waiting',?,NOW(),NOW()) ON DUPLICATE KEY UPDATE account_id=VALUES(account_id),amount=VALUES(amount),status=IF(status='paid',status,'waiting'),expire_at=VALUES(expire_at),updated_at=NOW()", sessionNo, req.TradeNo, req.OutTradeNo, req.UID, ch, account.ID, round2(req.Amount), expireAt.Format("2006-01-02 15:04:05"))
	if err != nil {
		bad(w, err.Error(), 500)
		return
	}
	_, _ = a.db.ExecContext(r.Context(), "UPDATE "+a.table("nl_account")+" SET last_assigned_at=NOW(),updated_at=NOW() WHERE id=?", account.ID)
	if err := a.db.QueryRowContext(r.Context(), "SELECT session_no FROM "+a.table("nl_collect_session")+" WHERE epay_trade_no=?", req.TradeNo).Scan(&sessionNo); err != nil {
		bad(w, err.Error(), 500)
		return
	}
	payURL := a.cfg.PublicBaseURL + "/pay/" + sessionNo
	writeJSON(w, 200, createSessionResp{Code: 0, Msg: "ok", SessionNo: sessionNo, PayURL: payURL, AccountID: account.ID})
}

func (a *App) readInternalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		bad(w, "读取请求失败", 400)
		return false
	}
	if err := a.authInternal(r.Context(), r, body, "epay"); err != nil {
		bad(w, err.Error(), 401)
		return false
	}
	if dst != nil && len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, dst); err != nil {
			bad(w, "JSON格式错误", 400)
			return false
		}
	}
	return true
}

func (a *App) internalStatus(w http.ResponseWriter, r *http.Request) {
	if !a.readInternalJSON(w, r, nil) {
		return
	}
	ctx := r.Context()
	strategy := a.effectivePickStrategy(ctx)
	accountSummary, _ := a.accountSummary(ctx, 12)
	writeJSON(w, 200, map[string]any{
		"code":            0,
		"msg":             "ok",
		"app":             appName,
		"time":            time.Now().Format(time.RFC3339),
		"public_base_url": a.cfg.PublicBaseURL,
		"pick_strategy": map[string]string{
			"value": strategy,
			"label": pickStrategyLabel(strategy),
		},
		"stats":            a.stats(ctx),
		"accounts_summary": accountSummary,
		"endpoints": []string{
			"POST /internal/collect-sessions",
			"POST /internal/status",
			"POST /internal/stats",
			"POST /internal/accounts/summary",
			"POST /internal/sessions/query",
			"POST /internal/sessions/cancel",
			"POST /internal/events/recent",
		},
	})
}

func (a *App) internalStats(w http.ResponseWriter, r *http.Request) {
	var req internalStatsReq
	if !a.readInternalJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	days := clampInt(req.Days, 1, 60, 14)
	months := clampInt(req.Months, 1, 24, 12)
	limit := clampInt(req.Limit, 1, 100, 20)
	strategy := a.effectivePickStrategy(ctx)
	stats := a.stats(ctx)
	daily := a.summaryByDay(ctx, days)
	monthly := a.summaryByMonth(ctx, months)
	if req.UID > 0 {
		stats = a.statsForUID(ctx, req.UID)
		daily = a.summaryByDayForUID(ctx, days, req.UID)
		monthly = a.summaryByMonthForUID(ctx, months, req.UID)
	}
	sessions, err := a.querySessions(ctx, internalSessionsQueryReq{UID: req.UID, Limit: limit})
	if err != nil {
		bad(w, err.Error(), 500)
		return
	}
	events, err := a.queryEvents(ctx, internalEventsRecentReq{UID: req.UID, Limit: limit})
	if err != nil {
		bad(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]any{
		"code": 0,
		"msg":  "ok",
		"pick_strategy": map[string]string{
			"value": strategy,
			"label": pickStrategyLabel(strategy),
		},
		"stats":           stats,
		"daily_summary":   daily,
		"monthly_summary": monthly,
		"recent_sessions": sessions,
		"recent_events":   events,
	})
}

func (a *App) internalAccountsSummary(w http.ResponseWriter, r *http.Request) {
	var req internalLimitReq
	if !a.readInternalJSON(w, r, &req) {
		return
	}
	summary, err := a.accountSummary(r.Context(), clampInt(req.Limit, 1, 200, 50))
	if err != nil {
		bad(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok", "accounts_summary": summary})
}

func (a *App) internalSessionsQuery(w http.ResponseWriter, r *http.Request) {
	var req internalSessionsQueryReq
	if !a.readInternalJSON(w, r, &req) {
		return
	}
	sessions, err := a.querySessions(r.Context(), req)
	if err != nil {
		bad(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok", "sessions": sessions})
}

func (a *App) internalCancelSession(w http.ResponseWriter, r *http.Request) {
	var req internalCancelSessionReq
	if !a.readInternalJSON(w, r, &req) {
		return
	}
	tradeNo := strings.TrimSpace(req.TradeNo)
	sessionNo := strings.TrimSpace(req.SessionNo)
	if tradeNo == "" && sessionNo == "" {
		bad(w, "trade_no 或 session_no 必填", 400)
		return
	}
	where := "epay_trade_no=?"
	arg := tradeNo
	if tradeNo == "" {
		where = "session_no=?"
		arg = sessionNo
	}
	res, err := a.db.ExecContext(r.Context(), "UPDATE "+a.table("nl_collect_session")+" SET status='canceled',updated_at=NOW() WHERE "+where+" AND status='waiting'", arg)
	if err != nil {
		bad(w, err.Error(), 500)
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		bad(w, "会话不存在或状态不允许取消", 409)
		return
	}
	_, _ = a.db.ExecContext(r.Context(), "INSERT INTO "+a.table("nl_audit_log")+" (actor,action,target_type,target_id,detail,ip,created_at) VALUES ('epay','cancel_session','collect_session',?,?,?,NOW())", firstNonEmpty(tradeNo, sessionNo), strings.TrimSpace(req.Reason), clientIP(r))
	writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok"})
}

func (a *App) internalEventsRecent(w http.ResponseWriter, r *http.Request) {
	var req internalEventsRecentReq
	if !a.readInternalJSON(w, r, &req) {
		return
	}
	events, err := a.queryEvents(r.Context(), req)
	if err != nil {
		bad(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, map[string]any{"code": 0, "msg": "ok", "events": events})
}

func (a *App) authInternal(ctx context.Context, r *http.Request, body []byte, scope string) error {
	if a.cfg.EPayInternalSecret == "" {
		return errors.New("内部密钥未配置")
	}
	ts := r.Header.Get("X-Timestamp")
	nonce := r.Header.Get("X-Nonce")
	sig := r.Header.Get("X-Signature")
	if ts == "" || nonce == "" || sig == "" {
		return errors.New("缺少内部签名")
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("时间戳错误")
	}
	if math.Abs(float64(time.Now().Unix()-n)) > 300 {
		return errors.New("请求已过期")
	}
	if !a.saveNonce(ctx, "internal:"+scope, nonce) {
		return errors.New("重复 nonce")
	}
	mac := hmac.New(sha256.New, []byte(a.cfg.EPayInternalSecret))
	mac.Write([]byte(ts))
	mac.Write([]byte("\n"))
	mac.Write([]byte(nonce))
	mac.Write([]byte("\n"))
	mac.Write(body)
	if !subtleEqual(sig, hex.EncodeToString(mac.Sum(nil))) {
		return errors.New("内部签名错误")
	}
	return nil
}

func (a *App) pickAccount(ctx context.Context, channel string, amount float64) (*Account, error) {
	strategy := a.effectivePickStrategy(ctx)
	orderBy := "a.last_notify_at IS NULL DESC, a.daily_received_amount ASC, a.id ASC"
	switch strategy {
	case "random":
		orderBy = "RAND()"
	case "round_robin":
		orderBy = "a.last_assigned_at IS NULL DESC, a.last_assigned_at ASC, a.id ASC"
	case "least_orders":
		orderBy = "(SELECT COUNT(*) FROM " + a.table("nl_collect_session") + " s WHERE s.account_id=a.id AND DATE(s.created_at)=CURDATE()) ASC, a.daily_received_amount ASC, a.id ASC"
	}
	var x Account
	err := a.db.QueryRowContext(ctx, "SELECT a.id,a.channel,a.account_alias,a.account_identifier,a.device_no,a.qrcode_url,a.status FROM "+a.table("nl_account")+" a WHERE a.channel=? AND a.status=1 AND (a.daily_limit_amount=0 OR a.daily_received_amount + ? <= a.daily_limit_amount) ORDER BY "+orderBy+" LIMIT 1", channel, round2(amount)).Scan(&x.ID, &x.Channel, &x.AccountAlias, &x.AccountIdentifier, &x.DeviceNo, &x.QRCodeURL, &x.Status)
	return &x, err
}

func (a *App) payPage(w http.ResponseWriter, r *http.Request) {
	sessionNo := strings.TrimPrefix(r.URL.Path, "/pay/")
	sessionNo = strings.Trim(sessionNo, "/")
	if sessionNo == "" {
		http.NotFound(w, r)
		return
	}
	s, err := a.getSessionByNo(r.Context(), sessionNo)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = payTpl.Execute(w, map[string]any{"S": s, "Title": "不凡收款台", "Now": time.Now(), "Expired": time.Now().After(s.ExpireAt)})
}
func (a *App) getSessionByNo(ctx context.Context, sessionNo string) (*CollectSession, error) {
	var s CollectSession
	err := a.db.QueryRowContext(ctx, "SELECT s.id,s.session_no,s.epay_trade_no,s.epay_out_trade_no,s.uid,s.channel,s.account_id,s.amount,s.status,s.expire_at,s.paid_at,s.created_at,a.account_alias,a.qrcode_url,s.notification_event_id FROM "+a.table("nl_collect_session")+" s LEFT JOIN "+a.table("nl_account")+" a ON s.account_id=a.id WHERE s.session_no=? LIMIT 1", sessionNo).Scan(&s.ID, &s.SessionNo, &s.EPayTradeNo, &s.EPayOutTradeNo, &s.UID, &s.Channel, &s.AccountID, &s.Amount, &s.Status, &s.ExpireAt, &s.PaidAt, &s.CreatedAt, &s.AccountAlias, &s.AccountQRCode, &s.NotificationID)
	return &s, err
}

func (a *App) matchEvent(ctx context.Context, eventPK int64, eventID, channel string, accountID sql.NullInt64, amount float64) (string, string) {
	if amount <= 0 {
		a.markEvent(ctx, eventPK, "ignored", "")
		return "ignored", ""
	}
	args := []any{channel, round2(amount), time.Now().Format("2006-01-02 15:04:05")}
	cond := "channel=? AND amount=? AND status='waiting' AND expire_at>=?"
	if accountID.Valid {
		cond += " AND account_id=?"
		args = append(args, accountID.Int64)
	}
	rows, err := a.db.QueryContext(ctx, "SELECT id,epay_trade_no FROM "+a.table("nl_collect_session")+" WHERE "+cond+" ORDER BY created_at ASC LIMIT 3", args...)
	if err != nil {
		log.Println(err)
		return "pending", ""
	}
	defer rows.Close()
	type cand struct {
		id    int64
		trade string
	}
	var cands []cand
	for rows.Next() {
		var c cand
		_ = rows.Scan(&c.id, &c.trade)
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return "pending", ""
	}
	if len(cands) > 1 {
		a.markEvent(ctx, eventPK, "ambiguous", "")
		return "ambiguous", ""
	}
	c := cands[0]
	paidAt := time.Now().Format("2006-01-02 15:04:05")
	sess, err := a.getSessionByTradeNo(ctx, c.trade)
	if err != nil {
		return "pending", ""
	}
	if err := a.markPaid(ctx, sess, eventID, channel, amount, paidAt, eventPK, 100); err != nil {
		return "pending", ""
	}
	go a.notifyEPay(c.trade, eventID, channel, amount, paidAt)
	return "matched", c.trade
}
func (a *App) markEvent(ctx context.Context, id int64, status, trade string) {
	_, _ = a.db.ExecContext(ctx, "UPDATE "+a.table("nl_notification_event")+" SET match_status=?,matched_trade_no=NULLIF(?,'') WHERE id=?", status, trade, id)
}

func (a *App) notifyEPay(tradeNo, eventID, channel string, amount float64, paidAt string) {
	if a.cfg.EPayBaseURL == "" || a.cfg.EPayInternalSecret == "" {
		return
	}
	payload := paidCallbackReq{TradeNo: tradeNo, EventID: eventID, Channel: channel, Amount: round2(amount), PaidAt: paidAt}
	body, _ := json.Marshal(payload)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randHex(12)
	mac := hmac.New(sha256.New, []byte(a.cfg.EPayInternalSecret))
	mac.Write([]byte(ts))
	mac.Write([]byte("\n"))
	mac.Write([]byte(nonce))
	mac.Write([]byte("\n"))
	mac.Write(body)
	req, err := http.NewRequest("POST", a.cfg.EPayBaseURL+"/notifyledger_internal.php", bytes.NewReader(body))
	if err != nil {
		log.Println(err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("EPay callback:", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		log.Printf("EPay callback status=%d body=%s", resp.StatusCode, string(b))
	}
}

func normalizeChannel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "wechat", "wxpay", "weixin", "微信", "com.tencent.mm":
		return "wxpay"
	case "alipay", "支付宝", "com.eg.android.alipaygphone":
		return "alipay"
	default:
		return s
	}
}
func channelFromPackage(pkg string) string { return normalizeChannel(pkg) }
func parseAmount(s string) float64 {
	re := regexp.MustCompile(`(?:¥|￥|人民币|收款|到账|入账|收钱|付款|金额|元|\s)([0-9]+(?:\.[0-9]{1,2})?)\s*(?:元|CNY|人民币)?`)
	ms := re.FindAllStringSubmatch(s, -1)
	if len(ms) == 0 {
		return 0
	}
	for _, m := range ms {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0 {
			return round2(v)
		}
	}
	return 0
}
func round2(f float64) float64 { return math.Round(f*100) / 100 }
func normalizeMillis(v int64) time.Time {
	if v > 1e12 {
		return time.UnixMilli(v)
	}
	return time.Unix(v, 0)
}
func parseTime(s string) (time.Time, error) {
	layouts := []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05"}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("bad time")
}
func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func parseADBDevices(output string) []ADBDevice {
	var devices []ADBDevice
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "* daemon") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		d := ADBDevice{Serial: fields[0], State: fields[1], Details: map[string]string{}}
		for _, field := range fields[2:] {
			key, value, ok := strings.Cut(field, ":")
			if ok && key != "" {
				d.Details[key] = value
			}
		}
		devices = append(devices, d)
	}
	return devices
}

func (d ADBDevice) DisplayName() string {
	model := strings.TrimSpace(d.Details["model"])
	product := strings.TrimSpace(d.Details["product"])
	model = strings.ReplaceAll(model, "_", " ")
	product = strings.ReplaceAll(product, "_", " ")
	switch {
	case model != "" && product != "" && model != product:
		return model + " / " + product
	case model != "":
		return model
	case product != "":
		return product
	default:
		return "ADB " + d.Serial
	}
}

func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func eventID(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprint(h, p, "|")
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}
func subtleEqual(a, b string) bool {
	return hmac.Equal([]byte(strings.TrimSpace(strings.ToLower(a))), []byte(strings.TrimSpace(strings.ToLower(b)))) || hmac.Equal([]byte(strings.TrimSpace(a)), []byte(strings.TrimSpace(b)))
}
func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		return strings.TrimSpace(strings.Split(x, ",")[0])
	}
	h := r.RemoteAddr
	if i := strings.LastIndex(h, ":"); i > 0 {
		return h[:i]
	}
	return h
}
func nullInt64Arg(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}
func nullFloatArg(v float64) any {
	if v > 0 {
		return round2(v)
	}
	return nil
}
func clampInt(v, min, max, def int) int {
	if v <= 0 {
		v = def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
func nullStringValue(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}
func nullTimeValue(v sql.NullTime) string {
	if v.Valid {
		return timeValue(v.Time)
	}
	return ""
}
func timeValue(v time.Time) string {
	if v.IsZero() {
		return ""
	}
	return v.Format("2006-01-02 15:04:05")
}
func nullFloatValue(v sql.NullFloat64) float64 {
	if v.Valid {
		return round2(v.Float64)
	}
	return 0
}
func sessionItem(s CollectSession) internalSessionItem {
	item := internalSessionItem{
		ID:             s.ID,
		SessionNo:      s.SessionNo,
		EPayTradeNo:    s.EPayTradeNo,
		EPayOutTradeNo: s.EPayOutTradeNo,
		UID:            s.UID,
		Channel:        s.Channel,
		AccountAlias:   nullStringValue(s.AccountAlias),
		AccountQRCode:  nullStringValue(s.AccountQRCode),
		Amount:         round2(s.Amount),
		Status:         s.Status,
		ExpireAt:       timeValue(s.ExpireAt),
		PaidAt:         nullTimeValue(s.PaidAt),
		CreatedAt:      timeValue(s.CreatedAt),
	}
	if s.AccountID.Valid {
		item.AccountID = s.AccountID.Int64
	}
	if s.NotificationID.Valid {
		item.NotificationID = s.NotificationID.Int64
	}
	return item
}
func eventItem(e NotificationEvent) internalEventItem {
	item := internalEventItem{
		ID:             e.ID,
		EventID:        e.EventID,
		DeviceNo:       e.DeviceNo,
		Channel:        e.Channel,
		PackageName:    nullStringValue(e.PackageName),
		RawTitle:       nullStringValue(e.RawTitle),
		RawText:        nullStringValue(e.RawText),
		ParsedAmount:   nullFloatValue(e.ParsedAmount),
		ParsedPayer:    nullStringValue(e.ParsedPayer),
		NotifyTime:     nullTimeValue(e.NotifyTime),
		ReceivedAt:     timeValue(e.ReceivedAt),
		MatchStatus:    e.MatchStatus,
		MatchedTradeNo: nullStringValue(e.MatchedTradeNo),
	}
	if e.AccountID.Valid {
		item.AccountID = e.AccountID.Int64
	}
	return item
}
func sanitizeSessionStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "waiting", "paid", "canceled", "expired":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return ""
	}
}
func sanitizeMatchStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "matched", "ambiguous", "ignored":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return ""
	}
}

var adminTpl = template.Must(template.New("admin").Funcs(template.FuncMap{
	"valid": func(s sql.NullString) string {
		if s.Valid {
			return s.String
		}
		return ""
	},
	"ftime": func(t sql.NullTime) string {
		if t.Valid {
			return t.Time.Format("01-02 15:04")
		}
		return "-"
	},
	"money": func(f float64) string { return fmt.Sprintf("%.2f", f) },
}).Parse(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<style>{{template "css"}}</style>
</head>
<body>
<div class="shell">
	<aside class="sidebar" aria-label="不凡收款管理导航">
		<div class="brand">
			<div class="brand-mark">不</div>
			<div><strong>不凡收款</strong><span>通知归集 · 订单回写</span></div>
		</div>
		<nav class="nav">
			<a class="{{if eq .View "overview"}}active{{end}}" href="/admin?view=overview{{if .Token}}&token={{.Token}}{{end}}"><span>01</span>总览</a>
			<a class="{{if eq .View "sessions"}}active{{end}}" href="/admin?view=sessions{{if .Token}}&token={{.Token}}{{end}}"><span>02</span>收款会话</a>
			<a class="{{if eq .View "events"}}active{{end}}" href="/admin?view=events{{if .Token}}&token={{.Token}}{{end}}"><span>03</span>通知流水 / 补单</a>
			<a class="{{if eq .View "devices"}}active{{end}}" href="/admin?view=devices{{if .Token}}&token={{.Token}}{{end}}"><span>04</span>监听设备</a>
			<a class="{{if eq .View "accounts"}}active{{end}}" href="/admin?view=accounts{{if .Token}}&token={{.Token}}{{end}}"><span>05</span>收款账号</a>
			<a class="{{if eq .View "payment"}}active{{end}}" href="/admin?view=payment{{if .Token}}&token={{.Token}}{{end}}"><span>06</span>支付台联动</a>
		</nav>
		<div class="side-card">
			<span>安卓通知接口</span>
			<code>{{.PublicBaseURL}}/api/device/notifications</code>
		</div>
		<div class="side-card compact"><span>今日到账</span><strong>¥{{index .Stats "amount"}}</strong></div>
	</aside>

	<main class="workspace">
		<header class="topbar">
			<div>
				<p class="eyebrow">BUFAN LEDGER · {{.View}}</p>
				<h1>{{if eq .View "overview"}}不凡收款管理端{{else if eq .View "sessions"}}收款会话{{else if eq .View "events"}}通知流水 / 人工补单{{else if eq .View "devices"}}监听设备{{else if eq .View "accounts"}}收款账号{{else}}支付台联动{{end}}</h1>
				<p class="sub">{{if eq .View "overview"}}只放概况和入口，具体操作拆到左侧独立页面。{{else if eq .View "sessions"}}处理 EPay 已下单但通知没有自动确认的会话。{{else if eq .View "events"}}处理模糊匹配、漏匹配和需要人工指定订单的通知。{{else if eq .View "devices"}}绑定手机、查看心跳，并启停监听设备。{{else if eq .View "accounts"}}维护微信/支付宝收款账号、设备绑定和收款码。{{else}}查看 EPay 插件与 Go 收款端、安卓端之间的联动流程。{{end}}</p>
			</div>
			<div class="top-actions">
				<a class="ghost" href="/healthz" target="_blank" rel="noreferrer">健康检查</a>
				<a class="ghost" href="/admin?view=events{{if .Token}}&token={{.Token}}{{end}}">处理补单</a>
			</div>
		</header>

		{{if eq .View "overview"}}
		<section class="module">
			<div class="section-head"><div><p class="eyebrow">Overview</p><h2>收款总结</h2></div><p>像微信收款总结一样，按天看今日/昨日/近 7 日，按月看本月/今年/累计，并带明细表。</p></div>
			<div class="summary-grid summary-grid-extended">
				<div class="summary-card primary"><small>今日到账</small><b>¥{{index .Stats "amount"}}</b><span>{{index .Stats "today_orders"}} 笔收款</span></div>
				<div class="summary-card"><small>昨日到账</small><b>¥{{index .Stats "yesterday_amount"}}</b><span>{{index .Stats "yesterday_orders"}} 笔收款</span></div>
				<div class="summary-card"><small>近 7 日到账</small><b>¥{{index .Stats "week_amount"}}</b><span>{{index .Stats "week_orders"}} 笔收款</span></div>
				<div class="summary-card"><small>本月到账</small><b>¥{{index .Stats "month_amount"}}</b><span>{{index .Stats "month_orders"}} 笔 · 均 ¥{{index .Stats "avg_amount"}}</span></div>
				<div class="summary-card"><small>近 30 日到账</small><b>¥{{index .Stats "last30_amount"}}</b><span>{{index .Stats "last30_orders"}} 笔滚动统计</span></div>
				<div class="summary-card"><small>今年到账</small><b>¥{{index .Stats "year_amount"}}</b><span>{{index .Stats "year_orders"}} 笔收款</span></div>
				<div class="summary-card"><small>累计到账</small><b>¥{{index .Stats "total_amount"}}</b><span>{{index .Stats "total_orders"}} 笔 · 最近 {{index .Stats "last_paid_at"}}</span></div>
				<div class="summary-card"><small>待处理会话</small><b>¥{{index .Stats "waiting_amount"}}</b><span>{{index .Stats "waiting"}} 笔待通知/确认</span></div>
			</div>
			<div class="chart-layout">
				<div class="panel chart-panel">
					<div class="chart-head"><div><p class="eyebrow">Daily</p><h3>近 14 天收款柱状表</h3></div><span>按到账日期汇总金额与笔数</span></div>
					<div class="bar-chart daily-chart">{{range .DailySummary}}<div class="bar-item" title="{{.Label}} {{.SubLabel}}：¥{{money .Amount}} / {{.Count}}笔 / 客单¥{{money .Avg}}"><em>¥{{money .Amount}}</em><div class="bar-track"><div class="bar" style="height:{{.Percent}}%"></div></div><span>{{.Label}}</span><small>{{.Count}}笔</small></div>{{end}}</div>
				</div>
				<div class="panel chart-panel">
					<div class="chart-head"><div><p class="eyebrow">Monthly</p><h3>近 12 个月收款柱状表</h3></div><span>按月份汇总金额与笔数</span></div>
					<div class="bar-chart month-chart">{{range .MonthlySummary}}<div class="bar-item" title="{{.SubLabel}}年{{.Label}}：¥{{money .Amount}} / {{.Count}}笔 / 客单¥{{money .Avg}}"><em>¥{{money .Amount}}</em><div class="bar-track"><div class="bar month" style="height:{{.Percent}}%"></div></div><span>{{.Label}}</span><small>{{.Count}}笔</small></div>{{end}}</div>
				</div>
			</div>
			<div class="detail-grid analytics-detail-grid">
				<div class="panel mini-table"><div class="chart-head"><div><p class="eyebrow">Daily Detail</p><h3>每日明细</h3></div></div><table class="summary-table"><thead><tr><th>日期</th><th>笔数</th><th>金额</th><th>客单</th></tr></thead><tbody>{{range .DailySummary}}<tr><td>{{.Label}}<br><small>{{.SubLabel}}</small></td><td>{{.Count}}</td><td>¥{{money .Amount}}</td><td>¥{{money .Avg}}</td></tr>{{end}}</tbody></table></div>
				<div class="panel mini-table"><div class="chart-head"><div><p class="eyebrow">Monthly Detail</p><h3>每月明细</h3></div></div><table class="summary-table"><thead><tr><th>月份</th><th>笔数</th><th>金额</th><th>客单</th></tr></thead><tbody>{{range .MonthlySummary}}<tr><td>{{.SubLabel}}<br><small>{{.Label}}</small></td><td>{{.Count}}</td><td>¥{{money .Amount}}</td><td>¥{{money .Avg}}</td></tr>{{end}}</tbody></table></div>
				<div class="panel ops-panel"><div class="chart-head"><div><p class="eyebrow">Operations</p><h3>运营状态</h3></div></div><div class="ops-grid ops-grid-compact"><div><small>设备</small><b>{{index .Stats "devices"}}</b><span>监听手机</span></div><div><small>账号</small><b>{{index .Stats "accounts"}}</b><span>收款账号</span></div><div><small>待匹配</small><b>{{index .Stats "waiting"}}</b><span>需关注</span></div><div><small>模糊通知</small><b>{{index .Stats "event_ambiguous"}}</b><span>需人工判断</span></div><div><small>今日匹配率</small><b>{{index .Stats "today_match_rate"}}</b><span>{{index .Stats "events"}} 条通知</span></div><div><small>已回写</small><b>{{index .Stats "paid"}}</b><span>累计成功</span></div></div></div>
			</div>
			<div class="quick-grid">
				<a class="quick" href="/admin?view=sessions{{if .Token}}&token={{.Token}}{{end}}"><b>收款会话</b><span>手动确认到账</span></a>
				<a class="quick" href="/admin?view=events{{if .Token}}&token={{.Token}}{{end}}"><b>通知流水 / 补单</b><span>模糊通知人工匹配</span></a>
				<a class="quick" href="/admin?view=devices{{if .Token}}&token={{.Token}}{{end}}"><b>监听设备</b><span>新增手机和密钥</span></a>
				<a class="quick" href="/admin?view=accounts{{if .Token}}&token={{.Token}}{{end}}"><b>收款账号</b><span>配置微信/支付宝账号</span></a>
			</div>
		</section>
		{{else if eq .View "sessions"}}
		<section class="module">
			<div class="section-head"><div><p class="eyebrow">Sessions</p><h2>收款会话</h2></div><p>只显示订单会话，不混设备和账号配置。</p></div>
			<div class="panel table-panel"><table><thead><tr><th>订单</th><th>商户</th><th>渠道</th><th>账号</th><th>金额</th><th>状态</th><th>动作</th></tr></thead><tbody>{{range .Sessions}}<tr><td><strong>{{.EPayTradeNo}}</strong><br><small>{{.EPayOutTradeNo}}</small></td><td>{{.UID}}</td><td><span class="tag">{{.Channel}}</span></td><td>{{valid .AccountAlias}}</td><td>¥{{money .Amount}}</td><td><span class="status status-{{.Status}}">{{.Status}}</span><br><small>{{.ExpireAt.Format "01-02 15:04"}}</small></td><td>{{if ne .Status "paid"}}<form method="post" action="/admin/sessions/manual-paid" class="inline"><input type="hidden" name="token" value="{{$.Token}}"><input type="hidden" name="view" value="sessions"><input type="hidden" name="trade_no" value="{{.EPayTradeNo}}"><input name="buyer_hint" placeholder="备注/付款人"><button>手动确认</button></form>{{else}}<span class="ok">已到账</span>{{end}}</td></tr>{{else}}<tr><td colspan="7" class="empty">暂无收款会话。EPay 下单后会在这里出现。</td></tr>{{end}}</tbody></table></div>
		</section>
		{{else if eq .View "events"}}
		<section class="module">
			<div class="section-head"><div><p class="eyebrow">Events</p><h2>通知流水 / 人工补单</h2></div><p>只处理通知事件，不混会话列表和设备配置。</p></div>
			<div class="panel table-panel"><table><thead><tr><th>时间</th><th>设备</th><th>渠道</th><th>金额</th><th>状态</th><th>通知内容</th><th>人工处理</th></tr></thead><tbody>{{range .Events}}<tr><td>{{.ReceivedAt.Format "01-02 15:04"}}</td><td>{{.DeviceNo}}</td><td><span class="tag">{{.Channel}}</span></td><td>{{if .ParsedAmount.Valid}}¥{{printf "%.2f" .ParsedAmount.Float64}}{{else}}-{{end}}</td><td><span class="status status-{{.MatchStatus}}">{{.MatchStatus}}</span><br><small>{{valid .MatchedTradeNo}}</small></td><td><strong>{{valid .RawTitle}}</strong><br><small>{{valid .RawText}}</small></td><td>{{if ne .MatchStatus "matched"}}<form method="post" action="/admin/events/manual-match" class="inline"><input type="hidden" name="token" value="{{$.Token}}"><input type="hidden" name="view" value="events"><input type="hidden" name="id" value="{{.ID}}"><input name="trade_no" placeholder="EPay系统订单号"><label class="check"><input type="checkbox" name="force" value="1">强制</label><button>补单</button></form><form method="post" action="/admin/events/ignore" class="inline"><input type="hidden" name="token" value="{{$.Token}}"><input type="hidden" name="view" value="events"><input type="hidden" name="id" value="{{.ID}}"><button>忽略</button></form>{{else}}<span class="ok">已匹配</span>{{end}}</td></tr>{{else}}<tr><td colspan="7" class="empty">暂无通知流水。安卓端授权通知后会上报到这里。</td></tr>{{end}}</tbody></table></div>
		</section>
		{{else if eq .View "devices"}}
		<section class="module narrow">
			<div class="section-head"><div><p class="eyebrow">Devices</p><h2>监听设备</h2></div><p>设备页只做手机绑定、密钥和启停。服务器装好 adb 后，可一键导入 USB / 无线 ADB 已连接手机。</p></div>
			{{if .ScanMsg}}<div class="panel"><p class="hint">{{.ScanMsg}}</p></div>{{end}}
			<div class="panel form-panel"><form method="post" action="/admin/devices/scan-adb" class="stack-form"><input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="view" value="devices"><input name="scan_secret" placeholder="批量设备密钥；留空用默认/随机"><button>扫描已连接设备</button></form><p class="hint">会执行 <code>adb devices -l</code>，只导入状态为 device 的手机。未授权设备请先在手机上确认 USB 调试授权。</p></div>
			<div class="panel form-panel"><form method="post" action="/admin/devices" class="stack-form"><input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="view" value="devices"><input name="device_no" placeholder="设备编号，如 phone-a"><input name="device_name" placeholder="设备名称"><input name="secret" placeholder="设备密钥"><button>保存设备</button></form><p class="hint">安卓端通知地址：{{.PublicBaseURL}}/api/device/notifications</p></div>
			<div class="panel list-panel">{{range .Devices}}<div class="line"><div><b>{{.DeviceNo}}</b><span>{{valid .DeviceName}} · 密钥 {{.Secret}} · {{ftime .LastHeartbeatAt}} 心跳</span></div><form method="post" action="/admin/devices/status" class="inline"><input type="hidden" name="token" value="{{$.Token}}"><input type="hidden" name="view" value="devices"><input type="hidden" name="id" value="{{.ID}}">{{if eq .Status 1}}<input type="hidden" name="status" value="0"><button>停用</button>{{else}}<input type="hidden" name="status" value="1"><button>启用</button>{{end}}</form></div>{{else}}<p class="empty">还没有绑定设备。</p>{{end}}</div>
		</section>
		{{else if eq .View "accounts"}}
		<section class="module narrow">
			<div class="section-head"><div><p class="eyebrow">Accounts</p><h2>收款账号</h2></div><p>账号页维护微信/支付宝账号、收款码和码商分配策略。策略属于收款管理端，EPay 只负责下单联动。</p></div>
			<div class="panel form-panel"><form method="post" action="/admin/settings/pick-strategy" class="stack-form"><input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="view" value="accounts"><select name="account_pick_strategy"><option value="least_amount" {{if eq .PickStrategy "least_amount"}}selected{{end}}>金额最少优先</option><option value="least_orders" {{if eq .PickStrategy "least_orders"}}selected{{end}}>订单数最少优先</option><option value="round_robin" {{if eq .PickStrategy "round_robin"}}selected{{end}}>轮询分配</option><option value="random" {{if eq .PickStrategy "random"}}selected{{end}}>随机分配</option></select><button>保存分配策略</button></form><p class="hint">当前策略：{{.PickStrategyLabel}}。下一笔 EPay 订单创建收款会话时立即按该策略分配账号。</p></div>
			<div class="panel form-panel"><form method="post" action="/admin/accounts" class="stack-form"><input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="view" value="accounts"><select name="channel"><option value="wxpay">微信</option><option value="alipay">支付宝</option></select><input name="account_alias" placeholder="账号别名，如 微信-A号"><input name="account_identifier" placeholder="账号标识，可选"><input name="device_no" placeholder="绑定设备编号"><input name="qrcode_url" placeholder="静态收款码图片 URL，可为空"><button>保存账号</button></form></div>
			<div class="panel list-panel">{{range .Accounts}}<div class="line"><div><b>{{.AccountAlias}}</b><span>{{.Channel}} · {{.DeviceNo}}</span></div><form method="post" action="/admin/accounts/status" class="inline"><input type="hidden" name="token" value="{{$.Token}}"><input type="hidden" name="view" value="accounts"><input type="hidden" name="id" value="{{.ID}}">{{if eq .Status 1}}<input type="hidden" name="status" value="0"><button>停用</button>{{else}}<input type="hidden" name="status" value="1"><button>启用</button>{{end}}</form></div>{{else}}<p class="empty">还没有添加收款账号。</p>{{end}}</div>
		</section>
		{{else}}
		<section class="module narrow">
			<div class="section-head"><div><p class="eyebrow">Payment</p><h2>支付台联动</h2></div><p>这里放联动说明，不再和业务表格挤一起。</p></div>
			<div class="panel guide"><ol><li>EPay 通道选择 notifyledger 插件。</li><li>账号、设备、收款码和分配策略只在 Go 收款管理端配置；当前策略：<code>{{.PickStrategyLabel}}</code>（{{.PickStrategy}}）。</li><li>EPay 插件请求 Go 的 <code>/internal/collect-sessions</code> 创建会话，Go 按收款管理端策略分配账号。</li><li>安卓端持续监听，把微信 / 支付宝通知推送到 <code>/api/device/notifications</code>。</li><li>匹配成功后 Go 回调 EPay <code>/notifyledger_internal.php</code> 完成订单。</li></ol></div>
		</section>
		{{end}}
	</main>
</div>
</body>
</html>
{{define "css"}}
:root{--bg:#f7f6f3;--panel:#fff;--text:#37352f;--muted:#787774;--line:#e5e3dd;--line-soft:#f0eee8;--hover:#efedea;--active:#e3e1db;--blue:#2383e2;--green:#0f7b6c;--red:#eb5757;--yellow:#dfab01;--radius:8px;--space-xs:4px;--space-sm:8px;--space-md:12px;--space-lg:16px;--space-xl:24px;--space-2xl:32px;--space-3xl:48px}*{box-sizing:border-box}html{scroll-behavior:smooth}body{margin:0;background:var(--bg);color:var(--text);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Noto Sans SC","Microsoft YaHei",sans-serif}.shell{display:grid;grid-template-columns:260px minmax(0,1fr);min-height:100vh}.sidebar{position:sticky;top:0;height:100vh;padding:var(--space-xl);border-right:1px solid var(--line);background:rgba(247,246,243,.96);display:flex;flex-direction:column;gap:var(--space-xl)}.brand{display:flex;gap:var(--space-md);align-items:center}.brand-mark{width:34px;height:34px;border:1px solid var(--line);border-radius:var(--radius);display:grid;place-items:center;background:var(--panel);font-weight:700}.brand strong,.brand span{display:block}.brand span{margin-top:2px;color:var(--muted);font-size:12px}.nav{display:flex;flex-direction:column;gap:var(--space-xs)}.nav a,.ghost,.quick{color:var(--text);text-decoration:none;border-radius:6px;padding:9px 10px;transition:background-color .15s ease,color .15s ease}.nav a{display:flex;gap:var(--space-sm);align-items:center}.nav a span{color:var(--muted);font-size:11px}.nav a:hover,.ghost:hover,.quick:hover{background:var(--hover)}.nav a:active,.ghost:active,.quick:active{background:var(--active)}.nav a.active{background:var(--active);font-weight:600}.side-card{border:1px solid var(--line);border-radius:var(--radius);background:var(--panel);padding:var(--space-md);display:flex;flex-direction:column;gap:var(--space-sm);box-shadow:0 1px 2px rgba(55,53,47,.04)}.side-card span{color:var(--muted);font-size:12px}.side-card code{font-size:12px;line-height:1.5;word-break:break-all;color:var(--text)}.side-card strong{font-size:22px}.compact{margin-top:auto}.workspace{max-width:1220px;width:100%;padding:var(--space-2xl);display:flex;flex-direction:column;gap:var(--space-2xl)}.topbar{display:flex;justify-content:space-between;gap:var(--space-xl);align-items:flex-start}.top-actions{display:flex;gap:var(--space-sm);flex-wrap:wrap}.eyebrow{margin:0 0 var(--space-sm);font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--muted)}h1,h2,h3,p{margin-top:0}h1{font-size:30px;line-height:1.2;margin-bottom:var(--space-sm)}h2{font-size:19px;line-height:1.25;margin-bottom:0}.sub,.section-head p,small,.hint{color:var(--muted)}.sub{max-width:68ch;margin-bottom:0}.module{display:flex;flex-direction:column;gap:var(--space-md)}.module.narrow{max-width:920px}.section-head{display:flex;justify-content:space-between;gap:var(--space-xl);align-items:end}.section-head p{max-width:48ch;margin-bottom:0}.metric-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:var(--space-md)}.quick-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:var(--space-md);margin-top:var(--space-md)}.metric,.panel,.quick{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:0 1px 2px rgba(55,53,47,.04);position:relative}.metric{padding:var(--space-lg)}.quick{display:flex;flex-direction:column;gap:var(--space-sm);padding:var(--space-lg)}.quick span{color:var(--muted);font-size:13px}.metric:before,.summary-card:before,.panel:before,.line:before,.quick:before{content:"⋮⋮";position:absolute;left:8px;top:10px;color:#b8b4ad;font-size:12px;letter-spacing:-2px;opacity:0;transition:opacity .15s ease;pointer-events:none}.metric:hover:before,.summary-card:hover:before,.panel:hover:before,.line:hover:before,.quick:hover:before{opacity:1}.metric small,.metric span{display:block;color:var(--muted)}.metric b{display:block;font-size:24px;line-height:1.2;margin:var(--space-sm) 0}.summary-grid{display:grid;grid-template-columns:1.25fr repeat(3,1fr);gap:var(--space-md)}.summary-grid-extended{grid-template-columns:repeat(4,minmax(0,1fr))}.summary-card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);box-shadow:0 1px 2px rgba(55,53,47,.04);padding:var(--space-lg);position:relative}.summary-card.primary{background:#fbfaf8}.summary-card small,.summary-card span{display:block;color:var(--muted)}.summary-card b{display:block;font-size:28px;line-height:1.15;margin:var(--space-sm) 0}.chart-layout{display:grid;grid-template-columns:1.3fr 1fr;gap:var(--space-md)}.chart-panel{min-width:0}.chart-head{display:flex;justify-content:space-between;gap:var(--space-lg);align-items:flex-start;margin-bottom:var(--space-lg)}.chart-head h3{font-size:17px;margin:0}.chart-head span{color:var(--muted);font-size:12px}.bar-chart{height:260px;display:grid;align-items:end;gap:var(--space-sm);padding-top:var(--space-sm)}.daily-chart{grid-template-columns:repeat(14,minmax(28px,1fr))}.month-chart{grid-template-columns:repeat(12,minmax(32px,1fr))}.bar-item{height:100%;min-width:0;display:grid;grid-template-rows:auto 1fr auto auto;gap:var(--space-xs);text-align:center}.bar-item em{font-style:normal;font-size:11px;color:var(--text);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.bar-track{height:190px;border:1px solid var(--line-soft);border-radius:6px;background:#fbfaf8;display:flex;align-items:end;overflow:hidden}.bar{width:100%;min-height:0;background:var(--green);border-radius:6px 6px 0 0}.bar.month{background:var(--blue)}.bar-item span,.bar-item small{font-size:11px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.detail-grid{display:grid;grid-template-columns:1fr 1fr;gap:var(--space-md)}.analytics-detail-grid{grid-template-columns:1fr 1fr .85fr}.mini-table{overflow:auto}.summary-table{min-width:0}.ops-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:var(--space-md)}.ops-grid-compact{grid-template-columns:1fr}.ops-grid div{border:1px solid var(--line-soft);border-radius:var(--radius);padding:var(--space-md);background:#fbfaf8}.ops-grid small,.ops-grid span{display:block;color:var(--muted)}.ops-grid b{display:block;font-size:22px;margin:var(--space-xs) 0}.panel{padding:var(--space-lg)}.table-panel{padding:0;overflow:auto}table{width:100%;min-width:880px;border-collapse:collapse;background:var(--panel)}th,td{text-align:left;padding:12px;border-top:1px solid var(--line-soft);vertical-align:top}th{background:#fbfaf8;color:var(--muted);font-weight:600;border-top:0}tr:hover td{background:var(--hover)}input,select{border:1px solid var(--line);border-radius:6px;padding:8px 10px;background:#fff;color:var(--text);font:inherit}input:focus,select:focus{outline:none;border-color:rgba(35,131,226,.45);box-shadow:0 0 0 3px rgba(35,131,226,.13)}button{border:1px solid var(--line);background:transparent;border-radius:6px;padding:8px 10px;color:var(--text);font:inherit;transition:background-color .15s ease,color .15s ease,border-color .15s ease;white-space:nowrap;cursor:pointer}button:hover{background:var(--hover)}button:active{background:var(--active)}.inline{display:flex;gap:var(--space-sm);align-items:center;flex-wrap:wrap;margin:0 0 var(--space-sm)}.inline input:not([type]),.inline input[type=text]{min-width:150px;max-width:220px}.check{display:flex;align-items:center;gap:var(--space-xs);font-size:12px;color:var(--muted)}.tag,.status{display:inline-flex;align-items:center;border:1px solid var(--line);border-radius:6px;padding:2px 7px;font-size:12px;background:#fbfaf8;color:var(--muted)}.status-paid,.status-matched{color:var(--green);background:#edf7f3}.status-waiting,.status-pending{color:var(--yellow);background:#fbf3db}.status-ambiguous{color:var(--red);background:#fdebec}.ok{color:var(--green)}.empty{color:var(--muted);padding:var(--space-xl);text-align:center}.form-panel{display:flex;flex-direction:column;gap:var(--space-md)}.stack-form{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:var(--space-sm)}.stack-form button{justify-self:start}.list-panel{padding:0}.line{position:relative;display:flex;justify-content:space-between;gap:var(--space-lg);align-items:center;border-top:1px solid var(--line-soft);padding:var(--space-md) var(--space-md) var(--space-md) var(--space-xl)}.line:first-child{border-top:0}.line b,.line span{display:block}.line span{font-size:12px;color:var(--muted);margin-top:2px}.line:hover{background:var(--hover)}.guide ol{margin:0;padding-left:22px;line-height:1.9}.guide code{background:#fbfaf8;border:1px solid var(--line);border-radius:6px;padding:2px 6px}@media(max-width:980px){.shell{display:block}.sidebar{position:sticky;height:auto;z-index:2;padding:var(--space-md);border-right:0;border-bottom:1px solid var(--line)}.brand,.side-card{display:none}.nav{flex-direction:row;overflow:auto;white-space:nowrap}.workspace{padding:var(--space-xl) var(--space-md)}.topbar,.section-head{display:block}.top-actions{margin-top:var(--space-md)}.summary-grid,.summary-grid-extended,.chart-layout,.detail-grid,.analytics-detail-grid{grid-template-columns:1fr}.stack-form{grid-template-columns:1fr}.module.narrow{max-width:none}}@media(max-width:640px){.workspace{padding:var(--space-lg) var(--space-md);gap:var(--space-xl)}h1{font-size:25px}.metric-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.inline input:not([type]),.inline input[type=text]{max-width:160px}.line{display:block}.line .inline{margin-top:var(--space-sm)}}
{{end}}`))
var payTpl = template.Must(template.New("pay").Funcs(template.FuncMap{"valid": func(s sql.NullString) string {
	if s.Valid {
		return s.String
	}
	return ""
}, "money": func(f float64) string { return fmt.Sprintf("%.2f", f) }}).Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title><style>body{margin:0;background:#f7f6f3;color:#37352f;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Noto Sans SC",sans-serif}.box{max-width:420px;margin:8vh auto;padding:24px;background:#fff;border:1px solid #e5e3dd;border-radius:8px;box-shadow:0 1px 2px rgba(55,53,47,.04)}h1{font-size:24px;margin:0 0 8px}.sub{color:#787774}.amount{font-size:36px;font-weight:700;margin:20px 0}.qr{border:1px solid #e5e3dd;border-radius:8px;padding:12px;text-align:center;background:#fbfaf8}.qr img{max-width:260px;width:100%;border-radius:6px}.hint{color:#787774;font-size:13px;line-height:1.7}.ok{color:#0f7b6c}.bad{color:#eb5757}.meta{border-top:1px solid #f0eee8;margin-top:18px;padding-top:14px;font-size:12px;color:#787774}</style></head><body><div class="box"><h1>不凡收款台</h1><div class="sub">订单 {{.S.EPayTradeNo}}</div><div class="amount">¥{{money .S.Amount}}</div>{{if eq .S.Status "paid"}}<p class="ok">已到账，请返回商户页面。</p>{{else if .Expired}}<p class="bad">订单已过期，请重新发起支付。</p>{{else}}<div class="qr">{{if valid .S.AccountQRCode}}<img src="{{valid .S.AccountQRCode}}" alt="收款码">{{else}}<p class="hint">当前账号未配置静态收款码。请按金额向收款账号付款，通知到账后会自动确认。</p>{{end}}</div><p class="hint">请使用 {{.S.Channel}} 支付，金额必须与页面一致。支付后保持页面不动，系统会通过手机通知自动确认。</p>{{end}}<div class="meta">收款账号：{{valid .S.AccountAlias}}<br>商户订单：{{.S.EPayOutTradeNo}}<br>过期时间：{{.S.ExpireAt.Format "2006-01-02 15:04:05"}}</div></div></body></html>`))
