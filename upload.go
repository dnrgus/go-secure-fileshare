package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// handleUploadForm: POST /upload (multipart/form-data, 필드명 "file")
func (s *Server) handleUploadForm(w http.ResponseWriter, r *http.Request) {
	// ParseMultipartForm 대신 스트리밍 리더를 써서 파일 전체를 메모리/자동 임시파일에 올리지 않는다.
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, r, http.StatusBadRequest, `multipart/form-data 형식이 필요합니다 (예: curl -F "file=@경로")`)
		return
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(w, r, http.StatusBadRequest, "업로드 데이터를 읽는 중 오류가 발생했습니다")
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		m, err := s.ingest(part, part.FileName())
		_ = part.Close()
		if err != nil {
			s.respondErr(w, r, err)
			return
		}
		s.respondUploaded(w, r, m)
		return
	}
	fail(w, r, http.StatusBadRequest, `"file" 필드에 파일이 없습니다`)
}

// handleUploadPut: PUT /upload/{name}  →  curl -T a.png http://host/upload/
func (s *Server) handleUploadPut(w http.ResponseWriter, r *http.Request) {
	m, err := s.ingest(r.Body, r.PathValue("name"))
	if err != nil {
		s.respondErr(w, r, err)
		return
	}
	s.respondUploaded(w, r, m)
}

// ingest 는 업로드 본문을 임시 파일로 스트리밍 저장한 뒤 랜덤 ID 로 확정한다.
// 사용자 입력(파일명)은 경로에 전혀 쓰이지 않는다.
func (s *Server) ingest(src io.Reader, rawName string) (*Meta, error) {
	name := path.Base(strings.ReplaceAll(rawName, `\`, "/"))
	if name == "" || name == "." || name == "/" {
		return nil, errors.New("invalid filename")
	}

	tmp, err := s.store.CreateTemp()
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath) // 실패한 업로드는 흔적을 남기지 않는다
		}
	}()

	size, err := io.Copy(tmp, src)
	if err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	m := &Meta{ID: id, Name: name, Size: size, CreatedAt: time.Now().UTC()}
	if err := s.store.Commit(m, tmpPath); err != nil {
		return nil, err
	}
	committed = true
	return m, nil
}

func (s *Server) respondErr(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("업로드 처리 오류: %v", err)
	fail(w, r, http.StatusBadRequest, "업로드에 실패했습니다")
}

func (s *Server) respondUploaded(w http.ResponseWriter, r *http.Request, m *Meta) {
	link := s.baseURL(r) + "/f/" + m.ID + "/" + url.PathEscape(m.Name)
	// curl 용 평문 응답: 첫 줄이 곧 공유 링크이므로 `| head -n1` 로 스크립트에서 쓰기 쉽다.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "%s\n", link)
}
