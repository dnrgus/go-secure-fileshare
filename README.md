# FileShare — Go 기반 파일 공유 서비스

파일을 업로드하면 **외부 공유 링크**를 만들어 주는 작은 웹 백엔드입니다.
웹 UI와 `curl` **양쪽 모두**로 업로드/다운로드할 수 있고, 업로드 시 **파일명·확장자·MIME(내용)·경로·크기** 검증을 거칩니다.

- 언어/의존성: **Go 1.22+, 표준 라이브러리만 사용** (`go.sum` 없음, 바이너리 약 6 MB 단일 파일, 웹 UI는 `go:embed` 로 내장)
- 학습 목표: Go 웹 백엔드 구조(라우팅·미들웨어·스트리밍 I/O·graceful shutdown) + 파일 업로드 보안

---

## 1. 기능 요약

| 기능 | 내용 |
|---|---|
| 업로드 (웹) | 드래그 앤 드롭 / 파일 선택, 진행률 표시, 결과에 링크·만료·SHA-256·삭제 키 표시 |
| 업로드 (curl) | `curl -F "file=@a.pdf" URL/upload` (multipart) 또는 `curl -T a.pdf URL/upload/` (PUT) |
| 공유 링크 | `/f/<128비트 랜덤 ID>/<파일명>` — 링크를 아는 사람만 접근 (추측 불가) |
| 다운로드 | 브라우저 / `curl -OJ` 모두 가능, `Range`·`HEAD` 지원 |
| 삭제 | 업로드 때 받은 **삭제 키**로만 삭제 가능 (`DELETE /f/<id>` + `X-Delete-Key` 헤더) |
| 자동 만료 | 기본 24시간 후 비공개 처리 + 10분 주기 정리로 디스크에서도 삭제 |
| 보안 검증 | 파일명 정규화, 확장자 허용 목록, 이중 확장자, **내용 기반 MIME 검사**, 크기·총량 제한, IP별 속도 제한 (7장 참고) |

---

## 2. 빠른 시작

```bash
go run .                                   # http://localhost:8080
MAX_UPLOAD_MB=50 TTL_HOURS=72 go run .     # 설정 변경 (환경변수, 5장)

go test -race ./...                        # 단위/통합 테스트 (81개)
./scripts/e2e.sh                           # 실제 서버를 띄워 진짜 curl 로 39개 항목 검증
./scripts/mutation_check.sh                # 방어 코드를 일부러 망가뜨려 테스트가 잡아내는지 검증 (15개)
```

### curl 사용법

```bash
# 업로드 (multipart) — 응답 첫 줄이 공유 링크
curl -F "file=@./report.pdf" http://localhost:8080/upload

# 업로드 (PUT) — URL 끝의 / 때문에 curl 이 로컬 파일명을 자동으로 붙인다
curl -T ./report.pdf http://localhost:8080/upload/

# JSON 으로 받기 (스크립트용)
curl -H "Accept: application/json" -F "file=@./report.pdf" http://localhost:8080/upload

# 링크만 변수에 담기
LINK=$(curl -sS -F "file=@./report.pdf" http://localhost:8080/upload | head -n1)

# 다운로드 — -J 가 있어야 원래 파일명(한글 포함)으로 저장된다 (아래 '알아둘 점' 참고)
curl -OJ "$LINK"

# 삭제 (업로드 응답의 delete 줄을 그대로 복사해 쓰면 된다)
curl -X DELETE -H "X-Delete-Key: <삭제키>" http://localhost:8080/f/<id>
```

> **알아둘 점 — `curl -O` vs `curl -OJ`** (curl 8.5.0 에서 확인)
> `-O` 는 URL 마지막 조각을 **디코딩하지 않고** 파일명으로 쓰므로 공백·`'`·한글이 있으면 `%EB%B3%B4…` 같은 이름으로 저장됩니다.
> `-OJ` 는 응답의 `Content-Disposition: filename="…"` 을 쓰므로 원래 이름이 복원됩니다.
> 그래서 서버는 `filename*=UTF-8''…`(브라우저용, RFC 5987)와 UTF-8 `filename="…"`(curl -J 용)을 **둘 다** 보냅니다.
> 업로드 응답의 `get:` 줄이 이미 `curl -OJ '<링크>'` 형태로 안내해 줍니다.

