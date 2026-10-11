package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain 静音日志：每个用例都建新库，迁移日志会刷屏。
func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// newTestStore 建一个临时库，测试结束自动清理。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("ADMIN_PASSWORD", "test-password") // 避免走到随机密码分支
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

/* ── 规则列表匹配（密钥的模型 / IP 白名单共用）────────────────────────────── */

func TestMatchRuleList(t *testing.T) {
	cases := []struct {
		name  string
		list  string
		value string
		want  bool
	}{
		{"精确命中", "a,b,c", "b", true},
		{"未命中", "a,b,c", "d", false},
		{"忽略空格", "a, b , c", "b", true},
		{"前缀通配命中", "10.0.0.*", "10.0.0.5", true},
		{"前缀通配不命中", "10.0.0.*", "10.0.1.5", false},
		{"模型前缀通配", "ministral-*", "ministral-8b-latest", true},
		{"空列表", "", "a", false},
		{"空列表加空值", "  ,  ", "a", false},
		{"忽略空项", "a,,b", "b", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchRuleList(c.list, c.value); got != c.want {
				t.Errorf("matchRuleList(%q, %q) = %v，期望 %v", c.list, c.value, got, c.want)
			}
		})
	}
}

/* ── 密钥状态推导 ───────────────────────────────────────────────────────── */

func TestKeyState(t *testing.T) {
	future := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02 15:04:05")
	past := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")

	cases := []struct {
		name string
		key  RouterKey
		want string
	}{
		{"正常", RouterKey{Active: true}, "ok"},
		{"已停用", RouterKey{Active: false}, "disabled"},
		{"未过期", RouterKey{Active: true, ExpiresAt: future}, "ok"},
		{"已过期", RouterKey{Active: true, ExpiresAt: past}, "expired"},
		{"额度未用尽", RouterKey{Active: true, QuotaTokens: 100, UsedTokens: 99}, "ok"},
		{"额度刚好用尽", RouterKey{Active: true, QuotaTokens: 100, UsedTokens: 100}, "over_quota"},
		{"额度超出", RouterKey{Active: true, QuotaTokens: 100, UsedTokens: 150}, "over_quota"},
		{"停用优先于过期", RouterKey{Active: false, ExpiresAt: past}, "disabled"},
		{"过期优先于超额", RouterKey{Active: true, ExpiresAt: past, QuotaTokens: 1, UsedTokens: 99}, "expired"},
		{"无额度限制", RouterKey{Active: true, QuotaTokens: 0, UsedTokens: 999999}, "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keyState(c.key); got != c.want {
				t.Errorf("keyState = %q，期望 %q", got, c.want)
			}
		})
	}
}

/* ── 密钥鉴权 ───────────────────────────────────────────────────────────── */

