
# tv_eye

[简体中文](./README.md) | [English](./README_en.md)

---

## Why This Project Exists

The first program I wrote for my kid -- TV Sentinel.

Every time the parental lock kicked in, the kid would run to grandma and grandpa to get it unlocked, then keep watching non-stop.

So I did some vibe coding: hooked up the home surveillance camera to keep an eye on the TV in real time. Once continuous playback exceeds the limit, the TV shuts off automatically and the smart speaker announces it. If someone turns the TV back on during the cooldown period, it gets warned and shut off again.

---

## What Is This

TV Sentinel is a home TV supervision tool built in Go, deeply integrating **go2rtc** and **FFmpeg**. It leverages your existing RTSP cameras to detect TV screen state in real time through computer vision, and combines with Home Assistant for automatic shutdown and voice alerts -- designed specifically for parents who need to manage their children's screen time.

It is also a lightweight self-hosted NVR (Network Video Recorder), suitable for deployment on home NAS (FnOS, Synology, QNAP, Unraid, etc.) and low-power micro-servers.

## TV Sentinel Feature Deep Dive

The core capability of TV Sentinel is **using a camera to "watch over" the TV**: analyzing images to determine whether the TV is playing, then automatically executing shutdown and alerts based on time rules.

### How It Works

```
Camera RTSP Stream
    │
    ▼
┌─────────────┐    ROI Perspective     ┌──────────────┐
│  Frame       │ ──── Correction ────▶ │  TV State     │
│  Capture     │                       │  Detection    │
│  (go2rtc)   │                       │  (HSV+Edge+   │
│             │                       │   FrameDiff)  │
└─────────────┘                       └──────┬───────┘
                                           │ rawOn/Off
                                           ▼
                                    ┌──────────────┐
                                    │ State Machine │
                                    │ OFF→PENDING   │
                                    │ →TRIGGERED    │
                                    │ →RESTING      │
                                    └──────┬───────┘
                                           │ Timeout/Violation
                                           ▼
                              ┌──────────────────────────┐
                              │  Home Assistant Actions   │
                              │  1. Smart Speaker Alert   │
                              │  2. IR Remote Shutdown    │
                              │  3. WeChat Notification   │
                              └──────────────────────────┘
```

### Detection Mechanism

- **ROI Perspective Correction**: Select the TV area in the camera frame by configuring four corner coordinates manually, or enable auto-calibration (detects the largest rectangular contour in the frame)
- **Multi-dimensional Image Analysis**: Combines HSV brightness (V), saturation (S), Laplacian edge detection, and inter-frame difference (Frame Diff) to determine whether the TV is playing
- **Baseline Auto-calibration**: Collects baseline data from multiple frames while the TV is off, then adaptively adjusts detection thresholds to accommodate different ambient lighting conditions
- **Debounce State Machine**: Requires consecutive multi-frame confirmation before switching states, preventing flickering false positives

### Time Control Rules

| Rule | Default | Description |
|------|---------|-------------|
| Single Session Limit | 5 min | Auto shutdown after continuous playback exceeds limit |
| Cooldown Rest Period | 20 min | Mandatory rest after shutdown; turning on TV during this period triggers another shutdown |
| Daily Total Limit | 60 min | Locked until midnight after exceeding |
| Monitor Time Range | 08:00-23:00 | Only active during specified time window |

### Home Assistant Integration

Three-layer enforcement via Home Assistant API:

1. **Smart Speaker Voice Alert** -- Plays a reminder message through Xiaomi AI Speaker or similar devices when time is up
2. **IR Remote Shutdown** -- Simulates remote control power-off via IR transmitter (e.g., Broadlink)
3. **WeChat Notification** -- Pushes a WeChat message to the parent every time a shutdown action is executed

### Web Monitoring Dashboard

- Real-time TV state display (ON/OFF), current session duration, daily cumulative time
- Countdown showing remaining time until auto-shutdown
- Cooldown remaining time updates in real time
- Operation log recording all events (power on, shutdown, timeout, violation, etc.)
- Live preview of TV screen ROI area