---

## 3. API 레퍼런스

| 메서드 | 경로 | 설명 | 성공 | 주요 실패 |
|---|---|---|---|---|
| `GET` | `/` | 웹 UI | 200 | |
| `GET` | `/static/app.js`, `/static/style.css` | 정적 파일 (**허용 목록 2개만** 서빙) | 200 | 그 외 404 |
| `GET` | `/api/limits` | 최대 크기·보관 시간·허용 확장자 (UI 안내용) | 200 | |
| `GET` | `/healthz` | 헬스 체크 | 200 `ok` | |
| `POST` | `/upload` | multipart/form-data, 필드명 **`file`** | **201** | 400, 413, 415, 429, 507 |
| `PUT` | `/upload/{name}` | 본문 = 파일 내용 (`curl -T`) | **201** | 400, 413, 415, 429, 507 |
| `GET`/`HEAD` | `/f/{id}` 또는 `/f/{id}/{name}` | 다운로드 (`{name}` 은 장식용, 무시됨) | 200 / 206 | 404 |
| `DELETE` | `/f/{id}` + 헤더 `X-Delete-Key` | 삭제 | 200 | 403, 404 |

### 상태 코드

| 코드 | 의미 | 발생 조건 |
|---|---|---|
| 201 | 업로드 성공 | |
| 400 | 잘못된 요청 | `file` 필드 없음, 빈 파일, 제어문자/방향문자/NUL 포함 파일명, 파일명 너무 김, multipart 아님 |
| 403 | 삭제 키 불일치 | |
| 404 | 없음 | 잘못된 ID 형식 / 존재하지 않음 / **만료** — 모두 같은 응답 (존재 여부 탐색 방지) |
| 413 | 너무 큼 | `MAX_UPLOAD_MB` 초과 (Content-Length 위조·chunked 전송으로도 우회 불가) |
| 415 | 허용되지 않는 형식 | 확장자 없음/미허용, 위험 이중 확장자, **내용과 확장자 불일치** |
| 429 | 속도 제한 | IP당 분당 `RATE_PER_MIN` 초과 (`Retry-After` 헤더 포함) |
| 507 | 저장 공간 부족 | `MAX_TOTAL_MB` 초과 |

### 응답 예시

`curl -F "file=@hello.txt" URL/upload` (평문, **첫 줄이 링크**):

```text
http://127.0.0.1:8080/f/39ee455ed93dd4b3254687c2b2fb0696/hello.txt
get:     curl -OJ 'http://127.0.0.1:8080/f/39ee455ed93dd4b3254687c2b2fb0696/hello.txt'
expires: 2026-10-05T06:26:33Z
delete:  curl -X DELETE -H 'X-Delete-Key: 9c29600fca21f477698bb50c69fb8ce6' http://127.0.0.1:8080/f/39ee455ed93dd4b3254687c2b2fb0696
```

`Accept: application/json` (웹 UI가 사용):

```json
{"id":"69714710832ba1a62a05465044f76395","name":"hello.txt","size":16,"mime":"text/plain; charset=utf-8","sha256":"537c6349a9bd55a1c5de25d1efd05af33decf7dcbf984f04e6a472bc28c574e8","url":"http://127.0.0.1:8080/f/69714710832ba1a62a05465044f76395/hello.txt","expires_at":"2026-10-05T06:26:33.342682326Z","delete_key":"407381f4dd36239f33bc1a7b3fb61aa0"}
```

거부 예시 (실제 출력):

