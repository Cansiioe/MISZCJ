package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const mistralBase = "https://api.mistral.ai"

// newUpstreamClient 构造访问 Mistral 的 HTTP 客户端。
// 优先读 MISTRAL_PROXY，其次读标准环境变量 HTTPS_PROXY；都没设就直连。
func newUpstreamClient() *http.Client {
	proxyURL := getEnv("MISTRAL_PROXY", os.Getenv("HTTPS_PROXY"))
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
			log.Printf("上游请求走代理: %s", proxyURL)
		} else {
			log.Printf("代理地址解析失败 %q: %v", proxyURL, err)
		}
	}
	return &http.Client{Transport: transport, Timeout: 180 * time.Second}
}

type Proxy struct {
	store    *Store
	accounts []Account
	counter  atomic.Uint64
	mu       sync.RWMutex
	client   *http.Client
	filter   *ModelFilter
	limiter  *rateLimiter

	// 加权轮询状态
	lastIdx        int
	currentWeights []int
}

// defaultWeight 额度未知的账号按这个权重参与轮询。
// 取一个偏小的值，这样已知额度高的账号能拿到更多流量，但未知账号也不会被饿死。
const defaultWeight = 100

func NewProxy(store *Store) *Proxy {
	return &Proxy{
		store:   store,
		client:  newUpstreamClient(),
		filter:  &ModelFilter{},
		limiter: newRateLimiter(),
	}
}

// ── 模型白名单 ────────────────────────────────────────────────────────────────

// ModelFilter 按 config/models.available.json 过滤可用模型。
// 免费层账号对 mistral-small/medium/latest 这类旗舰模型的限额是 0，
// 放行只会让客户端拿到难懂的 429，所以这里提前拦掉。
type ModelFilter struct {
	allow   []string
	def     string
	enabled bool
}

type modelsFile struct {
	Allow   []string `json:"allow"`
	Default string   `json:"default"`
}

// loadModelFilter 读取白名单文件；缺失 / 为空 / 解析失败时返回空过滤器（等于不过滤）。
func loadModelFilter(path string) *ModelFilter {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("模型白名单: 读不到 %s，不做过滤", path)
		return &ModelFilter{}
	}
	var mf modelsFile
	if err := json.Unmarshal(data, &mf); err != nil {
		log.Printf("模型白名单: %s 解析失败: %v（不做过滤）", path, err)
		return &ModelFilter{}
	}
	allow := make([]string, 0, len(mf.Allow))
	for _, m := range mf.Allow {
		if s := strings.TrimSpace(m); s != "" {
			allow = append(allow, s)
		}
	}
	if len(allow) == 0 {
		log.Printf("模型白名单: %s 的 allow 为空，不做过滤", path)
		return &ModelFilter{}
	}
	log.Printf("模型白名单: 放行 %d 个模型（来自 %s，默认 %s）", len(allow), path, mf.Default)
	return &ModelFilter{allow: allow, def: mf.Default, enabled: true}
}

// Allows 判断模型是否放行；支持尾部 * 通配。空过滤器一律放行。
func (f *ModelFilter) Allows(model string) bool {
	if f == nil || !f.enabled || model == "" {
		return true
	}
	for _, rule := range f.allow {
		if strings.HasSuffix(rule, "*") {
			if strings.HasPrefix(model, strings.TrimSuffix(rule, "*")) {
				return true
			}
			continue
		}
		if rule == model {
			return true
		}
	}
	return false
}

func (f *ModelFilter) Enabled() bool { return f != nil && f.enabled }

func (f *ModelFilter) Allowed() []string {
	if f == nil {
		return nil
	}
	return f.allow
}

func (f *ModelFilter) DefaultModel() string {
	if f == nil {
		return ""
	}
	return f.def
}

// SetModelFilter 装载白名单。
func (p *Proxy) SetModelFilter(f *ModelFilter) {
	if f == nil {
		f = &ModelFilter{}
	}
	p.filter = f
}

