# FileShare

Go로 만든 파일 공유 서비스입니다. 파일을 올리면 공유 링크가 생깁니다. 웹 화면과 `curl` 둘 다로 올리고 받을 수 있으며, 업로드할 때 파일명, 확장자, 실제 내용(MIME), 경로, 크기를 검사합니다.

- 언어와 의존성: Go 1.22 이상, 표준 라이브러리만 사용합니다. `go.sum`이 없고, 웹 UI는 `go:embed`로 바이너리에 들어갑니다. 빌드 결과는 약 6 MB짜리 파일 하나입니다.
- 목표: Go 웹 백엔드 구조(라우팅, 미들웨어, 스트리밍 I/O, graceful shutdown)와 파일 업로드 보안을 공부하는 것.

---

## 1. 기능

| 기능 | 내용 |
|---|---|
| 업로드 (웹) | 드래그 앤 드롭 또는 파일 선택. 진행률을 보여 주고, 결과에 링크, 만료 시각, SHA-256, 삭제 키를 표시합니다. |
| 업로드 (curl) | `curl -F "file=@a.pdf" URL/upload` (multipart) 또는 `curl -T a.pdf URL/upload/` (PUT) |
| 공유 링크 | `/f/<128비트 랜덤 ID>/<파일명>`. 링크를 아는 사람만 열 수 있고, ID는 추측하기 어렵습니다. |
| 다운로드 | 브라우저나 `curl -OJ`로 받을 수 있고, `Range`와 `HEAD` 요청을 지원합니다. |
| 삭제 | 업로드할 때 받은 삭제 키가 있어야 지울 수 있습니다 (`DELETE /f/<id>` + `X-Delete-Key` 헤더). |
| 자동 만료 | 기본 24시간이 지나면 접근이 막히고, 10분마다 도는 정리 작업이 디스크에서도 지웁니다. |
| 검증 | 파일명 정규화, 확장자 허용 목록, 이중 확장자 차단, 내용 기반 MIME 검사, 크기와 총량 제한, IP별 속도 제한. 자세한 내용은 7장에 있습니다. |

---

## 2. 빠른 시작

```bash
go run .                                   # http://localhost:8080
MAX_UPLOAD_MB=50 TTL_HOURS=72 go run .     # 설정 변경 (환경변수, 5장 참고)

go test -race ./...                        # 단위/통합 테스트 (81개)
./scripts/e2e.sh                           # 서버를 띄우고 실제 curl로 39개 항목 검증
./scripts/mutation_check.sh                # 방어 코드를 하나씩 망가뜨려 테스트가 잡는지 확인 (15개)
```

### curl 사용법

```bash
# 업로드 (multipart). 응답의 첫 줄이 공유 링크입니다.
curl -F "file=@./report.pdf" http://localhost:8080/upload

# 업로드 (PUT). URL 끝의 /가 있으면 curl이 로컬 파일명을 자동으로 붙입니다.
curl -T ./report.pdf http://localhost:8080/upload/

# JSON으로 받기 (스크립트용)
curl -H "Accept: application/json" -F "file=@./report.pdf" http://localhost:8080/upload

# 링크만 변수에 담기
LINK=$(curl -sS -F "file=@./report.pdf" http://localhost:8080/upload | head -n1)

# 다운로드. -J가 있어야 원래 파일명(한글 포함)으로 저장됩니다. 아래 설명 참고.
curl -OJ "$LINK"

# 삭제. 업로드 응답의 delete 줄을 그대로 복사해 쓰면 됩니다.
curl -X DELETE -H "X-Delete-Key: <삭제키>" http://localhost:8080/f/<id>
```

> **`curl -O`와 `curl -OJ`의 차이** (curl 8.5.0에서 확인)
> `-O`는 URL의 마지막 조각을 디코딩하지 않고 파일명으로 씁니다. 그래서 공백, `'`, 한글이 있으면 `%EB%B3%B4...` 같은 이름으로 저장됩니다.
> `-OJ`는 응답의 `Content-Disposition: filename="..."`을 쓰므로 원래 이름이 복원됩니다.
> 서버는 `filename*=UTF-8''...`(브라우저용, RFC 5987)와 UTF-8 `filename="..."`(curl -J용)을 함께 보냅니다.
> 업로드 응답의 `get:` 줄이 `curl -OJ '<링크>'` 형태로 안내해 줍니다.

---

## 3. API

