# TV Monitor Feature Design

## Summary

Add a built-in TV watching time monitor to CamKeep. Use gocv to capture frames from RTSP streams, analyze the ROI region (TV screen) via brightness/frame-diff pixel analysis, enforce viewing time limits (per-session + rest interval + daily total), and trigger Home Assistant actions (TTS + turn off TV) when limits are exceeded.

## Requirements

- Detect whether the TV screen is on (has content) using ROI pixel analysis
- Support auto-calibration of the TV screen ROI region (Canny edge detection for largest rectangle)
- Fallback to manual ROI configuration if auto-calibration fails
- Enforce three viewing rules:
  - **Session limit**: max continuous viewing time per session (e.g., 5 min)
  - **Rest interval**: mandatory rest period after each session (e.g., 20 min), TV turned off if opened during rest
  - **Daily limit**: max total viewing time per day (e.g., 60 min)
- Only monitor within a configurable time range (e.g., 08:00-23:00)
- Trigger HA REST API actions: TTS announcement + turn off TV
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
    max_session_minutes: 5    # single session max viewing time (minutes)
    rest_minutes: 20          # mandatory rest between sessions (minutes)
    max_daily_minutes: 60     # daily total max viewing time (minutes)
    ha_url: "http://homeassistant.local:8123"
    ha_token: "eyJhbGci..."
    ha_control_service: "remote.turn_off"       # HA service to control TV (leave empty to skip)
    ha_control_entity_id: "remote.tv_remote"     # HA entity for TV remote
    ha_tts_entity_id: "media_player.xiao_ai"     # HA entity for TTS speaker (leave empty to skip)
    ha_tts_message: "看电视时间到了，休息一下吧"   # TTS text
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

Each monitor has an independent state machine with 4 states:

```
IDLE ──(TV ON)──► WATCHING ──(session ≥ max_session OR daily ≥ max_daily)──► TURNING_OFF
  ▲                    │                                                        │
  │                    │(TV OFF)                                                │
  │                    ▼                                                        │
  │                 IDLE                                                        ▼
  │                                                              RESTING ──(rest elapsed)──► IDLE
  │                                                                │
  └──(rest elapsed & daily < max_daily)                           │
                                                                   │(TV ON during rest)
                                                                   ▼
                                                              TURNING_OFF (immediate)
```

### State Descriptions

