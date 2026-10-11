package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FarmManager struct {
	store   *Store
	farmDir string
	dataDir string
	mu      sync.Mutex
	running bool
	cmd     *exec.Cmd
	runID   int64
	logPath string
}

func NewFarmManager(store *Store, farmDir, dataDir string) *FarmManager {
	return &FarmManager{store: store, farmDir: farmDir, dataDir: dataDir}
}

func (f *FarmManager) Start(maxAttempts int) error {
	return f.StartWith(maxAttempts, 0)
}

// StartWith 启动 farm.py。maxAttempts/concurrency <=0 时回退到 farm_config 或默认值。
func (f *FarmManager) StartWith(maxAttempts, concurrency int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running {
		return fmt.Errorf("注册机已在运行中")
	}

	cfg := f.store.GetFarmConfig()
	logDir := filepath.Join(f.dataDir, "logs")
	os.MkdirAll(logDir, 0755)

	// 优先使用本地 venv，没有就用 PATH 里的 python3。
	pythonBin := "python3"
	for _, cand := range []string{
		filepath.Join(f.farmDir, ".venv", "bin", "python3"),
		filepath.Join(f.farmDir, "venv", "bin", "python3"),
		"python3",
	} {
		if cand == "python3" {
			pythonBin = cand
			break
		}
		if _, err := os.Stat(cand); err == nil {
			pythonBin = cand
			break
		}
	}

	if maxAttempts <= 0 {
		if v, ok := cfg["MAX_ATTEMPTS"]; ok {
			maxAttempts, _ = strconv.Atoi(v)
		}
		if maxAttempts <= 0 {
			maxAttempts = 10
		}
	}

	if concurrency <= 0 {
		if v, ok := cfg["CONCURRENCY"]; ok {
			concurrency, _ = strconv.Atoi(v)
		}
		if concurrency <= 0 {
			concurrency = 1
		}
	}
	// HTTP 注册模式很轻量，并发可以比浏览器模式高。
	// FARM_MODE=browser 会回退到 Camoufox（旧路径）。
	mode := strings.ToLower(cfg["FARM_MODE"])
	if mode == "" {
		mode = strings.ToLower(os.Getenv("FARM_MODE"))
	}
	if mode == "" {
		mode = "http"
	}
	hardCap := 32
	if mode == "browser" {
		hardCap = 8 // 每个 worker 约占 Camoufox 300–600MB
	}
	if concurrency > hardCap {
		concurrency = hardCap
	}
	if concurrency < 1 {
		concurrency = 1
	}

	tmpLog := filepath.Join(logDir, fmt.Sprintf("farm-%d.log", time.Now().Unix()))
	logFile, err := os.Create(tmpLog)
	if err != nil {
		return fmt.Errorf("创建日志文件失败: %v", err)
	}

	var cmd *exec.Cmd
	if mode == "browser" {
		cmd = exec.Command("xvfb-run", "--auto-servernum", pythonBin, "farm.py")
	} else {
		// 纯 HTTP 注册（默认路径）—— 不需要浏览器 / xvfb
		cmd = exec.Command(pythonBin, "farm_http.py")
	}
	cmd.Dir = f.farmDir

	env := os.Environ()
	for k, v := range cfg {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	// 显式覆盖放在最后，确保优先级高于 cfg
	env = append(env, fmt.Sprintf("MAX_ATTEMPTS=%d", maxAttempts))
	env = append(env, fmt.Sprintf("CONCURRENCY=%d", concurrency))
	env = append(env, "PYTHONUNBUFFERED=1")
	env = append(env, fmt.Sprintf("RESULTS_FILE=%s", filepath.Join(f.farmDir, "results.json")))
	env = append(env, "FARM_MODE="+mode)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("启动注册机失败: %v", err)
	}

	f.cmd = cmd
	f.running = true
	f.logPath = tmpLog
	f.runID = f.store.StartFarmRun(cmd.Process.Pid, maxAttempts, tmpLog)

	log.Printf("注册机: 已启动 PID=%d runID=%d 尝试次数=%d 并发=%d 日志=%s",
		cmd.Process.Pid, f.runID, maxAttempts, concurrency, tmpLog)

	go func() {
		err := cmd.Wait()
		logFile.Close()
		status := "success"
		if err != nil {
			status = "failed"
		}
		// 结果只导入到独立的 router.db
		newAccounts := 0
		resultsPath := filepath.Join(f.farmDir, "results.json")
		if count, err := importResultsJSON(f.store, resultsPath); err == nil {
			newAccounts = count
			log.Printf("注册机: 已从 %s 导入 %d 个账号 → router.db", resultsPath, count)
		} else {
			log.Printf("注册机: 自动导入出错: %v", err)
		}
		f.store.EndFarmRun(f.runID, status, newAccounts)
		f.mu.Lock()
		f.running = false
		f.cmd = nil
		f.mu.Unlock()
		log.Printf("注册机: 运行结束 状态=%s 新增账号=%d", status, newAccounts)
	}()

	return nil
}

func (f *FarmManager) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running || f.cmd == nil {
		return fmt.Errorf("注册机未在运行中")
	}
	if err := f.cmd.Process.Kill(); err != nil {
		return err
	}
	return nil
}

func (f *FarmManager) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *FarmManager) GetLogPath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logPath
}

func (f *FarmManager) GetLogTail(n int) string {
	f.mu.Lock()
	logPath := f.logPath
	f.mu.Unlock()
	if logPath == "" {
		// 回退到最近一次运行的日志
		runs := f.store.GetFarmRuns(1)
		if len(runs) > 0 {
			logPath = runs[0].LogPath
		}
	}
	if logPath == "" {
		return ""
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func importResultsJSON(store *Store, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var results []struct {
		Email     string `json:"email"`
		APIKey    string `json:"api_key"`
		Password  string `json:"password"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &results); err != nil {
		return 0, err
	}
	count := 0
	for _, r := range results {
		if r.Email == "" || r.APIKey == "" {
			continue
		}
		// 密码一并存下，用于后续登录 console 查计划与余额
		if err := store.AddAccountFull(r.Email, r.APIKey, r.Password, "注册机"); err == nil {
			count++
		}
	}
	os.WriteFile(path, []byte("[]"), 0644)
	return count, nil
}
