# TV Monitor Feature Design

## Summary

Add a built-in TV watching time monitor to CamKeep. Use gocv to capture frames from RTSP streams, analyze the ROI region (TV screen) via brightness/frame-diff pixel analysis, track continuous and daily viewing time, and trigger Home Assistant actions (TTS + turn off TV) when thresholds are exceeded.

## Requirements

- Detect whether the TV screen is on (has content) using ROI pixel analysis
- Support auto-calibration of the TV screen ROI region (Canny edge detection for largest rectangle)
- Fallback to manual ROI configuration if auto-calibration fails
- Track two metrics per monitor: single-session continuous time and daily cumulative time
- Only monitor within a configurable time range (e.g., 08:00-23:00)
- Trigger HA REST API actions when either threshold is exceeded
- Support enable/disable per monitor in config
- Follow existing hot-restart lifecycle (shared context, WaitGroup)

## Configuration

New `tv_monitors` section in `conf.yaml`:

```yaml
tv_monitors:
  - camera_id: "摄像头1"
    enabled: true
    monitor_time: "08:00-23:00"
    roi_x: 0.2            # ROI left (percentage 0.0-1.0)
    roi_y: 0.1            # ROI top
    roi_w: 0.6            # ROI width
    roi_h: 0.5            # ROI height
    roi_auto_calibrate: true  # auto-detect TV region on startup
    check_interval: 30        # frame capture interval (seconds)
    brightness_threshold: 30  # mean brightness below this = TV off
    frame_diff_threshold: 5.0 # std dev diff between frames below this = static screen
    max_session_minutes: 45   # max continuous viewing time
    max_daily_minutes: 120    # max daily total viewing time
    cooldown_minutes: 30      # cooldown after triggering before re-triggering
    ha_url: "http://homeassistant.local:8123"
    ha_token: "eyJhbGci..."
    ha_service: "media_player.turn_off"
    ha_entity_id: "media_player.xiao_ai"
    ha_message: "看电视时间太长了，休息一下眼睛吧"  # optional TTS message
```

## Detection Algorithm

### Auto-Calibration

On first startup (when ROI config is zero/auto), capture a frame and:
1. Convert to grayscale
2. Apply GaussianBlur + Canny edge detection
3. FindContours, filter for rectangles
4. Select the largest rectangle as the TV screen ROI
5. Save detected ROI to config (persists across restarts)

If calibration fails (TV off, no clear rectangle), log a warning and require manual config.

### TV On/Off Detection (per frame)

1. Capture frame from RTSP stream via gocv
2. Crop ROI region
3. Convert to grayscale
4. Compute mean brightness (`gocv.Mean().Val1`)
5. Compute frame-diff: subtract previous frame's grayscale from current, calculate std dev of the difference
6. Decision:
   - **TV ON**: `meanBrightness > brightnessThreshold` AND `frameDiffStdDev > frameDiffThreshold`
   - **TV OFF**: otherwise
   - First frame has no previous frame → use brightness only

## State Machine

Each monitor has an independent state machine:

```
States: OFF → ON → TRIGGERED → COOLDOWN → OFF
```

- **OFF**: TV detected as off. Reset session timer.
- **ON**: TV detected as on. Accumulate session time and daily time.
- **TRIGGERED**: Threshold exceeded. Call HA API once. Record trigger timestamp.
- **COOLDOWN**: Post-trigger cooldown period. Continue tracking but don't re-trigger.

### Time Tracking

- `sessionStart time.Time` — when current ON session began
- `sessionMinutes float64` — accumulated continuous ON minutes
- `dailyMinutes float64` — accumulated total ON minutes today
- `lastDate string` — for daily reset detection (reset dailyMinutes when date changes)
- `lastTrigger time.Time` — last HA trigger timestamp

Every `check_interval` seconds, if state is ON:
- `dailyMinutes += checkInterval / 60.0`
- `sessionMinutes = time.Since(sessionStart).Minutes()`

Trigger when `sessionMinutes >= maxSessionMinutes` OR `dailyMinutes >= maxDailyMinutes`.

## Home Assistant Integration