- **IDLE**: TV is off (or not monitored). Waiting for TV to turn on. No timers running.
- **WATCHING**: TV is on. Accumulating session time and daily time.
  - If `sessionMinutes >= max_session_minutes` → TTS + turn off TV → transition to RESTING
  - If `dailyMinutes >= max_daily_minutes` → TTS + turn off TV → transition to RESTING (rest doesn't matter, daily limit locks out for the day)
  - If TV goes off naturally → reset session timer, transition to IDLE
- **RESTING**: Mandatory rest period after a session ended by the monitor.
  - If TV turns on during rest → immediately turn off TV (no TTS, or short warning)
  - When `restMinutes >= rest_minutes` AND `dailyMinutes < max_daily_minutes` → transition to IDLE (ready for next session, but does NOT turn TV on)
  - When `dailyMinutes >= max_daily_minutes` → stay in RESTING until next day reset (daily limit exhausted)
- **TURNING_OFF**: Transient state while calling HA API. Immediately transitions to RESTING after HA call completes.

### Time Tracking

```go
sessionStart  time.Time  // when current WATCHING session began
dailyMinutes  float64    // total ON minutes today (accumulated across all sessions)
lastDate      string     // for daily reset at midnight
restStart     time.Time  // when current RESTING period began
dailyLocked   bool       // true when dailyMinutes >= max_daily_minutes
```

Every `check_interval` seconds:
- If state is WATCHING:
  - `sessionMinutes = time.Since(sessionStart).Minutes()`
  - `dailyMinutes += checkInterval / 60.0`
- If state is RESTING:
  - `restMinutes = time.Since(restStart).Minutes()`
- At midnight (date change): reset `dailyMinutes = 0`, `dailyLocked = false`, transition to IDLE if resting

### Example Timeline (max_session=5min, rest=20min, max_daily=60min)

```
08:00  IDLE — TV off
10:00  TV turned on → WATCHING
10:05  Session hit 5min → TTS → turn off → RESTING (restStart = 10:05)
10:10  TV turned on during rest → immediately turn off → still RESTING
10:25  Rest elapsed (20min) → IDLE (ready, won't auto-open TV)
10:30  TV turned on → WATCHING
10:35  Session hit 5min → TTS → turn off → RESTING
10:55  Rest elapsed → IDLE
... (repeats)
11:55  Daily total reaches 60min → TTS → turn off → RESTING (dailyLocked=true)
       Rest elapsed but dailyLocked → stays RESTING
       Any TV on → immediately turn off
00:00  Daily reset → IDLE
```

## Home Assistant Integration

Two independent HA actions, both optional:

### Action 1: TV Control (remote/button)

Only executed if `ha_control_service` and `ha_control_entity_id` are both non-empty.

```
POST {ha_url}/api/services/{domain}/{service}
Authorization: Bearer {ha_token}
Content-Type: application/json

{"entity_id": "ha_control_entity_id"}
```

Example: `ha_control_service: "remote.turn_off"`, `ha_control_entity_id: "remote.tv_remote"`

### Action 2: TTS Announcement

Only executed if `ha_tts_entity_id` and `ha_tts_message` are both non-empty.

```
POST {ha_url}/api/services/tts/google_translate_say
Authorization: Bearer {ha_token}
Content-Type: application/json

{"entity_id": "ha_tts_entity_id", "message": "ha_tts_message value"}
```

Example: `ha_tts_entity_id: "media_player.xiao_ai"`, `ha_tts_message: "看电视时间到了"`

### Execution Order

TTS is called first (if configured), then wait 5 seconds for playback, then TV control (if configured). Every action logs success/failure.

During rest period violations, a shorter TTS message "休息时间还没到哦，再等一下" is used instead of the configured message.

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
    RestMinutes          float64 `yaml:"rest_minutes"`
    MaxDailyMinutes      float64 `yaml:"max_daily_minutes"`
    HAURL                string  `yaml:"ha_url"`
    HAToken              string  `yaml:"ha_token"`
    HAControlService     string  `yaml:"ha_control_service"`
    HAControlEntityID    string  `yaml:"ha_control_entity_id"`
    HATTSEntityID        string  `yaml:"ha_tts_entity_id"`
    HATTSMessage         string  `yaml:"ha_tts_message"`
}
```

```go
// internal/tvmonitor/monitor.go
type MonitorState int

const (
    StateIdle MonitorState = iota
    StateWatching
    StateTurningOff
    StateResting
)

type TVMonitor struct {
    config         TVMonitorConfig
    rtspURL        string
    state          MonitorState
    sessionStart   time.Time
    dailyMinutes   float64
    lastDate       string
    restStart      time.Time
    dailyLocked    bool
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
[tvmonitor:摄像头1] State: IDLE → WATCHING
[tvmonitor:摄像头1] Session 5.2min exceeded limit 5min, turning off TV
[tvmonitor:摄像头1] TTS sent: "看电视时间到了，休息一下吧"
[tvmonitor:摄像头1] Called HA service media_player.turn_off for media_player.xiao_ai
[tvmonitor:摄像头1] State: WATCHING → RESTING (rest 20min)
[tvmonitor:摄像头1] TV turned on during rest (7.5min remaining), turning off immediately
[tvmonitor:摄像头1] Rest period complete, ready for next session
[tvmonitor:摄像头1] Daily total 61.0min exceeded limit 60min, locked until midnight
[tvmonitor:摄像头1] Daily reset, all counters cleared
```

## Out of Scope

- Web UI for TV monitor configuration (future enhancement)
- Notifications beyond HA (WeChat, Telegram, etc.)
- Per-child recognition or multiple TV support beyond config-based
- Recording or storing detection frames
