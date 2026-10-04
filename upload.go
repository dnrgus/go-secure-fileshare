package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

// handleUploadForm: POST /upload (multipart/form-data, 필드명 "file")
func (s *Server) handleUploadForm(w http.ResponseWriter, r *http.Request) {
	limit := s.cfg.MaxUpload + multipartOverhead
	if r.ContentLength > limit { // 본문을 읽기 전에 빠르게 거절
		fail(w, r, http.StatusRequestEntityTooLarge, s.tooLargeMsg())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit) // 실제 읽은 바이트 수도 강제 제한 (Content-Length 위조 대비)

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
			s.respondErr(w, r, s.mapReadErr(err))
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		m, key, err := s.ingest(part, part.FileName())
		_ = part.Close()
		if err != nil {
			s.respondErr(w, r, err)
			return
		}
		s.respondUploaded(w, r, m, key)
		return
	}
	fail(w, r, http.StatusBadRequest, `"file" 필드에 파일이 없습니다`)
}

// handleUploadPut: PUT /upload/{name}  →  curl -T a.png http://host/upload/
func (s *Server) handleUploadPut(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > s.cfg.MaxUpload {
		fail(w, r, http.StatusRequestEntityTooLarge, s.tooLargeMsg())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload+1)
	m, key, err := s.ingest(r.Body, r.PathValue("name"))
	if err != nil {
		s.respondErr(w, r, err)
		return
	}
	s.respondUploaded(w, r, m, key)
}

// ingest 가 업로드 보안 파이프라인의 핵심이다.
//
//  1. 파일명 검증/정규화 + 확장자 허용 목록 (validate.go)
//  2. 앞 512바이트로 실제 MIME 판별 → 확장자와 일치해야 통과
//  3. 크기 제한을 지키며 임시 파일로 스트리밍 저장 + SHA-256 계산
//  4. 서버가 만든 랜덤 ID 로 최종 경로 확정 (사용자 입력은 경로에 전혀 쓰이지 않음)
func (s *Server) ingest(src io.Reader, rawName string) (*Meta, string, error) {
	name, ext, herr := validateFilename(rawName)
	if herr != nil {
		return nil, "", herr
	}

	tmp, err := s.store.CreateTemp()
	if err != nil {
		return nil, "", err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath) // 실패/거절된 업로드는 흔적을 남기지 않는다
		}
	}()

	lr := &io.LimitedReader{R: src, N: s.cfg.MaxUpload + 1} // 1바이트 더 읽어서 초과 여부 판정
	head := make([]byte, 512)
	n, err := io.ReadFull(lr, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, "", s.mapReadErr(err)
	}
	head = head[:n]
	if n == 0 {
		return nil, "", newErr(http.StatusBadRequest, "빈 파일은 업로드할 수 없습니다")
	}

	mimeType, err := checkMIME(ext, head)
	if err != nil {
		return nil, "", newErr(http.StatusUnsupportedMediaType, "%s", err.Error())
	}

	hasher := sha256.New()
	w := io.MultiWriter(tmp, hasher)
	if _, err := w.Write(head); err != nil {
		return nil, "", err
	}
	rest, err := io.Copy(w, lr)
	if err != nil {
		return nil, "", s.mapReadErr(err)
	}
	size := int64(n) + rest
	if size > s.cfg.MaxUpload {
		return nil, "", newErr(http.StatusRequestEntityTooLarge, "%s", s.tooLargeMsg())
	}
	if err := tmp.Close(); err != nil {
		return nil, "", err
	}

	id, err := randomHex(16)
	if err != nil {
		return nil, "", err
	}
	key, err := randomHex(16)
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	m := &Meta{
		ID:         id,
		Name:       name,
		Size:       size,
		MIME:       mimeType,
		SHA256:     hex.EncodeToString(hasher.Sum(nil)),
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.cfg.TTL),
		DeleteHash: hashKey(key),
	}
	if err := s.store.Commit(m, tmpPath); err != nil {
		if errors.Is(err, ErrQuota) {
			return nil, "", newErr(http.StatusInsufficientStorage, "서버 저장 공간이 부족합니다")
		}
		return nil, "", err
	}
	committed = true
	return m, key, nil
}

func (s *Server) tooLargeMsg() string {
	return fmt.Sprintf("파일이 너무 큽니다 (최대 %dMB)", s.cfg.MaxUpload>>20)
}

func (s *Server) mapReadErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return newErr(http.StatusRequestEntityTooLarge, "%s", s.tooLargeMsg())
	}
	log.Printf("업로드 읽기 오류: %v", err)
	return newErr(http.StatusBadRequest, "업로드 데이터를 읽는 중 오류가 발생했습니다")
}

// respondErr: httpError 는 그대로 노출하고, 그 외 내부 오류는 로그에만 남기고 일반 메시지로 가린다.
func (s *Server) respondErr(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	if errors.As(err, &he) {
		fail(w, r, he.Status, he.Msg)
		return
	}
	log.Printf("업로드 처리 오류: %v", err)
	fail(w, r, http.StatusInternalServerError, "서버 내부 오류")
}

func (s *Server) respondUploaded(w http.ResponseWriter, r *http.Request, m *Meta, key string) {
	base := s.baseURL(r)
	link := base + "/f/" + m.ID + "/" + url.PathEscape(m.Name)
	w.Header().Set("Cache-Control", "no-store")

	// curl 용 평문 응답: 첫 줄이 곧 공유 링크이므로 `| head -n1` 로 스크립트에서 쓰기 쉽다.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "%s\n", link)
	fmt.Fprintf(w, "get:     curl -OJ '%s'\n", link) // PathEscape 가 ' 를 %27 로 바꾸므로 작은따옴표로 안전하게 감쌀 수 있다
	fmt.Fprintf(w, "expires: %s\n", m.ExpiresAt.Format(time.RFC3339))
	fmt.Fprintf(w, "delete:  curl -X DELETE -H 'X-Delete-Key: %s' %s/f/%s\n", key, base, m.ID)
}
