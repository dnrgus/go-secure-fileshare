package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// multipart 경계/헤더 등 오버헤드를 감안한 요청 본문 여유분
const multipartOverhead = 1 << 20

type Server struct {
	cfg     Config
	store   *Store
	limiter *limiter
	web     fs.FS
	index   []byte
}

func NewServer(cfg Config, store *Store, web fs.FS) *Server {
	idx, err := fs.ReadFile(web, "index.html")
	if err != nil {
		log.Fatalf("index.html 로드 실패: %v", err)
	}
	return &Server{cfg: cfg, store: store, limiter: newLimiter(cfg.RatePerMin), web: web, index: idx}
}

// Routes 는 Go 1.22 의 메서드+경로 패턴 라우팅을 사용한다.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /static/{file}", s.handleStatic)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /api/limits", s.handleLimits)

	mux.HandleFunc("POST /upload", s.handleUploadForm)      // curl -F "file=@a.png" URL/upload
	mux.HandleFunc("PUT /upload/{name}", s.handleUploadPut) // curl -T a.png URL/upload/

	mux.HandleFunc("GET /f/{id}", s.handleDownload)
	mux.HandleFunc("GET /f/{id}/{name}", s.handleDownload) // name 은 장식용 (curl -O 가 파일명을 얻도록)
	mux.HandleFunc("DELETE /f/{id}", s.handleDelete)

	return s.recoverMW(s.logMW(s.headersMW(mux)))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.index)
}

// handleStatic 은 허용 목록에 있는 두 파일만 서빙한다 (디렉터리 리스팅/임의 파일 노출 방지).
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name != "app.js" && name != "style.css" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeFileFS(w, r, s.web, name)
}

func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"max_upload_bytes": s.cfg.MaxUpload,
		"ttl_hours":        int(s.cfg.TTL / time.Hour),
		"allowed":          allowedExtensions(),
	})
}

// ---- 미들웨어 ----

const pageCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func (s *Server) headersMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", pageCSP) // 다운로드 핸들러는 더 엄격한 값으로 덮어쓴다
		if r.TLS != nil || (s.cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Unwrap() http.ResponseWriter { return sr.ResponseWriter }

func (s *Server) logMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %s %d %s", clientIP(r, s.cfg.TrustProxy), r.Method, redactPath(r.URL.Path), rec.status, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v", rec)
				fail(w, r, http.StatusInternalServerError, "서버 내부 오류")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// redactPath: 공유 링크의 ID 는 곧 접근 권한(capability)이므로 로그에는 앞 6자만 남긴다.
func redactPath(p string) string {
	if rest, ok := strings.CutPrefix(p, "/f/"); ok {
		id, _, _ := strings.Cut(rest, "/")
		if len(id) > 6 {
			id = id[:6] + "…"
		}
		return "/f/" + id
	}
	return p
}

// ---- 응답/요청 헬퍼 ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// wantsJSON: 웹 UI 는 Accept: application/json 으로 요청하고, curl 은 기본값(*/*)이라 평문을 받는다.
func wantsJSON(r *http.Request) bool {
	return r.URL.Query().Get("format") == "json" ||
		strings.Contains(r.Header.Get("Accept"), "application/json")
}

// fail 은 오류를 클라이언트 종류(JSON/평문)에 맞게 내려준다.
func fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if wantsJSON(r) {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "error: %s\n", msg)
}

// baseURL: BASE_URL 이 있으면 그것을 쓰고, 없으면 요청의 Host 로 조립한다.
// Host 헤더는 클라이언트가 바꿀 수 있으므로 운영 환경에서는 BASE_URL 지정을 권장한다.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if s.cfg.TrustProxy {
		if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host
}

// clientIP: 프록시를 신뢰할 때만 X-Forwarded-For 를 본다.
// 신뢰하는 프록시가 "오른쪽 끝"에 덧붙인 값이 진짜 접속자이므로 마지막 항목을 쓴다 (왼쪽 값은 위조 가능).
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