| 메서드 | 경로 | 설명 | 성공 | 주요 실패 |
|---|---|---|---|---|
| `GET` | `/` | 웹 UI | 200 | |
| `GET` | `/static/app.js`, `/static/style.css` | 정적 파일 (허용 목록의 두 파일만 제공) | 200 | 그 외 404 |
| `GET` | `/api/limits` | 최대 크기, 보관 시간, 허용 확장자 (UI 안내용) | 200 | |
| `GET` | `/healthz` | 헬스 체크 | 200 `ok` | |
| `POST` | `/upload` | multipart/form-data, 필드명 `file` | 201 | 400, 413, 415, 429, 507 |
| `PUT` | `/upload/{name}` | 본문이 파일 내용 (`curl -T`) | 201 | 400, 413, 415, 429, 507 |
| `GET`, `HEAD` | `/f/{id}` 또는 `/f/{id}/{name}` | 다운로드. `{name}`은 장식용이라 무시됩니다. | 200 / 206 | 404 |
| `DELETE` | `/f/{id}` + 헤더 `X-Delete-Key` | 삭제 | 200 | 403, 404 |

### 상태 코드

| 코드 | 의미 | 발생 조건 |
|---|---|---|
| 201 | 업로드 성공 | |
| 400 | 잘못된 요청 | `file` 필드가 없음, 빈 파일, 제어 문자나 방향 문자가 든 파일명, 파일명이 너무 김, multipart가 아님 |
| 403 | 삭제 키 불일치 | |
| 404 | 없음 | 잘못된 ID 형식, 존재하지 않음, 만료. 세 경우 응답이 같아서 파일 존재 여부를 알 수 없습니다. |
| 413 | 너무 큼 | `MAX_UPLOAD_MB` 초과. Content-Length를 속이거나 chunked로 보내도 우회되지 않습니다. |
| 415 | 허용되지 않는 형식 | 확장자가 없거나 허용 목록에 없음, 위험한 이중 확장자, 내용과 확장자가 맞지 않음 |
| 429 | 속도 제한 | IP당 분당 `RATE_PER_MIN` 초과. `Retry-After` 헤더가 붙습니다. |
| 507 | 저장 공간 부족 | `MAX_TOTAL_MB` 초과 |

### 응답 예시

`curl -F "file=@hello.txt" URL/upload`의 평문 응답입니다. 첫 줄이 링크입니다.

```text
http://127.0.0.1:8080/f/39ee455ed93dd4b3254687c2b2fb0696/hello.txt
get:     curl -OJ 'http://127.0.0.1:8080/f/39ee455ed93dd4b3254687c2b2fb0696/hello.txt'
expires: 2026-10-05T06:26:33Z
delete:  curl -X DELETE -H 'X-Delete-Key: 9c29600fca21f477698bb50c69fb8ce6' http://127.0.0.1:8080/f/39ee455ed93dd4b3254687c2b2fb0696
```

`Accept: application/json` 요청에 대한 응답입니다. 웹 UI가 이 형식을 씁니다.

```json
{"id":"69714710832ba1a62a05465044f76395","name":"hello.txt","size":16,"mime":"text/plain; charset=utf-8","sha256":"537c6349a9bd55a1c5de25d1efd05af33decf7dcbf984f04e6a472bc28c574e8","url":"http://127.0.0.1:8080/f/69714710832ba1a62a05465044f76395/hello.txt","expires_at":"2026-10-05T06:26:33.342682326Z","delete_key":"407381f4dd36239f33bc1a7b3fb61aa0"}
```

거부되는 경우의 출력입니다.

```text
$ curl -F "file=@evil.html" URL/upload
error: 허용되지 않는 확장자입니다: .html (허용: .csv .docx .gif .jpeg .jpg .json .log .md .pdf .png .pptx .txt .webp .xlsx .zip)   (HTTP 415)

$ curl -F "file=@evil.html;filename=safe.txt" URL/upload          # 내용은 html인데 이름만 .txt
error: 파일 내용(text/html)이 확장자(.txt)와 일치하지 않습니다   (HTTP 415)

$ curl -F "file=@hello.txt;filename=shell.php.txt" URL/upload
error: 위험한 이중 확장자가 포함되어 있습니다: .php   (HTTP 415)

$ curl -F "file=@big.txt" URL/upload                               # 서버 한도 1MB, 파일 2MB
error: 파일이 너무 큽니다 (최대 1MB)   (HTTP 413)
```

허용 확장자는 `.txt .md .csv .json .log .pdf .png .jpg .jpeg .gif .webp .zip .docx .xlsx .pptx`입니다.