func TestAuthorizeKey(t *testing.T) {
	s := newTestStore(t)

	plain := s.AddRouterKeyWith("普通", "", 0, "", "")
	expired := s.AddRouterKeyWith("过期",
		time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05"), 0, "", "")
	quota := s.AddRouterKeyWith("超额", "", 100, "", "")
	modelScoped := s.AddRouterKeyWith("限模型", "", 0, "ministral-8b-latest", "")
	ipScoped := s.AddRouterKeyWith("限IP", "", 0, "", "10.0.0.*")
	disabled := s.AddRouterKeyWith("停用", "", 0, "", "")
	s.RevokeRouterKey(disabled)
	s.db.Exec("UPDATE router_keys SET used_tokens=200 WHERE key=?", quota)

	t.Run("无效密钥", func(t *testing.T) {
		r := s.AuthorizeKey("nm-nonexistent", "", "")
		if r.OK || r.Code != "invalid_key" {
			t.Errorf("应判无效，得到 ok=%v code=%q", r.OK, r.Code)
		}
	})
	t.Run("普通密钥放行", func(t *testing.T) {
		if r := s.AuthorizeKey(plain, "1.2.3.4", "any-model"); !r.OK {
			t.Errorf("应放行，得到 %q %s", r.Code, r.Reason)
		}
	})
	t.Run("已过期", func(t *testing.T) {
		if r := s.AuthorizeKey(expired, "", ""); r.OK || r.Code != "key_expired" {
			t.Errorf("应判过期，得到 ok=%v code=%q", r.OK, r.Code)
		}
	})
	t.Run("额度用尽", func(t *testing.T) {
		if r := s.AuthorizeKey(quota, "", ""); r.OK || r.Code != "key_over_quota" {
			t.Errorf("应判超额，得到 ok=%v code=%q", r.OK, r.Code)
		}
	})
	t.Run("已停用", func(t *testing.T) {
		if r := s.AuthorizeKey(disabled, "", ""); r.OK || r.Code != "key_disabled" {
			t.Errorf("应判停用，得到 ok=%v code=%q", r.OK, r.Code)
		}
	})
	t.Run("模型白名单_命中", func(t *testing.T) {
		if r := s.AuthorizeKey(modelScoped, "", "ministral-8b-latest"); !r.OK {
			t.Errorf("名单内模型应放行，得到 %q", r.Code)
		}
	})
	t.Run("模型白名单_越权", func(t *testing.T) {
		r := s.AuthorizeKey(modelScoped, "", "codestral-latest")
		if r.OK || r.Code != "model_not_allowed_for_key" {
			t.Errorf("名单外模型应拒绝，得到 ok=%v code=%q", r.OK, r.Code)
		}
	})
	t.Run("模型白名单_空模型名跳过校验", func(t *testing.T) {
		// /v1/models 这类请求没有模型名，不该被模型白名单拦掉
		if r := s.AuthorizeKey(modelScoped, "", ""); !r.OK {
			t.Errorf("空模型名应跳过模型白名单，得到 %q", r.Code)
		}
	})
	t.Run("IP白名单_命中", func(t *testing.T) {
		if r := s.AuthorizeKey(ipScoped, "10.0.0.7", ""); !r.OK {
			t.Errorf("名单内 IP 应放行，得到 %q", r.Code)
		}
	})
	t.Run("IP白名单_越权", func(t *testing.T) {
		r := s.AuthorizeKey(ipScoped, "203.0.113.1", "")
		if r.OK || r.Code != "ip_not_allowed" {
			t.Errorf("名单外 IP 应拒绝，得到 ok=%v code=%q", r.OK, r.Code)
		}
	})
}

/* ── 账号 ───────────────────────────────────────────────────────────────── */

func TestAddAccountFullKeepsPassword(t *testing.T) {
	s := newTestStore(t)

	if err := s.AddAccountFull("a@example.com", "key-1", "pw-1", "farm"); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	_, pw, _ := s.GetAccountSecret(1)
	if pw != "pw-1" {
		t.Errorf("密码应为 pw-1，得到 %q", pw)
	}

	// 重复导入：不应把已有密码覆盖成空
	if err := s.AddAccountFull("a@example.com", "key-2", "", "farm"); err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}
	email, pw2, key2 := s.GetAccountSecret(1)
	if pw2 != "pw-1" {
		t.Errorf("空密码不该覆盖原值，得到 %q", pw2)
	}
	if key2 != "key-1" {
		t.Errorf("密钥不该被覆盖，得到 %q", key2)
	}
	if email != "a@example.com" {
		t.Errorf("邮箱不符: %q", email)
	}

	// 带新密码导入应更新密码
	s.AddAccountFull("a@example.com", "key-3", "pw-2", "farm")
	_, pw3, _ := s.GetAccountSecret(1)
	if pw3 != "pw-2" {
		t.Errorf("新密码应覆盖旧值，得到 %q", pw3)
	}
}

func TestAccountStatsAndRateLimit(t *testing.T) {
	s := newTestStore(t)
	s.AddAccount("a@example.com", "k1", "farm")
	s.AddAccount("b@example.com", "k2", "farm")

	s.UpdateAccountRateLimit(1, 750)
	s.UpdateAccountRateLimit(2, 0) // 0 应被忽略

	alive := s.GetAliveAccounts()
	if len(alive) != 2 {
		t.Fatalf("应有 2 个存活账号，得到 %d", len(alive))
	}
	if alive[0].RateLimitReq != 750 {
		t.Errorf("账号 1 额度应为 750，得到 %d", alive[0].RateLimitReq)
	}
	if alive[1].RateLimitReq != 0 {
		t.Errorf("传 0 不该写入，得到 %d", alive[1].RateLimitReq)
	}

	s.MarkDead(1, "认证失败")
	if got := len(s.GetAliveAccounts()); got != 1 {
		t.Errorf("标记失效后应剩 1 个存活，得到 %d", got)
	}
	dead := s.GetDeadAccounts()
	if len(dead) != 1 || dead[0].DeadReason != "认证失败" {
		t.Errorf("失效账号信息不对: %+v", dead)
	}

	s.ReviveAccount(1)
	if got := len(s.GetAliveAccounts()); got != 2 {
		t.Errorf("恢复后应有 2 个存活，得到 %d", got)
	}
}

