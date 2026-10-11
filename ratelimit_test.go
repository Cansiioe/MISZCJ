package main

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiterDisabledByDefault(t *testing.T) {
	t.Setenv("UPSTREAM_REQ_PER_MINUTE", "")
	rl := newRateLimiter()
	if rl.Status()["enabled"].(bool) {
		t.Fatal("未配置时不该启用限流")
	}
	// 未启用时必须立刻放行，不能阻塞
	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := rl.Wait(context.Background()); err != nil {
			t.Fatalf("未启用时不该报错: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("未启用时不该有延迟，实际耗时 %v", elapsed)
	}
}

func TestRateLimiterBurstThenThrottle(t *testing.T) {
	t.Setenv("UPSTREAM_BURST", "3")
	rl := newRateLimiter()
	rl.SetLimit(6000) // 100/s，够快，测试不会太慢

	// 前 3 个走突发额度，应立刻通过
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := rl.Wait(context.Background()); err != nil {
			t.Fatalf("突发额度内不该报错: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("突发额度内应立刻返回，实际 %v", elapsed)
	}

	// 第 4 个开始要等令牌补充（100/s → 约 10ms）
	start = time.Now()
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatalf("等待令牌时不该报错: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Millisecond {
		t.Errorf("超出突发额度后应排队等待，实际只用了 %v", elapsed)
	}
}

func TestRateLimiterBurstCappedByLimit(t *testing.T) {
	// 突发额度不能超过每分钟限额本身
	t.Setenv("UPSTREAM_BURST", "1000")
	rl := newRateLimiter()
	rl.SetLimit(5)

	st := rl.Status()
	if st["limit_per_min"].(int) != 5 {
		t.Errorf("每分钟限额应为 5，得到 %v", st["limit_per_min"])
	}
	if st["burst"].(int) != 5 {
		t.Errorf("突发额度应被压到 5，得到 %v", st["burst"])
	}
}

func TestRateLimiterSetLimitZeroDisables(t *testing.T) {
	t.Setenv("UPSTREAM_BURST", "1")
	rl := newRateLimiter()
	rl.SetLimit(1)
	if !rl.Status()["enabled"].(bool) {
		t.Fatal("SetLimit(1) 后应处于启用状态")
	}
	rl.SetLimit(0)
	if rl.Status()["enabled"].(bool) {
		t.Error("SetLimit(0) 应关闭限流")
	}
}

func TestRateLimiterRespectsContextCancel(t *testing.T) {
	t.Setenv("UPSTREAM_BURST", "1")
	rl := newRateLimiter()
	rl.SetLimit(1) // 极慢：1/分钟

	// 先用掉唯一的令牌
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatalf("首个令牌应可用: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := rl.Wait(ctx)
	if err == nil {
		t.Fatal("限速中且 ctx 超时，应返回错误")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("应及时响应 ctx 取消，实际 %v", elapsed)
	}
}
