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

// dangerousExt: 파일명 중간에 끼어 있으면 이중 확장자 공격(shell.php.png)으로 보고 거부한다.
var dangerousExt = map[string]bool{
	"php": true, "phtml": true, "php3": true, "php5": true, "phar": true,
	"jsp": true, "jspx": true, "asp": true, "aspx": true, "cgi": true,
	"exe": true, "dll": true, "so": true, "msi": true, "com": true, "scr": true,
	"bat": true, "cmd": true, "ps1": true, "vbs": true, "hta": true,
	"sh": true, "bash": true, "py": true, "pl": true, "rb": true,
	"js": true, "mjs": true, "html": true, "htm": true, "xhtml": true, "svg": true,
	"jar": true, "war": true,
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
		// NUL, 개행 등(헤더 인젝션/잘림 공격 방지) + 방향 제어 문자(확장자 위장 방지)
		if unicode.IsControl(r) || isBidiControl(r) {
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

	// 이중 확장자: "shell.php.png" → 이름 첫 조각(shell)을 제외한 중간 조각에 위험 확장자가 있으면 거부.
	stem := strings.TrimSuffix(name, path.Ext(name))
	parts := strings.Split(strings.ToLower(stem), ".")
	for _, p := range parts[1:] {
		if dangerousExt[p] {
			return "", "", newErr(http.StatusUnsupportedMediaType, "위험한 이중 확장자가 포함되어 있습니다: .%s", p)
		}
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

// isBidiControl: 방향 제어 문자(RLO 등)는 "evil\u202egnp.exe" 처럼 파일명 표시를 뒤집어 확장자를 속이는 데 쓰인다.
func isBidiControl(r rune) bool {
	return r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') ||
		(r >= '\u2066' && r <= '\u2069')
}

// contentDisposition 은 다운로드 시 항상 attachment 로 내려 브라우저가 파일을 "실행/렌더링"하지 못하게 한다.
//
// 파일명은 두 가지로 모두 보낸다.
//   - filename*=UTF-8”...  (RFC 5987/6266): 브라우저는 이쪽을 우선한다.
//   - filename="..."        : curl -J 는 filename* 를 해석하지 못하고 이것만 쓴다.
//     따옴표·역슬래시·세미콜론·퍼센트·제어문자만 '_' 로 바꾸고 UTF-8 은 그대로 둔다
//     (RFC 7230 의 quoted-string 은 obs-text(0x80~0xFF)를 허용한다).
//     헤더 인젝션을 막는 CR/LF 등 제어문자는 업로드 단계에서 이미 거부되지만 여기서도 한 번 더 치환한다.
func contentDisposition(name string) string {
	fallback := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isBidiControl(r) || r == '"' || r == '\\' || r == ';' || r == '%' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, rfc5987Escape(name))
}

func rfc5987Escape(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte(attrChars, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
