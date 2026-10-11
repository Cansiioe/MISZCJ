package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type Account struct {
	ID         int    `json:"id"`
	Email      string `json:"email"`
	APIKey     string `json:"api_key"`
	Source     string `json:"source"`
	AddedAt    string `json:"added_at"`
	IsDead     bool   `json:"is_dead"`
	DeadReason string `json:"dead_reason,omitempty"`
	DeadAt     string `json:"dead_at,omitempty"`

	// 账号密码只用于登录 console 查计划与余额，永不返回给前端
	Password    string `json:"-"`
	HasPassword bool   `json:"has_password"`

	// 实测的每分钟请求上限，用于加权轮询（0 = 未知，按默认权重算）
	RateLimitReq int `json:"rate_limit_req"`

	// 以下为查询时聚合出的运行期指标，不落库
	Requests    int64  `json:"requests"`
	TotalTokens int64  `json:"total_tokens"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

type RouterKey struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
	Active    bool   `json:"active"`

	// 分发策略
	ExpiresAt     string `json:"expires_at,omitempty"` // 空 = 永不过期
	QuotaTokens   int64  `json:"quota_tokens"`         // 0 = 不限
	UsedTokens    int64  `json:"used_tokens"`
	AllowedModels string `json:"allowed_models,omitempty"` // 逗号分隔，空 = 跟随全局白名单
	AllowedIPs    string `json:"allowed_ips,omitempty"`    // 逗号分隔，空 = 不限来源
	LastUsedAt    string `json:"last_used_at,omitempty"`
	Requests      int64  `json:"requests"`

	// 派生状态，前端直接用：ok / expired / over_quota / disabled
	State string `json:"state,omitempty"`
}

// RequestLog 单次调用审计记录。
type RequestLog struct {
	ID               int    `json:"id"`
	Ts               string `json:"ts"`
	AccountID        int    `json:"account_id"`
	AccountEmail     string `json:"account_email,omitempty"`
	KeyPrefix        string `json:"key_prefix,omitempty"`
	KeyLabel         string `json:"key_label,omitempty"`
	IP               string `json:"ip,omitempty"`
	Model            string `json:"model"`
	UpstreamModel    string `json:"upstream_model,omitempty"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	Status           int    `json:"status"`
	ErrorCode        string `json:"error_code,omitempty"`
	ErrorMessage     string `json:"error_message,omitempty"`
	LatencyMs        int    `json:"latency_ms"`
}

// LogQuery 日志筛选条件。
type LogQuery struct {
	KeyPrefix string
	Model     string
	Status    string // ok / fail / "" 全部
	Since     string
	Limit     int
	Offset    int
}

type FarmRun struct {
	ID          int    `json:"id"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at"`
	Status      string `json:"status"`
	MaxAttempts int    `json:"max_attempts"`
	PID         int    `json:"pid"`
	NewAccounts int    `json:"new_accounts"`
	LogPath     string `json:"log_path"`
}

