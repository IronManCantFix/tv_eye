# 电视开机检测算法重设计 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 重写 tvmonitor 包，实现 HSV 多指标检测 + OFF/PENDING/TRIGGERED/RESTING 四态状态机 + 自适应阈值校准

**Architecture:** 三层分离 — Detector（图像分析）、StateMachine（纯状态转换）、Monitor（编排+副作用）

**Tech Stack:** Go, GoCV (gocv.io/x/gocv), Gin, YAML

---

## 文件变更清单

| 操作 | 文件 | 职责 |
|------|------|------|
| 修改 | `constant/config.go` | TVMonitorConfig 新增 7 个配置字段 |
| 修改 | `internal/tvmonitor/config.go` | ApplyDefaults 新增默认值 |
| 修改 | `internal/app/config_loader.go` | 默认配置模板更新 |
| 新建 | `internal/tvmonitor/state_machine.go` | 纯状态转换，OFF/PENDING/TRIGGERED/RESTING |
| 重写 | `internal/tvmonitor/detector.go` | HSV 多指标检测 + 帧间差异 + 自适应校准 |
| 重写 | `internal/tvmonitor/monitor.go` | 编排层，使用 StateMachine 驱动 |
| 修改 | `internal/tvmonitor/status.go` | MonitorStatus 新增 Metrics + State 映射更新 |
| 修改 | `static/index.js` | 前端适配新的四态值 |

---

### Task 1: 扩展配置结构

**Files:**
- Modify: `constant/config.go:52-76`
- Modify: `internal/tvmonitor/config.go`
- Modify: `internal/app/config_loader.go:46-64`

- [ ] **Step 1: 在 `constant/config.go` 的 TVMonitorConfig 中新增 7 个字段**

在 `FrameDiffThreshold` 字段后面添加：

```go
EdgeThreshold         float64 `yaml:"edge_threshold"`          // Laplacian 方差阈值（默认 30）
SaturationThreshold   float64 `yaml:"saturation_threshold"`    // S 通道均值阈值（默认 20）
MotionThreshold       float64 `yaml:"motion_threshold"`        // 帧间差异阈值（默认 5）
TargetDuration        int     `yaml:"target_duration"`          // 触发持续时长（秒，默认 300）
DebounceFrames        int     `yaml:"debounce_frames"`          // 防抖帧数（默认 3）
AutoCalibrateBaseline bool    `yaml:"auto_calibrate_baseline"`  // 启动时自适应校准（默认 false）
BaselineFrames        int     `yaml:"baseline_frames"`          // 基准采集帧数（默认 10）
```

- [ ] **Step 2: 更新 `internal/tvmonitor/config.go` 的 ApplyDefaults**

```go
func ApplyDefaults(cfg *constant.TVMonitorConfig) {
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 10
	}
	if cfg.BrightnessThreshold == 0 {
		cfg.BrightnessThreshold = 15
	}
	if cfg.EdgeThreshold == 0 {
		cfg.EdgeThreshold = 30
	}
	if cfg.SaturationThreshold == 0 {
		cfg.SaturationThreshold = 20
	}
	if cfg.MotionThreshold == 0 {
		cfg.MotionThreshold = cfg.FrameDiffThreshold
		if cfg.MotionThreshold == 0 {
			cfg.MotionThreshold = 5
		}
	}
	if cfg.TargetDuration == 0 {
		cfg.TargetDuration = int(cfg.MaxSessionMinutes * 60)
		if cfg.TargetDuration == 0 {
			cfg.TargetDuration = 300
		}
	}
	if cfg.DebounceFrames == 0 {
		cfg.DebounceFrames = 3
	}
	if cfg.BaselineFrames == 0 {
		cfg.BaselineFrames = 10
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
	if cfg.LogLevel == "" {
		cfg.LogLevel = "state"
	}
}
```

- [ ] **Step 3: 更新 `internal/app/config_loader.go` 默认配置模板**

将 `TVMonitors` 默认模板更新为：

