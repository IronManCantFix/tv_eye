# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

CamKeep is a lightweight private NVR (Network Video Recorder) for home NAS devices, written in Go. It integrates **go2rtc** (stream media engine) and **FFmpeg** to provide RTSP camera recording, live streaming (WebRTC/MSE/HLS), and web-based management UI.

## Build & Run

```bash
# Build locally (requires Go 1.25.4+)
go build -ldflags="-s -w -X main.Version=dev" -o camkeep main.go

# Run (expects ./go2rtc binary and ffmpeg/ffprobe on PATH)
./camkeep

# Docker build (multi-arch, downloads go2rtc automatically)
docker build --build-arg VERSION=dev -t camkeep .

# Docker run
docker run -d --name camkeep --network host -e TZ=Asia/Shanghai \
  -v ./config:/app/config -v ./records:/app/records camkeep:latest
```

No test suite exists in this project. There are no linting tools configured.

## Release

Pushing a `v*` tag triggers GitHub Actions (`.github/workflows/release.yml`) which:
1. Builds binaries for linux/amd64 and linux/arm64, bundles with go2rtc, uploads to GitHub Releases
2. Builds multi-arch Docker images and pushes to both Docker Hub (`r0n9/camkeep`) and GHCR

## Architecture

**Entry point:** `main.go` → `internal/app/app.go:Run()`

**Startup sequence:**
1. Start go2rtc daemon as child process (`task.StartGo2rtcDaemon`)
2. Poll until go2rtc API is ready (max 10s)
3. Load/initialize `config/conf.yaml`
4. Load manual recording overrides from `config/overrides.json`
5. Register all camera RTSP streams into go2rtc via HTTP API
6. Start web server (Gin on port 9110)
7. Start per-camera recording goroutines, cleanup task, and daily merge task
8. Block on SIGINT/SIGTERM for graceful shutdown

**Key packages:**

| Package | Responsibility |
|---------|---------------|
| `internal/app/` | Application lifecycle, web routes, config loading, task orchestration |
| `internal/task/` | Camera recording (FFmpeg), go2rtc integration, file cleanup, daily merge |
| `internal/service/` | In-memory camera status tracking (`StatusMap` with `sync.RWMutex`) |
| `constant/` | Config structs (`Camera`, `Config`, `DailyMergeConfig`), file paths, global `ConfigMux` |
| `util/` | Time range check, RTSP URL auth escaping |
| `slog/` | Rotating log writer with gzip compression |

**Recording modes:**
- `normal`: Stream copy (zero re-encode) via FFmpeg segment muxer
- `timelapse`: Frame capture at intervals, re-encoded to H.264

**Hot reload flow** (`restartTasks` in `task_lifecycle.go`):
1. Cancel old context → WaitGroup blocks until all FFmpeg processes exit
2. Unregister old go2rtc streams, register new ones
3. Swap `currentConfig` under `ConfigMux` write lock
4. Clean ghost camera statuses from `StatusMap`
5. Restart all tasks with new context

**go2rtc integration:** CamKeep manages go2rtc via its REST API (`/api/streams`). The web server reverse-proxies go2rtc's WebRTC/streaming endpoints so everything is accessible on port 9110.

**Concurrency model:** All state shared across goroutines uses explicit mutexes:
- `constant.ConfigMux` (RWMutex) — protects `currentConfig`
- `service.StatusMux` (RWMutex) — protects `service.StatusMap`
- `overrideMux` (RWMutex in `task/camera.go`) — protects manual recording overrides
- `restartMux` (Mutex in `app.go`) — serializes hot restart operations

## Configuration

- Config file: `config/conf.yaml` (YAML, auto-created with defaults if missing)
- Overrides file: `config/overrides.json` (persists manual start/stop commands across restarts)
- Recording output: `./records/{camera_id}/{date}/{id}_{timestamp}.{format}`
- Logs: `./log/camkeep.YYYYMMDD.log` (rotated daily, gzipped after 7 days, deleted after 30)
