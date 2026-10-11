package main

// 上游代理池。
//
// 之前 MISTRAL_PROXY 只能填一个地址，那个代理一挂整个链路就断，
// 而且不会有任何提示。这里改成池子：多代理轮询使用，连续失败的暂时拉黑，
// 到期自动放回；全挂时按配置决定是直连还是直接报错。

import (
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type upstreamProxyPool struct {
	mu          sync.Mutex
	proxies     []*url.URL
	idx         int
	fails       map[string]int
	banned      map[string]time.Time
	banAfter    int
	banFor      time.Duration
	allowDirect bool

	used    map[string]int64
	blocked map[string]int64
}

func newUpstreamProxyPool() *upstreamProxyPool {
	raw := os.Getenv("MISTRAL_PROXIES")
	if raw == "" {
		raw = os.Getenv("MISTRAL_PROXY")
	}
	if raw == "" {
		raw = os.Getenv("HTTPS_PROXY")
	}

	p := &upstreamProxyPool{
		fails:       map[string]int{},
		banned:      map[string]time.Time{},
		banAfter:    3,
		banFor:      60 * time.Second,
		allowDirect: true,
		used:        map[string]int64{},
		blocked:     map[string]int64{},
	}
	if v := os.Getenv("PROXY_BAN_AFTER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.banAfter = n
		}
	}
	if v := os.Getenv("PROXY_BAN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.banFor = time.Duration(n) * time.Second
		}
	}
	if os.Getenv("PROXY_ALLOW_DIRECT") == "0" {
		p.allowDirect = false
	}

	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == ' ' || r == ';'
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "://") {
			part = "http://" + part
		}
		u, err := url.Parse(part)
		if err != nil || u.Host == "" {
			log.Printf("代理池: 忽略无效地址 %q", part)
			continue
		}
		p.proxies = append(p.proxies, u)
	}

	if len(p.proxies) == 0 {
		log.Printf("上游代理: 未配置，直连 api.mistral.ai")
	} else {
		log.Printf("上游代理: 已加载 %d 个（轮询，连续失败 %d 次暂停 %v）",
			len(p.proxies), p.banAfter, p.banFor)
	}
	return p
}

// pick 轮询取一个可用代理；全被拉黑时按配置返回 nil（直连）。
func (p *upstreamProxyPool) pick() *url.URL {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.proxies) == 0 {
		return nil
	}

	p.expireBans()

	for i := 0; i < len(p.proxies); i++ {
		p.idx = (p.idx + 1) % len(p.proxies)
		u := p.proxies[p.idx]
		if _, bad := p.banned[u.String()]; bad {
			continue
		}
		p.used[u.String()]++
		return u
	}
	return nil // 全挂
}

// expireBans 解除到期的封禁。调用方必须已持有锁。
// 单独抽出来是因为状态查询也要走一遍——否则在没有新请求时，
// 已经到期的代理会一直显示成「已拉黑」。
func (p *upstreamProxyPool) expireBans() {
	now := time.Now()
	for k, until := range p.banned {
		if now.After(until) {
			delete(p.banned, k)
			p.fails[k] = 0
			log.Printf("代理池: %s 封禁到期，重新放回", k)
		}
	}
}

func (p *upstreamProxyPool) markOK(u *url.URL) {
	if u == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := u.String()
	p.fails[key] = 0
	delete(p.banned, key)
}

func (p *upstreamProxyPool) markFail(u *url.URL) {
	if u == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := u.String()
	p.fails[key]++
	if p.fails[key] >= p.banAfter {
		if _, already := p.banned[key]; !already {
			p.banned[key] = time.Now().Add(p.banFor)
			p.blocked[key]++
			log.Printf("代理池: %s 连续失败 %d 次，暂停 %v", key, p.fails[key], p.banFor)
		}
	}
}

// HasUsable 是否还有可用代理（供判断是否需要报错）。
func (p *upstreamProxyPool) HasUsable() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.proxies) == 0 {
		return true // 没配代理就是直连，不算不可用
	}
	for _, u := range p.proxies {
		if _, bad := p.banned[u.String()]; !bad {
			return true
		}
	}
	return false
}

// Status 供控制台展示。
func (p *upstreamProxyPool) Status() []map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireBans()
	out := make([]map[string]interface{}, 0, len(p.proxies))
	for _, u := range p.proxies {
		key := u.String()
		item := map[string]interface{}{
			"proxy":   key,
			"used":    p.used[key],
			"fails":   p.fails[key],
			"blocked": p.blocked[key],
			"banned":  false,
		}
		if until, bad := p.banned[key]; bad {
			item["banned"] = true
			item["resume_in"] = int(time.Until(until).Seconds())
		}
		out = append(out, item)
	}
	return out
}

// Summary 一行摘要。
func (p *upstreamProxyPool) Summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireBans()
	if len(p.proxies) == 0 {
		return "直连"
	}
	usable := 0
	for _, u := range p.proxies {
		if _, bad := p.banned[u.String()]; !bad {
			usable++
		}
	}
	return strconv.Itoa(usable) + "/" + strconv.Itoa(len(p.proxies)) + " 可用"
}