func TestGetAccountsWithStats(t *testing.T) {
	s := newTestStore(t)
	s.AddAccount("a@example.com", "k1", "farm")
	s.AddAccount("b@example.com", "k2", "farm")
	s.MarkDead(2, "挂了")

	// 给账号 1 造两条调用记录
	s.LogRequest(LogEntry{AccountID: 1, Model: "m", PromptTokens: 10, CompletionTokens: 5, Status: 200})
	s.LogRequest(LogEntry{AccountID: 1, Model: "m", PromptTokens: 4, CompletionTokens: 1, Status: 500, ErrorMessage: "boom"})

	alive := s.GetAccountsWithStats(false)
	if len(alive) != 1 {
		t.Fatalf("存活应为 1，得到 %d", len(alive))
	}
	a := alive[0]
	if a.Requests != 2 || a.TotalTokens != 20 {
		t.Errorf("统计不对：requests=%d tokens=%d，期望 2/20", a.Requests, a.TotalTokens)
	}
	if a.LastError != "boom" {
		t.Errorf("最近错误应为 boom，得到 %q", a.LastError)
	}
	if a.IsDead {
		t.Error("存活账号的 is_dead 应为 false")
	}

	dead := s.GetAccountsWithStats(true)
	if len(dead) != 1 || !dead[0].IsDead {
		t.Errorf("失效列表不对: %+v", dead)
	}
}

/* ── 日志与清理 ─────────────────────────────────────────────────────────── */

func TestQueryLogsFilters(t *testing.T) {
	s := newTestStore(t)
	s.AddAccount("a@example.com", "k1", "farm")
	s.LogRequest(LogEntry{AccountID: 1, KeyPrefix: "nm-aaa", Model: "m1", Status: 200, LatencyMs: 100})
	s.LogRequest(LogEntry{AccountID: 1, KeyPrefix: "nm-bbb", Model: "m2", Status: 429, ErrorCode: "rate_limited"})
	s.LogRequest(LogEntry{AccountID: 1, KeyPrefix: "nm-aaa", Model: "m1", Status: 500})

	t.Run("全部", func(t *testing.T) {
		items, total := s.QueryLogs(LogQuery{})
		if total != 3 || len(items) != 3 {
			t.Errorf("应返回 3 条，得到 total=%d len=%d", total, len(items))
		}
	})
	t.Run("按密钥", func(t *testing.T) {
		items, total := s.QueryLogs(LogQuery{KeyPrefix: "nm-aaa"})
		if total != 2 || len(items) != 2 {
			t.Errorf("nm-aaa 应有 2 条，得到 %d", total)
		}
	})
	t.Run("按模型", func(t *testing.T) {
		_, total := s.QueryLogs(LogQuery{Model: "m2"})
		if total != 1 {
			t.Errorf("m2 应有 1 条，得到 %d", total)
		}
	})
	t.Run("只成功", func(t *testing.T) {
		_, total := s.QueryLogs(LogQuery{Status: "ok"})
		if total != 1 {
			t.Errorf("成功应有 1 条，得到 %d", total)
		}
	})
	t.Run("只失败", func(t *testing.T) {
		_, total := s.QueryLogs(LogQuery{Status: "fail"})
		if total != 2 {
			t.Errorf("失败应有 2 条，得到 %d", total)
		}
	})
	t.Run("分页", func(t *testing.T) {
		items, total := s.QueryLogs(LogQuery{Limit: 2, Offset: 0})
		if total != 3 || len(items) != 2 {
			t.Errorf("分页应返回 total=3 len=2，得到 %d/%d", total, len(items))
		}
	})
	t.Run("按时间过滤", func(t *testing.T) {
		_, total := s.QueryLogs(LogQuery{Since: time.Now().Add(time.Hour).Format("2006-01-02 15:04:05")})
		if total != 0 {
			t.Errorf("未来时间应无记录，得到 %d", total)
		}
	})
}

