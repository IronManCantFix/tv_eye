# 电视开机检测算法重设计

## 概述

重写 `internal/tvmonitor/` 包，采用三层分离架构，新增 HSV 多指标检测、帧间差异、自适应阈值校准，以及 OFF/PENDING/TRIGGERED/RESTING 四态状态机。

### 改动范围

- **完整重写**：检测算法 + 状态机 + 业务逻辑（会话计时、休息、每日限额、HA 控制）
- **架构**：三层分离 — Detector（图像分析）、StateMachine（纯状态转换）、Monitor（编排 + 副作用）
- **触发模式**：周期性重复触发（每达到 target_duration 触发一次，直到电视关机）
- **自适应阈值**：启动时尝试采集关机基准，失败回退到固定阈值

---

## 架构

```
RTSP 流
  │
  ▼ Monitor 层取帧、Drain 缓冲
ROI 透视校正（保留不变）
  │
  ▼ Detector 层
HSV 转换 → V 通道均值（亮度）
         → S 通道均值（色彩）
灰度图   → Laplacian 方差（内容复杂度）
与上一帧 → AbsDiff 均值（帧间差异）
  │
  ▼ 多指标联合判断 → rawOn bool
  │
  ▼ StateMachine 层
防抖确认 → 状态转换 → shouldTrigger bool
  │
  ▼ Monitor 层
执行副作用：HA 关机、TTS 播报、前端状态更新、日志记录
```

### 文件结构

| 文件 | 职责 |
|------|------|
| `detector.go` | 图像分析：HSV/Laplacian/帧间差异/自适应校准，输出 `rawOn` + `Metrics` |
| `state_machine.go` | **新文件**。纯状态转换：OFF/PENDING/TRIGGERED/RESTING，无副作用 |
| `monitor.go` | 编排层：RTSP 读写、驱动 Detector + StateMachine、执行副作用 |
| `status.go` | 前端状态：MonitorStatus 新增 Metrics 字段 |
| `config.go` | ApplyDefaults 新增默认值 |
| `ha_client.go` | 无变更 |
| `constant/config.go` | TVMonitorConfig 新增配置字段 |
| `config_loader.go` | 默认配置模板更新 |

---

## 第一层：Detector

### 处理流程

1. `WarpPerspective` 提取 ROI 区域（保留不变）
2. 转 HSV，分离 V/S 通道
3. 灰度图 → Laplacian 方差
4. 当前帧与上一帧 `AbsDiff` → 帧间差异均值
5. 多指标联合判断

### 检测指标

| 指标 | 来源 | 默认阈值 | 作用 |
|------|------|---------|------|
| V 通道均值 | HSV V 通道 | `> 15` | 屏幕发光，替代灰度亮度 |
| Laplacian 方差 | 灰度图 | `> EdgeThreshold`（默认 30） | 画面有内容纹理 |
| S 通道均值 | HSV S 通道 | `> SaturationThreshold`（默认 20） | 画面有色彩，排除环境光白色反射 |
| 帧间差异均值 | AbsDiff | `> MotionThreshold`（默认 5） | 画面有动态变化 |

### 联合判断

```go
brightnessOK := vMean > threshold.V
edgeOK       := laplacianStdDev > threshold.Edge
colorOK      := sMean > threshold.Saturation
motionOK     := frameDiffMean > threshold.Motion

rawOn := brightnessOK && edgeOK && (colorOK || motionOK)
```

**设计依据**：
- V 通道 `max(R,G,B)` 比灰度 `0.299R+0.587G+0.114B` 对冷色调内容更准确
- S 通道排除白色环境光反射（反射光饱和度接近 0）
- 帧间差异区分静态壁纸和动态节目
- `colorOK || motionOK`：满足其一即可，避免静态但彩色的画面（如暂停）被误判

### 自适应阈值校准

启动时尝试采集关机基准（默认 10 帧）：

```go
// 采集基准
baselineV   = avg(10帧 V 通道均值)
baselineLap = avg(10帧 Laplacian 方差)

// 检测时使用相对阈值
brightnessOK = vMean   > max(baselineV * 1.5, fixedV)   // 取自适应和固定阈值的较大值
edgeOK       = lapVar  > max(baselineLap * 2.0, fixedEdge)
```

- 如果采集失败（帧为空、流断开），完全回退到 conf.yaml 中的固定阈值
- `auto_calibrate_baseline: false` 时跳过，直接用固定阈值

### 方法签名变更

```go
type Metrics struct {
    VMean      float64
    SMean      float64
    LapVar     float64
    FrameDiff  float64
}

func (d *Detector) TVState(frame gocv.Mat) (rawOn bool, metrics Metrics)
```

### 保留不变

- `buildPerspectiveTransform`
- `AutoCalibrateROI`
- `DrawROI`（新增 metrics 展示）
- `Close()`

---

## 第二层：StateMachine

### 状态定义

```go
type State int

const (
    StateOff       State = iota  // 电视关机
    StatePending                 // 开机已确认，正在累计持续时长
    StateTriggered               // 已触发事件；电视仍开着时周期性重复触发
    StateResting                 // 休息期，电视被强制关机
)
```

