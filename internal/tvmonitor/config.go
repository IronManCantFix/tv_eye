package tvmonitor

import "github.com/r0n9/camkeep/constant"

// ApplyDefaults fills in zero/empty fields with sensible defaults.
func ApplyDefaults(cfg *constant.TVMonitorConfig) {
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 30
	}
	if cfg.BrightnessThreshold == 0 {
		cfg.BrightnessThreshold = 30
	}
	if cfg.FrameDiffThreshold == 0 {
		cfg.FrameDiffThreshold = 5.0
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
}
