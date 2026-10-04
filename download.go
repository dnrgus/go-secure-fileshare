package main

import (
	"errors"
	"log"
	"net/http"
)

// handleDownload: GET /f/{id}  또는  GET /f/{id}/{name}
// {name} 은 무시한다 (curl -O 가 올바른 파일명으로 저장하도록 URL 에만 붙여 둔 장식).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Printf("메타 읽기 오류: %v", err)
		}
		// 잘못된 ID / 없음을 구분하지 않고 똑같이 404 (존재 여부 탐색 방지)
		fail(w, r, http.StatusNotFound, "파일을 찾을 수 없습니다")
		return
	}
	f, err := s.store.Open(m.ID)
	if err != nil {
		fail(w, r, http.StatusNotFound, "파일을 찾을 수 없습니다")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+m.Name+`"`)

	// ServeContent 가 Range / HEAD / If-Modified-Since 를 처리한다.
	http.ServeContent(w, r, "", m.CreatedAt, f)
}
