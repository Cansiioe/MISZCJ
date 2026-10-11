package main

// 账号额度查询。
//
// 额度（计划 / 余额 / 订阅）只能从 console 侧拿到，而 console 只认登录会话，
// 不认 API Key —— 所以必须用注册时保存的密码走一遍 Ory 登录流程。
// 拿到的数据会缓存一段时间，避免每次开页面都重新登录。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mistralAuthBase    = "https://auth.mistral.ai"
	mistralConsoleBase = "https://console.mistral.ai"
	quotaUA            = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	quotaCacheTTL = 10 * time.Minute
)

// AccountQuota 单个账号的计划、额度与可用模型。
type AccountQuota struct {
	AccountID int    `json:"account_id"`
	Email     string `json:"email"`
	Status    string `json:"status"` // ok / no_password / error
	Error     string `json:"error,omitempty"`

	Plan          string   `json:"plan,omitempty"` // FREE / PAY_AS_YOU_GO
	WalletAmount  float64  `json:"wallet_amount"`
	OngoingUsage  float64  `json:"ongoing_usage"`
	Currency      string   `json:"currency,omitempty"`
	Subscriptions []string `json:"subscriptions,omitempty"`

	// 实时速率额度：直接读 chat 响应头，不需要登录，最可靠
	RateLimitTokensPerMin int    `json:"rate_limit_tokens_min"`
	RateRemainTokens      int    `json:"rate_remain_tokens"`
	RateLimitReqPerMin    int    `json:"rate_limit_req_min"`
	RateRemainReq         int    `json:"rate_remain_req"`
	ProbeModel            string `json:"probe_model,omitempty"`

	Models     []string `json:"models,omitempty"`
	ModelCount int      `json:"model_count"`
	MaxRate    int      `json:"max_rate,omitempty"` // 可用模型里的最高 req/min

	CheckedAt string `json:"checked_at"`
}

// quotaCache 缓存每个账号的额度查询结果。
type quotaCacheEntry struct {
	quota AccountQuota
	at    time.Time
}

var (
	quotaMu    sync.Mutex
	quotaCache = map[int]quotaCacheEntry{}
)

func quotaTransport() *http.Transport {
	tr := &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	}
	proxyURL := getEnv("MISTRAL_PROXY", os.Getenv("HTTPS_PROXY"))
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return tr
}

// consoleSession 持有一次登录会话。
type consoleSession struct {
	client *http.Client
}

func newConsoleSession() *consoleSession {
	jar, _ := cookiejar.New(nil)
	return &consoleSession{
		client: &http.Client{
			Jar:       jar,
			Timeout:   30 * time.Second,
			Transport: quotaTransport(),
		},
	}
}

// noRedirectClient 用于拿 302 上的 Location（flow id）。
func (s *consoleSession) noRedirectClient() *http.Client {
	return &http.Client{
		Jar:       s.client.Jar,
		Timeout:   30 * time.Second,
		Transport: quotaTransport(),
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *consoleSession) do(method, rawURL string, body []byte, contentType string) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", quotaUA)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

// login 走 Ory 的 identifier_first + password 两段式登录。
func (s *consoleSession) login(email, password string) error {
	startURL := mistralAuthBase + "/self-service/login/browser?return_to=" +
		url.QueryEscape(mistralConsoleBase+"/")
	req, _ := http.NewRequest("GET", startURL, nil)
	req.Header.Set("User-Agent", quotaUA)
	req.Header.Set("Accept", "text/html")
	resp, err := s.noRedirectClient().Do(req)
	if err != nil {
		return fmt.Errorf("登录入口不可达: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	flowID := queryValue(resp.Header.Get("Location"), "flow")
	if flowID == "" {
		return fmt.Errorf("未取到登录 flow（HTTP %d）", resp.StatusCode)
	}

	flowRaw, code, err := s.do("GET", mistralAuthBase+"/self-service/login/flows?id="+flowID, nil, "")
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("读取登录 flow 失败（HTTP %d）", code)
	}
	var flow map[string]any
	if err := json.Unmarshal(flowRaw, &flow); err != nil {
		return fmt.Errorf("解析登录 flow 失败: %w", err)
	}
	action := flowAction(flow, mistralAuthBase+"/self-service/login?flow="+flowID)

	// 第一段：提交邮箱
	form := url.Values{"method": {"identifier_first"}, "identifier": {email}, "csrf_token": {flowCSRF(flow)}}
	raw1, code, err := s.do("POST", action, []byte(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("提交邮箱失败（HTTP %d）", code)
	}
	var step2 map[string]any
	if err := json.Unmarshal(raw1, &step2); err != nil {
		return fmt.Errorf("解析登录第二步失败: %w", err)
	}
	action2 := flowAction(step2, action)

	// 第二段：提交密码
	form2 := url.Values{"method": {"password"}, "identifier": {email}, "password": {password}, "csrf_token": {flowCSRF(step2)}}
	raw2, code, err := s.do("POST", action2, []byte(form2.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("提交密码失败（HTTP %d）", code)
	}
	var step3 map[string]any
	json.Unmarshal(raw2, &step3)

	// 有 ory_session cookie 才算成功
	for _, c := range s.client.Jar.Cookies(mustURL(mistralAuthBase)) {
		if strings.HasPrefix(c.Name, "ory_session") {
			return nil
		}
	}
	if msg := sessionError(step3); msg != "" {
		return fmt.Errorf("登录被拒: %s", msg)
	}
	return fmt.Errorf("登录后未取得会话")
}

func (s *consoleSession) getJSON(path string, out any) error {
	raw, code, err := s.do("GET", mistralConsoleBase+path, nil, "")
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("%s 返回 HTTP %d", path, code)
	}
	return json.Unmarshal(raw, out)
}

/* ── 辅助 ─────────────────────────────────────────────────────────────── */

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func queryValue(rawURL, key string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}

func flowAction(flow map[string]any, fallback string) string {
	if ui, ok := flow["ui"].(map[string]any); ok {
		if a, ok := ui["action"].(string); ok && a != "" {
			return a
		}
	}
	return fallback
}

func flowCSRF(flow map[string]any) string {
	ui, ok := flow["ui"].(map[string]any)
	if !ok {
		return ""
	}
	nodes, _ := ui["nodes"].([]any)
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		attrs, _ := node["attributes"].(map[string]any)
		if attrs["name"] == "csrf_token" {
			if v, ok := attrs["value"].(string); ok {
				return v
			}
		}
	}
	return ""
}

// sessionError 从 Ory 的返回里挖出人类可读的错误信息。
func sessionError(step map[string]any) string {
	ui, ok := step["ui"].(map[string]any)
	if !ok {
		return ""
	}
	msgs, _ := ui["messages"].([]any)
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			if t, ok := mm["text"].(string); ok {
				return t
			}
		}
	}
	return ""
}