func (p *Proxy) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts = p.store.GetAliveAccounts()
	log.Printf("proxy: 已加载 %d 个账号", len(p.accounts))
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

// pick 选一个账号。
// 语义：同一账号连续处理 proxy_limit 个请求后再换人；换人时按额度加权，
// 额度高的账号分到更多流量（实测账号之间有 125 与 750 的差距，均分是浪费）。
func (p *Proxy) pick() (Account, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.accounts) == 0 {
		return Account{}, false
	}

	limitStr := p.store.GetSetting("proxy_limit")
	limit, err := strconv.ParseUint(limitStr, 10, 64)
	if err != nil || limit == 0 {
		limit = 1 // 默认每个账号每轮只处理 1 个请求
	}

	seq := p.counter.Add(1) - 1
	if limit > 1 && seq%limit != 0 {
		return p.accounts[p.lastIdx], true
	}

	p.lastIdx = p.weightedPick()
	return p.accounts[p.lastIdx], true
}

// weightedPick 平滑加权轮询（Nginx 那套算法）。
// 每轮给各账号的当前权重加上它的权重，选最大的，再把它减去总权重——
// 这样既按权重分配，又不会让低权重账号长时间得不到调度。
func (p *Proxy) weightedPick() int {
	n := len(p.accounts)
	if n <= 1 {
		return 0
	}
	if len(p.currentWeights) != n {
		p.currentWeights = make([]int, n)
	}

	total, best := 0, 0
	for i, a := range p.accounts {
		w := a.RateLimitReq
		if w <= 0 {
			w = defaultWeight
		}
		p.currentWeights[i] += w
		total += w
		if p.currentWeights[i] > p.currentWeights[best] {
			best = i
		}
	}
	p.currentWeights[best] -= total
	return best
}

// Weights 返回当前各账号的调度权重，供控制台展示。
func (p *Proxy) Weights() map[int]int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[int]int, len(p.accounts))
	for _, a := range p.accounts {
		w := a.RateLimitReq
		if w <= 0 {
			w = defaultWeight
		}
		out[a.ID] = w
	}
	return out
}

// clientIP 取真实来源 IP：优先 X-Forwarded-For 的第一段。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// writeAPIError 统一 OpenAI 风格的错误响应。
func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{"message": message, "code": code},
	})
}

// keyPrefix 日志里只保留密钥前 12 位，避免完整密钥落盘。
func keyPrefix(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12]
}

