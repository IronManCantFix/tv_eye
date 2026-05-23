package constant

import (
	"fmt"
	"sync"
)

// ROIPoint 表示 ROI 四边形的一个顶点 (百分比坐标 0.0~1.0)
type ROIPoint [2]float64

func (p *ROIPoint) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var slice []float64
	if err := unmarshal(&slice); err != nil {
		return err
	}
	if len(slice) != 2 {
		return fmt.Errorf("ROI point needs exactly 2 elements, got %d", len(slice))
	}
	(*p)[0] = slice[0]
	(*p)[1] = slice[1]
	return nil
}

func (p ROIPoint) IsZero() bool {
	return p[0] == 0 && p[1] == 0
}

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
	MotionDetect    bool   `yaml:"motion_detect"`    // 是否开启动检录制，仅 normal 模式生效
	// motionDetectRatioThreshold: 判定发生运动的变化像素比例阈值，仅 motion_detect=true 时生效。
	MotionDetectRatioThreshold float64 `yaml:"motionDetectRatioThreshold"`

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
	ROITopLeft      ROIPoint `yaml:"roi_top_left"`
	ROITopRight     ROIPoint `yaml:"roi_top_right"`
	ROIBottomRight  ROIPoint `yaml:"roi_bottom_right"`
	ROIBottomLeft   ROIPoint `yaml:"roi_bottom_left"`
	ROIAutoCalibrate    bool    `yaml:"roi_auto_calibrate"`
	CheckInterval       int     `yaml:"check_interval"`
	BrightnessThreshold float64 `yaml:"brightness_threshold"`
	FrameDiffThreshold  float64 `yaml:"frame_diff_threshold"`
	EdgeThreshold       float64 `yaml:"edge_threshold"`
	SaturationThreshold float64 `yaml:"saturation_threshold"`
	MotionThreshold     float64 `yaml:"motion_threshold"`
	TargetDuration      int     `yaml:"target_duration"`
	DebounceFrames      int     `yaml:"debounce_frames"`
	AutoCalibrateBaseline bool  `yaml:"auto_calibrate_baseline"`
	BaselineFrames      int     `yaml:"baseline_frames"`
	MaxSessionMinutes   float64 `yaml:"max_session_minutes"`
	RestMinutes         float64 `yaml:"rest_minutes"`
	MaxDailyMinutes     float64 `yaml:"max_daily_minutes"`
	ActionGraceSec      int     `yaml:"action_grace_sec"` // 红外/遥控触发后保护期(秒)，期间假定电视已关闭，防止 toggle 型遥控被重复按下重新开机
	HAURL               string  `yaml:"ha_url"`
	HAToken             string  `yaml:"ha_token"`
	HAControlService    string  `yaml:"ha_control_service"`    // 控制遥控器的 HA 服务 (如 remote.turn_off)，留空则不控制
	HAControlEntityID   string  `yaml:"ha_control_entity_id"`  // 遥控器实体 ID (如 remote.tv_remote)
	HATTSEntityID       string  `yaml:"ha_tts_entity_id"`      // TTS 播报实体 ID (如 media_player.xiao_ai)，留空则不播报
	HATTSMessage        string  `yaml:"ha_tts_message"`        // TTS 播报文本
	HAIRTurnOffButtonID string  `yaml:"ha_ir_turn_off_button"` // 红外关机按钮实体 ID
	HATTSService        string  `yaml:"ha_tts_service"`        // 音箱播放文本的 HA 服务 (如 notify.xiaomi_cn_xxx)
	HANotifyService     string  `yaml:"ha_notify_service"`     // 微信通知的 HA 服务 (如 hassbox_notify.hassbox_notify)，留空则不发送通知
	// 三种动作的独立开关。指针类型 nil 视为启用 (向后兼容)，仅当显式设为 false 时关闭。
	EnableTVShutdown    *bool `yaml:"enable_tv_shutdown,omitempty"`    // 是否执行 HA 关闭电视 (红外/遥控)
	EnableVoiceNotify   *bool `yaml:"enable_voice_notify,omitempty"`   // 是否执行音箱语音播报
	EnablePhoneNotify   *bool `yaml:"enable_phone_notify,omitempty"`   // 是否发送微信/手机推送通知
	LogLevel            string  `yaml:"log_level"`             // 日志级别: "state"(默认,仅状态变化), "tick"(每次检测), "summary"(每5分钟摘要)
}

// IsTVShutdownEnabled 返回 EnableTVShutdown 的实际值，nil 视为 true。
func (c *TVMonitorConfig) IsTVShutdownEnabled() bool {
	return c.EnableTVShutdown == nil || *c.EnableTVShutdown
}

// IsVoiceNotifyEnabled 返回 EnableVoiceNotify 的实际值，nil 视为 true。
func (c *TVMonitorConfig) IsVoiceNotifyEnabled() bool {
	return c.EnableVoiceNotify == nil || *c.EnableVoiceNotify
}

// IsPhoneNotifyEnabled 返回 EnablePhoneNotify 的实际值，nil 视为 true。
func (c *TVMonitorConfig) IsPhoneNotifyEnabled() bool {
	return c.EnablePhoneNotify == nil || *c.EnablePhoneNotify
}

// Config 对应 yaml 配置文件
type Config struct {
	DailyMerge DailyMergeConfig  `yaml:"daily_merge"`
	Cameras    []Camera          `yaml:"cameras"`
	TVMonitors []TVMonitorConfig `yaml:"tv_monitors"`
}