```go
TVMonitors: []constant.TVMonitorConfig{
	{
		CameraID:            "摄像头1",
		Enabled:             false,
		MonitorTime:         "08:00-23:00",
		ROIAutoCalibrate:    true,
		CheckInterval:       10,
		BrightnessThreshold: 15,
		EdgeThreshold:       30,
		SaturationThreshold: 20,
		MotionThreshold:     5,
		TargetDuration:      300,
		DebounceFrames:      3,
		MaxSessionMinutes:   5,
		RestMinutes:         20,
		MaxDailyMinutes:     60,
		HAURL:               "http://homeassistant.local:8123",
		HATTSService:        "notify.xiaomi_cn",
		HATTSMessage:        "看电视时间到了，休息一下吧",
		HAIRTurnOffButtonID: "button.tv_ir_power_off",
	},
},
```

- [ ] **Step 4: 构建验证**

Run: `go build ./...`
Expected: 编译通过，无错误

---

### Task 2: 创建 StateMachine

**Files:**
- Create: `internal/tvmonitor/state_machine.go`

- [ ] **Step 1: 创建 `state_machine.go`，实现纯状态转换逻辑**

```go
package tvmonitor

import "fmt"

type State int

const (
	StateOff       State = iota
	StatePending
	StateTriggered
	StateResting
)

func (s State) String() string {
	switch s {
	case StatePending:
		return "PENDING"
	case StateTriggered:
		return "TRIGGERED"
	case StateResting:
		return "RESTING"
	default:
		return "OFF"
	}
}

type StateMachine struct {
	state           State
	onFrameCount    int
	offFrameCount   int
	debounceCount   int
	triggerFrames   int
	triggeredCount  int
	restStartSet    bool
}

func NewStateMachine(debounceCount, triggerFrames int) *StateMachine {
	return &StateMachine{
		state:         StateOff,
		debounceCount: debounceCount,
		triggerFrames: triggerFrames,
	}
}

func (sm *StateMachine) Update(rawOn bool) (newState State, shouldTrigger bool) {
	if rawOn {
		sm.offFrameCount = 0
		sm.onFrameCount++
	} else {
		sm.onFrameCount = 0
		sm.offFrameCount++
	}

	switch sm.state {
	case StateOff:
		if sm.onFrameCount >= sm.debounceCount {
			sm.state = StatePending
			sm.onFrameCount = sm.debounceCount
		}

	case StatePending:
		if sm.offFrameCount >= sm.debounceCount {
			sm.state = StateOff
			sm.onFrameCount = 0
		} else if sm.onFrameCount >= sm.triggerFrames {
			sm.state = StateTriggered
			sm.triggeredCount = 0
			shouldTrigger = true
		}

	case StateTriggered:
		if sm.offFrameCount >= sm.debounceCount {
			sm.state = StateOff
			sm.onFrameCount = 0
			sm.triggeredCount = 0
		} else {
			sm.triggeredCount++
			if sm.triggeredCount >= sm.triggerFrames {
				sm.state = StateResting
				sm.triggeredCount = 0
				sm.restStartSet = false
				shouldTrigger = true
			}
		}

	case StateResting:
		if sm.offFrameCount >= sm.debounceCount {
			sm.state = StateOff
			sm.onFrameCount = 0
		}
	}

	return sm.state, shouldTrigger
}

func (sm *StateMachine) ForceResting() {
	sm.state = StateResting
	sm.onFrameCount = 0
	sm.offFrameCount = 0
	sm.triggeredCount = 0
	sm.restStartSet = false
}

func (sm *StateMachine) ForceOff() {
	sm.state = StateOff
	sm.onFrameCount = 0
	sm.offFrameCount = 0
	sm.triggeredCount = 0
}

func (sm *StateMachine) State() State {
	return sm.state
}

func (sm *StateMachine) SetRestStartSet(v bool) {
	sm.restStartSet = v
}

func (sm *StateMachine) RestStartSet() bool {
	return sm.restStartSet
}

func (sm *StateMachine) OnFrameCount() int {
	return sm.onFrameCount
}
```

- [ ] **Step 2: 构建验证**

Run: `go build ./...`
Expected: 编译通过

---

### Task 3: 重写 Detector

**Files:**
- Rewrite: `internal/tvmonitor/detector.go`

- [ ] **Step 1: 重写 detector.go，实现 HSV 多指标检测 + 帧间差异 + 自适应校准**

完整文件内容：

