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

// haCooldown limits HA calls to at most once per this duration.
const haCooldown = 30 * time.Second

type TVMonitor struct {
	config       constant.TVMonitorConfig
	rtspURL      string
	sm           *StateMachine
	ha           *HAClient
	detector     *Detector

	sessionStart time.Time
	dailyMinutes float64
	lastDate     string
	restStart    time.Time
	dailyLocked  bool
	tickCount            int
	restViolationLogged  bool
	mu                   sync.Mutex

	lastHACall   time.Time
}

func NewTVMonitor(cfg constant.TVMonitorConfig, rtspURL string) *TVMonitor {
	m := &TVMonitor{
		config:  cfg,
		rtspURL: rtspURL,
		ha:      NewHAClient(cfg),
	}
	RegisterMonitor(cfg.CameraID, cfg.MaxSessionMinutes, cfg.MaxDailyMinutes, cfg.RestMinutes, cfg.TargetDuration, cfg.MonitorTime)
	return m
}

// Run is the main goroutine. Blocks until context is cancelled.
func (m *TVMonitor) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	defer UnregisterMonitor(m.config.CameraID)
	defer RemoveSnapshot(m.config.CameraID)

	triggerFrames := m.config.TargetDuration / m.config.CheckInterval
	if triggerFrames < m.config.DebounceFrames {
		triggerFrames = m.config.DebounceFrames
	}
	m.sm = NewStateMachine(m.config.DebounceFrames, triggerFrames)

	log.Printf("[tvmonitor:%s] Starting monitor (trigger=%ds/%dframes, debounce=%d, rest=%.0fm, daily=%.0fm)",
		m.config.CameraID, m.config.TargetDuration, triggerFrames, m.config.DebounceFrames, m.config.RestMinutes, m.config.MaxDailyMinutes)

	// Try auto-calibrate if ROI is zero and auto_calibrate is enabled
	if m.config.ROIAutoCalibrate && (m.config.ROITopLeft.IsZero() && m.config.ROITopRight.IsZero()) {
		log.Printf("[tvmonitor:%s] Attempting auto-calibration...", m.config.CameraID)
		cap, err := gocv.OpenVideoCapture(m.rtspURL)
		if err == nil {
			frame := gocv.NewMat()
			if cap.Read(&frame) && !frame.Empty() {
				fw, fh := float64(frame.Cols()), float64(frame.Rows())
				frame.Close()
				if tl, tr, br, bl, err := AutoCalibrateROI(m.rtspURL, fw, fh); err != nil {
					log.Printf("[tvmonitor:%s] Auto-calibration failed: %v (will use manual ROI)", m.config.CameraID, err)
				} else {
					m.config.ROITopLeft, m.config.ROITopRight = tl, tr
					m.config.ROIBottomRight, m.config.ROIBottomLeft = br, bl
				}
			} else {
				frame.Close()
			}
			cap.Close()
		}
	}

	// If no valid ROI, enter waiting state
	if m.config.ROITopLeft.IsZero() && m.config.ROITopRight.IsZero() && m.config.ROIBottomRight.IsZero() && m.config.ROIBottomLeft.IsZero() {
		log.Printf("[tvmonitor:%s] No valid ROI configured, waiting for manual configuration", m.config.CameraID)
		SetMonitorMessage(m.config.CameraID, "NO_ROI", "未配置电视区域。请在 conf.yaml 中设置 ROI，或确保摄像头画面中有明显的电视屏幕边框以启用自动校准。")
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

	reconnectAttempts := 0
	const maxFailures = 10

	for {
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
			cap.Set(gocv.VideoCaptureBufferSize, 1)
			frame = newFrame
			detector = NewDetector(m.config, frame.Cols(), frame.Rows())
			m.detector = detector

			if m.config.AutoCalibrateBaseline {
				log.Printf("[tvmonitor:%s] Collecting baseline (%d frames)...", m.config.CameraID, m.config.BaselineFrames)
				if err := detector.CalibrateBaseline(cap, m.config.BaselineFrames); err != nil {
					log.Printf("[tvmonitor:%s] Baseline calibration failed: %v (using fixed thresholds)", m.config.CameraID, err)
				}
				freshFrame := gocv.NewMat()
				if ok := cap.Read(&freshFrame); ok && !freshFrame.Empty() {
					frame.Close()
					frame = freshFrame
				} else {
					freshFrame.Close()
				}
			}

			log.Printf("[tvmonitor:%s] Stream connected successfully", m.config.CameraID)
			SetMonitorMessage(m.config.CameraID, StateOff.String(), "")
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
			action := m.tick(cap, &frame, detector)
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
				}
			} else {
				reconnectAttempts = 0
			}
		}
	}
}

