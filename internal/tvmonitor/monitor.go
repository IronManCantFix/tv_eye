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

	cap, err := gocv.OpenVideoCapture(m.rtspURL)
	if err != nil {
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
