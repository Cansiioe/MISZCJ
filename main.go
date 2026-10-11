package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

//go:embed web/index.html web/app.css web/app.js
var webFS embed.FS

var indexHTMLBytes []byte

func main() {
	port := getEnv("PORT", "7623")
	// 默认路径以当前工作目录为基准，便于整个目录直接搬移
	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd = "."
	}
	farmDir := getEnv("FARM_DIR", cwd)
	dataDir := getEnv("DATA_DIR", filepath.Join(cwd, "data"))

	os.MkdirAll(dataDir, 0755)
	os.MkdirAll(filepath.Join(dataDir, "logs"), 0755)

	// 载入内嵌的 index.html
	var err error
	indexHTMLBytes, err = webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("读取内嵌 index.html 失败: %v", err)
	}

	// 初始化数据库
	dbPath := filepath.Join(dataDir, "router.db")
	store, err := NewStore(dbPath)
	if err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	// 日志保留策略：启动时清一次，之后每 6 小时一次。
	// 默认保留 30 天；LOG_RETENTION_DAYS=0 表示永久保留。
	retentionDays := 30
	if v := os.Getenv("LOG_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			retentionDays = n
		}
	}
	go func() {
		if retentionDays <= 0 {
			log.Printf("日志保留: 已关闭自动清理，审计记录将永久保存")
			return
		}
		if n := store.CleanupLogs(retentionDays); n > 0 {
			log.Printf("日志清理: 删除 %d 条超过 %d 天的记录", n, retentionDays)
		}
		for {
			time.Sleep(6 * time.Hour)
			if n := store.CleanupLogs(retentionDays); n > 0 {
				log.Printf("日志清理: 删除 %d 条超过 %d 天的记录", n, retentionDays)
			}
		}
	}()

	// 初始化代理
	proxy := NewProxy(store)
	// 模型白名单（可选）：只放行免费层实测可用的模型
	modelsFile := getEnv("MODELS_FILE", filepath.Join(cwd, "config", "models.available.json"))
	proxy.SetModelFilter(loadModelFilter(modelsFile))
	proxy.Reload()

	// 失效账号自动恢复（可选）
	autoReviveMin := 0
	if v := os.Getenv("AUTO_REVIVE_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			autoReviveMin = n
		}
	}
	startAutoRevive(store, proxy, autoReviveMin)

	// 初始化注册机管理器
	farm := NewFarmManager(store, farmDir, dataDir)

	// 初始化管理后台
	admin := NewAdminHandler(store, proxy, farm)

	// 路由注册
	mux := http.NewServeMux()

	// OpenAI 兼容接口
	mux.HandleFunc("/v1/", proxy.ServeHTTP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "ok")
	})

	// 静态资源（CSS / JS）
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if name != "app.css" && name != "app.js" {
			http.NotFound(w, r)
			return
		}
		data, err := webFS.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(data)
	})

	// 管理后台
	mux.HandleFunc("/admin/", admin.ServeHTTP)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin/", 302)
			return
		}
		http.NotFound(w, r)
	})

	addr := ":" + port
	log.Printf("Mistral 路由已启动，监听 %s（farmDir=%s）", addr, farmDir)
	if err := http.ListenAndServe(addr, recoverMiddleware(mux)); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// recoverMiddleware 兜住 handler 里的 panic。
// 没有它的话，任何一个空指针都会把连接直接掐断，客户端只看到 EOF，日志里也难定位。
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic 已捕获 [%s %s]: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"message": "服务内部错误，请查看服务端日志",
						"code":    "internal_error",
					},
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getEnvInt 读整数环境变量，解析失败或未设置时返回 fallback。
func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