type UsageRow struct {
	Date             string `json:"date"`
	Model            string `json:"model"`
	Requests         int    `json:"requests"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
}

type Stats struct {
	TotalAccounts int     `json:"total_accounts"`
	AliveAccounts int     `json:"alive_accounts"`
	DeadAccounts  int     `json:"dead_accounts"`
	TotalRequests int64   `json:"total_requests"`
	TotalTokens   int64   `json:"total_tokens"`
	TotalKeys     int     `json:"total_keys"`
	ActiveKeys    int     `json:"active_keys"`
	TodayRequests int64   `json:"today_requests"`
	TodayTokens   int64   `json:"today_tokens"`
	SuccessRate   float64 `json:"success_rate"`
	AvgLatencyMs  int     `json:"avg_latency_ms"`
}

type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT UNIQUE NOT NULL,
  api_key TEXT NOT NULL,
  password TEXT,
  added_at TEXT DEFAULT (datetime('now')),
  source TEXT DEFAULT 'manual',
  is_dead INTEGER DEFAULT 0,
  dead_reason TEXT,
  dead_at TEXT
);
CREATE TABLE IF NOT EXISTS router_keys (
  key TEXT PRIMARY KEY,
  label TEXT,
  created_at TEXT DEFAULT (datetime('now')),
  active INTEGER DEFAULT 1,
  expires_at TEXT,
  quota_tokens INTEGER DEFAULT 0,
  used_tokens INTEGER DEFAULT 0,
  allowed_models TEXT,
  allowed_ips TEXT,
  last_used_at TEXT,
  requests INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT
);
CREATE TABLE IF NOT EXISTS farm_config (
  key TEXT PRIMARY KEY,
  value TEXT
);
CREATE TABLE IF NOT EXISTS farm_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at TEXT,
  ended_at TEXT,
  status TEXT,
  max_attempts INTEGER,
  pid INTEGER,
  new_accounts INTEGER DEFAULT 0,
  log_path TEXT
);
CREATE TABLE IF NOT EXISTS request_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT DEFAULT (datetime('now')),
  account_id INTEGER,
  key_prefix TEXT,
  ip TEXT,
  model TEXT,
  upstream_model TEXT,
  prompt_tokens INTEGER,
  completion_tokens INTEGER,
  total_tokens INTEGER,
  status_code INTEGER,
  error_code TEXT,
  error_message TEXT,
  latency_ms INTEGER
);
CREATE INDEX IF NOT EXISTS idx_logs_ts ON request_logs(ts DESC);
CREATE TABLE IF NOT EXISTS usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id INTEGER,
  date TEXT,
  model TEXT,
  prompt_tokens INTEGER DEFAULT 0,
  completion_tokens INTEGER DEFAULT 0,
  requests INTEGER DEFAULT 0
);`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// 老库补列：CREATE TABLE IF NOT EXISTS 不会给已存在的表新增字段
	if err := s.addColumns(); err != nil {
		return err
	}
	// 依赖新列的索引必须等补列之后再建，否则老库会报 no such column
	s.db.Exec("CREATE INDEX IF NOT EXISTS idx_logs_key ON request_logs(key_prefix)")

	// Bootstrap admin password once: ADMIN_PASSWORD env, else random (logged once).
	// Never ship a fixed weak default password in source.
	var cnt int
	s.db.QueryRow("SELECT COUNT(*) FROM settings WHERE key='auth_password'").Scan(&cnt)
	if cnt == 0 {
		pw := os.Getenv("ADMIN_PASSWORD")
		if pw == "" {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err != nil {
				log.Fatalf("生成管理员密码失败: %v", err)
			}
			pw = hex.EncodeToString(b)
			log.Printf("已生成初始管理员密码: %s（请在管理界面修改，或在首次启动前设置 ADMIN_PASSWORD）", pw)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("管理员密码哈希失败: %v", err)
		}
		s.db.Exec("INSERT INTO settings(key,value) VALUES('auth_password',?)", string(hash))
	}

	// Set default round-robin proxy limit to 1
	var cntLimit int
	s.db.QueryRow("SELECT COUNT(*) FROM settings WHERE key='proxy_limit'").Scan(&cntLimit)
	if cntLimit == 0 {
		s.db.Exec("INSERT INTO settings(key,value) VALUES('proxy_limit', '1')")
	}
	// Default farm config (generic placeholders only — set real values via Admin UI / .env)
	defaults := map[string]string{
		"OTP_PROVIDER":            "mailapi",
		"IMAP_HOST":               "imap.example.com",
		"IMAP_PORT":               "993",
		"MAILAPI_BASE":          "https://em.example.com/api/v1",
		"MAILAPI_KEY":           "",
		"MAILAPI_DOMAIN_ID":     "1",
		"MAILAPI_EXPIRES_HOURS": "24",
		"MAILAPI_PERMANENT":     "0",
		"SELFHOST_BASE":          "https://mail.example.com",
		"SELFHOST_ADMIN":         "admin@example.com",
		"SELFHOST_PASSWORD":      "",
		"SELFHOST_CREATE_USER":   "1",
		"MAX_ATTEMPTS":            "50",
		"CONCURRENCY":             "1",
	}
	for k, v := range defaults {
		s.db.Exec("INSERT OR IGNORE INTO farm_config(key,value) VALUES(?,?)", k, v)
	}

	// Default router key
	var keyCnt int
	s.db.QueryRow("SELECT COUNT(*) FROM router_keys").Scan(&keyCnt)
	if keyCnt == 0 {
		s.AddRouterKey("default")
	}

	return nil
}

