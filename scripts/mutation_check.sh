#!/usr/bin/env bash
# 방어 코드를 하나씩 일부러 망가뜨린(변이) 복사본에서 `go test` 를 돌려
# "테스트가 정말 그 방어를 검증하고 있는가"를 확인한다.
#   KILLED   : 테스트가 실패함 = 테스트가 그 방어를 지키고 있음 (좋음)
#   SURVIVED : 방어를 없애도 테스트가 통과함 = 테스트 구멍 (또는 중복 방어)
#   INVALID  : 변이 때문에 빌드가 깨짐 (변이 식을 고쳐야 함)
set -uo pipefail
cd "$(dirname "$0")/.."
SRC=$PWD
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

killed=0; survived=0; invalid=0

mutate() { # $1=설명  $2=파일  $3=sed 식
  local desc=$1 file=$2 expr=$3
  rm -rf "$WORK/m" && cp -r "$SRC" "$WORK/m" && cd "$WORK/m" || exit 1
  sed -i "$expr" "$file"
  if cmp -s "$file" "$SRC/$file"; then
    echo "[INVALID ] $desc (패치가 적용되지 않음)"; invalid=$((invalid+1)); cd "$SRC"; return
  fi
  if ! go build ./... >/dev/null 2>&1; then
    echo "[INVALID ] $desc (빌드 실패)"; invalid=$((invalid+1)); cd "$SRC"; return
  fi
  if go test -count=1 ./... >/dev/null 2>&1; then
    echo "[SURVIVED] $desc"; survived=$((survived+1))
  else
    echo "[KILLED  ] $desc"; killed=$((killed+1))
  fi
  cd "$SRC"
}

# --- 파일명/확장자/MIME ---
mutate "MIME 내용 검사 우회"                validate.go 's|if a == base {|if a == base \|\| true {|'
mutate "확장자 허용 목록 우회"              validate.go 's|if _, ok := allowedTypes\[ext\]; !ok {|if _, ok := allowedTypes[ext]; !ok \&\& false {|'
mutate "이중 확장자 검사 제거"              validate.go 's|if dangerousExt\[p\] {|if false {|'
mutate "경로 성분 제거(path.Base) 제거"     validate.go 's|name = path.Base(strings.ReplaceAll(raw, `\\`, "/"))|name = raw|'
mutate "제어문자(NUL/개행 등) 검사 제거"    validate.go 's|if unicode.IsControl(r) \|\| isBidiControl(r) {|if isBidiControl(r) {|'
mutate "방향 제어(RLO) 문자 검사 제거"      validate.go 's|if unicode.IsControl(r) \|\| isBidiControl(r) {|if unicode.IsControl(r) {|'
mutate "Content-Disposition attachment 제거" validate.go 's|attachment; filename|inline; filename|'
# --- 저장/경로/수명 ---
mutate "파일 ID 정규식 검증 전부 제거"      store.go    's|if !idPattern.MatchString(id) {|if false {|'
mutate "만료 검사 제거(Get)"                store.go    '0,/if m.Expired(now) {/s//if false {/'
mutate "저장 용량 한도 제거"                store.go    's|if s.used+m.Size > s.maxTotal {|if false {|'
mutate "실패 시 임시파일 삭제 제거"         upload.go   's|if !committed {|if !committed \&\& false {|'
# --- 접근 제어/남용 방지 ---
mutate "삭제 키 검사 우회"                  download.go 's|if !m.CheckDeleteKey(r.Header.Get("X-Delete-Key")) {|if !m.CheckDeleteKey(r.Header.Get("X-Delete-Key")) \&\& false {|'
mutate "Rate limit 제거"                    ratelimit.go 's|if v.tokens < 1 {|if false {|'
mutate "다운로드 CSP sandbox 제거"          download.go 's|default-src '"'"'none'"'"'; sandbox|default-src '"'"'none'"'"'|'
mutate "nosniff 헤더 제거"                  server.go   's|h.Set("X-Content-Type-Options", "nosniff")||'

echo
echo "결과: KILLED=$killed SURVIVED=$survived INVALID=$invalid"
[ "$survived" -eq 0 ] && [ "$invalid" -eq 0 ]
