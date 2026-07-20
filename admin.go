package main

import (
	"bufio"
	"encoding/json"
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
	mu      sync.Mutex
	tokens  map[string]time.Time
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
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid password"})
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
			Label string `json:"label"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Label == "" {
			body.Label = "default"
		}
		key := a.store.AddRouterKey(body.Label)
		writeJSON(w, map[string]string{"key": key})

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

	// ── Stats & Usage ─────────────────────────────────────────────────────
	case path == "/stats" && r.Method == http.MethodGet:
		if !a.auth(w, r) {
			return
		}
		writeJSON(w, a.store.GetStats())

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
			writeJSON(w, map[string]string{"error": "password too short"})
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
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
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
		if err := store.AddAccount(r.Email, r.APIKey, "farm"); err == nil {
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
		writeJSON(w, map[string]interface{}{"models": []string{}, "error": "no alive accounts"})
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

type QuotaResult struct {
	AccountID int    `json:"account_id"`
	Email     string `json:"email"`
	Status    string `json:"status"` // "ok", "dead", "error"
	Remaining int64  `json:"remaining,omitempty"`
	Limit     int64  `json:"limit,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleQuota checks token quota for all alive accounts concurrently
func (a *AdminHandler) handleQuota(w http.ResponseWriter, r *http.Request) {
	accounts := a.store.GetAliveAccounts()
	results := make([]QuotaResult, len(accounts))
	var wg sync.WaitGroup
	for i, acct := range accounts {
		wg.Add(1)
		go func(idx int, a Account) {
			defer wg.Done()
			res := QuotaResult{AccountID: a.ID, Email: a.Email}
			req, err := http.NewRequest("GET", mistralBase+"/v1/usage", nil)
			if err != nil {
				res.Status = "error"
				res.Error = err.Error()
				results[idx] = res
				return
			}
			req.Header.Set("Authorization", "Bearer "+a.APIKey)
			client := &http.Client{Timeout: 8 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				res.Status = "error"
				res.Error = err.Error()
				results[idx] = res
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				res.Status = "dead"
				results[idx] = res
				return
			}
			var data map[string]interface{}
			json.Unmarshal(body, &data)
			// Mistral free tier returns monthly_limit / monthly_usage
			res.Status = "ok"
			if limit, ok := data["monthly_limit"].(float64); ok {
				res.Limit = int64(limit)
			}
			if used, ok := data["monthly_usage"].(float64); ok {
				if res.Limit > 0 {
					res.Remaining = res.Limit - int64(used)
				}
			}
			// Fallback: if endpoint not found, return static
			if resp.StatusCode == 404 {
				res.Status = "ok"
				res.Remaining = 1_000_000
				res.Limit = 1_000_000
			}
			results[idx] = res
		}(i, acct)
	}
	wg.Wait()
	writeJSON(w, results)
}