// waitForROIRetry periodically attempts auto-calibration while waiting for a valid ROI.
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
				cap, err := gocv.OpenVideoCapture(m.rtspURL)
				if err == nil {
					frame := gocv.NewMat()
					if cap.Read(&frame) && !frame.Empty() {
						fw, fh := float64(frame.Cols()), float64(frame.Rows())
						frame.Close()
						if tl, tr, br, bl, err := AutoCalibrateROI(m.rtspURL, fw, fh); err != nil {
							log.Printf("[tvmonitor:%s] Auto-calibration still failing: %v", m.config.CameraID, err)
							SetMonitorMessage(m.config.CameraID, "NO_ROI", "未配置电视区域。请在 conf.yaml 中设置 ROI，或确保摄像头画面中有明显的电视屏幕边框以启用自动校准。")
						} else {
							m.config.ROITopLeft, m.config.ROITopRight = tl, tr
							m.config.ROIBottomRight, m.config.ROIBottomLeft = br, bl
							log.Printf("[tvmonitor:%s] Auto-calibration succeeded on retry!", m.config.CameraID)
							SetMonitorMessage(m.config.CameraID, StateOff.String(), "")
							cap.Close()
							return
						}
					} else {
						frame.Close()
					}
					cap.Close()
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

func (m *TVMonitor) tick(cap *gocv.VideoCapture, frame *gocv.Mat, detector *Detector) tickAction {
	m.mu.Lock()

	// Daily reset at midnight
	today := time.Now().Format("2006-01-02")
	if today != m.lastDate {
		log.Printf("[tvmonitor:%s] Daily reset, all counters cleared", m.config.CameraID)
		m.dailyMinutes = 0
		m.dailyLocked = false
		m.lastDate = today
		m.sm.ForceOff()
	}

	// Outside monitor time range
	if !util.IsWithinTimeRange(m.config.MonitorTime) {
		if m.sm.State() != StateOff {
			m.sm.ForceOff()
			AddLog(m.config.CameraID, "out_of_range", "超出监控时间范围，状态重置")
		}
		UpdateMonitorStatus(m.config.CameraID, m.sm.State(), false, m.sessionStart, m.dailyMinutes, m.restStart, m.dailyLocked, Metrics{})
		m.mu.Unlock()
		return tickActionNone
	}

	// Read the latest frame from the RTSP stream.
	// BufferSize is set to 1 on connect, so Read() returns the most recent
	// buffered frame. We still drain any remaining stale frames by reading
	// multiple times and keeping only the last one.
	if !cap.Read(frame) || frame.Empty() {
		UpdateMonitorStatus(m.config.CameraID, m.sm.State(), false, m.sessionStart, m.dailyMinutes, m.restStart, m.dailyLocked, Metrics{})
		m.mu.Unlock()
		return tickActionReconnect
	}

	rawOn, metrics := detector.TVState(*frame)

	// On-demand snapshot: only generate when frontend requests it
	if ConsumeSnapshotRequest(m.config.CameraID) {
		if jpeg := detector.DrawROI(*frame); jpeg != nil {
			UpdateSnapshot(m.config.CameraID, jpeg)
		}
	}

	m.tickCount++
	logLevel := m.config.LogLevel
	if logLevel == "" {
		logLevel = "state"
	}
	switch logLevel {
	case "tick":
		AddLog(m.config.CameraID, "detect", fmt.Sprintf("检测: %s (V:%.0f Lap:%.1f S:%.0f D:%.0f)",
			map[bool]string{true: "开启", false: "关闭"}[rawOn], metrics.VMean, metrics.LapVar, metrics.SMean, metrics.FrameDiff))
		log.Printf("[tvmonitor:%s] tick: rawOn=%v state=%s onFrame=%d",
			m.config.CameraID, rawOn, m.sm.State(), m.sm.OnFrameCount())
	case "summary":
		if m.tickCount%20 == 0 {
			AddLog(m.config.CameraID, "detect", fmt.Sprintf("定时摘要: %s (V:%.0f Lap:%.1f)",
				map[bool]string{true: "开启", false: "关闭"}[rawOn], metrics.VMean, metrics.LapVar))
		}
	}

	// Run state machine
	prevState := m.sm.State()
	newState, shouldTrigger := m.sm.Update(rawOn)

	// Track daily viewing time based on debounced state (not rawOn)
	if newState == StatePending || newState == StateTriggered {
		m.dailyMinutes += float64(m.config.CheckInterval) / 60.0
	}

	// Collect HA action to perform outside the lock
	var haAction func()

	if newState != prevState {
		log.Printf("[tvmonitor:%s] State: %s → %s", m.config.CameraID, prevState, newState)
	}

	switch newState {
	case StateOff:
		if prevState == StatePending || prevState == StateTriggered {
			AddLog(m.config.CameraID, "tv_off", "电视已关闭")
		}

	case StatePending:
		if prevState == StateOff {
			m.sessionStart = time.Now()
			AddLog(m.config.CameraID, "tv_on", "检测到电视开机，开始计时")
		}

	case StateTriggered:
		if shouldTrigger {
			sessionMin := time.Since(m.sessionStart).Minutes()
			if prevState == StatePending {
				AddLog(m.config.CameraID, "session_exceeded", fmt.Sprintf("单次观看 %.0f 分钟超限，关闭电视", sessionMin))
			} else {
				AddLog(m.config.CameraID, "re_trigger", fmt.Sprintf("电视仍未关闭（已 %.0f 分钟），再次尝试关机", sessionMin))
			}
			haAction = m.makeHAAction("")
		}

	case StateResting:
		if !m.sm.RestStartSet() {
			m.restStart = time.Now()
			m.sm.SetRestStartSet(true)
		}
		// Check if rest period has elapsed (and not daily locked)
		restElapsed := time.Since(m.restStart).Minutes()
		if restElapsed >= m.config.RestMinutes && !m.dailyLocked {
			log.Printf("[tvmonitor:%s] Rest period complete, ready for next session", m.config.CameraID)
			m.sm.ForceOff()
			AddLog(m.config.CameraID, "rest_complete", "休息时间结束，可继续观看")
		} else if rawOn {
			// Rest violation: bypass cooldown to ensure TV is turned off immediately
			haAction = m.forceShutdown("休息时间还没到哦，再等一下")
			if !m.restViolationLogged {
				AddLog(m.config.CameraID, "rest_violation", "休息期间电视被打开，持续关闭中")
				m.restViolationLogged = true
			}
		} else {
			m.restViolationLogged = false
		}
	}

	// Daily limit check (overrides state machine)
	// Skip if just exited resting to avoid re-entering rest immediately
	if m.dailyMinutes >= m.config.MaxDailyMinutes && rawOn {
		m.dailyLocked = true
		if m.sm.State() != StateResting && newState != StateResting {
			m.sm.ForceResting()
			m.restStart = time.Now()
			m.sm.SetRestStartSet(true)
			haAction = m.forceShutdown("")
			AddLog(m.config.CameraID, "daily_exceeded", fmt.Sprintf("今日观看 %.1f 分钟超限，锁定至次日零点", m.dailyMinutes))
		}
	}

	UpdateMonitorStatus(m.config.CameraID, m.sm.State(), rawOn, m.sessionStart, m.dailyMinutes, m.restStart, m.dailyLocked, metrics)

	m.mu.Unlock()

	// Execute HA action asynchronously with cooldown
	if haAction != nil {
		go haAction()
	}

	return tickActionNone
}

// makeHAAction returns an HA action closure, or nil if still within cooldown.
func (m *TVMonitor) makeHAAction(ttsOverride string) func() {
	if time.Since(m.lastHACall) < haCooldown {
		return nil
	}
	m.lastHACall = time.Now()
	prefix := m.prefix()
	msg := m.config.HATTSMessage
	ha := m.ha
	if ttsOverride != "" {
		return func() { ha.TriggerShutdown(prefix, ttsOverride) }
	}
	return func() { ha.TriggerShutdown(prefix, msg) }
}

// forceShutdown creates an HA action that bypasses the normal cooldown.
// Used for rest violations where the TV must be turned off immediately.
func (m *TVMonitor) forceShutdown(ttsOverride string) func() {
	m.lastHACall = time.Now()
	prefix := m.prefix()
	msg := m.config.HATTSMessage
	ha := m.ha
	if ttsOverride != "" {
		return func() { ha.TriggerShutdown(prefix, ttsOverride) }
	}
	return func() { ha.TriggerShutdown(prefix, msg) }
}

func (m *TVMonitor) prefix() string {
	return "tvmonitor:" + m.config.CameraID
}