---

## 4. 프로젝트 구조

```text
fileshare/
├── main.go            설정 로드(환경변수), 서버 기동, 타임아웃, graceful shutdown, 정리 루프
├── server.go          라우팅(Go 1.22 패턴), 미들웨어(보안 헤더, 로깅, panic 복구), 공통 헬퍼
├── upload.go          업로드 핸들러(POST, PUT)와 ingest(): 검증 파이프라인
├── download.go        다운로드와 삭제 핸들러
├── validate.go        파일명 정규화, 확장자 허용 목록, 이중 확장자, MIME 검사, Content-Disposition
├── store.go           디스크 저장소: 랜덤 ID, 메타데이터, rename으로 확정, 용량 한도, 만료 정리
├── ratelimit.go       IP별 토큰 버킷 속도 제한
├── web/               index.html, style.css, app.js (go:embed로 바이너리에 포함)
├── validate_test.go   파일명, MIME, 헤더 단위 테스트
├── server_test.go     httptest 기반 통합 테스트 (업로드, 다운로드, 삭제, 공격 시나리오)
├── scripts/
│   ├── e2e.sh             실제 서버에 curl로 검증. BASE=URL을 주면 배포된 서버도 검증
│   └── mutation_check.sh  방어 코드를 하나씩 제거해서 테스트가 잡는지 확인
├── Dockerfile, docker-compose.yml, deploy/   배포 설정 (DEPLOY.md 참고)
└── DEPLOY.md          배포 가이드
```

### 디스크 레이아웃

```text
DATA_DIR/
├── 3f2a...(32 hex).bin    파일 내용. 이름은 서버가 만든 랜덤 ID뿐이라 사용자 입력이 경로에 들어가지 않습니다.
├── 3f2a...(32 hex).json   메타데이터: 정제된 원본 이름, 크기, MIME, SHA-256, 생성/만료 시각, 삭제 키의 해시
└── tmp/                   업로드 중인 임시 파일. 검증을 통과하면 rename으로 확정하고, 실패하면 바로 지웁니다.
```

---

## 5. 설정 (환경변수)

| 변수 | 기본값 | 설명 |
|---|---|---|
| `ADDR` | `:8080` | 리슨 주소. 리버스 프록시 뒤라면 `127.0.0.1:8080`을 권장합니다. |
| `DATA_DIR` | `./data` | 파일을 저장할 디렉터리 |
| `BASE_URL` | (빈 값) | 공유 링크의 기준 주소 (예: `https://files.example.com`). 운영에서는 반드시 지정하세요. 비워 두면 요청의 `Host` 헤더로 링크를 만드는데, 클라이언트가 이 값을 바꿀 수 있습니다. |
| `MAX_UPLOAD_MB` | `20` | 파일 하나의 최대 크기 |
| `MAX_TOTAL_MB` | `1024` | 저장소 전체 최대 용량. 넘으면 507 |
| `TTL_HOURS` | `24` | 보관 시간 |
| `RATE_PER_MIN` | `30` | IP당 분당 업로드 횟수. `0` 이하면 제한하지 않습니다. |
| `TRUST_PROXY` | `false` | `true`면 `X-Forwarded-For`(맨 오른쪽 값)와 `X-Forwarded-Proto`를 믿습니다. 앱 포트가 프록시를 거치지 않고 바로 노출되어 있다면 켜면 안 됩니다. IP를 위조해 속도 제한을 우회할 수 있습니다. 반대로 프록시 뒤에 있는데 `false`로 두면 모든 사용자가 프록시 IP 하나로 묶여 속도 제한이 한꺼번에 걸립니다. |

---

## 6. 업로드 처리 순서 (`upload.go`의 `ingest`)

```text
요청 ─▶ ① IP 속도 제한                                   → 429
       ② Content-Length 사전 검사 + MaxBytesReader        → 413  (읽기 전과 읽는 중에 모두 확인)
       ③ multipart를 스트리밍으로 읽음 (파일 전체를 메모리에 올리지 않음)
       ④ 파일명 검증과 정규화 (경로 성분 제거, 제어 문자와 방향 문자 거부)   → 400
       ⑤ 확장자 허용 목록과 이중 확장자 검사                    → 415
       ⑥ 앞 512바이트로 실제 형식을 판별하고 확장자와 맞는지 확인  → 415
       ⑦ tmp/에 스트리밍으로 저장하면서 SHA-256 계산, 1바이트를 더 읽어 크기 초과 판정  → 413
       ⑧ 총 용량을 확인하고 랜덤 ID로 rename (원자적 확정) 후 메타 기록   → 507
       ⑨ 응답: 링크, 만료 시각, 삭제 키 (실패한 단계에서는 임시 파일을 바로 지움)
```

