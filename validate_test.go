package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestValidateFilename(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantName   string
		wantExt    string
		wantStatus int // 0 = 성공
	}{
		// 정상
		{"plain txt", "hello.txt", "hello.txt", ".txt", 0},
		{"uppercase ext", "REPORT.PDF", "REPORT.PDF", ".pdf", 0},
		{"dots in name", "my.report.v2.pdf", "my.report.v2.pdf", ".pdf", 0},
		{"korean", "보고서 최종.docx", "보고서 최종.docx", ".docx", 0},
		{"stem looks dangerous but is first part", "exe.pdf", "exe.pdf", ".pdf", 0},

		// 경로 조작 → 경로 성분이 제거되어 마지막 이름만 남는다
		{"unix traversal", "../../etc/passwd.txt", "passwd.txt", ".txt", 0},
		{"windows traversal", `..\..\boot.txt`, "boot.txt", ".txt", 0},
		{"absolute path", "/etc/cron.d/job.txt", "job.txt", ".txt", 0},
		{"nested path", "a/b/c/d.png", "d.png", ".png", 0},
		{"trailing spaces and dots", "note.txt. . ", "note.txt", ".txt", 0},
		{"special chars replaced", `a<b>:c"d|e?f*.txt`, "a_b__c_d_e_f_.txt", ".txt", 0},

		// 거부
		{"empty", "", "", "", http.StatusBadRequest},
		{"only dots", "....", "", "", http.StatusBadRequest},
		{"only slash", "/", "", "", http.StatusBadRequest},
		{"nul byte", "a.txt\x00.png", "", "", http.StatusBadRequest},
		{"RLO 방향 제어(확장자 위장)", "evil\u202egnp.exe.txt", "", "", http.StatusBadRequest},
		{"LRM", "a\u200e.txt", "", "", http.StatusBadRequest},
		{"newline", "a\n.txt", "", "", http.StatusBadRequest},
		{"invalid utf8", "a\xff\xfe.txt", "", "", http.StatusBadRequest},
		{"too long", strings.Repeat("a", 101) + ".txt", "", "", http.StatusBadRequest},
		{"no extension", "README", "", "", http.StatusUnsupportedMediaType},
		{"hidden file", ".htaccess", "", "", http.StatusUnsupportedMediaType},
		{"html", "index.html", "", "", http.StatusUnsupportedMediaType},
		{"php", "shell.php", "", "", http.StatusUnsupportedMediaType},
		{"php uppercase", "shell.PHP", "", "", http.StatusUnsupportedMediaType},
		{"exe", "setup.exe", "", "", http.StatusUnsupportedMediaType},
		{"svg (script 가능)", "logo.svg", "", "", http.StatusUnsupportedMediaType},
		{"ext after allowed", "a.png.php", "", "", http.StatusUnsupportedMediaType},
		{"double ext php", "shell.php.png", "", "", http.StatusUnsupportedMediaType},
		{"double ext jsp", "x.jsp.txt", "", "", http.StatusUnsupportedMediaType},
		{"double ext sh", "backup.sh.zip", "", "", http.StatusUnsupportedMediaType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, ext, herr := validateFilename(tc.in)
			if tc.wantStatus != 0 {
				if herr == nil {
					t.Fatalf("거부되어야 하는데 통과: name=%q ext=%q", name, ext)
				}
				if herr.Status != tc.wantStatus {
					t.Fatalf("status = %d, want %d (%s)", herr.Status, tc.wantStatus, herr.Msg)
				}
				return
			}
			if herr != nil {
				t.Fatalf("통과해야 하는데 거부: %d %s", herr.Status, herr.Msg)
			}
			if name != tc.wantName || ext != tc.wantExt {
				t.Fatalf("got (%q, %q), want (%q, %q)", name, ext, tc.wantName, tc.wantExt)
			}
			if strings.ContainsAny(name, `/\`) {
				t.Fatalf("정제된 이름에 경로 구분자가 남아 있음: %q", name)
			}
		})
	}
}

func TestCheckMIME(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF")
	gif := []byte("GIF89a\x01\x00\x01\x00")
	pdf := []byte("%PDF-1.7\n%âãÏÓ\n")
	zip := []byte("PK\x03\x04\x14\x00\x00\x00")
	text := []byte("그냥 평범한 텍스트입니다.\nhello world\n")
	jsonBody := []byte(`{"a": 1, "b": [1,2,3]}`)
	html := []byte("<!DOCTYPE html><html><body><script>alert(1)</script></body></html>")
	script := []byte("<script>alert(document.cookie)</script>")
	exe := []byte("MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff")
	elf := []byte("\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00")

	tests := []struct {
		name string
		ext  string
		data []byte
		ok   bool
	}{
		{"png ok", ".png", png, true},
		{"jpg ok", ".jpg", jpg, true},
		{"jpeg ok", ".jpeg", jpg, true},
		{"gif ok", ".gif", gif, true},
		{"pdf ok", ".pdf", pdf, true},
		{"zip ok", ".zip", zip, true},
		{"docx is zip", ".docx", zip, true},
		{"txt ok", ".txt", text, true},
		{"json ok", ".json", jsonBody, true},
		{"md ok", ".md", []byte("# 제목\n\n본문"), true},

		{"html as png", ".png", html, false},
		{"html as txt", ".txt", html, false},
		{"script as txt", ".txt", script, false},
		{"script as md", ".md", script, false},
		{"exe as jpg", ".jpg", exe, false},
		{"elf as pdf", ".pdf", elf, false},
		{"png as txt", ".txt", png, false},
		{"png as jpg", ".jpg", png, false},
		{"jpg as png", ".png", jpg, false},
		{"text as zip", ".zip", text, false},
		{"text as pdf", ".pdf", text, false},
		{"zip as png", ".png", zip, false},
		{"unknown ext", ".exe", exe, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkMIME(tc.ext, tc.data)
			if tc.ok && err != nil {
				t.Fatalf("통과해야 하는데 거부: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("거부되어야 하는데 통과: mime=%q", got)
			}
		})
	}
}
