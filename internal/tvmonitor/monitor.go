package tvmonitor

import (
	"context"
	"fmt"
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
	restHandled  bool // prevents repeated HA calls during rest violations
	tickCount    int
	mu           sync.Mutex
}

func NewTVMonitor(cfg constant.TVMonitorConfig, rtspURL string) *TVMonitor {
	m := &TVMonitor{
		config:  cfg,
		rtspURL: rtspURL,
		state:   StateIdle,
		ha:      NewHAClient(cfg),
	}
	RegisterMonitor(cfg.CameraID, cfg.MaxSessionMinutes, cfg.MaxDailyMinutes)
	return m
}

// Run is the main goroutine. Blocks until context is cancelled.
func (m *TVMonitor) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	defer UnregisterMonitor(m.config.CameraID)
	defer RemoveSnapshot(m.config.CameraID)

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

	// If no valid ROI, enter waiting state and retry periodically
	if m.config.ROIW <= 0 || m.config.ROIH <= 0 {
		log.Printf("[tvmonitor:%s] No valid ROI configured, waiting for manual configuration", m.config.CameraID)
		SetMonitorMessage(m.config.CameraID, "NO_ROI", "未配置电视区域。请在 conf.yaml 中设置 roi_x/roi_y/roi_w/roi_h，或确保摄像头画面中有明显的电视屏幕边框以启用自动校准。")
		m.waitForROIRetry(ctx)
		return
	}

	interval := time.Duration(m.config.CheckInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	m.lastDate = time.Now().Format("2006-01-02")

	var cap *gocv.VideoCapture
	var frame gocv.Mat
	var detector *Detector

	// reconnectAttempts counts consecutive failures; triggers reconnect after threshold.
	// Reset on any successful tick.
	reconnectAttempts := 0
	const maxFailures = 10

	for {
		// Ensure we have an open capture and a valid first frame.
		// This loop handles initial connection and reconnection after stream failures.
		if cap == nil || !cap.IsOpened() {
			detector = nil
			if frame.Ptr() != nil {
				frame.Close()
			}
			newCap, err := gocv.OpenVideoCapture(m.rtspURL)
			if err != nil {
				log.Printf("[tvmonitor:%s] Failed to open RTSP stream: %v", m.config.CameraID, err)
				SetMonitorMessage(m.config.CameraID, "ERROR", "无法连接摄像头视频流，等待重试...")
				cap = nil
				// Wait before retrying
				select {
				case <-ctx.Done():
					log.Printf("[tvmonitor:%s] Monitor stopped", m.config.CameraID)
					return
				case <-ticker.C:
					continue
				}
			}

			newFrame := gocv.NewMat()
			if ok := newCap.Read(&newFrame); !ok || newFrame.Empty() {
				log.Printf("[tvmonitor:%s] Failed to read initial frame", m.config.CameraID)
				SetMonitorMessage(m.config.CameraID, "ERROR", "无法读取视频画面，等待重试...")
				newCap.Close()
				newFrame.Close()
				cap = nil
				select {
				case <-ctx.Done():
					log.Printf("[tvmonitor:%s] Monitor stopped", m.config.CameraID)
					return
				case <-ticker.C:
					continue
				}
			}

			cap = newCap
			frame = newFrame
			detector = NewDetector(m.config, frame.Cols(), frame.Rows())
			m.detector = detector
			log.Printf("[tvmonitor:%s] Stream connected successfully", m.config.CameraID)
			SetMonitorMessage(m.config.CameraID, "IDLE", "")
			reconnectAttempts = 0
		}

		select {
		case <-ctx.Done():
			log.Printf("[tvmonitor:%s] Monitor stopped", m.config.CameraID)
			if detector != nil {
				detector.Close()
			}
			if frame.Ptr() != nil {
				frame.Close()
			}
			if cap != nil {
				cap.Close()
			}
			return
		case <-ticker.C:
			if cap == nil {
				continue
			}
			action := m.tick(cap, &frame)
			if action == tickActionReconnect {
				reconnectAttempts++
				if reconnectAttempts >= maxFailures {
					log.Printf("[tvmonitor:%s] Too many failures, closing stream for reconnect", m.config.CameraID)
					if detector != nil {
						detector.Close()
						detector = nil
						m.detector = nil
					}
					cap.Close()
					cap = nil
					// Next loop iteration will attempt to reconnect
				}
			} else {
				reconnectAttempts = 0
			}
		}
	}
}

// waitForROIRetry periodically attempts auto-calibration while waiting for a valid ROI.
// This allows the monitor to become active if the TV becomes visible later.
func (m *TVMonitor) waitForROIRetry(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.config.ROIAutoCalibrate {
				log.Printf("[tvmonitor:%s] Retrying auto-calibration...", m.config.CameraID)
				if x, y, w, h, err := AutoCalibrateROI(m.rtspURL, m.config); err != nil {
					log.Printf("[tvmonitor:%s] Auto-calibration still failing: %v", m.config.CameraID, err)
					SetMonitorMessage(m.config.CameraID, "NO_ROI", "未配置电视区域。请在 conf.yaml 中设置 roi_x/roi_y/roi_w/roi_h，或确保摄像头画面中有明显的电视屏幕边框以启用自动校准。")
				} else {
					m.config.ROIX, m.config.ROIY, m.config.ROIW, m.config.ROIH = x, y, w, h
					log.Printf("[tvmonitor:%s] Auto-calibration succeeded on retry!", m.config.CameraID)
					SetMonitorMessage(m.config.CameraID, "IDLE", "")
					return
				}
			}
		}
	}
}

type tickAction int

const (
	tickActionNone tickAction = iota
	tickActionReconnect
)

