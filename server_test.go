package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	pngData = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0}, 64)...)
	jpgData = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), bytes.Repeat([]byte{0}, 64)...)
	pdfData = []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n")
	zipData = append([]byte("PK\x03\x04\x14\x00\x00\x00"), bytes.Repeat([]byte{0}, 64)...)
)

var linkRe = regexp.MustCompile(`/f/([0-9a-f]{32})/`)

func newTestServer(t *testing.T, mutate func(*Config)) (*httptest.Server, *Server, string) {
	t.Helper()
	// DATA_DIR 를 임시 루트의 하위 폴더로 둔다. 루트에는 "바깥 파일"(미끼)을 둘 수 있어
	// 경로 조작이 DATA_DIR 밖으로 새는지 검증할 수 있다.
	dir := filepath.Join(t.TempDir(), "data")
	cfg := Config{
		Addr:       ":0",
		DataDir:    dir,
		MaxUpload:  1 << 20,
		MaxTotal:   64 << 20,
		TTL:        time.Hour,
		RatePerMin: 1000,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	store, err := NewStore(cfg.DataDir, cfg.MaxTotal)
	if err != nil {
		t.Fatal(err)
	}
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg, store, web)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, srv, dir
}

func multipartBody(t *testing.T, field, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func upload(t *testing.T, ts *httptest.Server, filename string, content []byte, headers map[string]string) (*http.Response, string) {
	t.Helper()
	body, ctype := multipartBody(t, "file", filename, content)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/upload", body)
	req.Header.Set("Content-Type", ctype)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	return do(t, req)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// dataFiles 는 DATA_DIR 최상위의 파일 이름 목록(tmp 폴더 제외)을 돌려준다.
func dataFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() == "tmp" {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

func TestUploadDownloadRoundTrip(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)
	content := []byte("안녕하세요 hello\n")

	resp, body := upload(t, ts, "인사말.txt", content, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	link := firstLine(body)
	if !strings.HasPrefix(link, ts.URL+"/f/") {
		t.Fatalf("링크 형식이 이상함: %q", link)
	}
	if !strings.Contains(body, "expires:") || !strings.Contains(body, "X-Delete-Key") {
		t.Fatalf("평문 응답에 만료/삭제 안내가 없음: %q", body)
	}

	resp, got := get(t, link)
	if resp.StatusCode != http.StatusOK || got != string(content) {
		t.Fatalf("다운로드 불일치: status=%d body=%q", resp.StatusCode, got)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "filename*=UTF-8''%EC%9D%B8%EC%82%AC%EB%A7%90.txt") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff 헤더 없음")
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Error("다운로드 CSP 에 sandbox 없음")
	}

	// 파일명 없이 ID 만으로도 접근 가능
	id := linkRe.FindStringSubmatch(link)[1]
	if resp, got := get(t, ts.URL+"/f/"+id); resp.StatusCode != 200 || got != string(content) {
		t.Errorf("ID 전용 URL 실패: %d", resp.StatusCode)
	}
}

func TestUploadJSON(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)
	resp, body := upload(t, ts, "a.png", pngData, map[string]string{"Accept": "application/json"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var r uploadResp
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.Name != "a.png" || r.Size != int64(len(pngData)) || r.MIME != "image/png" || len(r.SHA256) != 64 || len(r.DeleteKey) != 32 || r.URL == "" {
		t.Fatalf("JSON 응답 이상: %+v", r)
	}
}

func TestPutUpload(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/upload/notes.txt", strings.NewReader("curl -T 로 올린 내용"))
	resp, body := do(t, req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if _, got := get(t, firstLine(body)); got != "curl -T 로 올린 내용" {
		t.Fatalf("다운로드 내용 불일치: %q", got)
	}

	// PUT 도 같은 검증을 거쳐야 한다
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/upload/evil.php", strings.NewReader("<?php system($_GET[1]); ?>"))
	if resp, _ := do(t, req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("PUT evil.php status = %d, want 415", resp.StatusCode)
	}
}

func TestAllowedTypesAccepted(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)
	cases := []struct {
		name string
		data []byte
	}{
		{"a.png", pngData}, {"a.jpg", jpgData}, {"a.jpeg", jpgData}, {"a.pdf", pdfData},
		{"a.zip", zipData}, {"a.docx", zipData}, {"a.txt", []byte("text")}, {"a.json", []byte(`{"k":1}`)},
		{"a.csv", []byte("a,b\n1,2\n")}, {"a.md", []byte("# hi")},
	}
	for _, c := range cases {
		resp, body := upload(t, ts, c.name, c.data, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("%s: status = %d body=%s", c.name, resp.StatusCode, body)
		}
	}
}

func TestPathTraversalFilenames(t *testing.T) {
	ts, _, dir := newTestServer(t, nil)
	cases := map[string]string{
		"../../etc/passwd.txt":    "passwd.txt",
		`..\..\windows\win.txt`:   "win.txt",
		"/etc/cron.d/backdoor.md": "backdoor.md",
		"a/b/c/../../d.txt":       "d.txt",
	}
	for in, wantName := range cases {
		resp, body := upload(t, ts, in, []byte("harmless"), map[string]string{"Accept": "application/json"})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%q: status = %d body=%s", in, resp.StatusCode, body)
		}
		var r uploadResp
		json.Unmarshal([]byte(body), &r)
		if r.Name != wantName {
			t.Errorf("%q → 저장된 이름 %q, want %q", in, r.Name, wantName)
		}
	}

	// 디스크에는 서버가 만든 <id>.bin / <id>.json 외에 아무것도 없어야 한다.
	idFile := regexp.MustCompile(`^[0-9a-f]{32}\.(bin|json)$`)
	for _, name := range dataFiles(t, dir) {
		if !idFile.MatchString(name) {
			t.Errorf("DATA_DIR 에 예상 밖의 파일: %q", name)
		}
	}
	// DATA_DIR 바깥(부모 디렉터리)에는 아무것도 생기면 안 된다: 부모에는 "data" 폴더만 있어야 한다.
	parent, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(parent) != 1 || parent[0].Name() != "data" {
		var names []string
		for _, e := range parent {
			names = append(names, e.Name())
		}
		t.Errorf("DATA_DIR 바깥에 항목이 생김: %v", names)
	}
}

func TestRejectedUploads(t *testing.T) {
	ts, _, dir := newTestServer(t, nil)
	html := []byte("<!DOCTYPE html><html><script>alert(1)</script></html>")
	cases := []struct {
		name string
		file string
		data []byte
		want int
	}{
		{"html 확장자", "x.html", html, 415},
		{"php 확장자", "x.php", []byte("<?php ?>"), 415},
		{"php 대문자", "x.PHP", []byte("<?php ?>"), 415},
		{"exe", "x.exe", []byte("MZ\x90\x00"), 415},
		{"svg", "x.svg", []byte("<svg onload=alert(1)>"), 415},
		{"확장자 없음", "Makefile", []byte("all:"), 415},
		{"숨김 파일", ".htaccess", []byte("x"), 415},
		{"이중 확장자", "shell.php.png", pngData, 415},
		{"png 로 위장한 html", "evil.png", html, 415},
		{"txt 로 위장한 html", "evil.txt", html, 415},
		{"txt 로 위장한 script", "evil.txt", []byte("<script>alert(1)</script>"), 415},
		{"pdf 로 위장한 exe", "evil.pdf", []byte("MZ\x90\x00\x03\x00\x00\x00"), 415},
		{"jpg 로 위장한 png", "a.jpg", pngData, 415},
		{"zip 로 위장한 텍스트", "a.zip", []byte("not a zip"), 415},
		{"NUL 바이트 파일명", "a.txt\x00.png", []byte("x"), 400},
		{"빈 파일", "empty.txt", []byte{}, 400},
	}
	for _, c := range cases {
		resp, body := upload(t, ts, c.file, c.data, nil)
		if resp.StatusCode != c.want {
			t.Errorf("%s: status = %d, want %d (body=%q)", c.name, resp.StatusCode, c.want, body)
		}
	}
	if files := dataFiles(t, dir); len(files) != 0 {
		t.Errorf("거절된 업로드가 디스크에 남음: %v", files)
	}
	if tmp, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(tmp) != 0 {
		t.Errorf("tmp 에 임시 파일이 남음: %d개", len(tmp))
	}
}

func TestMissingFileField(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)
	body, ctype := multipartBody(t, "wrongname", "a.txt", []byte("x"))
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/upload", body)
	req.Header.Set("Content-Type", ctype)
	if resp, _ := do(t, req); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/upload", strings.NewReader("raw"))
	req.Header.Set("Content-Type", "text/plain")
	if resp, _ := do(t, req); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("non-multipart status = %d, want 400", resp.StatusCode)
	}
}

