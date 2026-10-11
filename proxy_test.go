package main

import (
	"encoding/json"
	"testing"
)

/* ── 模型白名单 ─────────────────────────────────────────────────────────── */

func TestModelFilterAllows(t *testing.T) {
	cases := []struct {
		name  string
		allow []string
		model string
		want  bool
	}{
		{"精确命中", []string{"a", "b"}, "a", true},
		{"不在名单", []string{"a", "b"}, "c", false},
		{"前缀通配命中", []string{"ministral-*"}, "ministral-8b-latest", true},
		{"前缀通配不命中", []string{"ministral-*"}, "codestral-latest", false},
		{"混用精确与通配_精确分支", []string{"codestral-latest", "ministral-*"}, "codestral-latest", true},
		{"混用精确与通配_通配分支", []string{"codestral-latest", "ministral-*"}, "ministral-3b-2512", true},
		{"混用精确与通配_都不命中", []string{"codestral-latest", "ministral-*"}, "voxtral-small-latest", false},
		{"空模型名一律放行", []string{"a"}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &ModelFilter{allow: c.allow, enabled: true}
			if got := f.Allows(c.model); got != c.want {
				t.Errorf("Allows(%q) = %v，期望 %v", c.model, got, c.want)
			}
		})
	}
}

func TestModelFilterDisabledPassesEverything(t *testing.T) {
	// 文件缺失 / allow 为空 / JSON 坏掉时过滤器不启用，
	// 此时必须放行一切，否则一个坏配置会把整个代理锁死。
	var empty ModelFilter
	if !empty.Allows("whatever") {
		t.Error("未启用的过滤器应放行所有模型")
	}
	var nilFilter *ModelFilter
	if !nilFilter.Allows("whatever") {
		t.Error("nil 过滤器应放行所有模型")
	}
}

func TestModelFilterHelpers(t *testing.T) {
	var f ModelFilter
	if f.Enabled() {
		t.Error("零值过滤器不该是启用状态")
	}
	if f.Allowed() != nil {
		t.Error("零值过滤器的 Allowed() 应为 nil")
	}
	if f.DefaultModel() != "" {
		t.Error("零值过滤器的默认模型应为空")
	}

	g := &ModelFilter{allow: []string{"x"}, def: "x", enabled: true}
	if !g.Enabled() || g.DefaultModel() != "x" || len(g.Allowed()) != 1 {
		t.Error("已启用过滤器的取值不正确")
	}
}

/* ── SSE usage 解析 ─────────────────────────────────────────────────────── */

func TestExtractUsageFromSSE(t *testing.T) {
	sse := []byte(
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]," +
			"\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":34}}\n\n" +
			"data: [DONE]\n\n")

	p, c := extractUsageFromSSE(sse)
	if p != 12 || c != 34 {
		t.Errorf("解析结果 prompt=%d completion=%d，期望 12/34", p, c)
	}
}

func TestExtractUsageFromSSEMissing(t *testing.T) {
	cases := map[string][]byte{
		"没有 usage 字段": []byte("data: {\"a\":1}\n\ndata: [DONE]\n"),
		"空输入":         nil,
		"只有 DONE":     []byte("data: [DONE]\n"),
		"非法 JSON":     []byte("data: {not json}\n"),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			p, c := extractUsageFromSSE(in)
			if p != 0 || c != 0 {
				t.Errorf("应返回 0/0，实际 %d/%d", p, c)
			}
		})
	}
}

func TestExtractUsageFromSSETakesLast(t *testing.T) {
	// usage 通常在最末一块；若中间也出现，应以最后一块为准
	sse := []byte(
		"data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n" +
			"data: {\"usage\":{\"prompt_tokens\":99,\"completion_tokens\":88}}\n\n")
	p, c := extractUsageFromSSE(sse)
	if p != 99 || c != 88 {
		t.Errorf("应取最后一块的 usage，实际 %d/%d", p, c)
	}
}

/* ── 其他小工具 ─────────────────────────────────────────────────────────── */

func TestKeyPrefix(t *testing.T) {
	if got := keyPrefix("nm-0123456789abcdef"); got != "nm-012345678" {
		t.Errorf("长密钥应截断到 12 位，得到 %q", got)
	}
	if got := keyPrefix("short"); got != "short" {
		t.Errorf("短密钥应原样返回，得到 %q", got)
	}
	if got := keyPrefix(""); got != "" {
		t.Errorf("空密钥应返回空，得到 %q", got)
	}
}

func TestIsChatEndpoint(t *testing.T) {
	yes := []string{"/v1/chat/completions", "/v1/fim/completions", "/v1/completions"}
	no := []string{"/v1/embeddings", "/v1/ocr", "/v1/moderations", "/v1/models"}
	for _, p := range yes {
		if !isChatEndpoint(p) {
			t.Errorf("%s 应被识别为对话类端点", p)
		}
	}
	for _, p := range no {
		if isChatEndpoint(p) {
			t.Errorf("%s 不应被识别为对话类端点", p)
		}
	}
}

func TestFilterModelsJSON(t *testing.T) {
	p := &Proxy{filter: &ModelFilter{allow: []string{"keep-me", "prefix-*"}, enabled: true}}

	in := []byte(`{"object":"list","data":[
		{"id":"keep-me","other":1},
		{"id":"prefix-a"},
		{"id":"drop-me"},
		{"id":"prefix-b"}
	]}`)

	out := p.filterModelsJSON(in)
	var parsed struct {
		Object string `json:"object"`
		Data   []struct {
			ID    string `json:"id"`
			Other int    `json:"other"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if parsed.Object != "list" {
		t.Errorf("object 字段应保留，得到 %q", parsed.Object)
	}
	got := make([]string, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		got = append(got, d.ID)
	}
	want := []string{"keep-me", "prefix-a", "prefix-b"}
	if len(got) != len(want) {
		t.Fatalf("过滤后剩 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项为 %q，期望 %q", i, got[i], want[i])
		}
	}
	if parsed.Data[0].Other != 1 {
		t.Error("保留项的额外字段被丢掉了")
	}
}

func TestFilterModelsJSONPassthrough(t *testing.T) {
	// 过滤器未启用时应原样返回
	p := &Proxy{filter: &ModelFilter{}}
	in := []byte(`{"object":"list","data":[{"id":"anything"}]}`)
	if string(p.filterModelsJSON(in)) != string(in) {
		t.Error("过滤器未启用时应原样透传")
	}
	// 上游返回非法 JSON 时也不能崩，原样返回
	p2 := &Proxy{filter: &ModelFilter{allow: []string{"x"}, enabled: true}}
	bad := []byte(`not json at all`)
	if string(p2.filterModelsJSON(bad)) != string(bad) {
		t.Error("解析失败时应原样透传")
	}
}
