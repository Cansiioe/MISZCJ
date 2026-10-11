package main

// 告警通知。
//
// 账号池全挂、注册机连续失败这类事，如果没人盯着控制台就完全不知道。
// 这里把关键事件推到 Webhook，支持几种常见机器人格式。
// 同一类告警带去重（默认 10 分钟只发一次），避免故障期间刷屏。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type alertKind string

const (
	alertGeneric  alertKind = "generic"  // 通用 JSON：{level,title,message,ts}
	alertWecom    alertKind = "wecom"    // 企业微信机器人
	alertDingtalk alertKind = "dingtalk" // 钉钉机器人
	alertFeishu   alertKind = "feishu"   // 飞书机器人
	alertTelegram alertKind = "telegram" // Telegram Bot
)

// Alerter 负责把事件推给 Webhook。
type Alerter struct {
	mu       sync.Mutex
	url      string
	kind     alertKind
	token    string // telegram
	chatID   string // telegram
	lastSent map[string]time.Time
	minGap   time.Duration
	client   *http.Client
	sent     int64
	failed   int64
}

func newAlerter() *Alerter {
	url := os.Getenv("ALERT_WEBHOOK_URL")
	kind := alertKind(strings.ToLower(getEnv("ALERT_WEBHOOK_TYPE", "generic")))

	// 告警默认直连，不走 MISTRAL_PROXY：企业微信/钉钉/飞书在国内直连即可，
	// 而代理通常不转发 127.0.0.1 这类本地地址（本地接收端会直接 502）。
	// 需要走代理的场景（例如 Telegram）设 ALERT_USE_PROXY=1。
	client := &http.Client{Timeout: 15 * time.Second}
	if os.Getenv("ALERT_USE_PROXY") == "1" {
		client.Transport = quotaTransport()
	}

	a := &Alerter{
		url:      url,
		kind:     kind,
		token:    os.Getenv("ALERT_TELEGRAM_TOKEN"),
		chatID:   os.Getenv("ALERT_TELEGRAM_CHAT"),
		lastSent: map[string]time.Time{},
		minGap:   10 * time.Minute,
		client:   client,
	}
	if url != "" {
		log.Printf("告警: 已配置 %s webhook（同类告警 %v 内只发一次）", kind, a.minGap)
	} else {
		log.Printf("告警: 未配置（设 ALERT_WEBHOOK_URL 可启用）")
	}
	return a
}

func (a *Alerter) Enabled() bool { return a != nil && a.url != "" }

// Send 异步推送告警。key 用于去重，同一 key 在 minGap 内只发一次。
// level 取值 info / warning / critical，主要影响通用格式的载荷。
func (a *Alerter) Send(key, level, title, message string) {
	if !a.Enabled() {
		return
	}
	a.mu.Lock()
	if last, ok := a.lastSent[key]; ok && time.Since(last) < a.minGap {
		a.mu.Unlock()
		return
	}
	a.lastSent[key] = time.Now()
	a.mu.Unlock()

	go func() {
		if err := a.post(level, title, message); err != nil {
			a.mu.Lock()
			a.failed++
			a.mu.Unlock()
			log.Printf("告警发送失败: %v", err)
			return
		}
		a.mu.Lock()
		a.sent++
		a.mu.Unlock()
		log.Printf("告警已发送 [%s] %s", level, title)
	}()
}

func (a *Alerter) post(level, title, message string) error {
	target := a.url
	now := time.Now().Format("2006-01-02 15:04:05")

	var payload []byte
	var err error
	switch a.kind {
	case alertWecom:
		payload, err = json.Marshal(map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"content": fmt.Sprintf("**%s**\n> %s\n> <font color=\"comment\">%s</font>", title, message, now),
			},
		})
	case alertDingtalk:
		payload, err = json.Marshal(map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": title,
				"text":  fmt.Sprintf("### %s\n\n%s\n\n> %s", title, message, now),
			},
		})
	case alertFeishu:
		payload, err = json.Marshal(map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": fmt.Sprintf("%s\n%s\n%s", title, message, now)},
		})
	case alertTelegram:
		if a.token == "" || a.chatID == "" {
			return fmt.Errorf("telegram 需要 ALERT_TELEGRAM_TOKEN 与 ALERT_TELEGRAM_CHAT")
		}
		target = fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", a.token)
		payload, err = json.Marshal(map[string]string{
			"chat_id": a.chatID,
			"text":    fmt.Sprintf("%s\n%s\n%s", title, message, now),
		})
	default:
		payload, err = json.Marshal(map[string]string{
			"level": level, "title": title, "message": message,
			"ts": time.Now().Format(time.RFC3339),
		})
	}
	if err != nil {
		return err
	}

	req, _ := http.NewRequest("POST", target, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// Status 供控制台展示。
func (a *Alerter) Status() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]interface{}{
		"enabled": a.Enabled(),
		"kind":    string(a.kind),
		"sent":    a.sent,
		"failed":  a.failed,
		"min_gap": a.minGap.String(),
	}
}
