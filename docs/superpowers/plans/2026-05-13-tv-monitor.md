# TV Monitor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a TV watching time monitor that uses gocv pixel analysis to detect TV on/off state, enforces session/daily/rest time limits, and triggers Home Assistant actions.

**Architecture:** New `internal/tvmonitor` package with 4 files. Config struct added to `constant/config.go`. Lifecycle integrated into existing `app.go` / `task_lifecycle.go`. Each enabled monitor runs as an independent goroutine with its own state machine.

**Tech Stack:** Go 1.25, gocv (OpenCV 4.x), HA REST API via net/http, existing Gin web server

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `constant/config.go` | Modify | Add `TVMonitorConfig` struct and `TVMonitors` field to `Config` |
| `internal/tvmonitor/config.go` | Create | `TVMonitorConfig` struct definition with defaults |
| `internal/tvmonitor/detector.go` | Create | ROI auto-calibration + brightness/frame-diff TV detection |
| `internal/tvmonitor/ha_client.go` | Create | HA REST API client (TTS + turn off) |
| `internal/tvmonitor/monitor.go` | Create | `TVMonitor` struct, state machine, main loop goroutine |
| `internal/app/task_lifecycle.go` | Modify | Start/stop TV monitor goroutines in `startTasks` |
| `internal/app/config_loader.go` | Modify | Validate and default TV monitor config |
| `Dockerfile` | Modify | Add OpenCV runtime libraries |

---

### Task 1: Add TVMonitorConfig to constant/config.go

**Files:**
- Modify: `constant/config.go`

- [ ] **Step 1: Add TVMonitorConfig struct and TVMonitors field**

Add after the `DailyMergeConfig` struct definition (line 24):

```go
type TVMonitorConfig struct {
	CameraID            string  `yaml:"camera_id"`
	Enabled             bool    `yaml:"enabled"`
	MonitorTime         string  `yaml:"monitor_time"`
	ROIX                float64 `yaml:"roi_x"`
	ROIY                float64 `yaml:"roi_y"`
	ROIW                float64 `yaml:"roi_w"`
	ROIH                float64 `yaml:"roi_h"`
	ROIAutoCalibrate    bool    `yaml:"roi_auto_calibrate"`
	CheckInterval       int     `yaml:"check_interval"`
	BrightnessThreshold float64 `yaml:"brightness_threshold"`
	FrameDiffThreshold  float64 `yaml:"frame_diff_threshold"`
	MaxSessionMinutes   float64 `yaml:"max_session_minutes"`
	RestMinutes         float64 `yaml:"rest_minutes"`
	MaxDailyMinutes     float64 `yaml:"max_daily_minutes"`
	HAURL               string  `yaml:"ha_url"`
	HAToken             string  `yaml:"ha_token"`
	HAService           string  `yaml:"ha_service"`
	HAEntityID          string  `yaml:"ha_entity_id"`
	HAMessage           string  `yaml:"ha_message"`
}
```

Add `TVMonitors` field to the `Config` struct:

```go
type Config struct {
	DailyMerge DailyMergeConfig  `yaml:"daily_merge"`
	Cameras    []Camera          `yaml:"cameras"`
	TVMonitors []TVMonitorConfig `yaml:"tv_monitors"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build ./...`
Expected: compile error because `internal/app/config_loader.go` references `Config` fields that still exist — should compile fine since we're only adding a new field.

- [ ] **Step 3: Commit**

```bash
git add constant/config.go
git commit -m "feat(tvmonitor): add TVMonitorConfig struct to constant"
```

---

### Task 2: Create internal/tvmonitor/config.go

**Files:**
- Create: `internal/tvmonitor/config.go`

- [ ] **Step 1: Create the package with defaults helper**

```go
package tvmonitor

import "github.com/r0n9/camkeep/constant"