```go
package tvmonitor

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"sort"
	"time"

	"github.com/r0n9/camkeep/constant"
	"gocv.io/x/gocv"
)

type Metrics struct {
	VMean     float64
	SMean     float64
	LapVar    float64
	FrameDiff float64
}

type Detector struct {
	config      constant.TVMonitorConfig
	perspectMat gocv.Mat
	warpSize    image.Point
	roiPoints   [4]image.Point

	prevGray    gocv.Mat
	hasPrev     bool

	// Thresholds (may be overridden by adaptive calibration)
	vThreshold   float64
	edgeThreshold float64

	lastMetrics Metrics
}

func NewDetector(cfg constant.TVMonitorConfig, frameWidth, frameHeight int) *Detector {
	d := &Detector{
		config:        cfg,
		vThreshold:    cfg.BrightnessThreshold,
		edgeThreshold: cfg.EdgeThreshold,
	}
	d.buildPerspectiveTransform(frameWidth, frameHeight)
	return d
}

// CalibrateBaseline collects baseline metrics from frames captured while TV is off.
// Returns adjusted V and Edge thresholds, or error if calibration fails.
func (d *Detector) CalibrateBaseline(cap *gocv.VideoCapture, numFrames int) error {
	var sumV, sumLap float64
	count := 0

	for i := 0; i < numFrames; i++ {
		frame := gocv.NewMat()
		if ok := cap.Read(&frame); !ok || frame.Empty() {
			frame.Close()
			continue
		}

		warped := gocv.NewMat()
		gocv.WarpPerspective(frame, &warped, d.perspectMat, d.warpSize)
		frame.Close()

		hsv := gocv.NewMat()
		gocv.CvtColor(warped, &hsv, gocv.ColorBGRToHSV)
		warped.Close()

		channels := gocv.Split(hsv)
		hsv.Close()
		vMean := channels[2].Mean().Val1
		for _, ch := range channels {
			ch.Close()
		}

		gray := gocv.NewMat()
		gocv.CvtColor(warped, &gray, gocv.ColorBGRToGray)
		gocv.GaussianBlur(gray, &gray, image.Pt(3, 3), 0, 0, gocv.BorderDefault)

		laplacian := gocv.NewMat()
		gocv.Laplacian(gray, &laplacian, gocv.MatTypeCV64F, 3, 1.0, 0.0, gocv.BorderDefault)
		gray.Close()

		mean := gocv.NewMat()
		stdDev := gocv.NewMat()
		gocv.MeanStdDev(laplacian, &mean, &stdDev)
		lapVar := stdDev.GetDoubleAt(0, 0)
		mean.Close()
		stdDev.Close()
		laplacian.Close()

		sumV += vMean
		sumLap += lapVar
		count++
	}

	if count < 3 {
		return fmt.Errorf("only collected %d valid frames (need at least 3)", count)
	}

	baselineV := sumV / float64(count)
	baselineLap := sumLap / float64(count)

	adaptiveV := baselineV * 1.5
	adaptiveEdge := baselineLap * 2.0

	if adaptiveV > d.vThreshold {
		d.vThreshold = adaptiveV
	}
	if adaptiveEdge > d.edgeThreshold {
		d.edgeThreshold = adaptiveEdge
	}

	log.Printf("[tvmonitor] Baseline calibrated: V=%.1f->threshold=%.1f, Lap=%.1f->threshold=%.1f (%d frames)",
		baselineV, d.vThreshold, baselineLap, d.edgeThreshold, count)
	return nil
}

func (d *Detector) TVState(frame gocv.Mat) (bool, Metrics) {
	// 1. ROI perspective correction
	warped := gocv.NewMat()
	defer warped.Close()
	gocv.WarpPerspective(frame, &warped, d.perspectMat, d.warpSize)

	// 2. Convert to HSV, extract V and S channels
	hsv := gocv.NewMat()
	gocv.CvtColor(warped, &hsv, gocv.ColorBGRToHSV)
	channels := gocv.Split(hsv)
	hsv.Close()
	vChannel := channels[2]
	sChannel := channels[1]
	defer func() {
		for _, ch := range channels {
			ch.Close()
		}
	}()

	vMean := vChannel.Mean().Val1
	sMean := sChannel.Mean().Val1

	// 3. Convert to gray for Laplacian
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(warped, &gray, gocv.ColorBGRToGray)
	gocv.GaussianBlur(gray, &gray, image.Pt(3, 3), 0, 0, gocv.BorderDefault)

	laplacian := gocv.NewMat()
	defer laplacian.Close()
	gocv.Laplacian(gray, &laplacian, gocv.MatTypeCV64F, 3, 1.0, 0.0, gocv.BorderDefault)

	mean := gocv.NewMat()
	stdDev := gocv.NewMat()
	defer mean.Close()
	defer stdDev.Close()
	gocv.MeanStdDev(laplacian, &mean, &stdDev)
	lapVar := stdDev.GetDoubleAt(0, 0)

	// 4. Frame diff with previous frame
	var frameDiff float64
	if d.hasPrev {
		diff := gocv.NewMat()
		gocv.AbsDiff(gray, &d.prevGray, &diff)
		frameDiff = diff.Mean().Val1
		diff.Close()
	}
	d.prevGray.Close()
	d.prevGray = gray.Clone()
	d.hasPrev = true

	metrics := Metrics{
		VMean:     vMean,
		SMean:     sMean,
		LapVar:    lapVar,
		FrameDiff: frameDiff,
	}
	d.lastMetrics = metrics

	// 5. Multi-criteria judgment
	brightnessOK := vMean > d.vThreshold
	edgeOK := lapVar > d.edgeThreshold
	colorOK := sMean > d.config.SaturationThreshold
	motionOK := frameDiff > d.config.MotionThreshold

	rawOn := brightnessOK && edgeOK && (colorOK || motionOK)

	log.Printf("[tvmonitor] TVState: raw=%v V=%.1f(>%.1f) Lap=%.1f(>%.1f) S=%.1f(>%.1f) Diff=%.1f(>%.1f)",
		rawOn, vMean, d.vThreshold, lapVar, d.edgeThreshold, sMean, d.config.SaturationThreshold, frameDiff, d.config.MotionThreshold)

	return rawOn, metrics
}

func (d *Detector) LastMetrics() Metrics {
	return d.lastMetrics
}

// Close releases gocv resources.
func (d *Detector) Close() {
	if !d.perspectMat.Empty() {
		d.perspectMat.Close()
	}
	if d.hasPrev {
		d.prevGray.Close()
		d.hasPrev = false
	}
}

// buildPerspectiveTransform builds a perspective transform matrix from the 4 ROI vertices.
func (d *Detector) buildPerspectiveTransform(w, h int) {
	tl := image.Pt(int(d.config.ROITopLeft[0]*float64(w)), int(d.config.ROITopLeft[1]*float64(h)))
	tr := image.Pt(int(d.config.ROITopRight[0]*float64(w)), int(d.config.ROITopRight[1]*float64(h)))
	br := image.Pt(int(d.config.ROIBottomRight[0]*float64(w)), int(d.config.ROIBottomRight[1]*float64(h)))
	bl := image.Pt(int(d.config.ROIBottomLeft[0]*float64(w)), int(d.config.ROIBottomLeft[1]*float64(h)))

	d.roiPoints = [4]image.Point{tl, tr, br, bl}

	dstW := int(math.Max(ptDist(tl, tr), ptDist(bl, br)))
	dstH := int(math.Max(ptDist(tl, bl), ptDist(tr, br)))
	if dstW <= 0 {
		dstW = 1
	}
	if dstH <= 0 {
		dstH = 1
	}
	d.warpSize = image.Pt(dstW, dstH)

	src := gocv.NewPoint2fVectorFromPoints([]gocv.Point2f{
		{X: float32(tl.X), Y: float32(tl.Y)},
		{X: float32(tr.X), Y: float32(tr.Y)},
		{X: float32(br.X), Y: float32(br.Y)},
		{X: float32(bl.X), Y: float32(bl.Y)},
	})
	dst := gocv.NewPoint2fVectorFromPoints([]gocv.Point2f{
		{X: 0, Y: 0},
		{X: float32(dstW), Y: 0},
		{X: float32(dstW), Y: float32(dstH)},
		{X: 0, Y: float32(dstH)},
	})
	d.perspectMat = gocv.GetPerspectiveTransform2f(src, dst)
	src.Close()
	dst.Close()
}

func ptDist(a, b image.Point) float64 {
	dx := float64(a.X - b.X)
	dy := float64(a.Y - b.Y)
	return math.Sqrt(dx*dx + dy*dy)
}

// DrawROI draws the ROI quadrilateral and metrics on a copy of the frame.
func (d *Detector) DrawROI(frame gocv.Mat) []byte {
	annotated := frame.Clone()
	defer annotated.Close()

	green := color.RGBA{G: 255, A: 255}
	red := color.RGBA{R: 255, A: 255}
	boxColor := green
	stateLabel := "TV OFF"
	if d.lastMetrics.LapVar > d.edgeThreshold {
		boxColor = red
		stateLabel = "TV ON"
	}

	pts := d.roiPoints
	thickness := 3
	gocv.Line(&annotated, pts[0], pts[1], boxColor, thickness)
	gocv.Line(&annotated, pts[1], pts[2], boxColor, thickness)
	gocv.Line(&annotated, pts[2], pts[3], boxColor, thickness)
	gocv.Line(&annotated, pts[3], pts[0], boxColor, thickness)

	labelPos := image.Pt(pts[0].X, pts[0].Y-8)
	if labelPos.Y < 16 {
		labelPos.Y = pts[0].Y + 20
	}
	gocv.PutText(&annotated, stateLabel, labelPos, gocv.FontHersheyPlain, 1.5, boxColor, 2)

	infoPos := image.Pt(pts[3].X, pts[3].Y+18)
	m := d.lastMetrics
	gocv.PutText(&annotated, fmt.Sprintf("V:%.0f LAP:%.1f S:%.0f D:%.0f", m.VMean, m.LapVar, m.SMean, m.FrameDiff), infoPos, gocv.FontHersheyPlain, 1.2, boxColor, 1)

	timePos := image.Pt(10, annotated.Rows()-10)
	gocv.PutText(&annotated, time.Now().Format("15:04:05"), timePos, gocv.FontHersheyPlain, 1.5, color.RGBA{R: 255, G: 255, B: 255, A: 200}, 2)

	buf, err := gocv.IMEncode(".jpg", annotated)
	if err != nil {
		return nil
	}
	defer buf.Close()
	return buf.GetBytes()
}

// AutoCalibrateROI attempts to detect the TV screen as the largest quadrilateral.
func AutoCalibrateROI(rtspURL string, fw, fh float64) (tl, tr, br, bl constant.ROIPoint, err error) {
	cap, err := gocv.OpenVideoCapture(rtspURL)
	if err != nil {
		return tl, tr, br, bl, fmt.Errorf("open stream for calibration: %w", err)
	}
	defer cap.Close()

	frame := gocv.NewMat()
	defer frame.Close()
	if ok := cap.Read(&frame); !ok || frame.Empty() {
		return tl, tr, br, bl, fmt.Errorf("failed to read frame for calibration")
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

	bestArea := 0.0
	var bestPoints []image.Point

	for i := 0; i < contours.Size(); i++ {
		approx := gocv.ApproxPolyDP(contours.At(i), 4, true)
		if approx.Size() != 4 {
			approx.Close()
			continue
		}
		r := gocv.BoundingRect(contours.At(i))
		area := float64(r.Dx()*r.Dy()) / (fw * fh)
		if area > bestArea {
			bestArea = area
			bestPoints = make([]image.Point, 4)
			for j := 0; j < 4; j++ {
				bestPoints[j] = approx.At(j)
			}
		}
		approx.Close()
	}

	if bestArea < 0.05 {
		return tl, tr, br, bl, fmt.Errorf("no suitable quadrilateral found (best area ratio: %.2f)", bestArea)
	}

	sorted := orderQuadPoints(bestPoints)

	tl = constant.ROIPoint{float64(sorted[0].X) / fw, float64(sorted[0].Y) / fh}
	tr = constant.ROIPoint{float64(sorted[1].X) / fw, float64(sorted[1].Y) / fh}
	br = constant.ROIPoint{float64(sorted[2].X) / fw, float64(sorted[2].Y) / fh}
	bl = constant.ROIPoint{float64(sorted[3].X) / fw, float64(sorted[3].Y) / fh}

	log.Printf("[tvmonitor] Auto-calibrated ROI: TL=[%.2f,%.2f] TR=[%.2f,%.2f] BR=[%.2f,%.2f] BL=[%.2f,%.2f] (area=%.0f%%)",
		tl[0], tl[1], tr[0], tr[1], br[0], br[1], bl[0], bl[1], bestArea*100)
	return
}

func orderQuadPoints(pts []image.Point) []image.Point {
	sorted := make([]image.Point, 4)
	copy(sorted, pts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Y < sorted[j].Y
	})

	top := sorted[:2]
	bottom := sorted[2:]

	if top[0].X > top[1].X {
		top[0], top[1] = top[1], top[0]
	}
	if bottom[0].X > bottom[1].X {
		bottom[0], bottom[1] = bottom[1], bottom[0]
	}

	return []image.Point{top[0], top[1], bottom[1], bottom[0]}
}
```

