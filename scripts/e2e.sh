#!/usr/bin/env bash
# 실제 서버 프로세스를 띄우고 진짜 curl 로 업로드/공유/다운로드/삭제/보안 검증을 수행한다.
#   사용법: ./scripts/e2e.sh            (로컬에서 서버를 직접 띄워 테스트)
#           BASE=https://내도메인 ./scripts/e2e.sh   (이미 배포된 서버를 테스트: 서버 기동 생략)
set -uo pipefail
cd "$(dirname "$0")/.."

PORT=${PORT:-18080}
WORK=$(mktemp -d)
SRV=""
cleanup() { [ -n "$SRV" ] && kill "$SRV" 2>/dev/null; rm -rf "$WORK"; }
trap cleanup EXIT

if [ -z "${BASE:-}" ]; then
  BASE=http://127.0.0.1:$PORT
  go build -o "$WORK/fileshare" . || exit 1
  ADDR=127.0.0.1:$PORT DATA_DIR="$WORK/data" MAX_UPLOAD_MB=1 RATE_PER_MIN=1000 \
    "$WORK/fileshare" >"$WORK/server.log" 2>&1 &
  SRV=$!
  for _ in $(seq 1 50); do curl -fsS "$BASE/healthz" >/dev/null 2>&1 && break; sleep 0.1; done
fi
curl -fsS "$BASE/healthz" >/dev/null || { echo "서버에 접속할 수 없습니다: $BASE"; exit 1; }

