package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

// Config 는 환경변수로 주입되는 서버 설정이다.
type Config struct {
	Addr string // 리슨 주소 (기본 :8080)
}

func loadConfig() Config {
	return Config{
		Addr: env("ADDR", ":8080"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := loadConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })

	log.Printf("FileShare 시작: addr=%s", cfg.Addr)
	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}
