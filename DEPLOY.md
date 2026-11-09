# 배포 가이드: 외부에서 접속 가능한 서버 만들기

이 문서는 서비스를 인터넷에 공개하는 방법을 설명합니다. 서버 계정과 도메인은 본인 것을 써야 합니다. 명령은 Ubuntu 22.04와 24.04 기준입니다.

> `Dockerfile`, `docker-compose.yml`, `deploy/Caddyfile`은 Docker Desktop(Windows)에서 `DOMAIN=localhost`로 실제로 빌드하고 띄워 봤습니다. HTTPS 업로드와 다운로드, HTTP에서 HTTPS로의 리다이렉트, Caddy의 크기 제한, 원격 모드 e2e 통과까지 확인했습니다 (README 8장).
> 실제 도메인으로 Let's Encrypt 인증서를 받는 과정과 `deploy/fileshare.service`(systemd)는 아직 검증하지 못했습니다. 문제가 생기면 6장의 문제 해결 표를 먼저 보세요.
>
> Docker Desktop에서는 앱 로그의 클라이언트 IP가 Docker 게이트웨이 주소(`172.x.0.1`)로 찍힙니다. 실제 Linux 서버에 배포한 뒤에는 접속자 IP가 찍히는지 로그로 한 번 확인하세요.

---

## 1. 어디에 배포할까

| 선택지 | 적합한 경우 | 장점 | 주의 |
|---|---|---|---|
| VPS 또는 클라우드 VM (Oracle Cloud, AWS Lightsail나 EC2, GCP, Vultr 등) | 가장 일반적인 선택 | 디스크가 영속적이고, 포트와 방화벽을 직접 다룰 수 있습니다. 이 가이드를 그대로 적용할 수 있습니다. | 무료나 저가 등급의 조건과 가격은 자주 바뀌니 가입 전에 공식 페이지에서 확인하세요. 보안 그룹이나 방화벽에서 80, 443 포트를 열어야 합니다. |
| PaaS (Fly.io, Railway, Render 등) | 서버 관리를 피하고 싶을 때 | Dockerfile로 바로 배포할 수 있고, HTTPS도 기본으로 제공합니다. | 파일이 재배포 후에도 남으려면 영속 볼륨을 `/data`에 붙여야 합니다. 무료 플랜에서는 디스크가 사라질 수 있으니 확인하세요. |
| 집이나 학교 서버 + Cloudflare Tunnel | 남는 PC가 있고 도메인이 있을 때 | 포트 포워딩 없이 외부에 공개할 수 있고, HTTPS도 제공합니다. | PC가 꺼지면 서비스도 꺼집니다. 확인 기간 동안 켜 두세요. |
| ngrok 같은 임시 터널 | 로컬에서 잠깐 확인할 때 | 1분이면 외부 주소가 생깁니다. | 주소가 바뀌고 세션이 끊깁니다. 최종 제출용으로는 맞지 않습니다. |

도메인이 없어도 됩니다. `http://<서버IP>:8080`으로 열면 됩니다(3장 C안). 다만 HTTP는 평문이라서 HTTPS를 권장합니다. 무료 DDNS(예: DuckDNS)나 저렴한 도메인에 A 레코드를 연결하면 Caddy가 인증서를 자동으로 받습니다.

---

## 2. 공통 준비

```bash
# 서버에서
sudo apt update && sudo apt install -y git ufw
sudo ufw allow OpenSSH && sudo ufw allow 80,443/tcp && sudo ufw enable   # 방화벽 (클라우드 보안 그룹도 따로 열어야 함)
git clone https://github.com/<계정>/<저장소>.git fileshare && cd fileshare
```

도메인을 쓴다면 DNS에 `files.example.com  A  <서버 공인 IP>` 레코드를 추가하고, 전파됐는지 확인합니다.

```bash
dig +short files.example.com        # 서버 IP가 나와야 함
```

---

## 3. 배포 방법

### A안 (권장): Docker Compose + Caddy, HTTPS 자동

