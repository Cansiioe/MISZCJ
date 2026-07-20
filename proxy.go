package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const mistralBase = "https://api.mistral.ai"

type Proxy struct {
	store    *Store
	accounts []Account
	counter  atomic.Uint64
	mu       sync.RWMutex
}

func NewProxy(store *Store) *Proxy {
	return &Proxy{store: store}
}

func (p *Proxy) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = p.store.GetAliveAccounts()
	log.Printf("proxy: loaded %d accounts", len(p.accounts))
}

type ProxyStatus struct {
	TotalAccounts int    `json:"total_accounts"`
	CurrentIndex  uint64 `json:"current_index"`
	NextAccount   string `json:"next_account,omitempty"`
}

func (p *Proxy) Status() ProxyStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := ProxyStatus{
		TotalAccounts: len(p.accounts),
		CurrentIndex:  p.counter.Load(),
	}
	if len(p.accounts) > 0 {
		next := p.counter.Load() % uint64(len(p.accounts))
		email := p.accounts[next].Email
		if len(email) > 4 {
			at := strings.Index(email, "@")
			if at > 2 {
				email = email[:2] + "***" + email[at:]
			}
		}
		s.NextAccount = email
	}
	return s
}

func (p *Proxy) pick() (Account, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.accounts) == 0 {
		return Account{}, false
	}
	
	limitStr := p.store.GetSetting("proxy_limit")
	limit, err := strconv.ParseUint(limitStr, 10, 64)
	if err != nil || limit == 0 {
		limit = 1 // default to 1 request per account per rotation
	}
	
	idx := (p.counter.Add(1) - 1) / limit % uint64(len(p.accounts))
	return p.accounts[idx], true
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Validate router key
	authHeader := r.Header.Get("Authorization")
	var routerKey string
	if strings.HasPrefix(authHeader, "Bearer ") {
		routerKey = strings.TrimPrefix(authHeader, "Bearer ")
	}
	if !p.store.ValidateRouterKey(routerKey) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"message": "invalid router key", "code": "invalid_key"},
		})
		return
	}

	// Handle /v1/models separately
	if r.URL.Path == "/v1/models" {
		p.proxyModels(w, r)
		return
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", 500)
		return
	}

	// Extract model from body
	var reqBody map[string]interface{}
	json.Unmarshal(body, &reqBody)
	model, _ := reqBody["model"].(string)

	// Try accounts round-robin (max 3 retries)
	for attempt := 0; attempt < 3; attempt++ {
		acct, ok := p.pick()
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"message": "no alive accounts", "code": "no_accounts"},
			})
			return
		}

		upstream := mistralBase + r.URL.Path
		if r.URL.RawQuery != "" {
			upstream += "?" + r.URL.RawQuery
		}

		req, _ := http.NewRequestWithContext(r.Context(), r.Method, upstream, bytes.NewReader(body))
		req.Header = r.Header.Clone()
		req.Header.Set("Authorization", "Bearer "+acct.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)

		// Check for auth errors
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			var errResp map[string]interface{}
			json.Unmarshal(respBody, &errResp)
			errMsg := ""
			if e, ok := errResp["error"].(map[string]interface{}); ok {
				errMsg, _ = e["message"].(string)
			}
			log.Printf("proxy: account %d dead: %s", acct.ID, errMsg)
			p.store.MarkDead(acct.ID, errMsg)
			p.Reload()
			continue
		}

		// Extract usage for logging
		var respData map[string]interface{}
		json.Unmarshal(respBody, &respData)
		promptTokens, completionTokens := 0, 0
		if usage, ok := respData["usage"].(map[string]interface{}); ok {
			if v, ok := usage["prompt_tokens"].(float64); ok {
				promptTokens = int(v)
			}
			if v, ok := usage["completion_tokens"].(float64); ok {
				completionTokens = int(v)
			}
		}

		errorCode := ""
		if resp.StatusCode >= 400 {
			if e, ok := respData["error"].(map[string]interface{}); ok {
				errorCode, _ = e["code"].(string)
			}
		}

		go p.store.LogRequest(acct.ID, model, promptTokens, completionTokens, resp.StatusCode, errorCode)

		// Forward response
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(503)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{"message": "all accounts exhausted", "code": "exhausted"},
	})
}

func (p *Proxy) proxyModels(w http.ResponseWriter, r *http.Request) {
	acct, ok := p.pick()
	if !ok {
		// Return hardcoded model list
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object": "list",
			"data": []map[string]string{
				{"id": "mistral-small-latest"},
				{"id": "mistral-medium-latest"},
				{"id": "mistral-large-latest"},
				{"id": "codestral-latest"},
				{"id": "open-mixtral-8x22b"},
			},
		})
		return
	}

	req, _ := http.NewRequestWithContext(r.Context(), "GET", mistralBase+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+acct.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "upstream error", 502)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