// ApplyDefaults fills in zero/empty fields with sensible defaults.
func ApplyDefaults(cfg *constant.TVMonitorConfig) {
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 30
	}
	if cfg.BrightnessThreshold == 0 {
		cfg.BrightnessThreshold = 30
	}
	if cfg.FrameDiffThreshold == 0 {
		cfg.FrameDiffThreshold = 5.0
	}
	if cfg.MaxSessionMinutes == 0 {
		cfg.MaxSessionMinutes = 5
	}
	if cfg.RestMinutes == 0 {
		cfg.RestMinutes = 20
	}
	if cfg.MaxDailyMinutes == 0 {
		cfg.MaxDailyMinutes = 60
	}
	if cfg.MonitorTime == "" {
		cfg.MonitorTime = "08:00-23:00"
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build ./internal/tvmonitor/`
Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/tvmonitor/config.go
git commit -m "feat(tvmonitor): create config package with defaults helper"
```

---

### Task 3: Create HA REST API client

**Files:**
- Create: `internal/tvmonitor/ha_client.go`

- [ ] **Step 1: Implement HAClient**

```go
package tvmonitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/r0n9/camkeep/constant"
)

type HAClient struct {
	config constant.TVMonitorConfig
	client *http.Client
}

func NewHAClient(cfg constant.TVMonitorConfig) *HAClient {
	return &HAClient{
		config: cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *HAClient) TurnOffTV() error {
	return c.callService(c.config.HAService, map[string]string{"entity_id": c.config.HAEntityID})
}

func (c *HAClient) SendTTS(message string) error {
	return c.callService("tts.google_translate_say", map[string]interface{}{
		"entity_id": c.config.HAEntityID,
		"message":   message,
	})
}

func (c *HAClient) callService(service string, body map[string]interface{}) error {
	parts := strings.SplitN(service, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid service format: %s (expected domain.service)", service)
	}

	url := fmt.Sprintf("%s/api/services/%s/%s", c.config.HAURL, parts[0], parts[1])
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.HAToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("HA request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HA returned status %d", resp.StatusCode)
	}
	return nil
}

// TriggerShutdown sends TTS (if message configured), waits 5s, then turns off TV.
func (c *HAClient) TriggerShutdown(prefix, message string) {
	if message != "" {
		log.Printf("[%s] TTS: %q", prefix, message)
		if err := c.SendTTS(message); err != nil {
			log.Printf("[%s] TTS failed: %v", prefix, err)
		}
		time.Sleep(5 * time.Second)
	}

	log.Printf("[%s] Calling HA service %s for %s", prefix, c.config.HAService, c.config.HAEntityID)
	if err := c.TurnOffTV(); err != nil {
		log.Printf("[%s] Turn off failed: %v", prefix, err)
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build ./internal/tvmonitor/`
Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/tvmonitor/ha_client.go
git commit -m "feat(tvmonitor): add HA REST API client for TTS and TV control"
```

---

### Task 4: Create TV detector (gocv frame capture + analysis)

**Files:**
- Create: `internal/tvmonitor/detector.go`

This task creates the detection layer. It opens an RTSP stream via gocv, captures frames, crops ROI, and determines TV on/off state.

- [ ] **Step 1: Implement detector**

```go
package tvmonitor

import (
	"fmt"
	"image"
	"log"

	"github.com/r0n9/camkeep/constant"
	"gocv.io/x/gocv"
)

type Detector struct {
	config   constant.TVMonitorConfig
	roiRect  image.Rectangle
	prevGray gocv.Mat
}

func NewDetector(cfg constant.TVMonitorConfig, frameWidth, frameHeight int) *Detector {
	d := &Detector{config: cfg}
	d.roiRect = d.pctToRect(frameWidth, frameHeight)
	return d
}

// pctToRect converts percentage-based ROI to pixel rectangle.
func (d *Detector) pctToRect(w, h int) image.Rectangle {
	x0 := int(d.config.ROIX * float64(w))
	y0 := int(d.config.ROIY * float64(h))
	x1 := int((d.config.ROIX + d.config.ROIW) * float64(w))
	y1 := int((d.config.ROIY + d.config.ROIH) * float64(h))
	return image.Rect(x0, y0, x1, y1)
}

// TVState returns true if the TV appears to be on in the given frame.
func (d *Detector) TVState(frame gocv.Mat) bool {
	roi := frame.Region(d.roiRect)
	defer roi.Close()

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	gocv.GaussianBlur(gray, &gray, image.Pt(5, 5), 0, 0, gocv.BorderDefault)

	mean := gray.Mean()
	brightness := mean.Val1
	tvOn := brightness > d.config.BrightnessThreshold

	// Frame-diff check (skip on first frame)
	if d.prevGray.Empty() {
		d.prevGray = gray.Clone()
		return tvOn
	}

	diff := gocv.NewMat()
	defer diff.Close()
	gocv.AbsDiff(gray, d.prevGray, &diff)

	stdDev := gocv.NewMat()
	meanDev := gocv.NewMat()
	defer stdDev.Close()
	defer meanDev.Close()
	gocv.MeanStdDev(diff, &meanDev, &stdDev)

	frameDiff := stdDev.GetFloatAt(0, 0)
	tvOn = tvOn && frameDiff > d.config.FrameDiffThreshold

	d.prevGray.Close()
	d.prevGray = gray.Clone()
	return tvOn
}

// Close releases gocv resources.
func (d *Detector) Close() {
	if !d.prevGray.Empty() {
		d.prevGray.Close()
	}
}

// AutoCalibrateROI attempts to detect the TV screen as the largest rectangle
// in the frame. Returns updated ROI percentages (x, y, w, h).
// Returns error if no suitable rectangle found.
func AutoCalibrateROI(rtspURL string, cfg constant.TVMonitorConfig) (x, y, w, h float64, err error) {
	cap, err := gocv.OpenVideoCapture(rtspURL)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("open stream for calibration: %w", err)
	}
	defer cap.Close()

	frame := gocv.NewMat()
	defer frame.Close()
	if ok := cap.Read(&frame); !ok || frame.Empty() {
		return 0, 0, 0, 0, fmt.Errorf("failed to read frame for calibration")
	}

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(frame, &gray, gocv.ColorBGRToGray)
	gocv.GaussianBlur(gray, &gray, image.Pt(5, 5), 0, 0, gocv.BorderDefault)

	edges := gocv.NewMat()
	defer edges.Close()
	gocv.Canny(gray, &edges, 50, 150)

	contours := gocv.FindContours(edges, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	fw := float64(frame.Cols())
	fh := float64(frame.Rows())
	bestArea := 0.0

	for i := 0; i < contours.Size(); i++ {
		approx := gocv.ApproxPolyDP(contours.At(i), 4, true)
		if approx.Size() != 4 {
			approx.Close()
			continue
		}
		r := gocv.BoundingRect(contours.At(i))
		area := float64(r.Dx() * r.Dy()) / (fw * fh)
		if area > bestArea {
			bestArea = area
			x = float64(r.Min.X) / fw
			y = float64(r.Min.Y) / fh
			w = float64(r.Dx()) / fw
			h = float64(r.Dy()) / fh
		}
		approx.Close()
	}

	if bestArea < 0.05 {
		return 0, 0, 0, 0, fmt.Errorf("no suitable rectangle found (best area ratio: %.2f)", bestArea)
	}

	log.Printf("[tvmonitor] Auto-calibrated ROI: x=%.2f y=%.2f w=%.2f h=%.2f (area=%.0f%%)", x, y, w, h, bestArea*100)
	return x, y, w, h, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build ./internal/tvmonitor/`
Expected: compiles. Note: requires `gocv.io/x/gocv` installed locally. Run `go get gocv.io/x/gocv` first if needed.

- [ ] **Step 3: Commit**

```bash
git add internal/tvmonitor/detector.go
git commit -m "feat(tvmonitor): add gocv TV detector with ROI calibration"
```

---

### Task 5: Create monitor state machine and main loop

**Files:**
- Create: `internal/tvmonitor/monitor.go`

This is the core: state machine (IDLE→WATCHING→RESTING) with time tracking, daily reset, and HA trigger integration.

- [ ] **Step 1: Implement TVMonitor**

```go
package tvmonitor

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/r0n9/camkeep/constant"
	"github.com/r0n9/camkeep/util"
	"gocv.io/x/gocv"
)

type MonitorState int

const (
	StateIdle MonitorState = iota
	StateWatching
	StateResting
)

func (s MonitorState) String() string {
	switch s {
	case StateWatching:
		return "WATCHING"
	case StateResting:
		return "RESTING"
	default:
		return "IDLE"
	}
}

type TVMonitor struct {
	config       constant.TVMonitorConfig
	rtspURL      string
	state        MonitorState
	sessionStart time.Time
	dailyMinutes float64
	lastDate     string
	restStart    time.Time
	dailyLocked  bool
	ha           *HAClient
	detector     *Detector
	mu           sync.Mutex
}

func NewTVMonitor(cfg constant.TVMonitorConfig, rtspURL string) *TVMonitor {
	return &TVMonitor{
		config:  cfg,
		rtspURL: rtspURL,
		state:   StateIdle,
		ha:      NewHAClient(cfg),
	}
}

// Run is the main goroutine. Blocks until context is cancelled.
func (m *TVMonitor) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	log.Printf("[tvmonitor:%s] Starting monitor (session=%.0fm, rest=%.0fm, daily=%.0fm)",
		m.config.CameraID, m.config.MaxSessionMinutes, m.config.RestMinutes, m.config.MaxDailyMinutes)

	// Try auto-calibrate if ROI is zero and auto_calibrate is enabled
	if m.config.ROIAutoCalibrate && (m.config.ROIX == 0 && m.config.ROIY == 0) {
		log.Printf("[tvmonitor:%s] Attempting auto-calibration...", m.config.CameraID)
		if x, y, w, h, err := AutoCalibrateROI(m.rtspURL, m.config); err != nil {
			log.Printf("[tvmonitor:%s] Auto-calibration failed: %v (will use manual ROI)", m.config.CameraID, err)
		} else {
			m.config.ROIX, m.config.ROIY, m.config.ROIW, m.config.ROIH = x, y, w, h
		}
	}

	// Validate ROI bounds
	if m.config.ROIW <= 0 || m.config.ROIH <= 0 {
		log.Printf("[tvmonitor:%s] No valid ROI configured, monitor disabled", m.config.CameraID)
		return
	}

	interval := time.Duration(m.config.CheckInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var cap *gocv.VideoCapture
	var err error

	// Open video capture
	if cap, err = gocv.OpenVideoCapture(m.rtspURL); err != nil {
		log.Printf("[tvmonitor:%s] Failed to open RTSP stream: %v", m.config.CameraID, err)
		return
	}
	defer cap.Close()

	frame := gocv.NewMat()
	defer frame.Close()

	// Read one frame to get dimensions for ROI
	if ok := cap.Read(&frame); !ok || frame.Empty() {
		log.Printf("[tvmonitor:%s] Failed to read initial frame", m.config.CameraID)
		return
	}

	m.detector = NewDetector(m.config, frame.Cols(), frame.Rows())
	defer m.detector.Close()

	// Initialize date
	m.lastDate = time.Now().Format("2006-01-02")

	for {
		select {
		case <-ctx.Done():
			log.Printf("[tvmonitor:%s] Monitor stopped", m.config.CameraID)
			return
		case <-ticker.C:
			m.tick(cap, &frame)
		}
	}
}

func (m *TVMonitor) tick(cap *gocv.VideoCapture, frame *gocv.Mat) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Daily reset at midnight
	today := time.Now().Format("2006-01-02")
	if today != m.lastDate {
		log.Printf("[tvmonitor:%s] Daily reset, all counters cleared", m.config.CameraID)
		m.dailyMinutes = 0
		m.dailyLocked = false
		m.lastDate = today
		if m.state == StateResting {
			m.setState(StateIdle)
		}
	}

	// Check if within monitor time range
	if !util.IsWithinTimeRange(m.config.MonitorTime) {
		return
	}

	// Read frame
	if ok := cap.Read(frame); !ok || frame.Empty() {
		return
	}

	tvOn := m.detector.TVState(*frame)

	switch m.state {
	case StateIdle:
		if tvOn {
			log.Printf("[tvmonitor:%s] TV detected ON", m.config.CameraID)
			m.sessionStart = time.Now()
			m.dailyMinutes += float64(m.config.CheckInterval) / 60.0
			m.setState(StateWatching)
		}

	case StateWatching:
		if !tvOn {
			log.Printf("[tvmonitor:%s] TV turned off naturally", m.config.CameraID)
			m.setState(StateIdle)
			return
		}

		m.dailyMinutes += float64(m.config.CheckInterval) / 60.0
		sessionMin := time.Since(m.sessionStart).Minutes()

		if m.dailyMinutes >= m.config.MaxDailyMinutes {
			log.Printf("[tvmonitor:%s] Daily total %.1fmin exceeded limit %.0fmin, locked until midnight",
				m.config.CameraID, m.dailyMinutes, m.config.MaxDailyMinutes)
			m.dailyLocked = true
			m.ha.TriggerShutdown(m.prefix(), m.config.HAMessage)
			m.restStart = time.Now()
			m.setState(StateResting)
		} else if sessionMin >= m.config.MaxSessionMinutes {
			log.Printf("[tvmonitor:%s] Session %.1fmin exceeded limit %.0fmin, turning off TV",
				m.config.CameraID, sessionMin, m.config.MaxSessionMinutes)
			m.ha.TriggerShutdown(m.prefix(), m.config.HAMessage)
			m.restStart = time.Now()
			m.setState(StateResting)
		}

	case StateResting:
		if tvOn {
			remaining := m.config.RestMinutes - time.Since(m.restStart).Minutes()
			log.Printf("[tvmonitor:%s] TV on during rest (%.1fmin remaining), turning off immediately",
				m.config.CameraID, remaining)
			m.ha.TriggerShutdown(m.prefix(), "休息时间还没到哦，再等一下")
		}

		restElapsed := time.Since(m.restStart).Minutes()
		if restElapsed >= m.config.RestMinutes && !m.dailyLocked {
			log.Printf("[tvmonitor:%s] Rest period complete, ready for next session", m.config.CameraID)
			m.setState(StateIdle)
		}
	}
}

