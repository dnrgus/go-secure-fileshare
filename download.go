package main

import (
	"errors"
	"log"
	"net/http"
	"time"
)

// handleDownload: GET /f/{id}  또는  GET /f/{id}/{name}
// {name} 은 무시한다 (curl -O 가 올바른 파일명으로 저장하도록 URL 에만 붙여 둔 장식).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.Get(r.PathValue("id"), time.Now())
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			log.Printf("메타 읽기 오류: %v", err)
		}
		// 잘못된 ID / 없음 / 만료를 구분하지 않고 똑같이 404 (존재 여부 탐색 방지)
		fail(w, r, http.StatusNotFound, "파일을 찾을 수 없습니다")
		return
	}
	f, err := s.store.Open(m.ID)
	if err != nil {
		fail(w, r, http.StatusNotFound, "파일을 찾을 수 없습니다")
		return
	}
	defer f.Close()

	h := w.Header()
	h.Set("Content-Type", m.MIME) // 업로드 때 내용으로 판별해 둔 값 (허용 목록 안의 안전한 타입뿐)
	h.Set("Content-Disposition", contentDisposition(m.Name))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox") // 어떤 경우에도 스크립트 실행 불가
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")

	// ServeContent 가 Range / HEAD / If-Modified-Since 를 처리한다. name="" 이므로 확장자로 타입을 추측하지 않는다.
	http.ServeContent(w, r, "", m.CreatedAt, f)
}

// handleDelete: DELETE /f/{id} + 헤더 X-Delete-Key (쿼리스트링은 로그에 남을 수 있어 헤더로 받는다)
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.store.Get(id, time.Now())
	if err != nil {
		fail(w, r, http.StatusNotFound, "파일을 찾을 수 없습니다")
		return
	}
	if !m.CheckDeleteKey(r.Header.Get("X-Delete-Key")) {
		fail(w, r, http.StatusForbidden, "삭제 키가 올바르지 않습니다")
		return
	}
	if err := s.store.Delete(id); err != nil && !errors.Is(err, ErrNotFound) {
		log.Printf("삭제 오류: %v", err)
		fail(w, r, http.StatusInternalServerError, "서버 내부 오류")
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("deleted\n"))
}