---

## NVR Recording Features

In addition to TV Sentinel, CamKeep is also a full-featured NVR recording system:

* **Simple single-container deployment**: Bundles go2rtc and FFmpeg, just start with Docker; Web console supports hot-reloading configuration.
* **Private local-network operation**: No cloud dependency, no forced accounts. Streams and recordings stay on your LAN and NAS.
* **Works with any RTSP source**: Compatible with Hikvision, Dahua, TP-Link, flashed cameras, and other RTSP video sources.
* **Multiple recording modes**: Scheduled recording, manual start/stop, motion recording, timelapse, TS/MP4 segments, date-based playback.
* **Automatic retention management**: Control retention days via `retention_days`, with background cleanup of expired recordings.
* **Low-latency WebRTC live view**: Supports 4/6-grid preview, double-click fullscreen, device status, and date-based playback.
* **Built for NAS and edge devices**: Native x86-64 and ARM64 support for Synology, QNAP, Unraid, FnOS, Raspberry Pi, etc.

---

## Quick Deployment

### 1. Prepare Configuration

Create a config directory on your NAS (e.g., `/vol1/CamKeep`), and create `config/conf.yaml`:

For detailed configuration options, see: [Configuration Usage (conf_usage.md)](https://github.com/r0n9/camkeep/blob/main/conf_usage.md)

```yaml
daily_merge:
  enabled: false          # Merge yesterday's video segments daily
  time: "03:30"           # Merge time

cameras:
  - id: "living-room"     # Unique camera ID
    rtsp_url: "rtsp://admin:123456@192.168.1.100:554/stream"
    retention_days: 7
    segment_duration: 300
    format: "ts"
    record_time: "00:00-24:00"
    mode: "normal"
    motion_detect: false

tv_monitors:
  - camera_id: "living-room"          # Matches the camera ID above
    enabled: true
    monitor_time: "08:00-23:00"       # Monitor time range
    target_duration: 300              # Single session limit (seconds)
    max_session_minutes: 5            # Single session limit (minutes)
    rest_minutes: 20                  # Cooldown rest period (minutes)
    max_daily_minutes: 60             # Daily total limit (minutes)
    roi_auto_calibrate: true          # Auto-detect TV area
    ha_url: "http://192.168.1.200:8123"  # Home Assistant URL
    ha_token: "Your Long-Lived Access Token"
    ha_tts_service: "notify.xiaomi_cn_xxx"  # Xiaomi Speaker
    ha_tts_message: "TV time is up, take a break!"
    ha_ir_turn_off_button: "button.tv_remote_power"  # IR power-off button
    ha_notify_service: "hassbox_notify.hassbox_notify"  # WeChat notification
```

### 2. Start Service

**Docker Run (Recommended):**

```bash
docker run -d \
  --name camkeep \
  --restart unless-stopped \
  --network host \
  -e TZ=Asia/Shanghai \
  -v ${PWD}/config:/app/config \
  -v ${PWD}/records:/app/records \
  r0n9/camkeep:latest
```

**Docker-Compose:**

```yaml
services:
  camkeep:
    image: r0n9/camkeep:latest
    container_name: camkeep
    restart: unless-stopped
    network_mode: "host"
    environment:
      - TZ=Asia/Shanghai
    volumes:
      - ./config:/app/config
      - ./records:/app/records
```

### 3. Get Started

Access `http://<Your-NAS-IP>:9110` in your browser to enter the monitoring center.

---

## Acknowledgements

Thanks to [r0n9](https://github.com/r0n9) for the original [CamKeep](https://github.com/r0n9/camkeep) project. TV Sentinel is built on top of it.

## License

This project is licensed under the **MIT License**. Issues and PRs are welcome.

This project uses:

- go2rtc -- https://github.com/AlexxIT/go2rtc
  Licensed under the MIT License.
