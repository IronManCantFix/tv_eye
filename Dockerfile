# =========================================================
# 1. Builder（编译阶段）
# =========================================================
FROM --platform=linux/amd64 swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25.4 AS builder

ARG VERSION=dev

ENV GO111MODULE=on \
    GOPROXY=https://goproxy.cn,direct \
    CGO_ENABLED=1

WORKDIR /app

# ---------------------------------------------------------
# 先复制 go mod（利用 Docker 缓存）
# ---------------------------------------------------------
COPY go.mod go.sum ./

RUN go mod download

# ---------------------------------------------------------
# 再复制源码
# ---------------------------------------------------------
COPY . .

# ---------------------------------------------------------
# 使用国内 apt 源（加速）
# ---------------------------------------------------------
RUN sed -i 's/deb.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list.d/debian.sources

# ---------------------------------------------------------
# 安装最小 OpenCV 编译依赖
# 不使用 libopencv-dev 全家桶
# ---------------------------------------------------------
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      gcc \
      g++ \
      libc6-dev \
      pkg-config \
      ffmpeg \
      libopencv-dev && \
    rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------
# 编译
# ---------------------------------------------------------
RUN go build \
      -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION}" \
      -o camkeep main.go

# ---------------------------------------------------------
# 分析二进制运行时依赖的 OpenCV 动态库
# ---------------------------------------------------------
RUN ldd camkeep | grep opencv | awk '{print $1}' | sort > /tmp/opencv_deps.txt && \
    cat /tmp/opencv_deps.txt


# =========================================================
# 2. Runtime（运行阶段）
# =========================================================
FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/debian:trixie-slim AS runtime

ENV TZ=Asia/Shanghai \
    DEBIAN_FRONTEND=noninteractive

WORKDIR /app

# ---------------------------------------------------------
# 国内 apt 源
# ---------------------------------------------------------
RUN sed -i 's/deb.debian.org/mirrors.aliyun.com/g' /etc/apt/sources.list.d/debian.sources

# ---------------------------------------------------------
# 安装运行时依赖：根据 builder 阶段 ldd 结果安装所有 OpenCV 运行时库
# ---------------------------------------------------------
COPY --from=builder /tmp/opencv_deps.txt /tmp/opencv_deps.txt
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ffmpeg \
      ca-certificates \
      tzdata && \
    apt-cache search '^libopencv-.*410$' | awk '{print $1}' | \
      xargs apt-get install -y --no-install-recommends && \
    rm -rf /var/lib/apt/lists/*

# ---------------------------------------------------------
# 复制 go2rtc（本地二进制）
# ---------------------------------------------------------
COPY third_party/go2rtc_linux_amd64 /app/go2rtc

RUN chmod +x /app/go2rtc

# ---------------------------------------------------------
# 复制程序
# ---------------------------------------------------------
COPY --from=builder /app/camkeep /app/camkeep
COPY --from=builder /app/static /app/static
COPY --from=builder /app/template /app/template

# ---------------------------------------------------------
# 端口
# ---------------------------------------------------------
EXPOSE 9110 1984 8554 8555

# ---------------------------------------------------------
# 启动
# ---------------------------------------------------------
CMD ["/app/camkeep"]