/* ── 对外查询 ─────────────────────────────────────────────────────────── */

// QueryAccountQuota 查一个账号的计划与余额；结果带缓存。
// force=true 时跳过缓存。
func QueryAccountQuota(store *Store, id int, force bool) AccountQuota {
	if !force {
		quotaMu.Lock()
		if e, ok := quotaCache[id]; ok && time.Since(e.at) < quotaCacheTTL {
			quotaMu.Unlock()
			return e.quota
		}
		quotaMu.Unlock()
	}

	email, password, apiKey := store.GetAccountSecret(id)
	q := AccountQuota{AccountID: id, Email: email, CheckedAt: time.Now().Format("2006-01-02 15:04:05")}

	// 无论登录是否成功，都先探测一遍：模型列表与实时速率额度不依赖登录
	applyProbe := func() {
		pr := probeAccount(apiKey)
		q.Models, q.ModelCount, q.MaxRate = pr.models, pr.count, pr.maxRate
		q.RateLimitTokensPerMin, q.RateRemainTokens = pr.limTok, pr.remTok
		q.RateLimitReqPerMin, q.RateRemainReq = pr.limReq, pr.remReq
		q.ProbeModel = pr.probeMdl
		// 把实测额度写回账号，加权轮询据此分配流量
		if pr.limReq > 0 {
			store.UpdateAccountRateLimit(id, pr.limReq)
		}
	}

	if password == "" {
		q.Status = "no_password"
		q.Error = "该账号没有保存密码，无法查询计划与余额（速率额度仍可读）"
		applyProbe()
		cacheQuota(id, q)
		return q
	}

	sess := newConsoleSession()
	if err := sess.login(email, password); err != nil {
		q.Status = "error"
		q.Error = "console 登录失败: " + err.Error()
		applyProbe()
		cacheQuota(id, q)
		return q
	}

	var orgs struct {
		Items []struct {
			ActiveAPIPlan string `json:"active_api_plan"`
			OrgTier       string `json:"org_tier"`
		} `json:"items"`
	}
	if err := sess.getJSON("/api/users/v2/organizations", &orgs); err == nil && len(orgs.Items) > 0 {
		q.Plan = orgs.Items[0].ActiveAPIPlan
	}

	var credits struct {
		WalletAmount float64 `json:"wallet_amount"`
		OngoingUsage float64 `json:"ongoing_usage_balance"`
		Currency     string  `json:"currency"`
	}
	if err := sess.getJSON("/api/billing/credits", &credits); err == nil {
		q.WalletAmount = credits.WalletAmount
		q.OngoingUsage = credits.OngoingUsage
		q.Currency = credits.Currency
	}

	var subs struct {
		Subscriptions []struct {
			Type string `json:"type"`
		} `json:"subscriptions"`
	}
	if err := sess.getJSON("/api/billing/subscriptions", &subs); err == nil {
		for _, s := range subs.Subscriptions {
			if s.Type != "" {
				q.Subscriptions = append(q.Subscriptions, s.Type)
			}
		}
	}

	applyProbe()
	q.Status = "ok"
	cacheQuota(id, q)
	return q
}

