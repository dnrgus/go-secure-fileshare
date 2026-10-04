# 배포 가이드 — 외부에서 접속 가능한 서버 만들기

과제 제출물 ①(외부에서 접속 가능한 웹 서비스 링크)을 만드는 방법입니다.
**서버 계정/도메인은 본인 것이 필요**하므로 이 문서의 절차는 직접 따라 해야 합니다. 아래 명령은 Ubuntu 22.04/24.04 기준입니다.

> `Dockerfile` · `docker-compose.yml` · `deploy/Caddyfile` 은 Docker Desktop(Windows)에서 `DOMAIN=localhost` 로 **실제 빌드·기동해 검증했습니다**
> (HTTPS 업로드/다운로드, HTTP→HTTPS 리다이렉트, Caddy 크기 제한, e2e 원격 모드 통과 — README 8장).
> 실제 도메인의 Let's Encrypt 인증서 발급과 `deploy/fileshare.service`(systemd)는 아직 검증하지 못했습니다. 문제가 생기면 6장 문제 해결 표를 먼저 보세요.
>
> Docker Desktop 에서는 앱 로그의 클라이언트 IP 가 Docker 게이트웨이 주소(`172.x.0.1`)로 찍힙니다. 실제 Linux 서버에 배포한 뒤에는 접속자 IP 가 찍히는지 로그로 한 번 확인하세요.

---

## 1. 어디에 배포할까

| 선택지 | 적합한 경우 | 장점 | 주의 |
|---|---|---|---|
| **VPS / 클라우드 VM** (Oracle Cloud, AWS Lightsail·EC2, GCP, Vultr 등) | 가장 일반적. 과제 요건(외부 접속 + 지속 동작)에 가장 무난 | 디스크가 영속적, 포트·방화벽을 직접 제어, 이 가이드가 그대로 적용됨 | 무료/저가 등급의 **조건·가격은 수시로 바뀌므로** 가입 전 공식 페이지에서 확인. 보안 그룹/방화벽에서 80·443 열기 필수 |
| **PaaS** (Fly.io, Railway, Render 등) | 서버 관리를 피하고 싶을 때 | Dockerfile 로 바로 배포, HTTPS 기본 제공 | **영속 볼륨**을 `/data` 에 붙여야 재배포 후에도 파일이 유지됨. 플랫폼마다 무료 플랜에서 디스크가 휘발될 수 있으니 확인 |
| **집/학교 서버 + Cloudflare Tunnel** | 남는 PC가 있고 도메인이 있을 때 | 포트포워딩 없이 외부 공개, HTTPS 제공 | 그 PC가 꺼지면 서비스도 꺼짐 → 제출 검증 기간 동안 켜 둘 것 |
| **ngrok 등 임시 터널** | 로컬 개발 확인용 | 1분 만에 외부 주소 | 주소가 바뀌고 세션이 끊김 → **최종 제출용으로는 부적합** |

도메인이 없어도 됩니다: `http://<서버IP>:8080` 으로 열어 제출할 수 있습니다(3장 C안). 다만 평문 HTTP 이므로 **HTTPS 를 권장**하고, 무료 DDNS(예: DuckDNS)나 저렴한 도메인에 A 레코드를 연결하면 Caddy 가 인증서를 자동으로 받아 줍니다.

---

## 2. 사전 준비 (공통)

```bash
# 서버에서
sudo apt update && sudo apt install -y git ufw
sudo ufw allow OpenSSH && sudo ufw allow 80,443/tcp && sudo ufw enable   # 방화벽 (클라우드 보안 그룹도 따로 열어야 함)
git clone https://github.com/<내계정>/<저장소>.git fileshare && cd fileshare
```

도메인을 쓴다면 DNS 에 `files.example.com  A  <서버 공인 IP>` 레코드를 추가하고, 전파됐는지 확인합니다.

```bash
dig +short files.example.com        # 서버 IP 가 나와야 함
```

---

## 3. 배포 방법

### A안 (권장). Docker Compose + Caddy — HTTPS 자동