```text
$ curl -F "file=@evil.html" URL/upload
error: 허용되지 않는 확장자입니다: .html (허용: .csv .docx .gif .jpeg .jpg .json .log .md .pdf .png .pptx .txt .webp .xlsx .zip)   (HTTP 415)

$ curl -F "file=@evil.html;filename=safe.txt" URL/upload          # 내용은 html, 이름만 .txt 로 위장
error: 파일 내용(text/html)이 확장자(.txt)와 일치하지 않습니다   (HTTP 415)

$ curl -F "file=@hello.txt;filename=shell.php.txt" URL/upload
error: 위험한 이중 확장자가 포함되어 있습니다: .php   (HTTP 415)

$ curl -F "file=@big.txt" URL/upload                               # 서버 한도 1MB, 파일 2MB
error: 파일이 너무 큽니다 (최대 1MB)   (HTTP 413)
```

허용 확장자: `.txt .md .csv .json .log .pdf .png .jpg .jpeg .gif .webp .zip .docx .xlsx .pptx`

---

## 4. 프로젝트 구조

```text
fileshare/
├── main.go            설정 로드(환경변수), 서버 기동, 타임아웃, graceful shutdown, 백그라운드 정리 루프
├── server.go          라우팅(Go 1.22 패턴), 미들웨어(보안 헤더·로깅·panic 복구), 공통 헬퍼
├── upload.go          업로드 핸들러(POST/PUT) + ingest(): 보안 검증 파이프라인의 핵심
├── download.go        다운로드/삭제 핸들러
├── validate.go        파일명 정규화, 확장자 허용 목록, 이중 확장자, MIME 검사, Content-Disposition
├── store.go           디스크 저장소: 랜덤 ID, 메타데이터, 원자적 확정(rename), 용량 한도, 만료 정리
├── ratelimit.go       IP별 토큰 버킷 속도 제한
├── web/               index.html · style.css · app.js  (go:embed 로 바이너리에 내장)
├── validate_test.go   파일명/MIME/헤더 단위 테스트
├── server_test.go     httptest 기반 통합 테스트 (업로드→다운로드→삭제 전 과정, 공격 시나리오)
├── scripts/
│   ├── e2e.sh             실제 서버 + 진짜 curl 로 검증 (BASE=URL 로 배포된 서버도 검증 가능)
│   └── mutation_check.sh  방어 코드를 하나씩 제거해 테스트가 잡는지 확인
├── Dockerfile · docker-compose.yml · deploy/   배포 설정 (DEPLOY.md 참고)
└── DEPLOY.md                                   배포 가이드
```

### 디스크 레이아웃

```text
DATA_DIR/
├── 3f2a…(32 hex).bin      파일 내용 — 이름은 서버가 만든 랜덤 ID 뿐 (사용자 입력이 경로에 쓰이지 않음)
├── 3f2a…(32 hex).json     메타데이터: 정제된 원본 이름, 크기, MIME, SHA-256, 생성/만료 시각, 삭제 키의 해시
└── tmp/                    업로드 중인 임시 파일 (검증 통과 시 rename 으로 확정, 실패 시 즉시 삭제)
```

---

## 5. 설정 (환경변수)

| 변수 | 기본값 | 설명 |
|---|---|---|
| `ADDR` | `:8080` | 리슨 주소. 리버스 프록시 뒤라면 `127.0.0.1:8080` 권장 |
| `DATA_DIR` | `./data` | 파일 저장 디렉터리 |
| `BASE_URL` | (빈 값) | 공유 링크의 기준 주소 (예: `https://files.example.com`). **운영에서는 반드시 지정** — 비우면 요청의 `Host` 헤더로 조립하는데 이 값은 클라이언트가 바꿀 수 있음 |
| `MAX_UPLOAD_MB` | `20` | 파일 1개 최대 크기 |
| `MAX_TOTAL_MB` | `1024` | 저장소 전체 최대 용량 (초과 시 507) |
| `TTL_HOURS` | `24` | 보관 시간 |
| `RATE_PER_MIN` | `30` | IP당 분당 업로드 횟수 (`0` 이하 = 제한 없음) |
| `TRUST_PROXY` | `false` | `true` 면 `X-Forwarded-For`(맨 오른쪽 값)·`X-Forwarded-Proto` 를 신뢰. **앱 포트가 프록시를 거치지 않고 직접 노출되면 절대 켜지 말 것** (IP 위조로 속도 제한 우회 가능). 반대로 프록시 뒤인데 `false` 면 모든 사용자가 프록시 IP 하나로 보여 속도 제한을 함께 맞게 됨 |