// addColumns 给已存在的表补齐新增字段（SQLite 没有 ADD COLUMN IF NOT EXISTS）。
func (s *Store) addColumns() error {
	wanted := []struct{ table, column, decl string }{
		{"accounts", "password", "TEXT"},
		{"accounts", "rate_limit_req", "INTEGER DEFAULT 0"},
		{"router_keys", "expires_at", "TEXT"},
		{"router_keys", "quota_tokens", "INTEGER DEFAULT 0"},
		{"router_keys", "used_tokens", "INTEGER DEFAULT 0"},
		{"router_keys", "allowed_models", "TEXT"},
		{"router_keys", "allowed_ips", "TEXT"},
		{"router_keys", "last_used_at", "TEXT"},
		{"router_keys", "requests", "INTEGER DEFAULT 0"},
		{"request_logs", "key_prefix", "TEXT"},
		{"request_logs", "ip", "TEXT"},
		{"request_logs", "upstream_model", "TEXT"},
		{"request_logs", "total_tokens", "INTEGER"},
		{"request_logs", "error_message", "TEXT"},
		{"request_logs", "latency_ms", "INTEGER"},
	}
	for _, w := range wanted {
		ok, err := s.hasColumn(w.table, w.column)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", w.table, w.column, w.decl)); err != nil {
			return fmt.Errorf("补列 %s.%s 失败: %w", w.table, w.column, err)
		}
		log.Printf("数据库迁移: %s 增加字段 %s", w.table, w.column)
	}
	return nil
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) GetAliveAccounts() []Account {
	rows, err := s.db.Query(`SELECT id,email,api_key,source,added_at,COALESCE(rate_limit_req,0)
		FROM accounts WHERE is_dead=0 ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var accounts []Account
	for rows.Next() {
		var a Account
		rows.Scan(&a.ID, &a.Email, &a.APIKey, &a.Source, &a.AddedAt, &a.RateLimitReq)
		accounts = append(accounts, a)
	}
	return accounts
}

func (s *Store) GetDeadAccounts() []Account {
	rows, err := s.db.Query("SELECT id,email,api_key,source,added_at,dead_reason,dead_at FROM accounts WHERE is_dead=1 ORDER BY dead_at DESC")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var accounts []Account
	for rows.Next() {
		var a Account
		a.IsDead = true
		rows.Scan(&a.ID, &a.Email, &a.APIKey, &a.Source, &a.AddedAt, &a.DeadReason, &a.DeadAt)
		accounts = append(accounts, a)
	}
	return accounts
}

func (s *Store) MarkDead(id int, reason string) {
	s.db.Exec("UPDATE accounts SET is_dead=1, dead_reason=?, dead_at=datetime('now') WHERE id=?", reason, id)
}

func (s *Store) ReviveAccount(id int) {
	s.db.Exec("UPDATE accounts SET is_dead=0, dead_reason=NULL, dead_at=NULL WHERE id=?", id)
}

func (s *Store) DeleteAccount(id int) {
	s.db.Exec("DELETE FROM accounts WHERE id=?", id)
}

func (s *Store) AddAccount(email, apiKey, source string) error {
	return s.AddAccountFull(email, apiKey, "", source)
}

// AddAccountFull 同时保存密码（用于登录 console 查计划与余额）。
// 已存在的账号只补密码，不覆盖原有密钥。
func (s *Store) AddAccountFull(email, apiKey, password, source string) error {
	_, err := s.db.Exec(`INSERT INTO accounts(email,api_key,password,source) VALUES(?,?,?,?)
		ON CONFLICT(email) DO UPDATE SET
		  password = CASE WHEN excluded.password != '' THEN excluded.password ELSE accounts.password END`,
		email, apiKey, password, source)
	return err
}

func (s *Store) GetSettings() map[string]string {
	rows, _ := s.db.Query("SELECT key,value FROM settings")
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		m[k] = v
	}
	return m
}

func (s *Store) GetFarmConfig() map[string]string {
	rows, _ := s.db.Query("SELECT key,value FROM farm_config")
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		m[k] = v
	}
	return m
}

func (s *Store) SaveFarmConfig(cfg map[string]string) error {
	for k, v := range cfg {
		if _, err := s.db.Exec("INSERT OR REPLACE INTO farm_config(key,value) VALUES(?,?)", k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetFarmRuns(limit int) []FarmRun {
	rows, err := s.db.Query("SELECT id,COALESCE(started_at,''),COALESCE(ended_at,''),COALESCE(status,''),COALESCE(max_attempts,0),COALESCE(pid,0),COALESCE(new_accounts,0),COALESCE(log_path,'') FROM farm_runs ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var runs []FarmRun
	for rows.Next() {
		var r FarmRun
		rows.Scan(&r.ID, &r.StartedAt, &r.EndedAt, &r.Status, &r.MaxAttempts, &r.PID, &r.NewAccounts, &r.LogPath)
		runs = append(runs, r)
	}
	return runs
}

func (s *Store) StartFarmRun(pid, maxAttempts int, logPath string) int64 {
	res, err := s.db.Exec("INSERT INTO farm_runs(started_at,status,max_attempts,pid,log_path) VALUES(datetime('now'),'running',?,?,?)", maxAttempts, pid, logPath)
	if err != nil {
		log.Printf("StartFarmRun 出错: %v", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *Store) EndFarmRun(id int64, status string, newAccounts int) {
	s.db.Exec("UPDATE farm_runs SET ended_at=datetime('now'),status=?,new_accounts=? WHERE id=?", status, newAccounts, id)
}

// LogEntry 一次调用的完整上下文，由 proxy 填充后落库。
type LogEntry struct {
	AccountID        int
	KeyPrefix        string
	IP               string
	Model            string
	UpstreamModel    string
	PromptTokens     int
	CompletionTokens int
	Status           int
	ErrorCode        string
	ErrorMessage     string
	LatencyMs        int
}

func (s *Store) LogRequest(e LogEntry) {
	total := e.PromptTokens + e.CompletionTokens
	s.db.Exec(`INSERT INTO request_logs
		(account_id,key_prefix,ip,model,upstream_model,prompt_tokens,completion_tokens,total_tokens,status_code,error_code,error_message,latency_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.AccountID, e.KeyPrefix, e.IP, e.Model, e.UpstreamModel,
		e.PromptTokens, e.CompletionTokens, total, e.Status, e.ErrorCode, e.ErrorMessage, e.LatencyMs)

	date := time.Now().Format("2006-01-02")
	s.db.Exec(`INSERT INTO usage(account_id,date,model,prompt_tokens,completion_tokens,requests) VALUES(?,?,?,?,?,1)
		ON CONFLICT DO UPDATE SET prompt_tokens=prompt_tokens+excluded.prompt_tokens,
		completion_tokens=completion_tokens+excluded.completion_tokens, requests=requests+1`,
		e.AccountID, date, e.Model, e.PromptTokens, e.CompletionTokens)
}

// QueryLogs 按条件分页查询调用日志，返回当页数据与命中总数。
func (s *Store) QueryLogs(q LogQuery) ([]RequestLog, int) {
	where := []string{"1=1"}
	args := []any{}
	if q.KeyPrefix != "" {
		where = append(where, "l.key_prefix = ?")
		args = append(args, q.KeyPrefix)
	}
	if q.Model != "" {
		where = append(where, "l.model = ?")
		args = append(args, q.Model)
	}
	switch q.Status {
	case "ok":
		where = append(where, "l.status_code >= 200 AND l.status_code < 300")
	case "fail":
		where = append(where, "l.status_code >= 400")
	}
	if q.Since != "" {
		where = append(where, "l.ts >= ?")
		args = append(args, q.Since)
	}
	cond := strings.Join(where, " AND ")

	var total int
	s.db.QueryRow("SELECT COUNT(*) FROM request_logs l WHERE "+cond, args...).Scan(&total)

	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	query := `SELECT l.id,l.ts,COALESCE(l.account_id,0),COALESCE(a.email,''),
		COALESCE(l.key_prefix,''),COALESCE(k.label,''),COALESCE(l.ip,''),
		COALESCE(l.model,''),COALESCE(l.upstream_model,''),
		COALESCE(l.prompt_tokens,0),COALESCE(l.completion_tokens,0),
		COALESCE(l.total_tokens, COALESCE(l.prompt_tokens,0) + COALESCE(l.completion_tokens,0)),
		COALESCE(l.status_code,0),COALESCE(l.error_code,''),COALESCE(l.error_message,''),
		COALESCE(l.latency_ms,0)
		FROM request_logs l
		LEFT JOIN accounts a ON a.id = l.account_id
		LEFT JOIN router_keys k ON substr(k.key,1,12) = l.key_prefix
		WHERE ` + cond + ` ORDER BY l.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, q.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0
	}
	defer rows.Close()
	var out []RequestLog
	for rows.Next() {
		var r RequestLog
		rows.Scan(&r.ID, &r.Ts, &r.AccountID, &r.AccountEmail, &r.KeyPrefix, &r.KeyLabel,
			&r.IP, &r.Model, &r.UpstreamModel, &r.PromptTokens, &r.CompletionTokens,
			&r.TotalTokens, &r.Status, &r.ErrorCode, &r.ErrorMessage, &r.LatencyMs)
		out = append(out, r)
	}
	return out, total
}

func (s *Store) ClearLogs() int64 {
	res, err := s.db.Exec("DELETE FROM request_logs")
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// CleanupLogs 删除超过保留期的日志，返回删除行数。
// keepDays <= 0 表示不清理（想永久保留审计记录时这样配）。
func (s *Store) CleanupLogs(keepDays int) int64 {
	if keepDays <= 0 {
		return 0
	}
	res, err := s.db.Exec("DELETE FROM request_logs WHERE ts < datetime('now', ?)",
		fmt.Sprintf("-%d days", keepDays))
	if err != nil {
		log.Printf("日志清理失败: %v", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// SeriesPoint 每日用量趋势的一个点。
type SeriesPoint struct {
	Date     string `json:"date"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
	Failures int64  `json:"failures"`
}

// GetDailySeries 返回最近 N 天的每日请求量 / Token / 失败数。
func (s *Store) GetDailySeries(days int) []SeriesPoint {
	if days <= 0 || days > 90 {
		days = 14
	}
	rows, err := s.db.Query(`
		SELECT date(ts) AS d, COUNT(*),
		       COALESCE(SUM(COALESCE(total_tokens, COALESCE(prompt_tokens,0) + COALESCE(completion_tokens,0))),0),
		       SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END)
		FROM request_logs
		WHERE ts >= datetime('now', ?)
		GROUP BY d ORDER BY d`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SeriesPoint
	for rows.Next() {
		var p SeriesPoint
		rows.Scan(&p.Date, &p.Requests, &p.Tokens, &p.Failures)
		out = append(out, p)
	}
	return out
}

func (s *Store) GetUsageSummary() []UsageRow {
	rows, err := s.db.Query("SELECT date,model,SUM(requests),SUM(prompt_tokens),SUM(completion_tokens) FROM usage GROUP BY date,model ORDER BY date DESC, model LIMIT 100")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []UsageRow
	for rows.Next() {
		var r UsageRow
		rows.Scan(&r.Date, &r.Model, &r.Requests, &r.PromptTokens, &r.CompletionTokens)
		result = append(result, r)
	}
	return result
}

type AccountUsageRow struct {
	AccountID        int    `json:"account_id"`
	Email            string `json:"email"`
	Requests         int    `json:"requests"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	LastUsed         string `json:"last_used"`
}

func (s *Store) GetUsageByAccount() []AccountUsageRow {
	rows, err := s.db.Query(`
		SELECT u.account_id, COALESCE(a.email,'deleted'), 
		       SUM(u.requests), SUM(u.prompt_tokens), SUM(u.completion_tokens),
		       MAX(u.date)
		FROM usage u
		LEFT JOIN accounts a ON a.id = u.account_id
		GROUP BY u.account_id
		ORDER BY SUM(u.requests) DESC
		LIMIT 100`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []AccountUsageRow
	for rows.Next() {
		var r AccountUsageRow
		rows.Scan(&r.AccountID, &r.Email, &r.Requests, &r.PromptTokens, &r.CompletionTokens, &r.LastUsed)
		r.TotalTokens = r.PromptTokens + r.CompletionTokens
		result = append(result, r)
	}
	return result
}

func (s *Store) GetStats() Stats {
	var st Stats
	s.db.QueryRow("SELECT COUNT(*) FROM accounts").Scan(&st.TotalAccounts)
	s.db.QueryRow("SELECT COUNT(*) FROM accounts WHERE is_dead=0").Scan(&st.AliveAccounts)
	s.db.QueryRow("SELECT COUNT(*) FROM accounts WHERE is_dead=1").Scan(&st.DeadAccounts)
	s.db.QueryRow("SELECT COUNT(*) FROM request_logs").Scan(&st.TotalRequests)
	// 老记录没有 total_tokens，回退到 prompt+completion 相加
	s.db.QueryRow("SELECT COALESCE(SUM(COALESCE(total_tokens, COALESCE(prompt_tokens,0) + COALESCE(completion_tokens,0))),0) FROM request_logs").Scan(&st.TotalTokens)
	s.db.QueryRow("SELECT COUNT(*) FROM router_keys").Scan(&st.TotalKeys)
	s.db.QueryRow("SELECT COUNT(*) FROM router_keys WHERE active=1").Scan(&st.ActiveKeys)

	today := time.Now().Format("2006-01-02")
	s.db.QueryRow("SELECT COUNT(*) FROM request_logs WHERE date(ts)=?", today).Scan(&st.TodayRequests)
	s.db.QueryRow("SELECT COALESCE(SUM(COALESCE(total_tokens, COALESCE(prompt_tokens,0) + COALESCE(completion_tokens,0))),0) FROM request_logs WHERE date(ts)=?", today).Scan(&st.TodayTokens)

	var okCount, allCount int64
	s.db.QueryRow("SELECT COUNT(*) FROM request_logs WHERE status_code >= 200 AND status_code < 300").Scan(&okCount)
	allCount = st.TotalRequests
	if allCount > 0 {
		st.SuccessRate = float64(okCount) / float64(allCount)
	}
	var avg sql.NullFloat64
	s.db.QueryRow("SELECT AVG(latency_ms) FROM request_logs WHERE latency_ms > 0").Scan(&avg)
	if avg.Valid {
		st.AvgLatencyMs = int(avg.Float64)
	}
	return st
}

func (s *Store) GetRouterKeys() []RouterKey {
	rows, err := s.db.Query(`SELECT key,label,created_at,active,COALESCE(expires_at,''),
		COALESCE(quota_tokens,0),COALESCE(used_tokens,0),COALESCE(allowed_models,''),
		COALESCE(allowed_ips,''),COALESCE(last_used_at,''),COALESCE(requests,0)
		FROM router_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var keys []RouterKey
	for rows.Next() {
		var k RouterKey
		rows.Scan(&k.Key, &k.Label, &k.CreatedAt, &k.Active, &k.ExpiresAt,
			&k.QuotaTokens, &k.UsedTokens, &k.AllowedModels, &k.AllowedIPs,
			&k.LastUsedAt, &k.Requests)
		k.State = keyState(k)
		keys = append(keys, k)
	}
	return keys
}

// keyState 推导密钥状态，前端直接展示，不必自己算。
func keyState(k RouterKey) string {
	if !k.Active {
		return "disabled"
	}
	if k.ExpiresAt != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", k.ExpiresAt); err == nil && time.Now().After(t) {
			return "expired"
		}
	}
	if k.QuotaTokens > 0 && k.UsedTokens >= k.QuotaTokens {
		return "over_quota"
	}
	return "ok"
}