// isChatEndpoint 判断是否属于需要过模型白名单的生成类端点。
// embeddings / ocr / moderations 等用另一套模型 id，不参与对话白名单。
func isChatEndpoint(path string) bool {
	return strings.HasSuffix(path, "/chat/completions") ||
		strings.HasSuffix(path, "/fim/completions") ||
		strings.HasSuffix(path, "/completions")
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 请求体后面转发还要用，先读出来
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求体失败", 500)
		return
	}

	authHeader := r.Header.Get("Authorization")
	var routerKey string
	if strings.HasPrefix(authHeader, "Bearer ") {
		routerKey = strings.TrimPrefix(authHeader, "Bearer ")
	}

	var reqBody map[string]interface{}
	json.Unmarshal(body, &reqBody)
	model, _ := reqBody["model"].(string)

	ip := clientIP(r)
	prefix := keyPrefix(routerKey)

	// 统一鉴权：密钥有效性 / 有效期 / Token 配额 / IP 白名单 / 模型白名单
	auth := p.store.AuthorizeKey(routerKey, ip, model)
	if !auth.OK {
		go p.store.LogRequest(LogEntry{
			KeyPrefix: prefix, IP: ip, Model: model,
			Status: http.StatusUnauthorized, ErrorCode: auth.Code, ErrorMessage: auth.Reason,
		})
		writeAPIError(w, http.StatusUnauthorized, auth.Code, auth.Reason)
		return
	}

	// /v1/models 不涉及具体模型，跳过模型白名单
	if r.URL.Path == "/v1/models" {
		p.proxyModels(w, r)
		return
	}

	// 全局模型白名单只针对对话类端点：embeddings / ocr / moderations 等
	// 用的是另一套模型 id（mistral-embed 等），不该被对话白名单拦掉。
	if isChatEndpoint(r.URL.Path) && !p.filter.Allows(model) {
		msg := fmt.Sprintf("模型 %q 不在可用白名单内（免费层不支持该模型），可用模型见 GET /v1/models", model)
		go p.store.LogRequest(LogEntry{
			KeyPrefix: prefix, IP: ip, Model: model,
			Status: http.StatusBadRequest, ErrorCode: "model_not_allowed", ErrorMessage: msg,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": msg,
				"code":    "model_not_allowed",
				"default": p.filter.DefaultModel(),
				"allowed": p.filter.Allowed(),
			},
		})
		return
	}

	// 按轮询顺序尝试账号（最多重试 3 次）
	for attempt := 0; attempt < 3; attempt++ {
		acct, ok := p.pick()
		if !ok {
			go p.store.LogRequest(LogEntry{
				KeyPrefix: prefix, IP: ip, Model: model,
				Status: http.StatusServiceUnavailable, ErrorCode: "no_accounts", ErrorMessage: "无可用账号",
			})
			writeAPIError(w, http.StatusServiceUnavailable, "no_accounts", "无可用账号")
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
		// 必须删掉客户端带来的 Accept-Encoding：一旦它存在，Go 就不会自动解压，
		// 我们拿到的是 gzip 字节流，usage 解析失败会让用量与配额全部记成 0。
		// 删掉后由 Transport 自行协商并透明解压。
		req.Header.Del("Accept-Encoding")

		// 上游限流：先取令牌，取不到就排队等，而不是直接打上游撞 429
		if err := p.limiter.Wait(r.Context()); err != nil {
			return // 客户端已断开
		}

		start := time.Now()
		resp, err := p.client.Do(req)
		if err != nil {
			continue
		}
		latency := int(time.Since(start).Milliseconds())

		// 顺手记下上游给的限额，供控制台展示
		if lim, _ := strconv.Atoi(resp.Header.Get("x-ratelimit-limit-req-minute")); lim > 0 {
			p.limiter.Observe(lim)
		}

		// 流式响应必须实时转发，不能整体缓冲——否则客户端拿不到打字机效果。
		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			p.streamResponse(w, resp, acct.ID, prefix, ip, model, latency, routerKey)
			return
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var respData map[string]interface{}
		json.Unmarshal(respBody, &respData)

		// 上游认证失败 → 标记账号失效，换下一个
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			errMsg := ""
			if e, ok := respData["error"].(map[string]interface{}); ok {
				errMsg, _ = e["message"].(string)
			}
			log.Printf("proxy: 账号 %d 已失效: %s", acct.ID, errMsg)
			p.store.MarkDead(acct.ID, errMsg)
			p.Reload()
			continue
		}

		// 提取用量与错误信息
		promptTokens, completionTokens := 0, 0
		if usage, ok := respData["usage"].(map[string]interface{}); ok {
			if v, ok := usage["prompt_tokens"].(float64); ok {
				promptTokens = int(v)
			}
			if v, ok := usage["completion_tokens"].(float64); ok {
				completionTokens = int(v)
			}
		}
		errorCode, errorMessage := "", ""
		if resp.StatusCode >= 400 {
			if e, ok := respData["error"].(map[string]interface{}); ok {
				errorCode, _ = e["code"].(string)
				errorMessage, _ = e["message"].(string)
			} else if m, ok := respData["message"].(string); ok {
				errorMessage = m
			}
		}

		tokens := promptTokens + completionTokens
		go p.store.LogRequest(LogEntry{
			AccountID: acct.ID, KeyPrefix: prefix, IP: ip, Model: model,
			PromptTokens: promptTokens, CompletionTokens: completionTokens,
			Status: resp.StatusCode, ErrorCode: errorCode, ErrorMessage: errorMessage,
			LatencyMs: latency,
		})
		go p.store.TouchKey(routerKey, tokens)

		// 原样转发响应
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	go p.store.LogRequest(LogEntry{
		KeyPrefix: prefix, IP: ip, Model: model,
		Status: http.StatusServiceUnavailable, ErrorCode: "exhausted", ErrorMessage: "所有账号均已耗尽",
	})
	writeAPIError(w, http.StatusServiceUnavailable, "exhausted", "所有账号均已耗尽")
}

