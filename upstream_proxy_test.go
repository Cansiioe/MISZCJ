package main

import (
	"net/url"
	"testing"
	"time"
)

func TestProxyPoolParsing(t *testing.T) {
	t.Setenv("MISTRAL_PROXIES", "http://127.0.0.1:7890, socks5://10.0.0.1:1080;1.2.3.4:8080")
	p := newUpstreamProxyPool()

	if len(p.proxies) != 3 {
		t.Fatalf("应解析出 3 个代理，得到 %d", len(p.proxies))
	}
	// 没有 scheme 的应补上 http://
	if got := p.proxies[2].String(); got != "http://1.2.3.4:8080" {
		t.Errorf("缺 scheme 的地址应补成 http://，得到 %q", got)
	}
}

func TestProxyPoolEmptyMeansDirect(t *testing.T) {
	t.Setenv("MISTRAL_PROXIES", "")
	t.Setenv("MISTRAL_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	p := newUpstreamProxyPool()

	if len(p.proxies) != 0 {
		t.Fatalf("不该解析出代理，得到 %d", len(p.proxies))
	}
	if u := p.pick(); u != nil {
		t.Errorf("没配代理时应返回 nil（直连），得到 %v", u)
	}
	if !p.HasUsable() {
		t.Error("没配代理时 HasUsable 应为 true（直连可用）")
	}
	if p.Summary() != "直连" {
		t.Errorf("摘要应为「直连」，得到 %q", p.Summary())
	}
}

func TestProxyPoolRoundRobin(t *testing.T) {
	t.Setenv("MISTRAL_PROXIES", "http://a:1,http://b:2")
	p := newUpstreamProxyPool()

	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		u := p.pick()
		if u == nil {
			t.Fatal("不该返回 nil")
		}
		seen[u.Host]++
	}
	if seen["a:1"] != 3 || seen["b:2"] != 3 {
		t.Errorf("轮询应各命中 3 次，实际 %v", seen)
	}
}

func TestProxyPoolBanAndExpire(t *testing.T) {
	t.Setenv("MISTRAL_PROXIES", "http://a:1,http://b:2")
	t.Setenv("PROXY_BAN_AFTER", "2")
	t.Setenv("PROXY_BAN_SECONDS", "1")
	p := newUpstreamProxyPool()

	if p.banAfter != 2 {
		t.Fatalf("banAfter 应为 2，得到 %d", p.banAfter)
	}

	bad, _ := url.Parse("http://a:1")

	// 失败 1 次：还不该拉黑
	p.markFail(bad)
	if _, banned := p.banned["http://a:1"]; banned {
		t.Error("未达阈值不该拉黑")
	}
	// 失败 2 次：拉黑
	p.markFail(bad)
	if _, banned := p.banned["http://a:1"]; !banned {
		t.Fatal("达到阈值应拉黑")
	}

	// 拉黑期间 pick 只会给出另一个
	for i := 0; i < 4; i++ {
		if u := p.pick(); u == nil || u.Host != "b:2" {
			t.Fatalf("拉黑期间应只用 b:2，得到 %v", u)
		}
	}
	if p.Summary() != "1/2 可用" {
		t.Errorf("摘要应为 1/2 可用，得到 %q", p.Summary())
	}

	// 等封禁到期，pick 应自动放回
	time.Sleep(1100 * time.Millisecond)
	if u := p.pick(); u == nil {
		t.Fatal("封禁到期后不该返回 nil")
	}
	if p.Summary() != "2/2 可用" {
		t.Errorf("解封后摘要应为 2/2 可用，得到 %q", p.Summary())
	}
}

func TestProxyPoolMarkOKClearsBan(t *testing.T) {
	t.Setenv("MISTRAL_PROXIES", "http://a:1")
	t.Setenv("PROXY_BAN_AFTER", "1")
	p := newUpstreamProxyPool()

	u, _ := url.Parse("http://a:1")
	p.markFail(u)
	if _, banned := p.banned["http://a:1"]; !banned {
		t.Fatal("应已拉黑")
	}
	p.markOK(u)
	if _, banned := p.banned["http://a:1"]; banned {
		t.Error("markOK 应清除拉黑状态")
	}
	if p.fails["http://a:1"] != 0 {
		t.Error("markOK 应把失败计数归零")
	}
}

func TestProxyPoolAllBannedReturnsNil(t *testing.T) {
	t.Setenv("MISTRAL_PROXIES", "http://a:1,http://b:2")
	t.Setenv("PROXY_BAN_AFTER", "1")
	t.Setenv("PROXY_BAN_SECONDS", "60")
	p := newUpstreamProxyPool()

	a, _ := url.Parse("http://a:1")
	b, _ := url.Parse("http://b:2")
	p.markFail(a)
	p.markFail(b)

	if u := p.pick(); u != nil {
		t.Errorf("全部拉黑时应返回 nil，得到 %v", u)
	}
	if p.HasUsable() {
		t.Error("全部拉黑时 HasUsable 应为 false")
	}
	if p.Summary() != "0/2 可用" {
		t.Errorf("摘要应为 0/2 可用，得到 %q", p.Summary())
	}
}

func TestProxyPoolStatusUnbansOnRead(t *testing.T) {
	// 解封不能只发生在 pick()：没有新请求时，状态查询也应看到已解封
	t.Setenv("MISTRAL_PROXIES", "http://a:1")
	t.Setenv("PROXY_BAN_AFTER", "1")
	t.Setenv("PROXY_BAN_SECONDS", "1")
	p := newUpstreamProxyPool()

	u, _ := url.Parse("http://a:1")
	p.markFail(u)
	if !p.Status()[0]["banned"].(bool) {
		t.Fatal("应处于拉黑状态")
	}

	time.Sleep(1100 * time.Millisecond)
	// 直接查状态（不调用 pick）
	if p.Status()[0]["banned"].(bool) {
		t.Error("封禁到期后，即使没有新请求，状态查询也应显示已解封")
	}
}

func TestProxyPoolStatusFields(t *testing.T) {
	t.Setenv("MISTRAL_PROXIES", "http://a:1")
	p := newUpstreamProxyPool()
	p.pick()
	p.pick()

	st := p.Status()
	if len(st) != 1 {
		t.Fatalf("应返回 1 条，得到 %d", len(st))
	}
	if st[0]["used"].(int64) != 2 {
		t.Errorf("使用次数应为 2，得到 %v", st[0]["used"])
	}
	if st[0]["banned"].(bool) {
		t.Error("不该是拉黑状态")
	}
}