```bash
# Docker 설치 (공식 스크립트)
curl -fsSL https://get.docker.com | sudo sh && sudo usermod -aG docker $USER && newgrp docker

# 기동 (DOMAIN만 지정하면 됩니다)
DOMAIN=files.example.com docker compose up -d --build

docker compose ps                   # app, caddy 둘 다 running이어야 함
docker compose logs -f              # Caddy가 인증서를 받는 로그 확인
```

구성은 `Caddy(80, 443, HTTPS 종단)`에서 `app(8080, 컴포즈 내부 네트워크에서만 접근)`으로 요청을 넘기는 형태입니다. 앱 포트는 밖으로 열지 않으므로 `TRUST_PROXY=true`가 안전합니다.
업로드 데이터는 named volume `data`에 저장되어, 컨테이너를 다시 만들어도 남아 있습니다.

### B안: 바이너리 + systemd + Caddy

```bash
# 1) 빌드. 서버에 Go가 있으면 서버에서, 없으면 로컬에서 크로스 컴파일한 뒤 scp로 옮깁니다.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o fileshare .
#   ARM 서버(예: Oracle Ampere)라면 GOARCH=arm64

# 2) 설치
sudo useradd --system --home /var/lib/fileshare --shell /usr/sbin/nologin fileshare
sudo install -d -o fileshare -g fileshare -m 750 /opt/fileshare /var/lib/fileshare
sudo install -o root -g root -m 755 fileshare /opt/fileshare/fileshare

# 3) 서비스 등록. deploy/fileshare.service의 BASE_URL을 실제 도메인으로 고친 뒤 실행하세요.
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

### C안: 도메인 없이 IP로 바로 (HTTP, 임시 또는 학습용)

```bash
go build -o fileshare . && \
ADDR=:8080 BASE_URL=http://<서버공인IP>:8080 DATA_DIR=/var/lib/fileshare ./fileshare
# 클라우드 보안 그룹이나 방화벽에서 8080/tcp를 열어야 합니다. 접속 주소: http://<서버공인IP>:8080
```

이 경우에는 `TRUST_PROXY`를 켜지 마세요. 프록시가 없으니 `false`가 맞습니다. 터미널을 닫아도 계속 돌리려면 B안처럼 systemd로 등록하세요.

---

## 4. 배포 후 검증 (공개 전 필수)

1. 브라우저로 접속해서 직접 확인합니다. 웹 UI는 자동 테스트가 JS 문법과 정적 파일 제공까지만 확인하므로 직접 눌러 보는 과정이 필요합니다.
   - 파일 선택이나 드래그로 업로드 → 진행률 → 링크와 삭제 키 표시 → 링크 복사 → 시크릿 창에서 링크를 열어 다운로드
   - 허용되지 않는 파일(`.html`)을 올렸을 때 빨간 오류 문구가 뜨는지
2. 내 PC에서 같은 스크립트로 curl 검증을 돌립니다.

```bash
BASE=https://files.example.com ./scripts/e2e.sh
# 기대 결과: 결과: PASS=… FAIL=0   (크기 제한과 디스크 검사 항목은 원격 모드에서 자동으로 건너뜁니다)
```

3. 응답 헤더를 확인합니다.

```bash
curl -sSI "$(curl -sS -F file=@README.md https://files.example.com/upload | head -n1)" | grep -i -E 'content-disposition|x-content-type|content-security|strict-transport'
```

4. 링크가 `https://도메인/...` 형태로 나오는지 확인합니다. `http://`나 내부 주소로 나오면 `BASE_URL` 설정을 다시 보세요.
5. 서버를 재부팅하거나 컨테이너를 재시작한 뒤에도 서비스가 올라오는지 확인합니다 (`restart: unless-stopped`, systemd `enable`).

> 원격 모드 e2e는 업로드를 약 18회 시도합니다. 기본 속도 제한(분당 30회) 안이지만, 연달아 두 번 돌리면 429가 날 수 있으니 1분 간격을 두세요.

---

## 5. 운영 체크리스트

