package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed web/index.html
var webFS embed.FS

var indexHTMLBytes []byte

func main() {
	port := getEnv("PORT", "7623")
	// Defaults are relative to current working directory so this repo is portable.
	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd = "."
	}
	farmDir := getEnv("FARM_DIR", cwd)
	dataDir := getEnv("DATA_DIR", filepath.Join(cwd, "data"))

	os.MkdirAll(dataDir, 0755)
	os.MkdirAll(filepath.Join(dataDir, "logs"), 0755)

	// Load embedded index.html
	var err error
	indexHTMLBytes, err = webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("Failed to read embedded index.html: %v", err)
	}

	// Init DB
	dbPath := filepath.Join(dataDir, "router.db")
	store, err := NewStore(dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}

	// Init proxy
	proxy := NewProxy(store)
	proxy.Reload()

	// Init farm manager
	farm := NewFarmManager(store, farmDir, dataDir)

	// Init admin handler
	admin := NewAdminHandler(store, proxy, farm)

	// Routes
	mux := http.NewServeMux()

	// OpenAI-compatible API
	mux.HandleFunc("/v1/", proxy.ServeHTTP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "ok")
	})

	// Admin
	mux.HandleFunc("/admin/", admin.ServeHTTP)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin/", 302)
			return
		}
		http.NotFound(w, r)
	})

	addr := ":" + port
	log.Printf("Mistral Router listening on %s (farmDir=%s)", addr, farmDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
