package task

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r0n9/camkeep/constant"
)

func TestCompareMotionFramesDetectsMotion(t *testing.T) {
	prevFrame := make([]byte, motionDetectFrameWidth*motionDetectFrameHeight)
	currentFrame := make([]byte, motionDetectFrameWidth*motionDetectFrameHeight)

	changedPixels := int(float64(len(currentFrame))*motionDetectRatioThreshold) + 1
	for i := 0; i < changedPixels; i++ {
		currentFrame[i] = byte(motionDetectPixelThreshold + 1)
	}

	stats := compareMotionFrames(prevFrame, currentFrame, motionDetectPixelThreshold, motionDetectRatioThreshold)
	if !stats.Motion {
		t.Fatal("expected frame diff above ratio threshold to be detected as motion")
	}
	if stats.DiffPixels != changedPixels {
		t.Fatalf("expected %d changed pixels, got %d", changedPixels, stats.DiffPixels)
	}
}

func TestCompareMotionFramesIgnoresNoise(t *testing.T) {
	prevFrame := make([]byte, motionDetectFrameWidth*motionDetectFrameHeight)
	currentFrame := make([]byte, motionDetectFrameWidth*motionDetectFrameHeight)

	for i := range currentFrame {
		currentFrame[i] = byte(motionDetectPixelThreshold)
	}

	stats := compareMotionFrames(prevFrame, currentFrame, motionDetectPixelThreshold, motionDetectRatioThreshold)
	if stats.Motion {
		t.Fatal("expected changes at pixel threshold to be treated as noise")
	}
	if stats.DiffPixels != 0 {
		t.Fatalf("expected no pixels above threshold, got %d", stats.DiffPixels)
	}
}

func TestMotionRatioThresholdUsesConfiguredValue(t *testing.T) {
	threshold := motionRatioThreshold(constant.Camera{
		ID:                         "cam1",
		Mode:                       "normal",
		MotionDetect:               true,
		MotionDetectRatioThreshold: 0.05,
	})

	if threshold != 0.05 {
		t.Fatalf("expected configured threshold, got %f", threshold)
	}
}

func TestMotionRecordingEnabledOnlyForNormalMode(t *testing.T) {
	if !motionRecordingEnabled(constant.Camera{Mode: "normal", MotionDetect: true}) {
		t.Fatal("expected motion recording enabled for normal mode")
	}
	if motionRecordingEnabled(constant.Camera{Mode: "timelapse", MotionDetect: true}) {
		t.Fatal("expected motion recording disabled for timelapse mode")
	}
	if motionRecordingEnabled(constant.Camera{Mode: "normal"}) {
		t.Fatal("expected motion recording disabled by default")
	}
}

func TestRecordingWindowEnabled(t *testing.T) {
	tests := []struct {
		name        string
		control     string
		inTimeRange bool
		want        bool
	}{
		{name: "auto in range", inTimeRange: true, want: true},
		{name: "auto out of range", inTimeRange: false, want: false},
		{name: "manual start ignores schedule", control: "start", inTimeRange: false, want: true},
		{name: "manual stop blocks schedule", control: "stop", inTimeRange: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recordingWindowEnabled(tt.control, tt.inTimeRange); got != tt.want {
				t.Fatalf("expected %t, got %t", tt.want, got)
			}
		})
	}
}

func TestNewMotionRecordSessionAppliesPreRecord(t *testing.T) {
	detectedAt := time.Date(2026, 5, 12, 10, 0, 0, 0, time.Local)
	session := newMotionRecordSession(detectedAt)
	if !session.StartTime.Equal(detectedAt.Add(-motionTimeShiftPreRecord)) {
		t.Fatalf("expected prerecord start %s, got %s", detectedAt.Add(-motionTimeShiftPreRecord), session.StartTime)
	}
}

func TestParseMotionTimeShiftSegmentStart(t *testing.T) {
	start, ok := parseMotionTimeShiftSegmentStart("loop_20260512_100001.mp4")
	if !ok {
		t.Fatal("expected segment filename parsed")
	}
	want := time.Date(2026, 5, 12, 10, 0, 1, 0, time.Local)
	if !start.Equal(want) {
		t.Fatalf("expected %s, got %s", want, start)
	}
	if _, ok := parseMotionTimeShiftSegmentStart("chunk_000.ts"); ok {
		t.Fatal("expected non-timeshift filename ignored")
	}
}

func TestMotionTimeShiftClipsAcrossSegments(t *testing.T) {
	camID := "test-timeshift-clips"
	bufferDir := motionTimeShiftDir(camID)
	t.Cleanup(func() {
		os.RemoveAll(bufferDir)
	})
	if err := os.RemoveAll(bufferDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bufferDir, 0755); err != nil {
		t.Fatal(err)
	}

	baseTime := time.Date(2026, 5, 12, 10, 0, 0, 0, time.Local)
	createTimeShiftTestSegment(t, bufferDir, baseTime)
	createTimeShiftTestSegment(t, bufferDir, baseTime.Add(motionTimeShiftSegmentDuration))

	clips, err := motionTimeShiftClips(camID, baseTime.Add(170*time.Second), baseTime.Add(190*time.Second), baseTime.Add(190*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(clips) != 2 {
		t.Fatalf("expected 2 clips across segment boundary, got %d", len(clips))
	}
	if got := clips[0].end.Sub(clips[0].start); got != 10*time.Second {
		t.Fatalf("expected first clip 10s, got %s", got)
	}
	if got := clips[1].end.Sub(clips[1].start); got != 10*time.Second {
		t.Fatalf("expected second clip 10s, got %s", got)
	}
}

func TestPruneMotionTimeShiftSegmentsKeepsNewestSegments(t *testing.T) {
	camID := "test-timeshift-prune"
	bufferDir := motionTimeShiftDir(camID)
	t.Cleanup(func() {
		os.RemoveAll(bufferDir)
	})
	if err := os.RemoveAll(bufferDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bufferDir, 0755); err != nil {
		t.Fatal(err)
	}

	baseTime := time.Date(2026, 5, 12, 10, 0, 0, 0, time.Local)
	var paths []string
	for i := 0; i < motionTimeShiftSegmentCount+2; i++ {
		paths = append(paths, createTimeShiftTestSegment(t, bufferDir, baseTime.Add(time.Duration(i)*motionTimeShiftSegmentDuration)))
	}

	pruneMotionTimeShiftSegments(camID, time.Time{})
	for i, path := range paths {
		_, err := os.Stat(path)
		if i < 2 && !os.IsNotExist(err) {
			t.Fatalf("expected old segment %s removed, err=%v", path, err)
		}
		if i >= 2 && err != nil {
			t.Fatalf("expected newer segment %s kept, err=%v", path, err)
		}
	}
}

func TestFormatSeconds(t *testing.T) {
	if got := formatSeconds(2 * time.Second); got != "2" {
		t.Fatalf("expected integer seconds, got %q", got)
	}
	if got := formatSeconds(1500 * time.Millisecond); got != "1.500" {
		t.Fatalf("expected millisecond precision, got %q", got)
	}
}

func createTimeShiftTestSegment(t *testing.T, dir string, start time.Time) string {
	t.Helper()
	name := motionTimeShiftFilePrefix + start.Format(motionTimeShiftTimeLayout) + motionTimeShiftSegmentExt
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("segment"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