func (s *Store) AddRouterKey(label string) string {
	return s.AddRouterKeyWith(label, "", 0, "", "")
}

// AddRouterKeyWith 建密钥并写入分发策略。expiresAt 为空表示永不过期。
func (s *Store) AddRouterKeyWith(label, expiresAt string, quotaTokens int64, allowedModels, allowedIPs string) string {
	b := make([]byte, 20)
	rand.Read(b)
	key := "nm-" + hex.EncodeToString(b)
	s.db.Exec(`INSERT INTO router_keys(key,label,expires_at,quota_tokens,allowed_models,allowed_ips)
		VALUES(?,?,?,?,?,?)`, key, label, expiresAt, quotaTokens, allowedModels, allowedIPs)
	return key
}

func (s *Store) UpdateRouterKey(key, label, expiresAt string, quotaTokens int64, allowedModels, allowedIPs string, active bool) error {
	act := 0
	if active {
		act = 1
	}
	_, err := s.db.Exec(`UPDATE router_keys SET label=?,expires_at=?,quota_tokens=?,
		allowed_models=?,allowed_ips=?,active=? WHERE key=?`,
		label, expiresAt, quotaTokens, allowedModels, allowedIPs, act, key)
	return err
}

func (s *Store) RevokeRouterKey(key string) {
	s.db.Exec("UPDATE router_keys SET active=0 WHERE key=?", key)
}

