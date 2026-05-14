# =========================================================
# 1. builder（完整 CGO + OpenCV）
# =========================================================
FROM --platform=linux/amd64 swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25.4 AS builder

ARG VERSION=dev

ENV GO111MODULE=on \
    GOPROXY=https://goproxy.cn,direct \
    CGO_ENABLED=1

WORKDIR /app

COPY . .

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      gcc \
      g++ \
      libc6-dev \
      pkg-config \
      libopencv-dev \
      ffmpeg && \
    rm -rf /var/lib/apt/lists/* && \
    go build \
      -ldflags="-s -w -X main.Version=${VERSION}" \
      -o camkeep main.go


# =========================================================
# 2. runtime（最小 Debian）
# =========================================================
FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/debian:bookworm-slim AS runtime

ENV TZ=Asia/Shanghai DEBIAN_FRONTEND=noninteractive

WORKDIR /app

# runtime 只装运行库（不是 -dev）
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ffmpeg \
      libopencv-core406 \
      libopencv-imgproc406 \
      libopencv-imgcodecs406 \
      libopencv-videoio406 \
      ca-certificates \
      tzdata \
      curl && \
    rm -rf /var/lib/apt/lists/*


# =========================================================
# 3. go2rtc
# =========================================================
ARG TARGETARCH
ARG Go2rtcVersion="v1.9.14"

RUN case "${TARGETARCH}" in \
      amd64) arch="x86_64" ;; \
      arm64) arch="arm64" ;; \
      *) arch="x86_64" ;; \
    esac && \
    curl -fSL --retry 3 \
      "https://github.com/AlexxIT/go2rtc/releases/download/${Go2rtcVersion}/go2rtc_linux_${arch}" \
      -o /app/go2rtc && \
    chmod +x /app/go2rtc


# =========================================================
# 4. 运行文件
# =========================================================
COPY --from=builder /app/camkeep /app/camkeep
COPY --from=builder /app/static /app/static
COPY --from=builder /app/template /app/template

EXPOSE 9110 1984 8554 8555

CMD ["/app/camkeep"]