사용자가 준 문자열로는 파일 경로를 만들지 않습니다. 원본 파일명은 메타데이터와 `Content-Disposition`에 표시용으로만 쓰입니다. 디스크 경로는 `crypto/rand`로 만든 ID로만 구성됩니다. 그래서 파일명 검증(④, ⑤)은 경로 조작을 막는 유일한 수단이 아니고, 그 위에 한 겹 더 얹은 방어입니다.

---

## 7. 보안 설계

| # | 위협 | 방어 | 코드 | 검증 |
|---|---|---|---|---|
| 1 | 파일명으로 경로 조작 (`../../etc/passwd`, 절대 경로, `..\`) | 경로 성분을 제거하고 마지막 이름만 사용. 디스크 경로는 랜덤 ID만으로 만듭니다. | `validate.go` `validateFilename`, `store.go` | `TestPathTraversalFilenames`, `TestValidateFilename`, e2e 7번 |
| 2 | URL의 ID로 경로 조작 (`/f/..%2f..%2fetc%2fpasswd`) | 경로를 만들기 전에 `^[0-9a-f]{32}$`로 검사하고, 모든 오류를 같은 404로 응답 | `store.go` `idPattern` | `TestDownloadCannotEscapeDataDir`, `TestDownloadInvalidIDs` |
| 3 | 위험한 확장자 업로드 (`.php .html .exe .svg .sh` 등) | 허용 목록에 없으면 거부 | `validate.go` `allowedTypes` | `TestRejectedUploads`, e2e 8번 |
| 4 | 이중 확장자 (`shell.php.png`) | 첫 조각을 뺀 중간 확장자가 위험 목록에 있으면 거부 | `validate.go` `dangerousExt` | `TestValidateFilename` |
| 5 | 확장자나 MIME 위조 (HTML을 `.png`로 이름만 바꿈) | 내용으로 판별한 결과(`http.DetectContentType`, 앞 512바이트)가 확장자와 맞아야 통과. 클라이언트가 보낸 `Content-Type`은 믿지 않습니다. | `validate.go` `checkMIME` | `TestCheckMIME`, `TestRejectedUploads`, e2e 9번 |
| 6 | 저장형 XSS (업로드한 파일이 브라우저에서 실행됨) | 항상 `attachment`, `nosniff`를 붙이고, 다운로드 응답에 `default-src 'none'; sandbox` CSP를 설정하며, 허용된 타입만 저장 | `download.go`, `server.go` | `TestUploadDownloadRoundTrip`, e2e 6번 |
| 7 | 헤더 인젝션, 방향 문자로 확장자 위장 (`evil‮gnp.exe`) | NUL, 개행, 방향 제어 문자를 거부하고, `"`, `\`, `;`, `%`는 치환. 이름은 RFC 5987로 인코딩합니다. | `validate.go` | `TestValidateFilename`, `TestContentDisposition`, e2e 5번 |
| 8 | 큰 업로드로 디스크 고갈 | Content-Length 사전 검사, `MaxBytesReader`, `LimitedReader`, 총 용량 한도 | `upload.go`, `store.go` `Commit` | `TestTooLarge` (chunked 포함), `TestStorageQuota`, e2e 10번 |
| 9 | 연속 업로드 남용 | IP별 토큰 버킷 (`RATE_PER_MIN`) | `ratelimit.go` | `TestRateLimit` |
| 10 | 링크 추측 | 128비트 `crypto/rand` ID | `store.go` `randomHex` | 확률적 방어 |
| 11 | 무단 삭제 | 별도의 128비트 삭제 키. 해시만 저장하고, 상수 시간 비교(`subtle`)를 쓰며, 쿼리 문자열이 아니라 헤더로 받습니다 (쿼리 문자열은 로그에 남음). | `store.go` `CheckDeleteKey` | `TestDelete`, e2e 11번 |
| 12 | 오래된 파일이 남음 | 만료 시 404를 주고, 10분마다 디스크에서도 지웁니다. | `store.go` `Cleanup` | `TestExpiry` |
| 13 | 임시 파일이 남음 | 실패하면 `defer`로 지우고, 1시간 넘은 `tmp/` 항목도 정리 | `upload.go`, `store.go` | `TestRejectedUploads`, e2e 12번 |
| 14 | 로그로 공유 권한이 새어 나감 | 공유 ID는 앞 6자만 기록하고, 삭제 키는 헤더라서 URL 로그에 남지 않습니다. | `server.go` `redactPath` | `TestRedactPath`, e2e 12번 |
| 15 | Slowloris 같은 연결 점유 | `ReadHeaderTimeout` 10초, `ReadTimeout`/`WriteTimeout` 5분, `IdleTimeout` 60초, 헤더 크기 제한 | `main.go` | 수동 확인 필요 |
| 16 | `X-Forwarded-For` 위조로 속도 제한 우회 | `TRUST_PROXY`가 켜졌을 때만 믿고, 맨 오른쪽 값을 씁니다. | `server.go` `clientIP` | `TestClientIP` |
| 17 | 웹 UI의 XSS와 클릭재킹 | 페이지 CSP(`script-src 'self'` 등), `X-Frame-Options: DENY`, `Referrer-Policy`, HTTPS일 때 HSTS. JS는 `textContent`만 씁니다. | `server.go` `headersMW`, `web/app.js` | `TestPagesAndHeaders` |
| 18 | `Host` 헤더 조작으로 링크 위조 | `BASE_URL`을 고정해서 지정 | `server.go` `baseURL` | 운영 설정으로 해결 |
| 19 | 컨테이너나 프로세스가 뚫렸을 때 피해 확대 | 비루트 실행, distroless 이미지, `read_only`, `cap_drop: ALL`, systemd 샌드박스 | `Dockerfile`, `docker-compose.yml`, `deploy/` | `docker inspect`로 `ReadOnly=true`, `CapDrop=[ALL]`, `User=65532` 확인 (systemd 샌드박스는 미검증) |

### 변이 검사로 테스트 확인하기

`./scripts/mutation_check.sh`는 방어 코드를 하나씩 지운 복사본에서 `go test`를 돌립니다. 방어를 지웠는데도 테스트가 통과하면, 그 테스트는 아무것도 보장하지 못하는 것입니다.

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

이 검사로 테스트의 허점을 하나 찾았습니다. 처음에는 `/f/../../etc/passwd` 같은 테스트가 있었는데, ID 정규식을 지워도 통과했습니다. 그 경로에 파일이 없어서 404가 났을 뿐, 정규식이 막은 것이 아니었습니다. 지금은 `DATA_DIR` 바깥에 형식이 맞는 미끼 파일(`secret.json`, `.bin`)을 두고 `..%2Fsecret`으로 읽을 수 있는지 시도하는 `TestDownloadCannotEscapeDataDir`가 이 방어를 직접 확인합니다.

---

## 8. 테스트 결과

| 구분 | 명령 | 결과 |
|---|---|---|
| 단위/통합 테스트 | `go test -race -count=1 ./...` | 최상위 21개, 하위 케이스 60개, 총 81개 통과. 레이스 디텍터 통과, 구문 커버리지 75.4% |
| 정적 검사 | `go vet ./...`, `gofmt -l .` | 경고 없음 |
| E2E (실제 서버 + curl 8.5.0) | `./scripts/e2e.sh` | 39개 항목 전부 통과 |
| 변이 검사 | `./scripts/mutation_check.sh` | 15개 변이 전부 탐지 |
| 웹 UI | `node --check web/app.js` + 정적 파일과 보안 헤더 테스트 | JS 문법과 제공 여부만 확인했습니다. 브라우저에서 직접 눌러 보는 테스트는 하지 않았으니 제출 전에 직접 확인하세요. |
| Docker 이미지 | `docker build` | 빌드 성공, 15MB. `--read-only --cap-drop ALL`로 띄운 뒤 Linux 컨테이너에서 원격 모드 e2e를 돌려 33개 항목 통과 |
| Docker Compose + Caddy | `DOMAIN=localhost docker compose up -d --build` | HTTPS 업로드와 다운로드 200, HTTP에서 HTTPS로 308 리다이렉트, 30MB 요청은 Caddy `max_size`가 413으로 차단, 컨테이너 `ReadOnly=true`, `CapDrop=[ALL]`, `User=65532` 확인 |
| systemd (`deploy/fileshare.service`) | - | 미검증 (유닛 문법만 확인) |

배포된 서버도 같은 스크립트로 검증할 수 있습니다. 크기 제한과 디스크 검사 항목은 자동으로 건너뜁니다.

```bash
BASE=https://files.example.com ./scripts/e2e.sh
```

Windows에서는 Git Bash로 `scripts/e2e.sh`를 그대로 돌리면 서버와 무관한 실패가 7개 나옵니다. Windows용 `curl.exe`가 한글 파일명을 깨뜨리고, 백신이 `.php` 테스트 파일 전송을 막기 때문입니다. Go가 없어도 Docker만 있으면 Linux 컨테이너 안에서 같은 검증을 할 수 있습니다. 프로젝트 폴더에서 PowerShell로 실행하세요.

```powershell
# 로컬 모드: 컨테이너 안에서 빌드와 기동까지 (39개 항목)
docker run --rm -v "${PWD}:/src:ro" -w /src -e LANG=C.UTF-8 -e GOFLAGS=-buildvcs=false golang:1.24-alpine `
  sh -c "apk add -q bash curl coreutils findutils grep && bash scripts/e2e.sh"

# 원격 모드: 배포된 서버 검증
docker run --rm -v "${PWD}:/src:ro" -w /src -e LANG=C.UTF-8 -e BASE=https://files.example.com alpine:3.20 `
  sh -c "apk add -q bash curl coreutils findutils && bash scripts/e2e.sh"
```

---

## 9. 알려진 한계

| 한계 | 설명과 완화 방법 |
|---|---|
| 폴리글롯 파일 | MIME 판별은 앞 512바이트의 매직 넘버만 봅니다. 유효한 PNG 뒤에 HTML을 붙인 파일은 통과할 수 있습니다. 그래서 내용 검사만 믿지 않고 `attachment`, `nosniff`, CSP `sandbox`를 항상 같이 씁니다. 더 강하게 막으려면 다운로드를 별도 도메인(오리진)에서 제공하세요. |
| zip 계열 내부 | `.zip`, `.docx`, `.xlsx`, `.pptx`는 `PK` 시그니처만 확인합니다. 서버는 압축을 풀지 않으므로 zip bomb에 당하지는 않지만, 받는 쪽은 보호되지 않습니다. |
| 악성코드 검사 없음 | 허용된 형식 안에 든 악성 문서(예: 매크로가 있는 docx)는 막지 못합니다. ClamAV 연동은 향후 과제입니다. |
| 인증 없음 | 링크를 아는 사람은 누구나 받을 수 있습니다. 누구나 업로드할 수 있어서, 남용은 속도 제한, 총량 한도, TTL로 줄이는 것뿐입니다. 비밀번호와 다운로드 횟수 제한은 없습니다. |
| 삭제 응답으로 존재 노출 | 없는 ID를 삭제하면 404, 있는 ID에 틀린 키를 주면 403이 나와서 "그 ID가 존재한다"는 사실이 드러납니다. ID가 128비트라 추측하기 어려워서 실질적인 위험은 낮다고 봅니다. |
| 단일 인스턴스 가정 | 속도 제한 상태가 메모리에 있어서 재시작하면 초기화되고, 서버를 여러 대로 늘리면 공유되지 않습니다. 용량 계산과 메타 기록도 프로세스 하나를 전제로 합니다. |
| TLS를 직접 처리하지 않음 | HTTPS는 Caddy나 nginx 같은 리버스 프록시가 처리한다고 가정합니다 (`DEPLOY.md`). |
| 정리 주기 | 만료된 파일은 바로 404가 되지만, 디스크에서는 최대 10분 뒤에 지워집니다. |

### 향후 과제

Go 1.24의 `os.Root`로 파일시스템 수준에서 경로 탈출을 한 번 더 막기, 다운로드 전용 도메인 분리, 비밀번호와 1회용 링크, ClamAV 연동, Prometheus 메트릭, Redis 기반 공유 속도 제한.

---

## 10. 과제 기준 대응

| 기준 | 근거 |
|---|---|
| 웹과 curl로 업로드와 공유가 되는지 | 웹 UI(`web/`)와 `curl -F`, `-T`, `-OJ`, `-X DELETE`. `scripts/e2e.sh` 39개 항목으로 확인 |
| 보안 검증(경로, MIME, 확장자 등) 적용 | 7장 표의 19개 항목, 테스트 81개, 변이 검사 15개 |
| 제출물이 정상 작동하는지 | 배포 후 `BASE=https://... ./scripts/e2e.sh`로 같은 방식으로 확인 (`DEPLOY.md`) |

**제출물:** 외부에서 접속 가능한 웹 서비스 링크, GitHub 저장소 URL, 블로그 링크.