func (m *TVMonitor) setState(s MonitorState) {
	if m.state != s {
		log.Printf("[tvmonitor:%s] State: %s → %s", m.config.CameraID, m.state, s)
		m.state = s
	}
}

func (m *TVMonitor) prefix() string {
	return "tvmonitor:" + m.config.CameraID
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build ./internal/tvmonitor/`
Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/tvmonitor/monitor.go
git commit -m "feat(tvmonitor): add state machine monitor with session/rest/daily limits"
```

---

### Task 6: Integrate monitor lifecycle into app

**Files:**
- Modify: `internal/app/task_lifecycle.go`

- [ ] **Step 1: Add TV monitor imports and goroutine startup in startTasks**

Add import for tvmonitor package and start monitor goroutines. Modify `startTasks` in `internal/app/task_lifecycle.go`:

Add import:
```go
import (
	"context"
	"log"

	"github.com/r0n9/camkeep/constant"
	"github.com/r0n9/camkeep/internal/service"
	"github.com/r0n9/camkeep/internal/task"
	"github.com/r0n9/camkeep/internal/tvmonitor"
)
```

Add to the end of `startTasks()` function (after the camera loop):

```go
// Start TV monitors
for i := range cfg.TVMonitors {
	tmcfg := cfg.TVMonitors[i]
	if !tmcfg.Enabled {
		continue
	}
	tvmonitor.ApplyDefaults(&tmcfg)

	// Resolve RTSP URL: use go2rtc proxy like camera tasks do
	rtspURL := fmt.Sprintf("rtsp://%s:8554/%s", constant.DefaultGo2rtcHost, tmcfg.CameraID)

	taskWg.Add(1)
	go tvmonitor.NewTVMonitor(tmcfg, rtspURL).Run(ctx, &taskWg)
}
```

Also add `"fmt"` to the imports.

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build ./...`
Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/app/task_lifecycle.go
git commit -m "feat(tvmonitor): integrate TV monitor into app lifecycle"
```

---

### Task 7: Validate TV monitor config on load

**Files:**
- Modify: `internal/app/config_loader.go`

- [ ] **Step 1: Add TV monitor config validation in validateAndFixConfig**

Add a new import and extend `validateAndFixConfig` to validate TV monitor configs. Add at the end of the function, before the final `return`:

```go
// Validate TV monitors
var validMonitors []constant.TVMonitorConfig
for _, tm := range cfg.TVMonitors {
	if tm.CameraID == "" {
		log.Println("警告: 发现空 camera_id 的 tv_monitor 配置，已跳过")
		continue
	}
	// Check that referenced camera exists
	found := false
	for _, cam := range uniqueCams {
		if cam.ID == tm.CameraID {
			found = true
			break
		}
	}
	if !found {
		log.Printf("警告: tv_monitor 引用的摄像头 [%s] 不存在，已跳过", tm.CameraID)
		continue
	}
	validMonitors = append(validMonitors, tm)
}
cfg.TVMonitors = validMonitors
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build ./...`
Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/app/config_loader.go
git commit -m "feat(tvmonitor): validate TV monitor config on load"
```

---

### Task 8: Update Dockerfile for OpenCV

**Files:**
- Modify: `Dockerfile`

- [ ] **Step 1: Add OpenCV dependencies**

Change the builder stage to install OpenCV dev headers and enable CGO. Change the runtime stage to install OpenCV runtime libraries.

Replace the builder stage `RUN CGO_ENABLED=0 ...` line with:

```dockerfile
RUN apk add --no-cache gcc musl-dev opencv-dev && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w -X main.Version=${VERSION}" -o camkeep main.go
```

Replace the runtime stage `RUN apk add --no-cache ffmpeg tzdata` with:

```dockerfile
RUN apk add --no-cache ffmpeg tzdata opencv
```

- [ ] **Step 2: Verify Dockerfile syntax**

Run: `cd /Users/huanghongda/develop/go/camkeep && docker build --check .` (or just visually verify the Dockerfile is valid)

- [ ] **Step 3: Commit**

```bash
git add Dockerfile
git commit -m "feat(tvmonitor): add OpenCV dependencies to Dockerfile"
```

---

### Task 9: Add go.mod dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add gocv dependency**

Run: `cd /Users/huanghongda/develop/go/camkeep && go get gocv.io/x/gocv`

- [ ] **Step 2: Verify full build**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build ./...`
Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "feat(tvmonitor): add gocv dependency"
```

---

### Task 10: End-to-end smoke test

**Files:** None (testing only)

- [ ] **Step 1: Add a minimal tv_monitors entry to conf.yaml for testing**

Add to `conf.yaml` (disabled by default):

```yaml
tv_monitors:
  - camera_id: "摄像头1"
    enabled: false
    roi_auto_calibrate: true
    max_session_minutes: 5
    rest_minutes: 20
    max_daily_minutes: 60
    ha_url: "http://homeassistant.local:8123"
    ha_token: ""
    ha_service: "media_player.turn_off"
    ha_entity_id: "media_player.xiao_ai"
    ha_message: "看电视时间到了，休息一下吧"
```

- [ ] **Step 2: Build and run briefly to verify no crash on startup**

Run: `cd /Users/huanghongda/develop/go/camkeep && go build -o camkeep main.go && timeout 5 ./camkeep || true`
Expected: starts up, logs show "tvmonitor" prefix only if enabled=true. With enabled=false, no monitor starts.

- [ ] **Step 3: Commit**

```bash
git add conf.yaml
git commit -m "feat(tvmonitor): add example tv_monitors config to conf.yaml"
```