- [ ] **Step 2: 构建验证**

Run: `go build ./...`
Expected: 编译通过

---

### Task 4: 更新 status.go

**Files:**
- Modify: `internal/tvmonitor/status.go`

- [ ] **Step 1: 更新 MonitorStatus 结构体和 UpdateMonitorStatus 函数**

主要变更：
1. 新增 `MonitorMetrics` 结构体
2. `MonitorStatus` 新增 `Metrics` 字段
3. `UpdateMonitorStatus` 签名新增 `metrics Metrics` 参数
4. 移除旧的 `MonitorState` 类型引用，改用新 `State` 类型
5. `RegisterMonitor` 默认状态改为 `"OFF"`

- [ ] **Step 2: 构建验证**

Run: `go build ./...`
Expected: 编译通过

---

### Task 5: 重写 Monitor

**Files:**
- Rewrite: `internal/tvmonitor/monitor.go`

- [ ] **Step 1: 重写 monitor.go，使用 Detector + StateMachine 三层架构**

主要变更：
1. 移除旧的 `MonitorState` 枚举（现在在 state_machine.go 中）
2. `TVMonitor` 结构体新增 `sm *StateMachine`，移除旧状态字段
3. `Run()` 中新增自适应基准采集逻辑
4. `tick()` 使用 `sm.Update(rawOn)` 驱动状态转换
5. RESTING 休息时长计时基于 `time.Since(restStart)`
6. 周期性触发：TRIGGERED 状态下每 triggerFrames 帧再次触发