### 状态转移

```
              连续 debounce 帧 ON             累计 triggerFrames
┌────────┐  ────────────────────→  ┌─────────┐  ──────────────────→  ┌───────────┐
│  OFF   │                         │ PENDING │                        │ TRIGGERED │
└────────┘                         └─────────┘                        └───────────┘
     ↑                                  │                                  │
     │    连续 debounce 帧 OFF           │   连续 debounce 帧 OFF           │  周期触发
     │◄─────────────────────────────────┘◄────────────────────────────────┘  后进入
     │                                                                    RESTING
     │                              ┌──────────┐◄───────────────────────────┘
     │                              │ RESTING  │   休息期间 TV ON → 持续关机
     │                              └──────────┘
     │                                  │
     │          休息时长结束             │
     └──────────────────────────────────┘
```

### 方法签名

```go
type StateMachine struct {
    state         State
    onFrameCount  int     // 连续 ON 帧计数
    offFrameCount int     // 连续 OFF 帧计数
    debounceCount int     // 防抖帧数（默认 3）
    triggerFrames int     // 触发所需累计帧数 = targetDuration / checkInterval
    triggeredCount int    // TRIGGERED 状态下累计帧数（用于周期触发）
}

func (sm *StateMachine) Update(rawOn bool) (newState State, shouldTrigger bool)
func (sm *StateMachine) ForceResting()
func (sm *StateMachine) ForceOff()
func (sm *StateMachine) State() State
```

### 状态转换规则

| 当前状态 | 条件 | 新状态 | shouldTrigger |
|---------|------|--------|--------------|
| OFF | `onFrameCount >= debounce` | PENDING | false |
| OFF | 其他 | OFF | false |
| PENDING | `offFrameCount >= debounce` | OFF | false |
| PENDING | `onFrameCount >= triggerFrames` | TRIGGERED | true |
| PENDING | 其他 | PENDING | false |
| TRIGGERED | `offFrameCount >= debounce` | OFF | false |
| TRIGGERED | `triggeredCount >= triggerFrames`（周期） | RESTING | true |
| TRIGGERED | 其他 | TRIGGERED | false |
| RESTING | `offFrameCount >= debounce` && `休息结束` | OFF | false |
| RESTING | TV ON | RESTING（保持） | false |

### 关键设计

- StateMachine **无副作用**：不知道 HA、TTS、前端状态的存在
- `shouldTrigger` 由 Monitor 层消费
- `ForceResting()` / `ForceOff()` 由 Monitor 层在每日限额等场景调用
- RESTING 的结束条件基于实际时间（`time.Since(restStart) >= restDuration`），不是帧数

---

## 第三层：Monitor

### 主循环

```go
func (m *Monitor) Run(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()

    // 1. ROI 自动校准（保留现有逻辑）
    // 2. 自适应阈值基准采集（新增）
    // 3. 初始化 Detector、StateMachine

    ticker := time.NewTicker(interval)
    for {
        // 确保有 RTSP 连接（保留现有重连逻辑）

        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            m.tick(cap, &frame)
        }
    }
}
```

### tick 逻辑

```go
func (m *Monitor) tick(cap, frame) {
    // 1. Drain 缓冲帧 + 取最新帧（保留）
    // 2. rawOn, metrics := detector.TVState(frame)
    // 3. newState, shouldTrigger := sm.Update(rawOn)

    // 4. 快照更新：detector.DrawROI(frame)

    // 5. 跨天重置
    if 跨天 { dailyMinutes = 0; dailyLocked = false }

    // 6. 超出监控时间 → 强制 OFF
    if !withinTimeRange { sm.ForceOff() }

    // 7. 累计每日观看时长
    if rawOn { dailyMinutes += interval / 60 }

    // 8. 每日限额（优先于状态机触发）
    if dailyMinutes >= maxDaily && rawOn {
        sm.ForceResting()
        dailyLocked = true
        triggerShutdown()
    }

    // 9. 状态机触发
    if shouldTrigger { triggerShutdown() }

    // 10. RESTING 期间 TV 仍 ON → 持续关机
    if newState == RESTING && rawOn { triggerShutdown() }

    // 11. 更新前端状态 + 日志
    updateMonitorStatus(...)
}
```

### 副作用函数

```go
func (m *Monitor) triggerShutdown() {
    // 1. TTS 播报（如有配置）
    // 2. 等待 5 秒
    // 3. 红外/遥控器关机（如有配置）
    // 保留现有 ha_client.go 的 TriggerShutdown 逻辑
}
```

### 保留不变的部分

- ROI 自动校准 + 重试逻辑
- 断流重连（maxFailures 计数器）
- Drain 缓冲帧优化
- `ha_client.go` 全部保留
- `AddLog` 日志系统

---

## 配置变更

### TVMonitorConfig 新增字段