pass=0; fail=0
ok()   { printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  [FAIL] %s\n' "$1"; fail=$((fail+1)); }
eq()   { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (기대: $3 / 실제: $2)"; fi; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

cd "$WORK"
echo "BASE = $BASE"

echo "== 1. curl -F 업로드 → 링크 → 다운로드"
printf 'hello fileshare\n' > hello.txt
resp=$(curl -sS -F "file=@hello.txt" "$BASE/upload")
link=$(printf '%s\n' "$resp" | head -n1)
echo "$resp" | sed 's/^/    | /'
[[ "$link" == "$BASE"/f/* ]] && ok "첫 줄이 공유 링크" || bad "링크 형식: $link"
eq "다운로드 내용 일치" "$(curl -sS "$link")" "hello fileshare"

echo "== 2. curl -T (PUT) 업로드"
printf 'put upload\n' > put.txt
link2=$(curl -sS -T put.txt "$BASE/upload/" | head -n1)
eq "PUT 업로드 후 내용 일치" "$(curl -sS "$link2")" "put upload"

echo "== 3. JSON 응답 + 해시 검증"
json=$(curl -sS -H 'Accept: application/json' -F "file=@hello.txt" "$BASE/upload")
sha=$(printf '%s' "$json" | sed -n 's/.*"sha256":"\([0-9a-f]*\)".*/\1/p')
eq "서버가 계산한 SHA-256 == sha256sum" "$sha" "$(sha256sum hello.txt | cut -d' ' -f1)"

echo "== 4. curl -OJ 로 저장 시 원래 파일명 복원"
# -J: Content-Disposition 의 filename 을 저장 이름으로 사용 (-O 만 쓰면 URL 의 %XX 가 디코딩되지 않은 채 저장되는 curl 의 특성이 있음)
mkdir -p dl && (cd dl && curl -sS -OJ "$link") && [ -f dl/hello.txt ] && ok "dl/hello.txt 로 저장됨" || bad "파일명 복원 실패: $(ls dl)"

echo "== 5. 한글/공백/작은따옴표 파일명"
printf '한글 내용\n' > '보고서 최종.txt'
printf 'quote\n' > "it's ok.txt"
klink=$(curl -sS -F "file=@보고서 최종.txt" "$BASE/upload" | head -n1)
echo "    | $klink"
eq "한글 파일 다운로드 내용 일치" "$(curl -sS "$klink")" "한글 내용"
qlink=$(curl -sS -F "file=@it's ok.txt" "$BASE/upload" | head -n1)
eq "작은따옴표 파일 다운로드 내용 일치" "$(curl -sS "$qlink")" "quote"
rm -rf dl2 && mkdir dl2 && (cd dl2 && curl -sS -OJ "$klink" && curl -sS -OJ "$qlink")
[ -f "dl2/보고서 최종.txt" ] && ok "curl -OJ → 한글 원래 이름('보고서 최종.txt')으로 저장" || bad "한글 이름 복원 실패: $(ls dl2)"
[ -f "dl2/it's ok.txt" ] && ok "curl -OJ → \"it's ok.txt\" 로 저장" || bad "따옴표 이름 복원 실패: $(ls dl2)"
# 응답 본문이 안내하는 'get:' 명령을 그대로 실행해도 동작해야 한다
getcmd=$(curl -sS -F "file=@보고서 최종.txt" "$BASE/upload" | sed -n 's/^get: *//p')
echo "    | 안내된 명령: $getcmd"
rm -rf dl3 && mkdir dl3 && (cd dl3 && eval "$getcmd" >/dev/null 2>&1) && [ -f "dl3/보고서 최종.txt" ] && ok "응답에 안내된 get: 명령이 그대로 동작" || bad "get: 명령 실패: $(ls dl3)"
eq "방향 제어(RLO) 파일명 거부" "$(code -F $'file=@hello.txt;filename=evil\xe2\x80\xaegnp.exe.txt' "$BASE/upload")" "400"

echo "== 6. 응답 헤더 (브라우저가 실행/렌더링하지 못하게)"
hdr=$(curl -sSI "$link" | tr -d '\r')
echo "$hdr" | grep -i -E '^(content-type|content-disposition|x-content-type-options|content-security-policy|cache-control):' | sed 's/^/    | /'
echo "$hdr" | grep -qi '^content-disposition: attachment' && ok "Content-Disposition: attachment" || bad "attachment 없음"
echo "$hdr" | grep -qi '^x-content-type-options: nosniff' && ok "nosniff" || bad "nosniff 없음"
echo "$hdr" | grep -qi "^content-security-policy: default-src 'none'; sandbox" && ok "다운로드 CSP sandbox" || bad "CSP 없음"

echo "== 7. 경로 조작 (파일명 위조)"
printf 'x\n' > a.txt
r=$(curl -sS -H 'Accept: application/json' -F "file=@a.txt;filename=../../etc/passwd.txt" "$BASE/upload")
eq "../../etc/passwd.txt → 저장 이름은 passwd.txt" "$(printf '%s' "$r" | sed -n 's/.*"name":"\([^"]*\)".*/\1/p')" "passwd.txt"
eq "절대경로 /etc/cron.d/x.txt → x.txt" "$(curl -sS -H 'Accept: application/json' -F 'file=@a.txt;filename=/etc/cron.d/x.txt' "$BASE/upload" | sed -n 's/.*"name":"\([^"]*\)".*/\1/p')" "x.txt"
eq "다운로드 URL 경로 조작 (..%2f) → 404" "$(code "$BASE/f/..%2f..%2fetc%2fpasswd")" "404"
eq "다운로드 URL 경로 조작 (%2e%2e) → 404" "$(code "$BASE/f/%2e%2e%2f%2e%2e%2fetc%2fpasswd")" "404"
eq "존재하지 않는 32자리 ID → 404" "$(code "$BASE/f/00000000000000000000000000000000")" "404"

echo "== 8. 확장자 검증"
printf '<html><script>alert(1)</script></html>\n' > page.html
printf '<?php system($_GET[1]); ?>\n' > shell.php
eq ".html 업로드 거부" "$(code -F 'file=@page.html' "$BASE/upload")" "415"
eq ".php 업로드 거부" "$(code -F 'file=@shell.php' "$BASE/upload")" "415"
eq "확장자 위조 x.PHP 거부" "$(code -F 'file=@a.txt;filename=x.PHP' "$BASE/upload")" "415"
eq "확장자 없음 거부" "$(code -F 'file=@a.txt;filename=Makefile' "$BASE/upload")" "415"

echo "== 9. MIME(내용) 검증 — 확장자만 바꿔서 위장"
eq "html 내용을 .txt 로 위장 → 거부" "$(code -F 'file=@page.html;filename=safe.txt' "$BASE/upload")" "415"
eq "html 내용을 .png 로 위장 → 거부" "$(code -F 'file=@page.html;filename=pic.png' "$BASE/upload")" "415"
eq "php 내용을 .jpg 로 위장 → 거부" "$(code -F 'file=@shell.php;filename=pic.jpg' "$BASE/upload")" "415"
eq "이중 확장자 shell.php.txt → 거부" "$(code -F 'file=@a.txt;filename=shell.php.txt' "$BASE/upload")" "415"
# 진짜 PNG(1x1)는 통과해야 한다
printf '\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89' > real.png
eq "정상 PNG 는 통과" "$(code -F 'file=@real.png' "$BASE/upload")" "201"
rm -rf "$WORK/chk"; mkdir "$WORK/chk"

echo "== 10. 크기 제한 (서버 MAX_UPLOAD_MB=1)"
if [ -n "$SRV" ]; then
  yes 'lorem ipsum dolor sit amet' | head -c 2000000 > big.txt
  eq "2MB 파일 multipart → 413" "$(code -F 'file=@big.txt' "$BASE/upload")" "413"
  eq "2MB 파일 PUT → 413" "$(code -T big.txt "$BASE/upload/")" "413"
  yes 'lorem ipsum dolor sit amet' | head -c 900000 > fit.txt
  eq "0.9MB 파일은 통과" "$(code -F 'file=@fit.txt' "$BASE/upload")" "201"
else
  echo "  (원격 서버 모드: 서버 설정을 알 수 없어 건너뜀)"
fi

echo "== 11. 삭제 (키 필요)"
dkey=$(printf '%s' "$resp" | sed -n "s/.*X-Delete-Key: \([0-9a-f]*\)'.*/\1/p")
id=$(printf '%s' "$link" | sed -n 's|.*/f/\([0-9a-f]\{32\}\)/.*|\1|p')
eq "키 없이 삭제 → 403" "$(code -X DELETE "$BASE/f/$id")" "403"
eq "틀린 키로 삭제 → 403" "$(code -X DELETE -H 'X-Delete-Key: nope' "$BASE/f/$id")" "403"
eq "실패한 삭제 후에도 파일 존재" "$(code "$link")" "200"
eq "올바른 키로 삭제 → 200" "$(code -X DELETE -H "X-Delete-Key: $dkey" "$BASE/f/$id")" "200"
eq "삭제 후 다운로드 → 404" "$(code "$link")" "404"

echo "== 12. 서버 디스크 상태 (로컬 모드)"
if [ -n "$SRV" ]; then
  odd=$(find "$WORK/data" -maxdepth 1 -type f -regextype posix-extended ! -regex '.*/[0-9a-f]{32}\.(bin|json)' | head)
  [ -z "$odd" ] && ok "DATA_DIR 에는 <id>.bin/<id>.json 만 존재" || bad "예상 밖의 파일: $odd"
  [ -z "$(ls -A "$WORK/data/tmp")" ] && ok "tmp 에 잔여 파일 없음 (거절된 업로드 정리됨)" || bad "tmp 잔여: $(ls "$WORK/data/tmp")"
  [ ! -e "$WORK/etc" ] && [ ! -e /tmp/passwd.txt ] && ok "작업 폴더/시스템 경로에 위조 파일 없음" || bad "위조 파일 생성됨"
  echo "    | 서버 로그 (공유 ID 는 앞 6자만 기록):"; grep -E '/f/' "$WORK/server.log" | head -3 | sed 's/^/    |   /'
fi

echo
echo "결과: PASS=$pass FAIL=$fail"
[ "$fail" -eq 0 ]