func (m *TVMonitor) tick(cap *gocv.VideoCapture, frame *gocv.Mat) tickAction {
	m.mu.Lock()

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

	// Skip everything if outside monitor time range
	if !util.IsWithinTimeRange(m.config.MonitorTime) {
		m.mu.Unlock()
		return tickActionNone
	}

	// Drain buffered frames: Grab (skip without decoding) is much faster than Read
	fps := cap.Get(gocv.VideoCaptureFPS)
	if fps < 1 { fps = 25 }
	staleFrames := int(fps * float64(m.config.CheckInterval))
	// Grab skips frames without decoding - very fast
	if staleFrames > 1 {
		cap.Grab(staleFrames - 1)
	}
	// Retrieve the latest frame (decode only this one)
	if !cap.Retrieve(frame) || frame.Empty() {
		m.mu.Unlock()
		return tickActionReconnect
	}

	tvOn := m.detector.TVState(*frame)

	if jpeg := m.detector.DrawROI(*frame); jpeg != nil {
		UpdateSnapshot(m.config.CameraID, jpeg)
	}

	log.Printf("[tvmonitor:%s] tick: tvOn=%v lapVar=%.1f state=%s",
		m.config.CameraID, tvOn, m.detector.lastLaplacianVar, m.state)

	// Log detect result based on log_level config
	m.tickCount++
	logLevel := m.config.LogLevel
	if logLevel == "" {
		logLevel = "state"
	}
	switch logLevel {
	case "tick":
		AddLog(m.config.CameraID, "detect", fmt.Sprintf("检测: %s (LAP:%.1f)", map[bool]string{true: "开启", false: "关闭"}[tvOn], m.detector.lastLaplacianVar))
	case "summary":
		// Log every 5 minutes (20 ticks at 15s interval)
		if m.tickCount % 20 == 0 {
			AddLog(m.config.CameraID, "detect", fmt.Sprintf("定时摘要: %s (LAP:%.1f)", map[bool]string{true: "开启", false: "关闭"}[tvOn], m.detector.lastLaplacianVar))
		}
	default: // "state" - no log for routine detection, only state changes logged below
	}


	// Collect HA actions to perform outside the lock
	var haAction func()

	switch m.state {
	case StateIdle:
		if tvOn {
			log.Printf("[tvmonitor:%s] TV detected ON", m.config.CameraID)
			m.sessionStart = time.Now()
			m.dailyMinutes += float64(m.config.CheckInterval) / 60.0
			m.setState(StateWatching)
			AddLog(m.config.CameraID, "tv_on", "检测到电视开机，开始计时")
		}

	case StateWatching:
		if !tvOn {
			log.Printf("[tvmonitor:%s] TV turned off naturally", m.config.CameraID)
			m.setState(StateIdle)
			AddLog(m.config.CameraID, "tv_off", "电视已关闭")
		} else {
			m.dailyMinutes += float64(m.config.CheckInterval) / 60.0
			sessionMin := time.Since(m.sessionStart).Minutes()

			if m.dailyMinutes >= m.config.MaxDailyMinutes {
				log.Printf("[tvmonitor:%s] Daily total %.1fmin exceeded limit %.0fmin, locked until midnight",
					m.config.CameraID, m.dailyMinutes, m.config.MaxDailyMinutes)
				m.dailyLocked = true
				prefix := m.prefix()
				msg := m.config.HATTSMessage
				ha := m.ha
				m.restStart = time.Now()
				m.setState(StateResting)
				m.restHandled = false
				haAction = func() { ha.TriggerShutdown(prefix, msg) }
				AddLog(m.config.CameraID, "daily_exceeded", fmt.Sprintf("今日观看 %.1f 分钟超限，锁定至次日零点", m.dailyMinutes))
			} else if sessionMin >= m.config.MaxSessionMinutes {
				log.Printf("[tvmonitor:%s] Session %.1fmin exceeded limit %.0fmin, turning off TV",
					m.config.CameraID, sessionMin, m.config.MaxSessionMinutes)
				prefix := m.prefix()
				msg := m.config.HATTSMessage
				ha := m.ha
				m.restStart = time.Now()
				m.setState(StateResting)
				m.restHandled = false
				haAction = func() { ha.TriggerShutdown(prefix, msg) }
				AddLog(m.config.CameraID, "session_exceeded", fmt.Sprintf("单次观看 %.0f 分钟超限，关闭电视", m.config.MaxSessionMinutes))
			}
		}

	case StateResting:
		if tvOn {
			remaining := m.config.RestMinutes - time.Since(m.restStart).Minutes()
			log.Printf("[tvmonitor:%s] TV on during rest (%.1fmin remaining), turning off",
				m.config.CameraID, remaining)
			prefix := m.prefix()
			ha := m.ha
			haAction = func() { ha.TriggerShutdown(prefix, "休息时间还没到哦，再等一下") }
			if !m.restHandled {
				AddLog(m.config.CameraID, "rest_violation", "休息期间电视被打开，持续关闭中")
			}
			m.restHandled = true
		} else {
			m.restHandled = false
		}

		restElapsed := time.Since(m.restStart).Minutes()
		if restElapsed >= m.config.RestMinutes && !m.dailyLocked {
			log.Printf("[tvmonitor:%s] Rest period complete, ready for next session", m.config.CameraID)
			m.setState(StateIdle)
				AddLog(m.config.CameraID, "rest_complete", "休息时间结束，可继续观看")
		}
	}

	m.mu.Unlock()

	// Perform HA action outside the lock to avoid blocking state machine
	if haAction != nil {
		haAction()
	}

	return tickActionNone
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