```go
EdgeThreshold         float64 `yaml:"edge_threshold"`          // Laplacian 方差阈值（默认 30）
SaturationThreshold   float64 `yaml:"saturation_threshold"`    // S 通道均值阈值（默认 20）
MotionThreshold       float64 `yaml:"motion_threshold"`        // 帧间差异阈值（默认 5）
TargetDuration        int     `yaml:"target_duration"`          // 触发持续时长（秒，默认 300）
DebounceFrames        int     `yaml:"debounce_frames"`          // 防抖帧数（默认 3）
AutoCalibrateBaseline bool    `yaml:"auto_calibrate_baseline"`  // 启动时自适应校准（默认 false）
BaselineFrames        int     `yaml:"baseline_frames"`          // 基准采集帧数（默认 10）
```

### 保留字段（向后兼容）

| 旧字段 | 用途 | 映射 |
|--------|------|------|
| `brightness_threshold` | V 通道均值阈值 | 直接使用（语义扩展为 HSV V 通道） |
| `frame_diff_threshold` | 帧间差异阈值 | 如果 `motion_threshold` 未设置则回退读取此值 |
| `check_interval` | 检测间隔 | 默认从 30 改为 10 秒 |
| `max_session_minutes` | 单次观看限制 | 映射为 `target_duration` 的默认值来源 |

### ApplyDefaults 更新

```go
if cfg.CheckInterval == 0 { cfg.CheckInterval = 10 }
if cfg.EdgeThreshold == 0 { cfg.EdgeThreshold = 30 }
if cfg.SaturationThreshold == 0 { cfg.SaturationThreshold = 20 }
if cfg.MotionThreshold == 0 {
    cfg.MotionThreshold = cfg.FrameDiffThreshold  // 向后兼容
    if cfg.MotionThreshold == 0 { cfg.MotionThreshold = 5 }
}
if cfg.TargetDuration == 0 { cfg.TargetDuration = int(cfg.MaxSessionMinutes * 60) }
if cfg.DebounceFrames == 0 { cfg.DebounceFrames = 3 }
if cfg.BaselineFrames == 0 { cfg.BaselineFrames = 10 }
```

---

## 前端状态

### MonitorStatus 结构体

新增 `Metrics` 字段，其余保持不变：

```go
type MonitorMetrics struct {
    VMean     float64 `json:"v_mean"`
    SMean     float64 `json:"s_mean"`
    LapVar    float64 `json:"lap_var"`
    FrameDiff float64 `json:"frame_diff"`
}

type MonitorStatus struct {
    CameraID       string          `json:"camera_id"`
    State          string          `json:"state"`           // "OFF"/"PENDING"/"TRIGGERED"/"RESTING"
    TVOn           bool            `json:"tv_on"`
    Message        string          `json:"message,omitempty"`
    SessionStart   string          `json:"session_start,omitempty"`
    DailyMinutes   float64         `json:"daily_minutes"`
    MaxDailyMins   float64         `json:"max_daily_mins"`
    SessionMins    float64         `json:"session_mins,omitempty"`
    MaxSessionMins float64         `json:"max_session_mins"`
    RestRemaining  float64         `json:"rest_remaining,omitempty"`
    DailyLocked    bool            `json:"daily_locked"`
    LastUpdated    string          `json:"last_updated"`
    Metrics        MonitorMetrics  `json:"metrics"`         // 新增
}
```

### State 值映射

| 内部 State | 前端 State 字符串 |
|-----------|------------------|
| StateOff | `"OFF"` |
| StatePending | `"PENDING"` |
| StateTriggered | `"TRIGGERED"` |
| StateResting | `"RESTING"` |

前端需适配新的 State 值。现有前端使用 `"IDLE"` / `"WATCHING"` / `"RESTING"`，需更新为新的四态值。

---

## 边界情况处理

| 场景 | 处理 |
|------|------|
| 应用重启 | 状态丢失，从 OFF 开始重新检测 |
| 检测间隔变更 | triggerFrames = targetDuration / checkInterval 自动重算 |
| 跨天 | dailyMinutes 清零，dailyLocked 解除，RESTING → OFF |
| 超出监控时间 | ForceOff()，状态回到 OFF |
| 帧读取失败 | 不调用 StateMachine.Update，状态保持不变 |
| 自适应基准采集失败 | 回退到固定阈值，打印警告日志 |
| 静态壁纸（有色彩无动态） | `colorOK=true` 可通过，判定为开机 |
| 白色反光（有亮度无色彩） | `colorOK=false`，需 `motionOK=true` 才通过；纯静态反光判定为关机 |

---

## 默认配置参考

```yaml
tv_monitors:
  - camera_id: "摄像头1"
    enabled: false
    monitor_time: "08:00-23:00"
    roi_auto_calibrate: true
    check_interval: 10
    brightness_threshold: 15
    edge_threshold: 30
    saturation_threshold: 20
    motion_threshold: 5
    target_duration: 300
    debounce_frames: 3
    max_session_minutes: 5
    rest_minutes: 20
    max_daily_minutes: 60
    auto_calibrate_baseline: false
    baseline_frames: 10
    ha_url: "http://homeassistant.local:8123"
    ha_tts_message: "看电视时间到了，休息一下吧"
    log_level: "state"
```
