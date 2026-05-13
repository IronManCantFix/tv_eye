package constant

import "sync"

var ConfigMux sync.RWMutex

type Camera struct {
	ID              string `yaml:"id"`
	RTSPUrl         string `yaml:"rtsp_url"`
	RetentionDays   int    `yaml:"retention_days"`
	SegmentDuration int    `yaml:"segment_duration"`
	Format          string `yaml:"format"`
	MinSizeKb       int64  `yaml:"min_size_kb"`
	RecordTime      string `yaml:"record_time"`
	Mode            string `yaml:"mode"`             // 模式: "normal" 或 "timelapse"，留空默认为 normal
	CaptureInterval int    `yaml:"capture_interval"` // 抓拍间隔(秒)，例如 5 表示每5秒抓一帧

	AutoDiscovered bool `yaml:"auto_discovered"` // 标识这个流是手动配置的，还是从 go2rtc 自动发现的
}

type DailyMergeConfig struct {
	Enabled bool   `yaml:"enabled"`
	Time    string `yaml:"time"`
}

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

// Config 对应 yaml 配置文件
type Config struct {
	DailyMerge DailyMergeConfig  `yaml:"daily_merge"`
	Cameras    []Camera          `yaml:"cameras"`
	TVMonitors []TVMonitorConfig `yaml:"tv_monitors"`
}
