package main

import (
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// allowedTypes: 허용 확장자 → 파일 "내용"으로 판별한 MIME 이 이 목록 안에 있어야 통과.
// http.DetectContentType 은 앞 512바이트의 매직 넘버를 보고 판별하므로
// 클라이언트가 보낸 Content-Type 헤더나 확장자 위조를 신뢰하지 않는다.
var allowedTypes = map[string][]string{
	".txt":  {"text/plain"},
	".md":   {"text/plain"},
	".csv":  {"text/plain"},
	".json": {"text/plain"},
	".log":  {"text/plain"},
	".pdf":  {"application/pdf"},
	".png":  {"image/png"},
	".jpg":  {"image/jpeg"},
	".jpeg": {"image/jpeg"},
	".gif":  {"image/gif"},
	".webp": {"image/webp"},
	".zip":  {"application/zip"},
	".docx": {"application/zip"},
	".xlsx": {"application/zip"},
	".pptx": {"application/zip"},
}

// httpError 는 클라이언트에게 그대로 보여줘도 되는 오류(상태 코드 + 메시지)다.
type httpError struct {
	Status int
	Msg    string
}

func (e *httpError) Error() string { return e.Msg }

func newErr(status int, format string, args ...any) *httpError {
	return &httpError{Status: status, Msg: fmt.Sprintf(format, args...)}
}

func allowedExtensions() []string {
	exts := make([]string, 0, len(allowedTypes))
	for e := range allowedTypes {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	return exts
}

// validateFilename 은 업로드된 원본 파일명을 검증/정규화하고 (정제된 이름, 소문자 확장자)를 돌려준다.
//
// 중요: 이 이름은 "표시용"일 뿐이다. 디스크 경로는 항상 서버가 만든 랜덤 ID 로만 만들기 때문에
// 파일명 기반 경로 조작(../, 절대경로, NUL 바이트)은 저장 단계에서 애초에 불가능하다.
// 여기서의 검증은 그 위에 얹는 2중 방어(defense in depth)다.
func validateFilename(raw string) (name, ext string, herr *httpError) {
	if raw == "" {
		return "", "", newErr(http.StatusBadRequest, "파일 이름이 비어 있습니다")
	}
	if !utf8.ValidString(raw) {
		return "", "", newErr(http.StatusBadRequest, "파일 이름이 올바른 UTF-8 이 아닙니다")
	}
	if len(raw) > 255 {
		return "", "", newErr(http.StatusBadRequest, "파일 이름이 너무 깁니다")
	}
	for _, r := range raw {
		// NUL, 개행 등(헤더 인젝션/잘림 공격 방지)
		if unicode.IsControl(r) {
			return "", "", newErr(http.StatusBadRequest, "파일 이름에 제어 문자를 쓸 수 없습니다")
		}
	}

	// 윈도우식 구분자도 슬래시로 통일한 뒤 마지막 요소만 취한다 (경로 성분 제거).
	name = path.Base(strings.ReplaceAll(raw, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, ". ") // 선행 점(.htaccess), 후행 점/공백 제거

	if name == "" || strings.Contains(name, "/") {
		return "", "", newErr(http.StatusBadRequest, "올바르지 않은 파일 이름입니다")
	}
	if utf8.RuneCountInString(name) > 100 {
		return "", "", newErr(http.StatusBadRequest, "파일 이름은 100자 이하여야 합니다")
	}

	ext = strings.ToLower(path.Ext(name))
	if ext == "" {
		return "", "", newErr(http.StatusUnsupportedMediaType, "확장자가 없는 파일은 업로드할 수 없습니다")
	}
	if _, ok := allowedTypes[ext]; !ok {
		return "", "", newErr(http.StatusUnsupportedMediaType,
			"허용되지 않는 확장자입니다: %s (허용: %s)", ext, strings.Join(allowedExtensions(), " "))
	}
	return name, ext, nil
}

// checkMIME 은 파일 앞부분(최대 512바이트)으로 실제 형식을 판별해 확장자와 일치하는지 확인한다.
// 통과하면 판별된 전체 MIME 문자열(예: "text/plain; charset=utf-8")을 돌려준다.
func checkMIME(ext string, head []byte) (string, error) {
	allowed, ok := allowedTypes[ext]
	if !ok {
		return "", fmt.Errorf("허용되지 않는 확장자입니다: %s", ext)
	}
	sniffed := http.DetectContentType(head)
	base, _, _ := strings.Cut(sniffed, ";")
	base = strings.TrimSpace(base)
	for _, a := range allowed {
		if a == base {
			return sniffed, nil
		}
	}
	return "", fmt.Errorf("파일 내용(%s)이 확장자(%s)와 일치하지 않습니다", base, ext)
}