func (s *Store) DeleteRouterKey(key string) {
	s.db.Exec("DELETE FROM router_keys WHERE key=?", key)
}

// KeyAuth 鉴权结果，Reason/Code 直接回给调用方。
type KeyAuth struct {
	OK     bool
	Key    RouterKey
	Code   string
	Reason string
}

// AuthorizeKey 完整校验：存在 / 启用 / 未过期 / 未超配额 / IP 放行 / 模型放行。
func (s *Store) AuthorizeKey(key, ip, model string) KeyAuth {
	var k RouterKey
	err := s.db.QueryRow(`SELECT key,label,created_at,active,COALESCE(expires_at,''),
		COALESCE(quota_tokens,0),COALESCE(used_tokens,0),COALESCE(allowed_models,''),
		COALESCE(allowed_ips,'') FROM router_keys WHERE key=?`, key).
		Scan(&k.Key, &k.Label, &k.CreatedAt, &k.Active, &k.ExpiresAt,
			&k.QuotaTokens, &k.UsedTokens, &k.AllowedModels, &k.AllowedIPs)
	if err != nil {
		return KeyAuth{Code: "invalid_key", Reason: "路由密钥无效"}
	}
	k.State = keyState(k)
	switch k.State {
	case "disabled":
		return KeyAuth{Key: k, Code: "key_disabled", Reason: "该密钥已被停用"}
	case "expired":
		return KeyAuth{Key: k, Code: "key_expired", Reason: "该密钥已过期"}
	case "over_quota":
		return KeyAuth{Key: k, Code: "key_over_quota", Reason: "该密钥的 Token 额度已用尽"}
	}
	if k.AllowedIPs != "" && !matchRuleList(k.AllowedIPs, ip) {
		return KeyAuth{Key: k, Code: "ip_not_allowed", Reason: "来源 IP 不在该密钥的白名单内"}
	}
	if k.AllowedModels != "" && model != "" && !matchRuleList(k.AllowedModels, model) {
		return KeyAuth{Key: k, Code: "model_not_allowed_for_key", Reason: "该密钥无权调用此模型"}
	}
	return KeyAuth{OK: true, Key: k}
}