func TestCleanupLogs(t *testing.T) {
	s := newTestStore(t)
	s.LogRequest(LogEntry{AccountID: 1, Model: "m", Status: 200})

	// 插入一条 100 天前的旧记录
	s.db.Exec(`INSERT INTO request_logs(ts,account_id,model,status_code,total_tokens)
		VALUES(datetime('now','-100 days'),1,'old',200,5)`)

	if n := s.CleanupLogs(30); n != 1 {
		t.Errorf("应清理 1 条，得到 %d", n)
	}
	if _, total := s.QueryLogs(LogQuery{}); total != 1 {
		t.Errorf("清理后应剩 1 条，得到 %d", total)
	}
	// keepDays<=0 表示不清理
	if n := s.CleanupLogs(0); n != 0 {
		t.Errorf("keepDays=0 不该删任何东西，得到 %d", n)
	}
}

func TestPurgeExpiredKeys(t *testing.T) {
	s := newTestStore(t)
	old := time.Now().UTC().Add(-60 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	recent := time.Now().UTC().Add(-1 * 24 * time.Hour).Format("2006-01-02 15:04:05")

	s.AddRouterKeyWith("很久前过期", old, 0, "", "")
	s.AddRouterKeyWith("刚过期", recent, 0, "", "")
	forever := s.AddRouterKeyWith("永不过期", "", 0, "", "")
	manual := s.AddRouterKeyWith("手动停用", "", 0, "", "")
	s.RevokeRouterKey(manual)

	// 只删「过期超过 30 天」的
	if n := s.PurgeExpiredKeys(30); n != 1 {
		t.Errorf("应删 1 把，得到 %d", n)
	}
	keys := s.GetRouterKeys()
	got := map[string]bool{}
	for _, k := range keys {
		got[k.Key] = true
	}
	if !got[forever] {
		t.Error("永不过期的密钥不该被删")
	}
	if !got[manual] {
		t.Error("手动停用的密钥不该被删（可能是刻意保留的）")
	}
	// 注意 NewStore 会自动建一把 default 密钥，所以总数是 4
	if len(keys) != 4 {
		t.Errorf("应剩 4 把密钥（含默认密钥），得到 %d", len(keys))
	}
}

/* ── 用量统计 ───────────────────────────────────────────────────────────── */

func TestGetStats(t *testing.T) {
	s := newTestStore(t)
	s.AddAccount("a@example.com", "k1", "farm")
	s.AddRouterKeyWith("k", "", 0, "", "")
	s.LogRequest(LogEntry{AccountID: 1, Model: "m", PromptTokens: 10, CompletionTokens: 5, Status: 200, LatencyMs: 200})
	s.LogRequest(LogEntry{AccountID: 1, Model: "m", PromptTokens: 2, CompletionTokens: 1, Status: 500, LatencyMs: 400})

	st := s.GetStats()
	if st.TotalAccounts != 1 || st.AliveAccounts != 1 {
		t.Errorf("账号统计不对: %+v", st)
	}
	if st.TotalRequests != 2 {
		t.Errorf("请求数应为 2，得到 %d", st.TotalRequests)
	}
	if st.TotalTokens != 18 {
		t.Errorf("token 总数应为 18，得到 %d", st.TotalTokens)
	}
	// 含 NewStore 自动创建的 default 密钥
	if st.TotalKeys != 2 {
		t.Errorf("密钥数应为 2（含默认密钥），得到 %d", st.TotalKeys)
	}
	if st.SuccessRate < 0.49 || st.SuccessRate > 0.51 {
		t.Errorf("成功率应约 0.5，得到 %f", st.SuccessRate)
	}
	if st.AvgLatencyMs != 300 {
		t.Errorf("平均延迟应为 300，得到 %d", st.AvgLatencyMs)
	}
	if st.TodayRequests != 2 {
		t.Errorf("今日请求应为 2，得到 %d", st.TodayRequests)
	}
}

func TestDailySeries(t *testing.T) {
	s := newTestStore(t)
	s.LogRequest(LogEntry{AccountID: 1, Model: "m", PromptTokens: 10, CompletionTokens: 10, Status: 200})
	s.LogRequest(LogEntry{AccountID: 1, Model: "m", Status: 429})

	series := s.GetDailySeries(7)
	if len(series) != 1 {
		t.Fatalf("应聚合出 1 天，得到 %d", len(series))
	}
	if series[0].Requests != 2 || series[0].Tokens != 20 || series[0].Failures != 1 {
		t.Errorf("趋势数据不对: %+v", series[0])
	}
	// 非法天数应回落到默认值而不是崩
	if got := s.GetDailySeries(0); got == nil {
		t.Error("days=0 应回落到默认窗口")
	}
}
