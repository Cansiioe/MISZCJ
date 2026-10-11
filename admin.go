package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ── Session store ─────────────────────────────────────────────────────────────
type sessionStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{tokens: map[string]time.Time{}}
}

func (s *sessionStore) create(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = time.Now().Add(24 * time.Hour)
}

func (s *sessionStore) valid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[token]
	return ok && time.Now().Before(exp)
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// ── Admin handler ─────────────────────────────────────────────────────────────
type AdminHandler struct {
	store    *Store
	proxy    *Proxy
	farm     *FarmManager
	sessions *sessionStore
}

func NewAdminHandler(store *Store, proxy *Proxy, farm *FarmManager) *AdminHandler {
	return &AdminHandler{
		store:    store,
		proxy:    proxy,
		farm:     farm,
		sessions: newSessionStore(),
	}
}

func (a *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin")

	// Serve SPA for non-API routes
	if !strings.HasPrefix(path, "/api") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTMLBytes)
		return
	}

	path = strings.TrimPrefix(path, "/api")

	switch {
	// ── Auth ──────────────────────────────────────────────────────────────
	case path == "/login" && r.Method == http.MethodPost:
		var body struct {
			Password string `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		settings := a.store.GetSettings()
		if err := bcrypt.CompareHashAndPassword([]byte(settings["auth_password"]), []byte(body.Password)); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "密码错误"})
			return
		}
		token := generateToken()
		a.sessions.create(token)
		http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/", HttpOnly: true, MaxAge: 86400})
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/logout" && r.Method == http.MethodPost:
		if c, err := r.Cookie("session"); err == nil {
			a.sessions.delete(c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Path: "/", MaxAge: -1})
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/auth-check":
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/settings" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		limit := a.store.GetSetting("proxy_limit")
		if limit == "" {
			limit = "1"
		}
		writeJSON(w, map[string]string{"proxy_limit": limit})

	case path == "/settings/proxy-limit" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var data map[string]string
		json.NewDecoder(r.Body).Decode(&data)
		limit := data["limit"]
		if limit != "" {
			a.store.SetSetting("proxy_limit", limit)
		}
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/accounts" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		accounts := a.store.GetAliveAccounts()
		for i := range accounts {
			accounts[i].APIKey = maskAPIKey(accounts[i].APIKey)
		}
		writeJSON(w, accounts)

	case path == "/accounts/dead" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		accounts := a.store.GetDeadAccounts()
		for i := range accounts {
			accounts[i].APIKey = maskAPIKey(accounts[i].APIKey)
		}
		writeJSON(w, accounts)

	case path == "/accounts/revive" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			ID int `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		a.store.ReviveAccount(body.ID)
		a.proxy.Reload()
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/accounts/delete" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			ID int `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		a.store.DeleteAccount(body.ID)
		a.proxy.Reload()
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/import" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			FilePath string `json:"file_path"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.FilePath == "" {
			// Portable default: results.json next to the farm working dir / CWD.
			cwd, _ := os.Getwd()
			body.FilePath = filepath.Join(cwd, "results.json")
		}
		count, err := importMistralResults(a.store, body.FilePath)
		if err != nil {
			w.WriteHeader(400)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		a.proxy.Reload()
		writeJSON(w, map[string]interface{}{"imported": count})

	// ── Router Keys ───────────────────────────────────────────────────────
	case path == "/keys" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.store.GetRouterKeys())

	case path == "/keys/generate" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			Label         string `json:"label"`
			ExpiresInDays int    `json:"expires_in_days"`
			QuotaTokens   int64  `json:"quota_tokens"`
			AllowedModels string `json:"allowed_models"`
			AllowedIPs    string `json:"allowed_ips"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Label == "" {
			body.Label = "默认"
		}
		expires := ""
		if body.ExpiresInDays > 0 {
			expires = time.Now().AddDate(0, 0, body.ExpiresInDays).Format("2006-01-02 15:04:05")
		}
		key := a.store.AddRouterKeyWith(body.Label, expires, body.QuotaTokens,
			strings.TrimSpace(body.AllowedModels), strings.TrimSpace(body.AllowedIPs))
		writeJSON(w, map[string]string{"key": key})

	case path == "/keys/update" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			Key           string `json:"key"`
			Label         string `json:"label"`
			ExpiresInDays int    `json:"expires_in_days"` // <=0 表示永不过期
			QuotaTokens   int64  `json:"quota_tokens"`
			AllowedModels string `json:"allowed_models"`
			AllowedIPs    string `json:"allowed_ips"`
			Active        bool   `json:"active"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		expires := ""
		if body.ExpiresInDays > 0 {
			expires = time.Now().AddDate(0, 0, body.ExpiresInDays).Format("2006-01-02 15:04:05")
		}
		if err := a.store.UpdateRouterKey(body.Key, body.Label, expires, body.QuotaTokens,
			strings.TrimSpace(body.AllowedModels), strings.TrimSpace(body.AllowedIPs), body.Active); err != nil {
			w.WriteHeader(500)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/keys/delete" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			Key string `json:"key"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		a.store.DeleteRouterKey(body.Key)
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/keys/revoke" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			Key string `json:"key"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		a.store.RevokeRouterKey(body.Key)
		writeJSON(w, map[string]string{"status": "ok"})

	// ── 调用日志 ──────────────────────────────────────────────────────────
	case path == "/logs" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		query := r.URL.Query()
		limit, _ := strconv.Atoi(query.Get("limit"))
		offset, _ := strconv.Atoi(query.Get("offset"))
		items, total := a.store.QueryLogs(LogQuery{
			KeyPrefix: query.Get("key"),
			Model:     query.Get("model"),
			Status:    query.Get("status"),
			Since:     query.Get("since"),
			Limit:     limit,
			Offset:    offset,
		})
		if items == nil {
			items = []RequestLog{}
		}
		writeJSON(w, map[string]interface{}{
			"items": items, "total": total,
			"limit": limit, "offset": offset,
		})

	case path == "/logs/clear" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, map[string]interface{}{"deleted": a.store.ClearLogs()})

	// ── 模型中心 ──────────────────────────────────────────────────────────
	case path == "/models/available" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.modelsInfo())

	// ── Stats & Usage ─────────────────────────────────────────────────────
	case path == "/stats" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.store.GetStats())

	case path == "/stats/series" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		series := a.store.GetDailySeries(days)
		if series == nil {
			series = []SeriesPoint{}
		}
		writeJSON(w, series)

	case path == "/accounts/stats" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		accts := a.store.GetAccountsWithStats(r.URL.Query().Get("dead") == "1")
		for i := range accts {
			accts[i].APIKey = maskAPIKey(accts[i].APIKey)
		}
		if accts == nil {
			accts = []Account{}
		}
		writeJSON(w, accts)

	case path == "/usage" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.store.GetUsageSummary())

	case path == "/usage/accounts" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.store.GetUsageByAccount())

	case path == "/models" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		a.handleModels(w, r)

	case path == "/quota" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		a.handleQuota(w, r)

	case path == "/proxy/status" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.proxy.Status())

	case path == "/scheduling" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		a.handleScheduling(w, r)

	case path == "/export" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		a.handleExport(w, r)

	case path == "/keys/purge-expired" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		a.handlePurgeExpiredKeys(w, r)

	case path == "/proxies" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, map[string]interface{}{
			"summary": a.proxy.proxyPool.Summary(),
			"items":   a.proxy.proxyPool.Status(),
		})

	case path == "/alerts" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		a.handleAlerts(w, r)

	case path == "/accounts/revive-all" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		a.handleReviveAll(w, r)

	case path == "/logs/cleanup" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			KeepDays int `json:"keep_days"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		n := a.store.CleanupLogs(body.KeepDays)
		writeJSON(w, map[string]interface{}{"deleted": n, "keep_days": body.KeepDays})

	// ── Farm ──────────────────────────────────────────────────────────────
	case path == "/farm/config" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.store.GetFarmConfig())

	case path == "/farm/config" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var cfg map[string]string
		json.NewDecoder(r.Body).Decode(&cfg)
		if err := a.store.SaveFarmConfig(cfg); err != nil {
			w.WriteHeader(500)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]string{"status": "ok"})

	case path == "/farm/runs" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.store.GetFarmRuns(20))

	case path == "/farm/start" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			MaxAttempts int `json:"max_attempts"`
			Concurrency int `json:"concurrency"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if err := a.farm.StartWith(body.MaxAttempts, body.Concurrency); err != nil {
			w.WriteHeader(400)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]string{"status": "started"})

	case path == "/farm/stop" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		if err := a.farm.Stop(); err != nil {
			w.WriteHeader(400)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]string{"status": "stopped"})

	case path == "/farm/log" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		n := 200
		if q := r.URL.Query().Get("n"); q != "" {
			n, _ = strconv.Atoi(q)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(a.farm.GetLogTail(n)))

	// ── Settings ──────────────────────────────────────────────────────────
	case path == "/settings/password" && r.Method == http.MethodPost:
		if !a.auth(w, r) {
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Password) < 4 {
			w.WriteHeader(400)
			writeJSON(w, map[string]string{"error": "密码太短（至少 4 位）"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err != nil {
			w.WriteHeader(500)
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		a.store.SetPassword(string(hash))
		writeJSON(w, map[string]string{"status": "ok"})

	default:
		http.NotFound(w, r)
	}
}

func (a *AdminHandler) auth(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie("session")
	if err != nil || !a.sessions.valid(c.Value) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]string{"error": "未登录或会话已过期"})
		return false
	}
	return true
}