// matchRuleList 匹配逗号分隔的规则列表，支持尾部 * 通配。
func matchRuleList(list, value string) bool {
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == value {
			return true
		}
		if strings.HasSuffix(item, "*") && strings.HasPrefix(value, strings.TrimSuffix(item, "*")) {
			return true
		}
	}
	return false
}

// TouchKey 累加密钥用量并刷新最后使用时间。
func (s *Store) TouchKey(key string, tokens int) {
	if key == "" {
		return
	}
	s.db.Exec(`UPDATE router_keys SET requests = COALESCE(requests,0) + 1,
		used_tokens = COALESCE(used_tokens,0) + ?, last_used_at = datetime('now') WHERE key=?`, tokens, key)
}

// ValidateRouterKey 只判断密钥是否可用的简版校验。
func (s *Store) ValidateRouterKey(key string) bool {
	return s.AuthorizeKey(key, "", "").OK
}

// ── Settings ──────────────────────────────────────────────────────────────────
func (s *Store) GetSetting(key string) string {
	var val string
	s.db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&val)
	return val
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=?", key, value, value)
	return err
}

func (s *Store) SetPassword(newHash string) {
	s.db.Exec("INSERT OR REPLACE INTO settings(key,value) VALUES('auth_password',?)", newHash)
}

