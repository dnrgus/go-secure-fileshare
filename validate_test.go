package main

import "testing"

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
