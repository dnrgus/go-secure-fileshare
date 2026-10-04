# syntax=docker/dockerfile:1

# ---- 1단계: 정적 바이너리 빌드 ----
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fileshare . \
 && mkdir -p /out/data/tmp

# ---- 2단계: 쉘/패키지 매니저가 없는 최소 이미지 + 비루트 사용자 ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fileshare /fileshare
# 네임드 볼륨은 비어 있으면 이 디렉터리의 소유권(65532)을 그대로 물려받는다.
COPY --from=build --chown=65532:65532 /out/data /data

ENV ADDR=:8080 \
    DATA_DIR=/data
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/fileshare"]