func (p *Proxy) proxyModels(w http.ResponseWriter, r *http.Request) {
	acct, ok := p.pick()
	if !ok {
		// 没有可用账号：有白名单就按白名单给，否则给一份内置兜底列表
		w.Header().Set("Content-Type", "application/json")
		data := make([]map[string]string, 0)
		if p.filter.Enabled() {
			for _, id := range p.filter.Allowed() {
				data = append(data, map[string]string{"id": id})
			}
		} else {
			for _, id := range []string{"ministral-8b-latest", "ministral-3b-latest", "codestral-latest"} {
				data = append(data, map[string]string{"id": id})
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": data})
		return
	}

	req, _ := http.NewRequestWithContext(r.Context(), "GET", mistralBase+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+acct.APIKey)
	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "上游服务错误", 502)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(p.filterModelsJSON(body))
}

// filterModelsJSON 按白名单裁剪 /v1/models 的返回；解析失败就原样透传。
func (p *Proxy) filterModelsJSON(body []byte) []byte {
	if !p.filter.Enabled() {
		return body
	}
	var payload struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	kept := make([]json.RawMessage, 0, len(payload.Data))
	for _, raw := range payload.Data {
		var m struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		if p.filter.Allows(m.ID) {
			kept = append(kept, raw)
		}
	}
	out, err := json.Marshal(map[string]interface{}{"object": "list", "data": kept})
	if err != nil {
		return body
	}
	return out
}

// streamResponse 把上游的 SSE 响应实时转发给客户端，结束后补记审计。
// 要点：边读边写边 Flush，同时保留尾部若干字节用于解析 usage。
// 之前是 io.ReadAll 整体缓冲，客户端要等模型全部生成完才收到第一个字。
func (p *Proxy) streamResponse(
	w http.ResponseWriter, resp *http.Response,
	accountID int, prefix, ip, model string, latency int, routerKey string,
) {
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	// 必须去掉 Content-Length，否则 Go 会缓冲到写满才发，流式就白做了
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush() // 先把响应头推出去，客户端可以立刻开始读
	}

	const tailMax = 16 << 10
	tail := make([]byte, 0, tailMax)

	buf := make([]byte, 8<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break // 客户端已断开
			}
			if flusher != nil {
				flusher.Flush()
			}
			tail = append(tail, buf[:n]...)
			if len(tail) > tailMax {
				tail = append(tail[:0], tail[len(tail)-tailMax:]...)
			}
		}
		if err != nil {
			break
		}
	}

	promptTokens, completionTokens := extractUsageFromSSE(tail)
	go p.store.LogRequest(LogEntry{
		AccountID: accountID, KeyPrefix: prefix, IP: ip, Model: model,
		PromptTokens: promptTokens, CompletionTokens: completionTokens,
		Status: resp.StatusCode, LatencyMs: latency,
	})
	go p.store.TouchKey(routerKey, promptTokens+completionTokens)
}

// extractUsageFromSSE 从 SSE 尾部找带 usage 的 chunk（Mistral 放在最后一块）。
func extractUsageFromSSE(tail []byte) (int, int) {
	lines := bytes.Split(tail, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var chunk struct {
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(payload, &chunk); err == nil && chunk.Usage != nil {
			return chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
		}
	}
	return 0, 0
}