```bash
# Docker 설치 (공식 스크립트)
curl -fsSL https://get.docker.com | sudo sh && sudo usermod -aG docker $USER && newgrp docker

# 기동 (DOMAIN 만 지정하면 됨)
DOMAIN=files.example.com docker compose up -d --build

docker compose ps                   # app, caddy 둘 다 running
docker compose logs -f              # Caddy 가 인증서를 받는 로그 확인
```

구성: `Caddy(80/443, HTTPS 종단)` → `app(8080, 컴포즈 내부 네트워크에서만 접근)`. 앱 포트는 밖으로 publish 하지 않으므로 `TRUST_PROXY=true` 가 안전합니다.
업로드 데이터는 네임드 볼륨 `data` 에 저장되어 컨테이너를 다시 만들어도 유지됩니다.

### B안. 바이너리 + systemd + Caddy

```bash
# 1) 빌드 (서버에 Go 가 있으면 서버에서, 없으면 로컬에서 크로스 컴파일 후 scp)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o fileshare .
#   ARM 서버(예: Oracle Ampere)라면 GOARCH=arm64

# 2) 설치
sudo useradd --system --home /var/lib/fileshare --shell /usr/sbin/nologin fileshare
sudo install -d -o fileshare -g fileshare -m 750 /opt/fileshare /var/lib/fileshare
sudo install -o root -g root -m 755 fileshare /opt/fileshare/fileshare

# 3) 서비스 등록 — deploy/fileshare.service 의 BASE_URL 을 실제 도메인으로 수정 후
sudo cp deploy/fileshare.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now fileshare
systemctl status fileshare && journalctl -u fileshare -f

# 4) Caddy 설치 후 리버스 프록시 설정
sudo apt install -y caddy
sudo tee /etc/caddy/Caddyfile >/dev/null <<'EOF'
files.example.com {
	encode zstd gzip
	request_body {
		max_size 25MB
	}
	reverse_proxy 127.0.0.1:8080
}
EOF
sudo systemctl reload caddy
```

### C안. 도메인 없이 IP 로 바로 (HTTP, 임시/학습용)

```bash
go build -o fileshare . && \
ADDR=:8080 BASE_URL=http://<서버공인IP>:8080 DATA_DIR=/var/lib/fileshare ./fileshare
# 클라우드 보안 그룹/방화벽에서 8080/tcp 를 열어야 함.  제출 링크: http://<서버공인IP>:8080
```

> 이 경우 `TRUST_PROXY` 는 켜지 마세요(프록시가 없으니 `false` 가 맞습니다). 터미널을 닫아도 유지하려면 B안의 systemd 로 등록하세요.

---

## 4. 배포 후 검증 (제출 전 필수)

1. **브라우저**로 접속해서 직접 확인 — 웹 UI 는 자동 테스트가 JS 문법·정적 서빙까지만 확인했습니다.
   - 파일 선택/드래그 업로드 → 진행률 → 링크·삭제 키 표시 → 링크 복사 → **시크릿 창에서 링크 열어 다운로드**
   - 허용되지 않는 파일(`.html`)을 올리면 빨간 오류 문구가 뜨는지
2. **같은 스크립트로 curl 검증** (내 PC 에서):

```bash
BASE=https://files.example.com ./scripts/e2e.sh
# 기대 결과: 결과: PASS=… FAIL=0   (크기 제한·디스크 검사 항목은 원격 모드에서 자동으로 건너뜀)
```

3. 응답 헤더 확인:

```bash
curl -sSI "$(curl -sS -F file=@README.md https://files.example.com/upload | head -n1)" | grep -i -E 'content-disposition|x-content-type|content-security|strict-transport'
```

4. **링크가 `https://도메인/...` 으로 나오는지** 확인 (`http://` 나 내부 주소로 나오면 `BASE_URL` 설정 문제).
5. 서버 재부팅/컨테이너 재시작 후에도 서비스가 올라오는지 (`restart: unless-stopped` / systemd `enable`).

> `scripts/e2e.sh` 의 원격 모드는 업로드를 약 18회 시도합니다. 기본 속도 제한(분당 30회) 안이지만, 연달아 두 번 돌리면 429 가 날 수 있으니 1분 간격을 두세요.

---

## 5. 운영 체크리스트