### Turn Off TV

```
POST {ha_url}/api/services/{domain}/{service}
Authorization: Bearer {ha_token}
Content-Type: application/json

{"entity_id": "ha_entity_id"}
```

Where `ha_service` like `media_player.turn_off` is split into domain=`media_player` and service=`turn_off`.

### TTS Announcement (if ha_message is set)

```
POST {ha_url}/api/services/tts/google_translate_say
Authorization: Bearer {ha_token}
Content-Type: application/json

{"entity_id": "ha_entity_id", "message": "ha_message value"}
```

TTS is called first, then wait 5 seconds before calling the turn-off action.

## Code Structure

```
internal/tvmonitor/
  monitor.go       # TVMonitor struct, state machine, main loop
  detector.go      # ROI auto-calibration + brightness/frame-diff detection
  ha_client.go     # HA REST API client
  config.go        # TVMonitorConfig struct definition
constant/
  config.go        # Add TVMonitors []TVMonitorConfig to Config struct
```

### Key Types

```go
// internal/tvmonitor/config.go
type TVMonitorConfig struct {
    CameraID             string  `yaml:"camera_id"`
    Enabled              bool    `yaml:"enabled"`
    MonitorTime          string  `yaml:"monitor_time"`
    ROIX                 float64 `yaml:"roi_x"`
    ROIY                 float64 `yaml:"roi_y"`
    ROIW                 float64 `yaml:"roi_w"`
    ROIH                 float64 `yaml:"roi_h"`
    ROIAutoCalibrate     bool    `yaml:"roi_auto_calibrate"`
    CheckInterval        int     `yaml:"check_interval"`
    BrightnessThreshold  float64 `yaml:"brightness_threshold"`
    FrameDiffThreshold   float64 `yaml:"frame_diff_threshold"`
    MaxSessionMinutes    float64 `yaml:"max_session_minutes"`
    MaxDailyMinutes      float64 `yaml:"max_daily_minutes"`
    CooldownMinutes      float64 `yaml:"cooldown_minutes"`
    HAURL                string  `yaml:"ha_url"`
    HAToken              string  `yaml:"ha_token"`
    HAService            string  `yaml:"ha_service"`
    HAEntityID           string  `yaml:"ha_entity_id"`
    HAMessage            string  `yaml:"ha_message"`
}
```

```go
// internal/tvmonitor/monitor.go
type TVMonitor struct {
    config     TVMonitorConfig
    rtspURL    string
    state      MonitorState  // OFF, ON, TRIGGERED, COOLDOWN
    sessionStart time.Time
    sessionMinutes float64
    dailyMinutes   float64
    lastDate       string
    lastTrigger    time.Time
    prevGray       gocv.Mat
    mu             sync.Mutex
}
```

## Lifecycle Integration

- Started in `app.go:Run()` after recording tasks start
- Each enabled monitor runs as a goroutine registered with `taskWg`
- Obeys global `ctxGlobal` cancellation for graceful shutdown
- Hot-restart: `restartTasks` cancels old context, TVMonitor goroutines exit, new ones start with updated config

## Dependencies

- `gocv.io/x/gocv` — requires OpenCV 4.x C library
- Docker: add `libopencv-dev` to build stage, `libopencv-core` + `libopencv-imgproc` to runtime stage

## Logging

All detection events, state transitions, and HA API calls are logged via the standard `log` package (routed to rotating log files via `slog`).

Log examples:
```
[tvmonitor:摄像头1] TV detected ON (brightness=128.5, diff=15.2)
[tvmonitor:摄像头1] Session time: 45.5min exceeded threshold 45min, triggering HA action
[tvmonitor:摄像头1] TTS sent: "看电视时间太长了"
[tvmonitor:摄像头1] Called HA service media_player.turn_off for media_player.xiao_ai
[tvmonitor:摄像头1] Daily total: 122.0min exceeded threshold 120min
```

## Out of Scope

- Web UI for TV monitor configuration (future enhancement)
- Notifications beyond HA (WeChat, Telegram, etc.)
- Per-child recognition or multiple TV support beyond config-based
- Recording or storing detection frames