func TestTooLarge(t *testing.T) {
	ts, _, dir := newTestServer(t, func(c *Config) { c.MaxUpload = 1024 })

	big := bytes.Repeat([]byte("a"), 4096)
	if resp, body := upload(t, ts, "big.txt", big, nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("multipart: status = %d, want 413 (body=%q)", resp.StatusCode, body)
	}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/upload/big.txt", bytes.NewReader(big))
	if resp, _ := do(t, req); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("PUT: status = %d, want 413", resp.StatusCode)
	}
	// Content-Length 를 숨기는 chunked 전송으로도 우회 불가
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/upload/big.txt", io.NopCloser(bytes.NewReader(big)))
	req.ContentLength = -1
	if resp, _ := do(t, req); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked PUT: status = %d, want 413", resp.StatusCode)
	}

	// 정확히 한도와 같은 크기는 통과
	ok := bytes.Repeat([]byte("a"), 1024)
	if resp, body := upload(t, ts, "ok.txt", ok, nil); resp.StatusCode != http.StatusCreated {
		t.Errorf("경계값(1024B): status = %d body=%q", resp.StatusCode, body)
	}

	if tmp, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(tmp) != 0 {
		t.Errorf("tmp 에 임시 파일이 남음: %d개", len(tmp))
	}
}

