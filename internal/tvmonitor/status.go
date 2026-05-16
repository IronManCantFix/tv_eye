package tvmonitor

import (
	"sync"
	"time"
)

type MonitorStatus struct {
	CameraID         string  `json:"camera_id"`
	State            string  `json:"state"`
	TVOn             bool    `json:"tv_on"`
	Message          string  `json:"message,omitempty"`
	SessionStart     string  `json:"session_start,omitempty"`
	DailyMinutes     float64 `json:"daily_minutes"`
	MaxDailyMins     float64 `json:"max_daily_mins"`
	SessionMins      float64 `json:"session_mins,omitempty"`
	MaxSessionMins   float64 `json:"max_session_mins"`
	TargetDuration   int     `json:"target_duration"`
	SessionTarget    string  `json:"session_target,omitempty"` // "HH:MM:SS" countdown target
	RestRemaining    float64 `json:"rest_remaining,omitempty"`
	RestMinutes      float64 `json:"rest_minutes"`
	RestTarget       string  `json:"rest_target,omitempty"` // "HH:MM:SS" countdown target
	DailyLocked      bool    `json:"daily_locked"`
	LastUpdated      string  `json:"last_updated"`
	Metrics          Metrics `json:"metrics"`
}

type LogEntry struct {
	Time      string `json:"time"`
	Date      string `json:"date"`
	CameraID  string `json:"camera_id"`
	EventType string `json:"event_type"`
	Message   string `json:"message"`
}

const maxLogs = 200

var (
	statusMap      = make(map[string]*MonitorStatus)
	statusMux      sync.RWMutex
	logEntries     []LogEntry
	logMux         sync.RWMutex
	snapshotMap    = make(map[string][]byte)
	snapshotMux    sync.RWMutex
	snapshotReqMap = make(map[string]bool)
	snapshotReqMux sync.Mutex
)

func RegisterMonitor(cameraID string, maxSession, maxDaily, restMinutes float64, targetDuration int) {
	statusMux.Lock()
	defer statusMux.Unlock()
	statusMap[cameraID] = &MonitorStatus{
		CameraID:       cameraID,
		State:          StateOff.String(),
		MaxSessionMins: maxSession,
		MaxDailyMins:   maxDaily,
		RestMinutes:    restMinutes,
		TargetDuration: targetDuration,
		LastUpdated:    time.Now().Format("15:04:05"),
	}
}

func UnregisterMonitor(cameraID string) {
	statusMux.Lock()
	defer statusMux.Unlock()
	delete(statusMap, cameraID)
}

func UpdateMonitorStatus(cameraID string, state State, tvOn bool, sessionStart time.Time, dailyMinutes float64, restStart time.Time, dailyLocked bool, metrics Metrics) {
	statusMux.Lock()
	defer statusMux.Unlock()

	s, ok := statusMap[cameraID]
	if !ok {
		return
	}
	s.State = state.String()
	s.TVOn = tvOn
	s.DailyMinutes = dailyMinutes
	s.DailyLocked = dailyLocked
	s.LastUpdated = time.Now().Format("15:04:05")
	s.Metrics = metrics

	if (state == StatePending || state == StateTriggered) && !sessionStart.IsZero() {
		s.SessionStart = sessionStart.Format("15:04:05")
		s.SessionMins = time.Since(sessionStart).Minutes()
		target := sessionStart.Add(time.Duration(s.TargetDuration) * time.Second)
		s.SessionTarget = target.Format("15:04:05")
	} else {
		s.SessionStart = ""
		s.SessionMins = 0
		s.SessionTarget = ""
	}

	if state == StateResting && !restStart.IsZero() {
		remaining := s.RestMinutes - time.Since(restStart).Minutes()
		if remaining < 0 {
			remaining = 0
		}
		s.RestRemaining = remaining
		restEnd := restStart.Add(time.Duration(s.RestMinutes) * time.Minute)
		s.RestTarget = restEnd.Format("15:04:05")
	} else {
		s.RestRemaining = 0
		s.RestTarget = ""
	}
}

func SetMonitorMessage(cameraID, state, message string) {
	statusMux.Lock()
	defer statusMux.Unlock()
	s, ok := statusMap[cameraID]
	if !ok {
		return
	}
	s.State = state
	s.Message = message
	s.LastUpdated = time.Now().Format("15:04:05")
}

func AddLog(cameraID, eventType, message string) {
	logMux.Lock()
	defer logMux.Unlock()
	now := time.Now()
	entry := LogEntry{
		Time:      now.Format("15:04:05"),
		Date:      now.Format("2006-01-02"),
		CameraID:  cameraID,
		EventType: eventType,
		Message:   message,
	}
	logEntries = append(logEntries, entry)
	if len(logEntries) > maxLogs {
		logEntries = logEntries[len(logEntries)-maxLogs:]
	}
}

func GetAllStatuses() []MonitorStatus {
	statusMux.RLock()
	defer statusMux.RUnlock()
	result := make([]MonitorStatus, 0, len(statusMap))
	for _, s := range statusMap {
		result = append(result, *s)
	}
	return result
}

func GetRecentLogs(max int) []LogEntry {
	logMux.RLock()
	defer logMux.RUnlock()
	today := time.Now().Format("2006-01-02")
	var todayLogs []LogEntry
	for _, e := range logEntries {
		if e.Date == today {
			todayLogs = append(todayLogs, e)
		}
	}
	if max <= 0 || max > len(todayLogs) {
		max = len(todayLogs)
	}
	start := len(todayLogs) - max
	result := make([]LogEntry, max)
	copy(result, todayLogs[start:])
	return result
}

func ClearLogs() {
	logMux.Lock()
	defer logMux.Unlock()
	logEntries = nil
}

// RequestSnapshot sets a flag so the next tick generates a snapshot.
func RequestSnapshot(cameraID string) {
	snapshotReqMux.Lock()
	defer snapshotReqMux.Unlock()
	snapshotReqMap[cameraID] = true
}

// ConsumeSnapshotRequest checks and clears the snapshot request flag.
func ConsumeSnapshotRequest(cameraID string) bool {
	snapshotReqMux.Lock()
	defer snapshotReqMux.Unlock()
	v := snapshotReqMap[cameraID]
	delete(snapshotReqMap, cameraID)
	return v
}

func UpdateSnapshot(cameraID string, jpeg []byte) {
	snapshotMux.Lock()
	defer snapshotMux.Unlock()
	snapshotMap[cameraID] = jpeg
}

func GetSnapshot(cameraID string) ([]byte, bool) {
	snapshotMux.RLock()
	defer snapshotMux.RUnlock()
	data, ok := snapshotMap[cameraID]
	return data, ok
}

func RemoveSnapshot(cameraID string) {
	snapshotMux.Lock()
	defer snapshotMux.Unlock()
	delete(snapshotMap, cameraID)
}