// ── Helpers ───────────────────────────────────────────────────────────────────
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:6] + "••••" + key[len(key)-4:]
}

func generateToken() string {
	b := make([]byte, 16)
	f, _ := os.Open("/dev/urandom")
	if f != nil {
		defer f.Close()
		f.Read(b)
	}
	var hex [32]byte
	const chars = "0123456789abcdef"
	for i, v := range b {
		hex[i*2] = chars[v>>4]
		hex[i*2+1] = chars[v&0xf]
	}
	return string(hex[:])
}

func importMistralResults(store *Store, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var results []struct {
		Email     string `json:"email"`
		APIKey    string `json:"api_key"`
		Password  string `json:"password"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &results); err != nil {
		return 0, err
	}
	count := 0
	for _, r := range results {
		if r.Email == "" || r.APIKey == "" {
			continue
		}
		if err := store.AddAccountFull(r.Email, r.APIKey, r.Password, "farm"); err == nil {
			count++
		}
	}
	os.WriteFile(path, []byte("[]"), 0644)
	return count, nil
}

func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// handleModels fetches available models from Mistral using one live account
func (a *AdminHandler) handleModels(w http.ResponseWriter, r *http.Request) {
	accounts := a.store.GetAliveAccounts()
	if len(accounts) == 0 {
		writeJSON(w, map[string]interface{}{"models": []string{}, "error": "无可用账号"})
		return
	}
	apiKey := accounts[0].APIKey
	req, _ := http.NewRequestWithContext(r.Context(), "GET", mistralBase+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, map[string]interface{}{"models": []string{}, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// handleQuota 查询账号的计划、余额与可用模型。
// 默认查全部存活账号；?id=1,2 指定账号，?force=1 跳过缓存。
func (a *AdminHandler) handleQuota(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	force := query.Get("force") == "1"

	var ids []int
	if spec := query.Get("id"); spec != "" {
		for _, part := range strings.Split(spec, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				ids = append(ids, n)
			}
		}
	} else {
		for _, acc := range a.store.GetAliveAccounts() {
			ids = append(ids, acc.ID)
		}
	}
	if len(ids) == 0 {
		writeJSON(w, []AccountQuota{})
		return
	}
	writeJSON(w, QueryAllQuota(a.store, ids, force))
	// 额度可能变了，重新加载账号，让加权轮询用上新权重
	a.proxy.Reload()
}

// handleScheduling 返回调度与限流状态，供控制台展示。
func (a *AdminHandler) handleScheduling(w http.ResponseWriter, r *http.Request) {
	out := a.proxy.limiter.Status()
	out["weights"] = a.proxy.Weights()
	out["proxy_limit"] = a.store.GetSetting("proxy_limit")
	writeJSON(w, out)
}

// handleAlerts 返回告警配置状态；?test=1 时顺带发一条测试告警。
func (a *AdminHandler) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("test") == "1" {
		a.proxy.alerter.Send("manual_test", "info", "测试告警",
			"来自 Mistral 路由控制台的测试消息，收到即表示 Webhook 配置正确。")
	}
	writeJSON(w, a.proxy.alerter.Status())
}

// handleReviveAll 立即探测所有失效账号，可用的自动复活。
func (a *AdminHandler) handleReviveAll(w http.ResponseWriter, r *http.Request) {
	n := a.store.AutoReviveDeadAccounts()
	if n > 0 {
		a.proxy.Reload()
		a.proxy.alerter.Send("auto_revive", "info", "账号已自动恢复",
			fmt.Sprintf("%d 个失效账号探测可用，已重新加入轮询。", n))
	}
	writeJSON(w, map[string]interface{}{"revived": n})
}

// handleExport 导出账号 / 密钥 / 日志，支持 JSON 与 CSV。
// 用途：备份、迁移、或丢进表格里分析。
func (a *AdminHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("type")
	format := q.Get("format")
	if format == "" {
		format = "json"
	}

	var header []string
	var rows [][]string
	var payload interface{}

	switch kind {
	case "accounts":
		header = []string{"id", "email", "api_key", "source", "is_dead", "dead_reason",
			"added_at", "requests", "total_tokens", "rate_limit_req", "last_used_at"}
		all := append(a.store.GetAccountsWithStats(false), a.store.GetAccountsWithStats(true)...)
		for _, x := range all {
			rows = append(rows, []string{
				strconv.Itoa(x.ID), x.Email, x.APIKey, x.Source,
				boolStr(x.IsDead), x.DeadReason, x.AddedAt,
				strconv.FormatInt(x.Requests, 10), strconv.FormatInt(x.TotalTokens, 10),
				strconv.Itoa(x.RateLimitReq), x.LastUsedAt,
			})
		}
		payload = all
	case "keys":
		header = []string{"key", "label", "state", "active", "created_at", "expires_at",
			"quota_tokens", "used_tokens", "requests", "allowed_models", "allowed_ips", "last_used_at"}
		keys := a.store.GetRouterKeys()
		for _, k := range keys {
			rows = append(rows, []string{
				k.Key, k.Label, k.State, boolStr(k.Active), k.CreatedAt, k.ExpiresAt,
				strconv.FormatInt(k.QuotaTokens, 10), strconv.FormatInt(k.UsedTokens, 10),
				strconv.FormatInt(k.Requests, 10), k.AllowedModels, k.AllowedIPs, k.LastUsedAt,
			})
		}
		payload = keys
	case "logs":
		header = []string{"id", "ts", "key_prefix", "account_email", "ip", "model",
			"prompt_tokens", "completion_tokens", "total_tokens", "status", "error_code",
			"error_message", "latency_ms"}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 10000 {
			limit = 5000
		}
		items, _ := a.store.QueryLogs(LogQuery{Limit: limit})
		for _, l := range items {
			rows = append(rows, []string{
				strconv.Itoa(l.ID), l.Ts, l.KeyPrefix, l.AccountEmail, l.IP, l.Model,
				strconv.Itoa(l.PromptTokens), strconv.Itoa(l.CompletionTokens), strconv.Itoa(l.TotalTokens),
				strconv.Itoa(l.Status), l.ErrorCode, l.ErrorMessage, strconv.Itoa(l.LatencyMs),
			})
		}
		payload = items
	default:
		w.WriteHeader(400)
		writeJSON(w, map[string]string{"error": "type 需为 accounts / keys / logs"})
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%s-%s.csv", kind, time.Now().Format("20060102-150405")))
		cw := csv.NewWriter(w)
		cw.Write(header)
		cw.WriteAll(rows)
		cw.Flush()
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%s-%s.json", kind, time.Now().Format("20060102-150405")))
	json.NewEncoder(w).Encode(payload)
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// handlePurgeExpiredKeys 清理已过期超过 N 天的密钥。
func (a *AdminHandler) handlePurgeExpiredKeys(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OlderThanDays int `json:"older_than_days"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.OlderThanDays <= 0 {
		body.OlderThanDays = 30
	}
	n := a.store.PurgeExpiredKeys(body.OlderThanDays)
	writeJSON(w, map[string]interface{}{"deleted": n, "older_than_days": body.OlderThanDays})
}
func (a *AdminHandler) modelsInfo() map[string]interface{} {
	f := a.proxy.filter
	out := map[string]interface{}{
		"enabled": f.Enabled(),
		"allowed": f.Allowed(),
		"default": f.DefaultModel(),
	}
	path := getEnv("MODELS_FILE", filepath.Join(a.farm.farmDir, "config", "models.available.json"))
	out["path"] = path
	if data, err := os.ReadFile(path); err == nil {
		var raw map[string]interface{}
		if json.Unmarshal(data, &raw) == nil {
			out["meta"] = raw
		}
	}

	// 附上能力矩阵：从上游拉一次（带 10 分钟缓存），用任意存活账号的 key
	alive := a.store.GetAliveAccounts()
	if len(alive) > 0 {
		caps := fetchModelCapabilities(alive[0].APIKey)
		byID := make(map[string]ModelCapability, len(caps))
		for _, c := range caps {
			byID[c.ID] = c
		}
		details := make([]ModelCapability, 0, len(f.Allowed()))
		for _, id := range f.Allowed() {
			if c, ok := byID[id]; ok {
				details = append(details, c)
			} else {
				details = append(details, ModelCapability{ID: id})
			}
		}
		out["details"] = details
		out["upstream_total"] = len(caps)
	}
	return out
}
