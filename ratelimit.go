package main

// 上游速率控制。
//
// 每个账号的每分钟请求限额是上游定的（实测 125 ~ 750 不等）。router 不加约束时，
// 多个客户端同时打就会一起撞 429，而且撞了之后要等整整一分钟窗口才恢复。
// 这里放一个全局令牌桶：请求先取令牌，取不到就排队等，而不是直接打上游。

import (
	"context"
	"log"
	"math"
	"os"
	"strconv"
	"sync"
	"time"
)

type rateLimiter struct {
	mu       sync.Mutex
	perMin   int     // 每分钟限额（展示用）
	capacity float64 // 桶容量 = 允许的突发量
	rate     float64 // 每秒补充的令牌数
	tokens   float64 // 当前可用令牌
	last     time.Time
	enabled  bool
	learned  int // 从上游响应头观察到的最小限额，仅作展示
	waited   int64
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{last: time.Now()}
	if v := os.Getenv("UPSTREAM_REQ_PER_MINUTE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			rl.SetLimit(n)
			log.Printf("上游限流: 已启用，%d req/min", n)
		}
	} else {
		log.Printf("上游限流: 未配置（设 UPSTREAM_REQ_PER_MINUTE 可启用）")
	}
	return rl
}

// SetLimit 设置每分钟限额；perMin<=0 表示关闭限流。
//
// 桶容量单独控制（UPSTREAM_BURST，默认 5）：如果把容量设成整个每分钟额度，
// 突发 60 个请求会一次性全部放行，等于没限流——上游按分钟窗口计数，照样撞 429。
// 所以只留很小的突发额度，其余靠平滑速率。
func (rl *rateLimiter) SetLimit(perMin int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if perMin <= 0 {
		rl.enabled = false
		return
	}
	burst := 5
	if v := os.Getenv("UPSTREAM_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			burst = n
		}
	}
	rl.perMin = perMin
	rl.rate = float64(perMin) / 60.0
	rl.capacity = math.Min(float64(perMin), float64(burst))
	if rl.tokens > rl.capacity || rl.tokens <= 0 {
		rl.tokens = rl.capacity
	}
	rl.last = time.Now()
	rl.enabled = true
}

// Wait 取一个令牌；不够就等到够。ctx 取消时立刻返回错误。
func (rl *rateLimiter) Wait(ctx context.Context) error {
	for {
		rl.mu.Lock()
		if !rl.enabled {
			rl.mu.Unlock()
			return nil
		}
		now := time.Now()
		rl.tokens += now.Sub(rl.last).Seconds() * rl.rate
		rl.last = now
		if rl.tokens > rl.capacity {
			rl.tokens = rl.capacity
		}
		if rl.tokens >= 1 {
			rl.tokens--
			rl.mu.Unlock()
			return nil
		}
		need := (1 - rl.tokens) / rl.rate
		rl.waited++
		rl.mu.Unlock()

		wait := time.Duration(need * float64(time.Second))
		if wait < 20*time.Millisecond {
			wait = 20 * time.Millisecond
		}
		if wait > 5*time.Second {
			wait = 5 * time.Second // 分段等待，便于及时响应 ctx 取消
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Observe 记录从上游响应头看到的限额（仅用于控制台展示，不参与限流决策）。
func (rl *rateLimiter) Observe(limitPerMin int) {
	if limitPerMin <= 0 {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.learned == 0 || limitPerMin < rl.learned {
		rl.learned = limitPerMin
	}
}

// Status 供控制台展示。
func (rl *rateLimiter) Status() map[string]interface{} {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return map[string]interface{}{
		"enabled":       rl.enabled,
		"limit_per_min": rl.perMin,
		"burst":         int(rl.capacity),
		"tokens":        int(rl.tokens),
		"learned":       rl.learned,
		"wait_count":    rl.waited,
	}
}
