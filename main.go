package main

import (
	"log"
	"net/http"
	"os"
	"strings"
)

// Config 는 환경변수로 주입되는 서버 설정이다.
type Config struct {
	Addr    string // 리슨 주소 (기본 :8080)
	DataDir string // 업로드 파일 저장 디렉터리
	BaseURL string // 공유 링크의 기준 URL (운영에서는 반드시 지정 권장)
}

func loadConfig() Config {
	return Config{
		Addr:    env("ADDR", ":8080"),
		DataDir: env("DATA_DIR", "./data"),
		BaseURL: strings.TrimRight(env("BASE_URL", ""), "/"),
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

	store, err := NewStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("저장소 초기화 실패: %v", err)
	}
	srv := NewServer(cfg, store)

	log.Printf("FileShare 시작: addr=%s data=%s", cfg.Addr, cfg.DataDir)
	log.Fatal(http.ListenAndServe(cfg.Addr, srv.Routes()))
}