| 항목 | 확인 |
|---|---|
| `BASE_URL` 을 실제 공개 주소로 지정 (Host 헤더 조작 방지) | ☐ |
| 프록시 뒤라면 `TRUST_PROXY=true`, 프록시 없이 직접 노출이면 `false` | ☐ |
| 앱 포트(8080)는 프록시 뒤라면 외부에 열지 않음 (`ADDR=127.0.0.1:8080` 또는 컴포즈 `expose`) | ☐ |
| HTTPS 적용 (Caddy 자동 인증서) — HSTS 헤더는 HTTPS 일 때 앱이 자동으로 붙임 | ☐ |
| 비루트로 실행 (컨테이너 `65532`, systemd `fileshare` 사용자) | ☐ |
| `MAX_UPLOAD_MB` ≤ 프록시 `max_size`, `MAX_TOTAL_MB` < 디스크 여유 공간 | ☐ |
| `TTL_HOURS` 를 목적에 맞게 (과제 검증용이면 24~72) | ☐ |
| `DATA_DIR` / 볼륨이 영속 디스크인지 | ☐ |
| 서버 OS 보안 업데이트 (`unattended-upgrades`), SSH 는 키 인증 | ☐ |

### 업데이트 절차

```bash
git pull && DOMAIN=files.example.com docker compose up -d --build        # A안
# B안:  새 바이너리를 /opt/fileshare/fileshare 로 교체 → sudo systemctl restart fileshare
```

재시작해도 업로드된 파일과 만료 시각은 `DATA_DIR` 에 남아 있어 그대로 유지됩니다 (속도 제한 카운터만 초기화).

---

## 6. 문제 해결

| 증상 | 원인 | 해결 |
|---|---|---|
| Caddy 로그에 인증서 발급 실패 | DNS 가 서버 IP 를 아직 안 가리킴 / 80·443 이 막힘 | `dig` 로 DNS 확인, 클라우드 보안 그룹 + `ufw` 에서 80/443 열기 |
| 접속은 되는데 모든 사용자가 곧바로 429 | 프록시 뒤인데 `TRUST_PROXY=false` → 모든 요청이 프록시 IP 하나로 보임 | `TRUST_PROXY=true` (단, 앱 포트가 직접 노출되지 않을 때만) |
| 업로드 시 413 인데 파일이 작음 | 프록시 `max_size` 또는 `MAX_UPLOAD_MB` 가 작게 설정 | 두 값을 함께 조정 (프록시 ≥ 앱) |
| 공유 링크가 `http://…` 또는 `localhost` 로 나옴 | `BASE_URL` 미설정 | `BASE_URL=https://도메인` 지정 후 재시작 |
| 컨테이너가 `permission denied` 로 `/data` 에 쓰지 못함 | 호스트 디렉터리를 바인드 마운트하면 소유권이 달라짐 | 네임드 볼륨 사용(기본), 또는 `sudo chown -R 65532:65532 <호스트 디렉터리>` |
| 재배포하면 파일이 사라짐 | 데이터가 영속 볼륨이 아님 (PaaS 기본 디스크 등) | `/data` 에 영속 볼륨 연결 |
| 업로드는 되는데 다운로드 시 한글 파일명이 `%EB…` 로 저장 | `curl -O` 사용 | `curl -OJ` 사용 (브라우저는 영향 없음) |
| 507 | `MAX_TOTAL_MB` 도달 | 만료를 기다리거나 값을 늘림 (`df -h` 로 디스크 여유 확인) |

---

## 7. 제출 전 최종 점검

| 제출물 | 확인 |
|---|---|
| ① 외부에서 접속 가능한 웹 서비스 링크 | ☐ 모바일 데이터(Wi-Fi 끈 상태)로도 접속되는지 · ☐ `BASE=… ./scripts/e2e.sh` 가 FAIL=0 · ☐ 제출 검증 기간 동안 서버 유지 |
| ② GitHub 저장소 URL | ☐ `data/`·`.env` 가 커밋되지 않음(`.gitignore`) · ☐ README 와 `go.mod` 포함 · ☐ 저장소 공개 여부 확인 |
| ③ 블로그 링크 (노션 X) | ☐ `BLOG_OUTLINE.md` 의 구성·증거 자료 포함 · ☐ 링크가 로그인 없이 열림 |
