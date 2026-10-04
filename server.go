package main

import (
	"fmt"
	"net/http"
)

type Server struct {
	cfg   Config
	store *Store
}

func NewServer(cfg Config, store *Store) *Server {
	return &Server{cfg: cfg, store: store}
}

// Routes 는 Go 1.22 의 메서드+경로 패턴 라우팅을 사용한다.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })

	mux.HandleFunc("POST /upload", s.handleUploadForm)      // curl -F "file=@a.png" URL/upload
	mux.HandleFunc("PUT /upload/{name}", s.handleUploadPut) // curl -T a.png URL/upload/

	mux.HandleFunc("GET /f/{id}", s.handleDownload)
	mux.HandleFunc("GET /f/{id}/{name}", s.handleDownload) // name 은 장식용 (curl -O 가 파일명을 얻도록)

	return mux
}

// ---- 응답/요청 헬퍼 ----

func fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
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
	return scheme + "://" + r.Host
}
