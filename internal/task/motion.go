package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/r0n9/camkeep/constant"
)

const (
	motionDetectFrameWidth       = 32
	motionDetectFrameHeight      = 24
	motionDetectFPS              = 2
	motionDetectPixelThreshold   = 15
	motionDetectRatioThreshold   = 0.01
	motionDetectRestartDelay     = 3 * time.Second
	motionDetectIdleLogInterval  = 5 * time.Second
	motionDetectAlertLogInterval = 2 * time.Second
)

type motionFrameStats struct {
	DiffSum    int
	DiffPixels int
	DiffRatio  float64
	Motion     bool
}

// MotionDetectTask implements 方案二：低分辨率灰度帧差检测。
func MotionDetectTask(ctx context.Context, wg *sync.WaitGroup, cam constant.Camera) {
	defer wg.Done()

	if !motionRecordingEnabled(cam) {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := runMotionDetector(ctx, cam); err != nil && ctx.Err() == nil {
			log.Printf("[%s] 动态检测进程退出: %v，%s 后重试", cam.ID, err, motionDetectRestartDelay)
		}

		timer := time.NewTimer(motionDetectRestartDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func runMotionDetector(ctx context.Context, cam constant.Camera) error {
	ratioThreshold := motionRatioThreshold(cam)
	safeRTSPURL := fmt.Sprintf("rtsp://%s:8554/%s", constant.DefaultGo2rtcHost, cam.ID)
	args := []string{
		"-loglevel", "error",
		"-rtsp_transport", "tcp",
		"-timeout", "5000000",
		"-i", safeRTSPURL,
		"-an",
		"-vf", fmt.Sprintf("fps=%d,scale=%d:%d", motionDetectFPS, motionDetectFrameWidth, motionDetectFrameHeight),
		"-pix_fmt", "gray",
		"-f", "rawvideo",
		"-",
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = os.Stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	log.Printf("[%s] 动态检测已启动: %dx%d gray @ %dfps, ratioThreshold=%.4f",
		cam.ID, motionDetectFrameWidth, motionDetectFrameHeight, motionDetectFPS, ratioThreshold)

	readErr := readMotionFrames(ctx, cam.ID, stdoutPipe, ratioThreshold)
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return readErr
	}
	if waitErr != nil {
		return waitErr
	}
	return readErr
}

func readMotionFrames(ctx context.Context, camID string, reader io.Reader, ratioThreshold float64) error {
	frameSize := motionDetectFrameWidth * motionDetectFrameHeight
	buf := make([]byte, frameSize)
	prevFrame := make([]byte, frameSize)
	hasPrevFrame := false
	var lastLog time.Time

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if _, err := io.ReadFull(reader, buf); err != nil {
			return err
		}

		if !hasPrevFrame {
			copy(prevFrame, buf)
			hasPrevFrame = true
			log.Printf("[%s] 动态检测已获得首帧，开始帧差验证", camID)
			continue
		}

		stats := compareMotionFrames(prevFrame, buf, motionDetectPixelThreshold, ratioThreshold)
		now := time.Now()
		if stats.Motion {
			markMotionDetected(camID, now)
		}
		logInterval := motionDetectIdleLogInterval
		if stats.Motion {
			logInterval = motionDetectAlertLogInterval
		}
		if lastLog.IsZero() || now.Sub(lastLog) >= logInterval {
			if stats.Motion {
				log.Printf("[%s] 动态检测: motion=%t, diffPixels=%d/%d, diffRatio=%.2f%%, diffSum=%d",
					camID, stats.Motion, stats.DiffPixels, frameSize, stats.DiffRatio*100, stats.DiffSum)
			}
			lastLog = now
		}

		copy(prevFrame, buf)
	}
}

func compareMotionFrames(prevFrame, currentFrame []byte, pixelThreshold int, ratioThreshold float64) motionFrameStats {
	frameSize := len(prevFrame)
	if len(currentFrame) < frameSize {
		frameSize = len(currentFrame)
	}
	if frameSize == 0 {
		return motionFrameStats{}
	}

	stats := motionFrameStats{}
	for i := 0; i < frameSize; i++ {
		diff := int(currentFrame[i]) - int(prevFrame[i])
		if diff < 0 {
			diff = -diff
		}
		stats.DiffSum += diff
		if diff > pixelThreshold {
			stats.DiffPixels++
		}
	}

	stats.DiffRatio = float64(stats.DiffPixels) / float64(frameSize)
	stats.Motion = stats.DiffRatio > ratioThreshold
	return stats
}