---

## 6. 업로드 처리 흐름 (`upload.go: ingest`)

```text
요청 ─▶ ① 속도 제한(IP)                                  → 429
       ② Content-Length 사전 검사 + MaxBytesReader        → 413   (본문을 읽기 전/읽는 동안 모두 강제)
       ③ multipart 를 스트리밍으로 읽음 (파일 전체를 메모리에 올리지 않음)
       ④ 파일명 검증·정규화 (경로 성분 제거, 제어/방향문자 거부)  → 400
       ⑤ 확장자 허용 목록 + 이중 확장자 검사                     → 415
       ⑥ 앞 512바이트로 실제 형식 판별 → 확장자와 일치해야 통과    → 415
       ⑦ 임시 파일(tmp/)에 스트리밍 저장하며 SHA-256 계산, 1바이트 더 읽어 크기 초과 판정  → 413
       ⑧ 총 용량 한도 확인 후 랜덤 ID 로 rename(원자적 확정) + 메타 기록                   → 507
       ⑨ 응답: 링크 + 만료 시각 + 삭제 키   (실패한 단계에서는 임시 파일을 즉시 삭제)
```

핵심 설계 원칙: **사용자가 준 문자열은 절대 파일시스템 경로를 만들지 않는다.**
원본 파일명은 메타데이터와 `Content-Disposition` 에 "표시용"으로만 쓰이고, 디스크 경로는 `crypto/rand` 로 만든 ID 로만 구성됩니다.
그래서 파일명 검증(④⑤)은 경로 조작을 막는 *유일한* 방어가 아니라, 그 위에 얹은 **다층 방어**입니다.

---

## 7. 보안 설계

