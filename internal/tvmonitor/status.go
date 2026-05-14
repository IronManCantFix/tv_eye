package tvmonitor

import (
	"log"
	"sync"
	"time"
)

type MonitorStatus struct {
	CameraID       string  `json:"camera_id"`
	State          string  `json:"state"`
	Message        string  `json:"message,omitempty"`
	SessionStart   string  `json:"session_start,omitempty"`
	DailyMinutes   float64 `json:"daily_minutes"`
	MaxDailyMins   float64 `json:"max_daily_mins"`
	SessionMins    float64 `json:"session_mins,omitempty"`
	MaxSessionMins float64 `json:"max_session_mins"`
	RestRemaining  float64 `json:"rest_remaining,omitempty"`
	DailyLocked    bool    `json:"daily_locked"`
	LastUpdated    string  `json:"last_updated"`
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
	statusMap   = make(map[string]*MonitorStatus)
	statusMux   sync.RWMutex
	logEntries  []LogEntry
	logMux      sync.RWMutex
	snapshotMap = make(map[string][]byte)
	snapshotMux sync.RWMutex
)

func RegisterMonitor(cameraID string, maxSession, maxDaily float64) {
	statusMux.Lock()
	defer statusMux.Unlock()
	statusMap[cameraID] = &MonitorStatus{
		CameraID:      cameraID,
		State:         "IDLE",
		MaxSessionMins: maxSession,
		MaxDailyMins:  maxDaily,
		LastUpdated:   time.Now().Format("15:04:05"),
	}
}

func UnregisterMonitor(cameraID string) {
	statusMux.Lock()
	defer statusMux.Unlock()
	delete(statusMap, cameraID)
}

func UpdateMonitorStatus(cameraID string, state MonitorState, sessionStart time.Time, dailyMinutes float64, restStart time.Time, dailyLocked bool) {
	statusMux.Lock()
	defer statusMux.Unlock()

	s, ok := statusMap[cameraID]
	if !ok {
		return
	}
	s.State = state.String()
	s.DailyMinutes = dailyMinutes
	s.DailyLocked = dailyLocked
	s.LastUpdated = time.Now().Format("15:04:05")

	if state == StateWatching && !sessionStart.IsZero() {
		s.SessionStart = sessionStart.Format("15:04:05")
		s.SessionMins = time.Since(sessionStart).Minutes()
	} else {
		s.SessionStart = ""
		s.SessionMins = 0
	}

	if state == StateResting && !restStart.IsZero() {
		remaining := s.MaxSessionMins - time.Since(restStart).Minutes()
		if remaining < 0 {
			remaining = 0
		}
		s.RestRemaining = remaining
	} else {
		s.RestRemaining = 0
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

func UpdateSnapshot(cameraID string, jpeg []byte) {
	snapshotMux.Lock()
	defer snapshotMux.Unlock()
	snapshotMap[cameraID] = jpeg
	log.Printf("[tvmonitor] snapshot updated: %s (%d bytes)", cameraID, len(jpeg))
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