| 항목 | 확인 |
|---|---|
| `BASE_URL`을 실제 공개 주소로 지정 (Host 헤더 조작 방지) | ☐ |
| 프록시 뒤라면 `TRUST_PROXY=true`, 프록시 없이 직접 노출이면 `false` | ☐ |
| 앱 포트(8080)는 프록시 뒤라면 외부에 열지 않음 (`ADDR=127.0.0.1:8080` 또는 컴포즈 `expose`) | ☐ |
| HTTPS 적용 (Caddy가 인증서 자동 발급). HSTS 헤더는 HTTPS일 때 앱이 자동으로 붙입니다. | ☐ |
| 비루트로 실행 (컨테이너는 `65532`, systemd는 `fileshare` 사용자) | ☐ |
| `MAX_UPLOAD_MB`가 프록시 `max_size` 이하, `MAX_TOTAL_MB`가 디스크 여유 공간보다 작음 | ☐ |
| `TTL_HOURS`를 목적에 맞게 설정 (검증용이면 24~72) | ☐ |
| `DATA_DIR`과 볼륨이 영속 디스크인지 | ☐ |
| 서버 OS 보안 업데이트(`unattended-upgrades`) 설정, SSH는 키 인증 사용 | ☐ |

### 업데이트 절차

```bash
git pull && DOMAIN=files.example.com docker compose up -d --build        # A안
# B안: 새 바이너리를 /opt/fileshare/fileshare로 교체한 뒤 sudo systemctl restart fileshare
```

재시작해도 업로드된 파일과 만료 시각은 `DATA_DIR`에 남아 그대로 유지됩니다. 속도 제한 카운터만 초기화됩니다.

---

## 6. 문제 해결

| 증상 | 원인 | 해결 |
|---|---|---|
| Caddy 로그에 인증서 발급 실패 | DNS가 아직 서버 IP를 가리키지 않음, 또는 80, 443이 막힘 | `dig`로 DNS를 확인하고, 클라우드 보안 그룹과 `ufw`에서 80, 443을 여세요. |
| 접속은 되는데 모든 사용자가 바로 429를 받음 | 프록시 뒤인데 `TRUST_PROXY=false`라서 모든 요청이 프록시 IP 하나로 보임 | `TRUST_PROXY=true`로 설정하세요 (앱 포트가 직접 노출되지 않을 때만). |
| 파일이 작은데 413이 나옴 | 프록시 `max_size`나 `MAX_UPLOAD_MB`가 작게 설정됨 | 두 값을 함께 맞추세요 (프록시 값이 앱 값 이상이어야 함). |
| 공유 링크가 `http://…`나 `localhost`로 나옴 | `BASE_URL`을 설정하지 않음 | `BASE_URL=https://도메인`을 지정하고 재시작하세요. |
| 컨테이너가 `permission denied`로 `/data`에 쓰지 못함 | 호스트 디렉터리를 바인드 마운트하면 소유자가 달라짐 | named volume을 쓰세요(기본값). 또는 `sudo chown -R 65532:65532 <호스트 디렉터리>`를 실행하세요. |
| 재배포하면 파일이 사라짐 | 데이터가 영속 볼륨이 아님 (PaaS 기본 디스크 등) | `/data`에 영속 볼륨을 연결하세요. |
| 업로드는 되는데 다운로드한 한글 파일명이 `%EB…`로 저장됨 | `curl -O`를 사용함 | `curl -OJ`를 사용하세요. 브라우저는 영향이 없습니다. |
| 507 | `MAX_TOTAL_MB` 한도에 도달 | 만료되기를 기다리거나 값을 늘리세요 (`df -h`로 디스크 여유를 확인). |

---

## 7. 제출 전 최종 점검

| 제출물 | 확인 |
|---|---|
| ① 외부에서 접속 가능한 웹 서비스 링크 | ☐ 모바일 데이터(Wi-Fi를 끈 상태)로도 접속되는지 · ☐ `BASE=… ./scripts/e2e.sh`가 FAIL=0인지 · ☐ 검증 기간 동안 서버를 유지하는지 |
| ② GitHub 저장소 URL | ☐ `data/`와 `.env`가 커밋되지 않았는지(`.gitignore`) · ☐ README와 `go.mod`가 포함되어 있는지 · ☐ 저장소 공개 여부 확인 |
| ③ 블로그 링크 | ☐ 주요 코드, 테스트 결과, 배포 검증 화면이 포함되어 있는지 · ☐ 로그인 없이 링크가 열리는지 |