func cacheQuota(id int, q AccountQuota) {
	quotaMu.Lock()
	quotaCache[id] = quotaCacheEntry{quota: q, at: time.Now()}
	quotaMu.Unlock()
}

// probeResult 一次账号探测的结果。
type probeResult struct {
	models   []string
	count    int
	maxRate  int
	limTok   int
	remTok   int
	limReq   int
	remReq   int
	probeMdl string
}

// probeAccount 用 API Key 探出该账号真正能调用的模型、最高速率，
// 以及实时的每分钟额度（后者直接读 chat 响应头，不依赖登录）。
func probeAccount(apiKey string) probeResult {
	var out probeResult
	if apiKey == "" {
		return out
	}
	client := &http.Client{Timeout: 25 * time.Second, Transport: quotaTransport()}

	req, _ := http.NewRequest("GET", mistralBase+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return out
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return out
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return out
	}

	type result struct {
		id  string
		lim int
	}
	ch := make(chan result, len(list.Data))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, m := range list.Data {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			payload, _ := json.Marshal(map[string]any{
				"model": id, "messages": []map[string]string{{"role": "user", "content": "hi"}}, "max_tokens": 1,
			})
			rq, _ := http.NewRequest("POST", mistralBase+"/v1/chat/completions", bytes.NewReader(payload))
			rq.Header.Set("Authorization", "Bearer "+apiKey)
			rq.Header.Set("Content-Type", "application/json")
			rp, err := client.Do(rq)
			if err != nil {
				ch <- result{id, 0}
				return
			}
			io.Copy(io.Discard, rp.Body)
			rp.Body.Close()
			lim := 0
			if rp.StatusCode == 200 {
				lim, _ = strconv.Atoi(rp.Header.Get("x-ratelimit-limit-req-minute"))
				if lim == 0 {
					lim = -1 // 可用但未给出速率
				}
				// 同一账号下不同模型的限额并不相同（实测 ministral-3b 给 750，
				// codestral 只给 125）。账号的调度能力应该按"最高"算，
				// 否则加权轮询会低估这个账号。
				tokLim, _ := strconv.Atoi(rp.Header.Get("x-ratelimit-limit-tokens-minute"))
				remTok, _ := strconv.Atoi(rp.Header.Get("x-ratelimit-remaining-tokens-minute"))
				remReq, _ := strconv.Atoi(rp.Header.Get("x-ratelimit-remaining-req-minute"))

				quotaMu.Lock()
				if lim > out.limReq {
					out.limReq = lim
					out.remReq = remReq
					out.probeMdl = id
				}
				if tokLim > out.limTok {
					out.limTok = tokLim
					out.remTok = remTok
				}
				quotaMu.Unlock()
			}
			ch <- result{id, lim}
		}(m.ID)
	}
	wg.Wait()
	close(ch)

	for r := range ch {
		if r.lim == 0 {
			continue
		}
		out.models = append(out.models, r.id)
		if r.lim > out.maxRate {
			out.maxRate = r.lim
		}
	}
	out.count = len(out.models)
	if out.maxRate < 0 {
		out.maxRate = 0
	}
	return out
}

// ModelCapability 单个模型的能力与元数据，直接来自 Mistral 的 /v1/models。
type ModelCapability struct {
	ID               string          `json:"id"`
	Name             string          `json:"name,omitempty"`
	Description      string          `json:"description,omitempty"`
	MaxContextLength int             `json:"max_context_length,omitempty"`
	Aliases          []string        `json:"aliases,omitempty"`
	Capabilities     map[string]bool `json:"capabilities,omitempty"`
	Type             string          `json:"type,omitempty"`
	DefaultTemp      float64         `json:"default_temperature,omitempty"`
}

var modelCapCache struct {
	sync.Mutex
	at   time.Time
	data []ModelCapability
}

// fetchModelCapabilities 从上游拉模型元数据并缓存 10 分钟。
func fetchModelCapabilities(apiKey string) []ModelCapability {
	modelCapCache.Lock()
	if len(modelCapCache.data) > 0 && time.Since(modelCapCache.at) < quotaCacheTTL {
		d := modelCapCache.data
		modelCapCache.Unlock()
		return d
	}
	modelCapCache.Unlock()

	if apiKey == "" {
		return nil
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: quotaTransport()}
	req, _ := http.NewRequest("GET", mistralBase+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)

	var list struct {
		Data []ModelCapability `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil
	}

	modelCapCache.Lock()
	modelCapCache.data = list.Data
	modelCapCache.at = time.Now()
	modelCapCache.Unlock()
	return list.Data
}

// QueryAllQuota 并发查询多个账号。
func QueryAllQuota(store *Store, ids []int, force bool) []AccountQuota {
	out := make([]AccountQuota, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(idx, accID int) {
			defer wg.Done()
			out[idx] = QueryAccountQuota(store, accID, force)
		}(i, id)
	}
	wg.Wait()
	log.Printf("额度查询完成: %d 个账号", len(ids))
	return out
}