- [ ] **Step 2: 构建验证**

Run: `go build ./...`
Expected: 编译通过

---

### Task 6: 更新前端适配新状态

**Files:**
- Modify: `static/index.js:1560-1647`

- [ ] **Step 1: 更新前端状态映射**

将 `stateColors`、`stateLabels`、`stateDots` 中的 `IDLE`/`WATCHING` 替换为 `OFF`/`PENDING`/`TRIGGERED`：

```javascript
const stateColors = {
    'OFF': 'bg-gray-100 text-gray-600 border-gray-200',
    'PENDING': 'bg-blue-50 text-blue-700 border-blue-200',
    'TRIGGERED': 'bg-red-50 text-red-700 border-red-200',
    'RESTING': 'bg-amber-50 text-amber-700 border-amber-200',
    'NO_ROI': 'bg-blue-50 text-blue-700 border-blue-200',
    'ERROR': 'bg-red-50 text-red-700 border-red-200'
};
const stateLabels = {
    'OFF': '关机',
    'PENDING': '观看中',
    'TRIGGERED': '已超时',
    'RESTING': '休息中',
    'NO_ROI': '未就绪',
    'ERROR': '异常'
};
const stateDots = {
    'OFF': 'bg-gray-400',
    'PENDING': 'bg-blue-500 animate-pulse',
    'TRIGGERED': 'bg-red-500 animate-pulse',
    'RESTING': 'bg-amber-500',
    'NO_ROI': 'bg-blue-400',
    'ERROR': 'bg-red-500'
};
```

更新 details 展示逻辑：
- `PENDING` 状态展示当前会话时长
- `TRIGGERED` 状态展示已超时提示
- `RESTING` 状态展示休息剩余时间（保持不变）

- [ ] **Step 2: 构建验证**

Run: `go build ./...`
Expected: 编译通过

---

### Task 7: 最终验证与提交

- [ ] **Step 1: 完整构建**

Run: `go build -ldflags="-s -w -X main.Version=dev" -o camkeep main.go`
Expected: 编译通过，生成 camkeep 二进制

- [ ] **Step 2: 提交所有变更**