// TestDownloadCannotEscapeDataDir: DATA_DIR 바깥에 "유효한 형식의 메타+내용" 미끼 파일을 심어 두고,
// ../ 로 그 파일을 읽어낼 수 있는지 시도한다. 단순히 "없는 경로라서 404" 인 것과 구분하기 위함이다.
func TestDownloadCannotEscapeDataDir(t *testing.T) {
	ts, _, dir := newTestServer(t, nil)
	root := filepath.Dir(dir)

	meta := Meta{
		ID: "secret", Name: "secret.txt", Size: 6, MIME: "text/plain; charset=utf-8",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), DeleteHash: hashKey("k"),
	}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(root, "secret.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.bin"), []byte("TOPSEC"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 미끼가 정말 "읽히는 위치"에 있는지 먼저 증명한다 (이 검증이 없으면 테스트가 무의미해질 수 있음).
	if _, err := os.Stat(filepath.Join(dir, "..", "secret.json")); err != nil {
		t.Fatalf("미끼 파일 위치가 잘못됨: %v", err)
	}

	for _, id := range []string{"..%2Fsecret", "%2e%2e%2fsecret", "..%5Csecret"} {
		resp, body := get(t, ts.URL+"/f/"+id)
		if resp.StatusCode != http.StatusNotFound || strings.Contains(body, "TOPSEC") {
			t.Errorf("/f/%s → %d %q (DATA_DIR 탈출!)", id, resp.StatusCode, body)
		}
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/f/"+id, nil)
		req.Header.Set("X-Delete-Key", "k")
		if resp, _ := do(t, req); resp.StatusCode != http.StatusNotFound {
			t.Errorf("DELETE /f/%s → %d, want 404", id, resp.StatusCode)
		}
	}
	for _, f := range []string{"secret.json", "secret.bin"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("DATA_DIR 바깥 파일 %s 이 삭제됨", f)
		}
	}
}

func TestDownloadInvalidIDs(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)
	ids := []string{
		"zzz",
		"..%2F..%2Fetc%2Fpasswd",
		"%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		strings.Repeat("0", 32), // 형식은 맞지만 없는 ID
		strings.Repeat("A", 32), // 대문자 hex 는 불허
		strings.Repeat("0", 31), // 길이 부족
		strings.Repeat("0", 33), // 길이 초과
		"00000000000000000000000000000000.bin",
	}
	for _, id := range ids {
		resp, _ := get(t, ts.URL+"/f/"+id)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("/f/%s → %d, want 404", id, resp.StatusCode)
		}
	}
}

func TestDelete(t *testing.T) {
	ts, _, dir := newTestServer(t, nil)
	_, body := upload(t, ts, "a.txt", []byte("bye"), map[string]string{"Accept": "application/json"})
	var r uploadResp
	json.Unmarshal([]byte(body), &r)

	del := func(key string) int {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/f/"+r.ID, nil)
		if key != "" {
			req.Header.Set("X-Delete-Key", key)
		}
		resp, _ := do(t, req)
		return resp.StatusCode
	}

	if code := del(""); code != http.StatusForbidden {
		t.Errorf("키 없음: %d, want 403", code)
	}
	if code := del("wrong"); code != http.StatusForbidden {
		t.Errorf("틀린 키: %d, want 403", code)
	}
	if code := del(r.ID); code != http.StatusForbidden { // ID 를 키로 쓸 수 없음
		t.Errorf("ID 를 키로 사용: %d, want 403", code)
	}
	if resp, _ := get(t, r.URL); resp.StatusCode != 200 {
		t.Fatal("실패한 삭제 시도 후에도 파일은 남아 있어야 함")
	}
	if code := del(r.DeleteKey); code != http.StatusOK {
		t.Errorf("올바른 키: %d, want 200", code)
	}
	if resp, _ := get(t, r.URL); resp.StatusCode != http.StatusNotFound {
		t.Errorf("삭제 후 다운로드: %d, want 404", resp.StatusCode)
	}
	if files := dataFiles(t, dir); len(files) != 0 {
		t.Errorf("삭제 후 디스크에 파일이 남음: %v", files)
	}
	if code := del(r.DeleteKey); code != http.StatusNotFound {
		t.Errorf("이중 삭제: %d, want 404", code)
	}
}

