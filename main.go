package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Config 는 환경변수로 주입되는 서버 설정이다.
type Config struct {
	Addr      string // 리슨 주소 (기본 :8080)
	DataDir   string // 업로드 파일 저장 디렉터리
	BaseURL   string // 공유 링크의 기준 URL (운영에서는 반드시 지정 권장)
	MaxUpload int64  // 파일 1개 최대 크기(바이트)
}

func loadConfig() Config {
	return Config{
		Addr:      env("ADDR", ":8080"),
		DataDir:   env("DATA_DIR", "./data"),
		BaseURL:   strings.TrimRight(env("BASE_URL", ""), "/"),
		MaxUpload: envInt("MAX_UPLOAD_MB", 20) << 20,
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		log.Fatalf("환경변수 %s 값이 정수가 아닙니다: %q", key, v)
	}
	return n
}

func main() {
	cfg := loadConfig()

	store, err := NewStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("저장소 초기화 실패: %v", err)
	}
	srv := NewServer(cfg, store)

	log.Printf("FileShare 시작: addr=%s data=%s maxUpload=%dMB", cfg.Addr, cfg.DataDir, cfg.MaxUpload>>20)
	log.Fatal(http.ListenAndServe(cfg.Addr, srv.Routes()))
}
