package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Config 는 환경변수로 주입되는 서버 설정이다.
type Config struct {
	Addr      string        // 리슨 주소 (기본 :8080)
	DataDir   string        // 업로드 파일 저장 디렉터리
	BaseURL   string        // 공유 링크의 기준 URL (운영에서는 반드시 지정 권장)
	MaxUpload int64         // 파일 1개 최대 크기(바이트)
	MaxTotal  int64         // 저장소 전체 최대 용량(바이트)
	TTL       time.Duration // 파일 보관 기간
}

func loadConfig() Config {
	return Config{
		Addr:      env("ADDR", ":8080"),
		DataDir:   env("DATA_DIR", "./data"),
		BaseURL:   strings.TrimRight(env("BASE_URL", ""), "/"),
		MaxUpload: envInt("MAX_UPLOAD_MB", 20) << 20,
		MaxTotal:  envInt("MAX_TOTAL_MB", 1024) << 20,
		TTL:       time.Duration(envInt("TTL_HOURS", 24)) * time.Hour,
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

	store, err := NewStore(cfg.DataDir, cfg.MaxTotal)
	if err != nil {
		log.Fatalf("저장소 초기화 실패: %v", err)
	}
	srv := NewServer(cfg, store)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go store.CleanupLoop(ctx, 10*time.Minute)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second, // Slowloris 완화
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Printf("FileShare 시작: addr=%s data=%s maxUpload=%dMB ttl=%s",
		cfg.Addr, cfg.DataDir, cfg.MaxUpload>>20, cfg.TTL)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Println("서버 종료")
}