// GetAccountsWithStats 返回带调用统计的账号列表（账号页用）。
func (s *Store) GetAccountsWithStats(dead bool) []Account {
	flag := 0
	if dead {
		flag = 1
	}
	rows, err := s.db.Query(`
		SELECT a.id, a.email, a.api_key, a.source, a.added_at, a.is_dead,
		       COALESCE(a.dead_reason,''), COALESCE(a.dead_at,''),
		       CASE WHEN COALESCE(a.password,'') != '' THEN 1 ELSE 0 END,
		       COALESCE(t.requests,0), COALESCE(t.tokens,0), COALESCE(t.last_ts,''),
		       COALESCE(t.last_err,'')
		FROM accounts a
		LEFT JOIN (
			SELECT account_id,
			       COUNT(*) AS requests,
			       COALESCE(SUM(total_tokens),0) AS tokens,
			       MAX(ts) AS last_ts,
			       (SELECT error_message FROM request_logs x
			        WHERE x.account_id = request_logs.account_id AND x.status_code >= 400
			        ORDER BY x.id DESC LIMIT 1) AS last_err
			FROM request_logs GROUP BY account_id
		) t ON t.account_id = a.id
		WHERE a.is_dead = ?
		ORDER BY a.id`, flag)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		var isDead, hasPw int
		rows.Scan(&a.ID, &a.Email, &a.APIKey, &a.Source, &a.AddedAt, &isDead,
			&a.DeadReason, &a.DeadAt, &hasPw, &a.Requests, &a.TotalTokens, &a.LastUsedAt, &a.LastError)
		a.IsDead = isDead == 1
		a.HasPassword = hasPw == 1
		out = append(out, a)
	}
	return out
}

// GetAccountSecret 取账号凭据，仅供服务端额度查询使用。
func (s *Store) GetAccountSecret(id int) (email, password, apiKey string) {
	s.db.QueryRow("SELECT email, COALESCE(password,''), api_key FROM accounts WHERE id=?", id).
		Scan(&email, &password, &apiKey)
	return
}

// UpdateAccountRateLimit 记下账号实测的每分钟请求上限，用于加权轮询。
func (s *Store) UpdateAccountRateLimit(id, perMin int) {
	if perMin <= 0 {
		return
	}
	s.db.Exec("UPDATE accounts SET rate_limit_req=? WHERE id=?", perMin, id)
}
