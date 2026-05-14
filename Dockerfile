# --- 阶段零：根据架构动态下载 go2rtc ---
FROM alpine:latest AS go2rtc-downloader

ARG TARGETARCH
ARG VERSION="dev"
ARG Go2rtcVersion="v1.9.14"

RUN apk add --no-cache wget

RUN echo "正在为 linux_${TARGETARCH} 下载 go2rtc..." && \
    wget -O /go2rtc https://github.com/AlexxIT/go2rtc/releases/download/${Go2rtcVersion}/go2rtc_linux_${TARGETARCH} && \
    chmod +x /go2rtc

# --- 阶段一：编译 CamKeep ---
# 不使用 --platform=$BUILDPLATFORM，直接在目标平台上编译，确保 CGO gcc 架构匹配
FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25.4 AS builder

ARG VERSION=dev

ENV GO111MODULE=on \
    GOPROXY=https://goproxy.cn,direct

WORKDIR /app
COPY . .

RUN apt-get update && \
    apt-get install -y --no-install-recommends gcc libc6-dev libopencv-dev pkg-config && \
    CGO_ENABLED=1 go build -ldflags="-s -w -X main.Version=${VERSION}" -o camkeep main.go

# --- 阶段二：构建最终运行环境 ---
FROM golang:1.25.4

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ffmpeg \
      libopencv-core-dev \
      libopencv-imgproc-dev \
      libopencv-imgcodecs-dev \
      libopencv-videoio-dev && \
    rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai
WORKDIR /app

COPY --from=builder /app/camkeep .
COPY --from=builder /app/static ./static
COPY --from=builder /app/template ./template
COPY --from=go2rtc-downloader /go2rtc ./go2rtc

EXPOSE 9110 1984 8554 8555
CMD ["./camkeep"]
