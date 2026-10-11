package main

// 失效账号的自动恢复。
//
// 账号被判死的原因不都是真的死了：网络抖动、代理临时不通、上游 5xx，
// 都会让一次请求拿到 401/403，于是账号被 MarkDead 后一直闲置，得人工点「恢复」。
// 这里定期用 API Key 探一次上游，还能用就自动复活。

import (
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// probeAccountAlive 用 API Key 打一次 /v1/models，判断账号是否真的还能用。
func probeAccountAlive(apiKey string, client *http.Client) bool {
	if apiKey == "" {
		return false
	}
	req, err := http.NewRequest("GET", mistralBase+"/v1/models", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == 200
}

// AutoReviveDeadAccounts 探测所有失效账号，可用的自动复活，返回复活数量。
func (s *Store) AutoReviveDeadAccounts() int {
	dead := s.GetDeadAccounts()
	if len(dead) == 0 {
		return 0
	}

	client := &http.Client{Timeout: 20 * time.Second, Transport: quotaTransport()}
	type result struct {
		id int
		ok bool
	}
	ch := make(chan result, len(dead))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for _, acc := range dead {
		wg.Add(1)
		go func(a Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ch <- result{a.ID, probeAccountAlive(a.APIKey, client)}
		}(acc)
	}
	wg.Wait()
	close(ch)

	revived := 0
	for r := range ch {
		if r.ok {
			s.ReviveAccount(r.id)
			revived++
		}
	}
	return revived
}

// startAutoRevive 起一个后台协程定期复活账号；minutes<=0 表示关闭。
func startAutoRevive(store *Store, proxy *Proxy, minutes int) {
	if minutes <= 0 {
		log.Printf("自动恢复: 未启用（设 AUTO_REVIVE_MINUTES 可开启，例如 30）")
		return
	}
	go func() {
		interval := time.Duration(minutes) * time.Minute
		log.Printf("自动恢复: 每 %v 探测一次失效账号", interval)
		for {
			time.Sleep(interval)
			n := store.AutoReviveDeadAccounts()
			if n > 0 {
				log.Printf("自动恢复: %d 个失效账号探测可用，已重新加入轮询", n)
				proxy.Reload()
				proxy.alerter.Send("auto_revive", "info", "账号已自动恢复",
					"检测到失效账号重新可用，已恢复轮询。")
			}
		}
	}()
}
