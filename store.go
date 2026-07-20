package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log"
	"os"
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
}

type RouterKey struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
	Active    bool   `json:"active"`
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
	TotalAccounts   int `json:"total_accounts"`
	AliveAccounts   int `json:"alive_accounts"`
	DeadAccounts    int `json:"dead_accounts"`
	TotalRequests   int `json:"total_requests"`
	TotalTokens     int `json:"total_tokens"`
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
  active INTEGER DEFAULT 1
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
  model TEXT,
  prompt_tokens INTEGER,
  completion_tokens INTEGER,
  status_code INTEGER,
  error_code TEXT
);
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

	// Bootstrap admin password once: ADMIN_PASSWORD env, else random (logged once).
	// Never ship a fixed weak default password in source.
	var cnt int
	s.db.QueryRow("SELECT COUNT(*) FROM settings WHERE key='auth_password'").Scan(&cnt)
	if cnt == 0 {
		pw := os.Getenv("ADMIN_PASSWORD")
		if pw == "" {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err != nil {
				log.Fatalf("failed to generate admin password: %v", err)
			}
			pw = hex.EncodeToString(b)
			log.Printf("Generated initial admin password: %s (change in Admin UI or set ADMIN_PASSWORD before first start)", pw)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("failed to hash admin password: %v", err)
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
		"IMAP_HOST":    "imap.example.com",
		"IMAP_PORT":    "993",
		"MAX_ATTEMPTS": "50",
		"CONCURRENCY":  "1",
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

func (s *Store) GetAliveAccounts() []Account {
	rows, err := s.db.Query("SELECT id,email,api_key,source,added_at FROM accounts WHERE is_dead=0 ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var accounts []Account
	for rows.Next() {
		var a Account
		rows.Scan(&a.ID, &a.Email, &a.APIKey, &a.Source, &a.AddedAt)
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
	_, err := s.db.Exec("INSERT OR IGNORE INTO accounts(email,api_key,source) VALUES(?,?,?)", email, apiKey, source)
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
		log.Printf("StartFarmRun error: %v", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *Store) EndFarmRun(id int64, status string, newAccounts int) {
	s.db.Exec("UPDATE farm_runs SET ended_at=datetime('now'),status=?,new_accounts=? WHERE id=?", status, newAccounts, id)
}

func (s *Store) LogRequest(accountID int, model string, promptTokens, completionTokens, statusCode int, errorCode string) {
	s.db.Exec("INSERT INTO request_logs(account_id,model,prompt_tokens,completion_tokens,status_code,error_code) VALUES(?,?,?,?,?,?)",
		accountID, model, promptTokens, completionTokens, statusCode, errorCode)
	date := time.Now().Format("2006-01-02")
	s.db.Exec(`INSERT INTO usage(account_id,date,model,prompt_tokens,completion_tokens,requests) VALUES(?,?,?,?,?,1)
		ON CONFLICT DO UPDATE SET prompt_tokens=prompt_tokens+excluded.prompt_tokens,
		completion_tokens=completion_tokens+excluded.completion_tokens, requests=requests+1`,
		accountID, date, model, promptTokens, completionTokens)
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
	s.db.QueryRow("SELECT COALESCE(SUM(prompt_tokens+completion_tokens),0) FROM request_logs").Scan(&st.TotalTokens)
	return st
}

func (s *Store) GetRouterKeys() []RouterKey {
	rows, _ := s.db.Query("SELECT key,label,created_at,active FROM router_keys ORDER BY created_at DESC")
	defer rows.Close()
	var keys []RouterKey
	for rows.Next() {
		var k RouterKey
		rows.Scan(&k.Key, &k.Label, &k.CreatedAt, &k.Active)
		keys = append(keys, k)
	}
	return keys
}

func (s *Store) AddRouterKey(label string) string {
	b := make([]byte, 20)
	rand.Read(b)
	key := "nm-" + hex.EncodeToString(b)
	s.db.Exec("INSERT INTO router_keys(key,label) VALUES(?,?)", key, label)
	return key
}

func (s *Store) RevokeRouterKey(key string) {
	s.db.Exec("UPDATE router_keys SET active=0 WHERE key=?", key)
}

func (s *Store) ValidateRouterKey(key string) bool {
	var active int
	s.db.QueryRow("SELECT active FROM router_keys WHERE key=?", key).Scan(&active)
	return active == 1
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
