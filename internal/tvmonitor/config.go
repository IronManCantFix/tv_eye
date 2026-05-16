package tvmonitor

import "github.com/r0n9/camkeep/constant"

// ApplyDefaults fills in zero/empty fields with sensible defaults.
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