| # | 위협 | 방어 | 코드 | 검증 |
|---|---|---|---|---|
| 1 | 파일명 경로 조작 (`../../etc/passwd`, 절대경로, `..\`) | 경로 성분 제거 후 마지막 이름만 사용 + **디스크 경로는 랜덤 ID 만** | `validate.go` `validateFilename`, `store.go` | `TestPathTraversalFilenames`, `TestValidateFilename`, e2e §7 |
| 2 | URL 의 ID 로 경로 조작 (`/f/..%2f..%2fetc%2fpasswd`) | 경로 만들기 **전에** `^[0-9a-f]{32}$` 검증, 모든 오류를 동일한 404 로 응답 | `store.go` `idPattern` | `TestDownloadCannotEscapeDataDir` (DATA_DIR 밖에 **미끼 파일**을 심어 탈출 여부 확인), `TestDownloadInvalidIDs` |
| 3 | 위험 확장자 업로드 (`.php .html .exe .svg .sh` …) | **허용 목록(allowlist)** 방식 — 목록에 없으면 거부 | `validate.go` `allowedTypes` | `TestRejectedUploads`, e2e §8 |
| 4 | 이중 확장자 (`shell.php.png`) | 첫 조각을 제외한 중간 확장자가 위험 목록에 있으면 거부 | `validate.go` `dangerousExt` | `TestValidateFilename` |
| 5 | 확장자/MIME 위조 (html 을 `.png` 로) | **내용 기반 판별** (`http.DetectContentType` 앞 512B) 결과가 확장자와 일치해야 함. 클라이언트가 보낸 `Content-Type` 은 신뢰하지 않음 | `validate.go` `checkMIME` | `TestCheckMIME`(23), `TestRejectedUploads`, e2e §9 |
| 6 | 저장형 XSS (업로드한 파일이 브라우저에서 실행) | 항상 `attachment` + `nosniff` + 다운로드 응답 CSP `default-src 'none'; sandbox` + 허용 타입만 저장 | `download.go`, `server.go` | `TestUploadDownloadRoundTrip`, e2e §6 |
| 7 | 헤더 인젝션 / 확장자 시각 위장 (`evil\u202egnp.exe`) | 제어문자(NUL·개행)·**방향 제어 문자** 거부, `"`·`\`·`;`·`%` 치환, RFC 5987 인코딩 | `validate.go` | `TestValidateFilename`, `TestContentDisposition`, e2e §5 |
| 8 | 대용량 업로드 / 디스크 고갈 | `Content-Length` 사전 검사 + `MaxBytesReader` + `LimitedReader` + **총 용량 쿼터** | `upload.go`, `store.go` `Commit` | `TestTooLarge`(chunked 포함), `TestStorageQuota`, e2e §10 |
| 9 | 연속 업로드 남용 | IP별 토큰 버킷 (`RATE_PER_MIN`) | `ratelimit.go` | `TestRateLimit` |
| 10 | 링크 추측(브루트포스) | 128비트 `crypto/rand` ID (약 3.4×10³⁸ 가지) | `store.go` `randomHex` | (확률적 방어) |
| 11 | 무단 삭제 | 별도 128비트 삭제 키, **해시만 저장**, 상수 시간 비교(`subtle`), 헤더로 전달(쿼리스트링은 로그에 남음) | `store.go` `CheckDeleteKey` | `TestDelete`, e2e §11 |
| 12 | 오래된 파일 방치 | TTL 만료 시 404 + 10분 주기 정리 | `store.go` `Cleanup` | `TestExpiry` |
| 13 | 임시 파일 잔류 | 실패 시 `defer` 로 삭제, 1시간 지난 `tmp/` 항목 정리 | `upload.go`, `store.go` | `TestRejectedUploads`, e2e §12 |
| 14 | 로그로 접근권한 유출 | 공유 ID 는 앞 6자만 로깅, 삭제 키는 헤더라 URL 로그에 안 남음 | `server.go` `redactPath` | `TestRedactPath`, e2e §12 |
| 15 | Slowloris 등 연결 점유 | `ReadHeaderTimeout 10s` · `ReadTimeout/WriteTimeout 5m` · `IdleTimeout 60s` · 헤더 크기 제한 | `main.go` | (수동 확인 필요) |
| 16 | `X-Forwarded-For` 위조로 속도 제한 우회 | `TRUST_PROXY` 일 때만 신뢰, **맨 오른쪽** 값 사용 | `server.go` `clientIP` | `TestClientIP` |
| 17 | 웹 UI 의 XSS·클릭재킹 | CSP(`script-src 'self'` 등)·`X-Frame-Options: DENY`·`Referrer-Policy`·HSTS(HTTPS 시), JS 는 `textContent` 만 사용 | `server.go` `headersMW`, `web/app.js` | `TestPagesAndHeaders` |
| 18 | `Host` 헤더 조작으로 링크 위조 | `BASE_URL` 고정 지정 | `server.go` `baseURL` | (운영 설정으로 해결) |
| 19 | 컨테이너/프로세스 침해 시 피해 확대 | 비루트 실행, distroless, `read_only`, `cap_drop: ALL`, systemd 샌드박스 | `Dockerfile`, `docker-compose.yml`, `deploy/` | `docker compose` 로 기동 후 `docker inspect` 로 `ReadOnly=true`·`CapDrop=[ALL]`·`User=65532` 확인 (systemd 샌드박스는 미검증) |

### "테스트가 정말 방어하고 있는가" — 변이 검사

`./scripts/mutation_check.sh` 는 방어 코드를 **하나씩 일부러 망가뜨린 복사본**에서 `go test` 를 돌립니다.
방어를 없앴는데도 테스트가 통과한다면 그 테스트는 아무것도 보장하지 못하는 것입니다.

```text
[KILLED  ] MIME 내용 검사 우회              [KILLED  ] 저장 용량 한도 제거
[KILLED  ] 확장자 허용 목록 우회            [KILLED  ] 실패 시 임시파일 삭제 제거
[KILLED  ] 이중 확장자 검사 제거            [KILLED  ] 삭제 키 검사 우회
[KILLED  ] 경로 성분 제거(path.Base) 제거   [KILLED  ] Rate limit 제거
[KILLED  ] 제어문자(NUL/개행 등) 검사 제거  [KILLED  ] 다운로드 CSP sandbox 제거
[KILLED  ] 방향 제어(RLO) 문자 검사 제거    [KILLED  ] nosniff 헤더 제거
[KILLED  ] Content-Disposition attachment 제거
[KILLED  ] 파일 ID 정규식 검증 전부 제거
[KILLED  ] 만료 검사 제거(Get)
결과: KILLED=15 SURVIVED=0 INVALID=0
```

> 이 검사로 실제 테스트 허점을 하나 찾아 고쳤습니다. 처음에는 `/f/../../etc/passwd` 류 테스트가 있었지만, ID 정규식을 지워도 통과했습니다.
> 그 경로에 파일이 *없어서* 404 가 나온 것이지 정규식이 막은 게 아니었기 때문입니다.
> 지금은 `DATA_DIR` **바깥**에 유효한 형식의 미끼 파일(`secret.json/.bin`)을 심어 두고 `..%2Fsecret` 으로 읽어낼 수 있는지 시도하는 `TestDownloadCannotEscapeDataDir` 가 이 방어를 직접 증명합니다.

---

## 8. 테스트/검증 결과

| 구분 | 명령 | 결과 |
|---|---|---|
| 단위/통합 테스트 | `go test -race -count=1 ./...` | 최상위 21개 + 하위 케이스 60개 = **81개 통과**, 레이스 디텍터 통과, 구문 커버리지 75.4% |
| 정적 검사 | `go vet ./...` · `gofmt -l .` | 경고 없음 |
| E2E (실서버 + 진짜 curl 8.5.0) | `./scripts/e2e.sh` | **39개 항목 전부 통과** |
| 변이 검사 | `./scripts/mutation_check.sh` | 15개 변이 전부 탐지 |
| 웹 UI | `node --check web/app.js` + 정적 파일/보안 헤더 테스트 | **JS 문법·서빙까지만 확인.** 브라우저에서 직접 클릭해 보는 테스트는 하지 않았으므로 제출 전에 직접 확인 필요 |
| Docker 이미지 | `docker build` | 빌드 성공 (15MB). 이 이미지를 `--read-only --cap-drop ALL` 로 띄우고 Linux 컨테이너에서 e2e 원격 모드 실행 → **33개 항목 전부 통과** |
| Docker Compose + Caddy | `DOMAIN=localhost docker compose up -d --build` | HTTPS 업로드·다운로드 200, HTTP→HTTPS 308 리다이렉트, 30MB 요청을 Caddy `max_size` 가 413 으로 차단, 컨테이너 `ReadOnly=true`·`CapDrop=[ALL]`·`User=65532` 확인 |
| systemd (`deploy/fileshare.service`) | — | **미검증** (유닛 문법만 확인) |

배포된 서버도 같은 스크립트로 검증할 수 있습니다 (크기 제한·디스크 검사 항목은 자동으로 건너뜀):

```bash
BASE=https://files.example.com ./scripts/e2e.sh
```

**Windows 에서 실행할 때:** Git Bash 에서 `scripts/e2e.sh` 를 그대로 돌리면 서버와 무관한 실패가 7개 납니다. Windows 용 `curl.exe` 가 한글 파일명을 깨뜨리고, 백신이 `.php` 테스트 파일 전송을 막기 때문입니다. Go 가 설치되어 있지 않아도 Docker 만 있으면 Linux 컨테이너 안에서 같은 검증을 돌릴 수 있습니다 (PowerShell, 프로젝트 폴더에서):

```powershell
# 로컬 모드: 컨테이너 안에서 빌드·기동까지 (39개 항목)
docker run --rm -v "${PWD}:/src:ro" -w /src -e LANG=C.UTF-8 -e GOFLAGS=-buildvcs=false golang:1.24-alpine `
  sh -c "apk add -q bash curl coreutils findutils grep && bash scripts/e2e.sh"

# 원격 모드: 배포된 서버 검증
docker run --rm -v "${PWD}:/src:ro" -w /src -e LANG=C.UTF-8 -e BASE=https://files.example.com alpine:3.20 `
  sh -c "apk add -q bash curl coreutils findutils && bash scripts/e2e.sh"
```

---

## 9. 알려진 한계 (솔직하게)

| 한계 | 설명 / 완화 |
|---|---|
| 폴리글롯 파일 | `DetectContentType` 은 앞 512바이트의 매직 넘버만 봅니다. 유효한 PNG 뒤에 HTML 을 붙인 파일은 통과할 수 있습니다. → 그래서 내용 검사에만 의존하지 않고 `attachment`+`nosniff`+CSP `sandbox` 를 **항상** 함께 씁니다. 더 강하게 하려면 다운로드를 별도 도메인(오리진)에서 서빙하세요 |
| zip 계열 내부 미검증 | `.zip/.docx/.xlsx/.pptx` 는 `PK` 시그니처만 확인합니다. 서버는 압축을 풀지 않으므로 서버가 zip bomb 에 당하지는 않지만, 받는 사람 쪽 보호는 못 합니다 |
| 악성코드 검사 없음 | 허용된 형식 안에 담긴 악성 문서(예: 매크로가 든 docx)는 막지 못합니다 (ClamAV 연동 등은 향후 과제) |
| 인증 없음 | 링크를 아는 사람은 누구나 다운로드합니다(capability URL). 공개 서비스라 누구나 업로드할 수 있으며, 남용은 속도 제한·총량 쿼터·TTL 로 *완화*할 뿐입니다. 비밀번호·다운로드 횟수 제한은 없습니다 |
| 삭제 응답의 존재 노출 | 삭제 시 없는 ID 는 404, 있는 ID + 틀린 키는 403 이라 "그 ID 가 존재한다"는 사실이 드러납니다. ID 가 128비트라 추측이 불가능해 실질 위험은 낮다고 판단했습니다 |
| 단일 인스턴스 가정 | 속도 제한 상태가 메모리에 있어 재시작하면 초기화되고, 서버를 여러 대로 늘리면 공유되지 않습니다. 용량 계산·메타 기록도 한 프로세스를 전제로 합니다 |
| TLS 직접 종단 없음 | HTTPS 는 Caddy/nginx 같은 리버스 프록시가 처리하는 구성을 가정합니다 (`DEPLOY.md`) |
| 만료 정리 주기 | 만료 파일은 즉시 404 가 되지만 디스크에서는 최대 10분 뒤에 지워집니다 |

### 향후 과제 후보

`os.Root`(Go 1.24) 로 경로 탈출을 파일시스템 수준에서 한 번 더 차단 · 다운로드 전용 도메인 분리 · 비밀번호/1회용 링크 · ClamAV 연동 · Prometheus 메트릭 · 공유 속도 제한(Redis)

---

## 10. 과제 완료 기준 대응

| 기준 | 충족 근거 |
|---|---|
| 웹과 `curl` 을 통한 업로드/공유 정상 동작 | 웹 UI(`web/`) + `curl -F`/`-T`/`-OJ`/`-X DELETE` — `scripts/e2e.sh` 39개 항목 |
| 보안 검증(경로·MIME·확장자 등) 적용 | 7장 표 19항목, 테스트 81개 + 변이 검사 15개 |
| 제출물 정상 작동 확인 | 배포 후 `BASE=https://… ./scripts/e2e.sh` 로 동일하게 검증 (`DEPLOY.md`) |

**제출물:** ① 외부 접속 가능한 웹 서비스 링크 → `DEPLOY.md` 따라 배포 · ② GitHub 저장소 URL · ③ 블로그 링크(노션 X)