func TestExpiry(t *testing.T) {
	ts, srv, dir := newTestServer(t, func(c *Config) { c.TTL = -time.Second })
	resp, body := upload(t, ts, "a.txt", []byte("x"), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp, _ := get(t, firstLine(body)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("만료된 파일 다운로드: %d, want 404", resp.StatusCode)
	}
	srv.store.Cleanup(time.Now())
	if files := dataFiles(t, dir); len(files) != 0 {
		t.Errorf("Cleanup 후에도 만료 파일이 남음: %v", files)
	}
}

func TestRateLimit(t *testing.T) {
	ts, _, _ := newTestServer(t, func(c *Config) { c.RatePerMin = 2 })
	var codes []int
	for i := 0; i < 3; i++ {
		resp, _ := upload(t, ts, "a.txt", []byte("x"), nil)
		codes = append(codes, resp.StatusCode)
	}
	if codes[0] != 201 || codes[1] != 201 || codes[2] != http.StatusTooManyRequests {
		t.Errorf("codes = %v, want [201 201 429]", codes)
	}
}

func TestStorageQuota(t *testing.T) {
	ts, _, _ := newTestServer(t, func(c *Config) { c.MaxTotal = 100 })
	data := bytes.Repeat([]byte("a"), 60)
	if resp, _ := upload(t, ts, "a.txt", data, nil); resp.StatusCode != 201 {
		t.Fatalf("첫 업로드: %d", resp.StatusCode)
	}
	if resp, _ := upload(t, ts, "b.txt", data, nil); resp.StatusCode != http.StatusInsufficientStorage {
		t.Errorf("용량 초과 업로드: %d, want 507", resp.StatusCode)
	}
}

func TestRangeAndHead(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)
	_, body := upload(t, ts, "a.txt", []byte("0123456789"), nil)
	link := firstLine(body)

	req, _ := http.NewRequest(http.MethodGet, link, nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, got := do(t, req)
	if resp.StatusCode != http.StatusPartialContent || got != "2345" {
		t.Errorf("Range: status=%d body=%q", resp.StatusCode, got)
	}

	req, _ = http.NewRequest(http.MethodHead, link, nil)
	resp, got = do(t, req)
	if resp.StatusCode != 200 || got != "" || resp.Header.Get("Content-Length") != "10" {
		t.Errorf("HEAD: status=%d len=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Length"), got)
	}
}

func TestPagesAndHeaders(t *testing.T) {
	ts, _, _ := newTestServer(t, nil)

	resp, body := get(t, ts.URL+"/")
	if resp.StatusCode != 200 || !strings.Contains(body, "FileShare") {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP 에 %q 없음: %s", want, csp)
		}
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("보안 헤더 누락")
	}

	for path, want := range map[string]int{
		"/static/app.js":       200,
		"/static/style.css":    200,
		"/static/index.html":   404, // 허용 목록 밖
		"/static/..%2fmain.go": 404,
		"/healthz":             200,
		"/api/limits":          200,
		"/nope":                404,
	} {
		if resp, _ := get(t, ts.URL+path); resp.StatusCode != want {
			t.Errorf("GET %s → %d, want %d", path, resp.StatusCode, want)
		}
	}

	// 허용되지 않는 메서드
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/f/"+strings.Repeat("0", 32), nil)
	if resp, _ := do(t, req); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /f/id → %d, want 405", resp.StatusCode)
	}
}

func TestRedactPath(t *testing.T) {
	id := strings.Repeat("ab", 16)
	if got := redactPath("/f/" + id + "/a.txt"); strings.Contains(got, id) || !strings.HasPrefix(got, "/f/ababab") {
		t.Errorf("redactPath = %q", got)
	}
	if got := redactPath("/upload"); got != "/upload" {
		t.Errorf("redactPath(/upload) = %q", got)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:4321"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")

	if got := clientIP(r, false); got != "10.0.0.5" {
		t.Errorf("trustProxy=false: %q", got)
	}
	if got := clientIP(r, true); got != "203.0.113.9" { // 왼쪽(위조 가능) 값이 아니라 오른쪽 끝
		t.Errorf("trustProxy=true: %q", got)
	}
}
