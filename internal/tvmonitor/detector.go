package tvmonitor

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"time"

	"github.com/r0n9/camkeep/constant"
	"gocv.io/x/gocv"
)

type Detector struct {
	config           constant.TVMonitorConfig
	roiRect          image.Rectangle
	onCount          int
	offCount         int
	lastStableState  bool
	lastLaplacianVar float64
}

const debounceCount = 3

func NewDetector(cfg constant.TVMonitorConfig, frameWidth, frameHeight int) *Detector {
	d := &Detector{config: cfg}
	d.roiRect = d.pctToRect(frameWidth, frameHeight)
	return d
}

// pctToRect converts percentage-based ROI to pixel rectangle, clamped to frame bounds.
func (d *Detector) pctToRect(w, h int) image.Rectangle {
	x0 := clamp(int(d.config.ROIX*float64(w)), 0, w)
	y0 := clamp(int(d.config.ROIY*float64(h)), 0, h)
	x1 := clamp(int((d.config.ROIX+d.config.ROIW)*float64(w)), 0, w)
	y1 := clamp(int((d.config.ROIY+d.config.ROIH)*float64(h)), 0, h)
	return image.Rect(x0, y0, x1, y1)
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// TVState returns true if the TV appears to be on in the given frame.
// Uses Laplacian variance to measure texture/detail richness:
//   - TV on: rich content (text, UI, people) → high variance
//   - TV off: uniform dark surface (even with reflections) → low variance
// Uses debounce: state only changes after debounceCount consecutive readings.
func (d *Detector) TVState(frame gocv.Mat) bool {
	roi := frame.Region(d.roiRect)
	defer roi.Close()

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)
	gocv.GaussianBlur(gray, &gray, image.Pt(3, 3), 0, 0, gocv.BorderDefault)

	laplacian := gocv.NewMat()
	defer laplacian.Close()
	gocv.Laplacian(gray, &laplacian, gocv.MatTypeCV64F, 3, 1.0, 0.0, gocv.BorderDefault)

	mean := gocv.NewMat()
	stdDev := gocv.NewMat()
	defer mean.Close()
	defer stdDev.Close()
	gocv.MeanStdDev(laplacian, &mean, &stdDev)

	stdDevF := gocv.NewMat()
	defer stdDevF.Close()
	stdDev.ConvertTo(&stdDevF, gocv.MatTypeCV32F)
	lapVar := float64(stdDevF.GetFloatAt(0, 0))
	d.lastLaplacianVar = lapVar

	tvOn := lapVar > d.config.BrightnessThreshold

	// Debounce: require consecutive readings before changing state
	if tvOn {
		d.onCount++
		d.offCount = 0
	} else {
		d.offCount++
		d.onCount = 0
	}

	if d.onCount >= debounceCount {
		d.lastStableState = true
	} else if d.offCount >= debounceCount {
		d.lastStableState = false
	}

	log.Printf("[tvmonitor] TVState: raw=%v lapVar=%.1f threshold=%.1f onCount=%d offCount=%d stable=%v",
		tvOn, lapVar, d.config.BrightnessThreshold, d.onCount, d.offCount, d.lastStableState)

	return d.lastStableState
}

// Close releases gocv resources.
func (d *Detector) Close() {}

// DrawROI draws the ROI rectangle on a copy of the frame and returns JPEG bytes.
func (d *Detector) DrawROI(frame gocv.Mat) []byte {
	annotated := frame.Clone()
	defer annotated.Close()

	green := color.RGBA{G: 255, A: 255}
	red := color.RGBA{R: 255, A: 255}
	boxColor := green
	stateLabel := "TV OFF"
	if d.lastStableState {
		boxColor = red
		stateLabel = "TV ON"
	}

	gocv.Rectangle(&annotated, d.roiRect, boxColor, 3)

	labelPos := image.Pt(d.roiRect.Min.X, d.roiRect.Min.Y-8)
	if labelPos.Y < 16 {
		labelPos.Y = d.roiRect.Min.Y + 20
	}
	gocv.PutText(&annotated, stateLabel, labelPos, gocv.FontHersheyPlain, 1.5, boxColor, 2)

	infoPos := image.Pt(d.roiRect.Min.X, d.roiRect.Max.Y+18)
	gocv.PutText(&annotated, fmt.Sprintf("LAP:%.1f", d.lastLaplacianVar), infoPos, gocv.FontHersheyPlain, 1.2, boxColor, 1)

	timePos := image.Pt(10, annotated.Rows()-10)
	gocv.PutText(&annotated, time.Now().Format("15:04:05"), timePos, gocv.FontHersheyPlain, 1.5, color.RGBA{R: 255, G: 255, B: 255, A: 200}, 2)

	buf, err := gocv.IMEncode(".jpg", annotated)
	if err != nil {
		return nil
	}
	return buf.GetBytes()
}

// AutoCalibrateROI attempts to detect the TV screen as the largest rectangle
// in the frame. Returns updated ROI percentages (x, y, w, h).
func AutoCalibrateROI(rtspURL string, cfg constant.TVMonitorConfig) (x, y, w, h float64, err error) {
	cap, err := gocv.OpenVideoCapture(rtspURL)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("open stream for calibration: %w", err)
	}
	defer cap.Close()

	frame := gocv.NewMat()
	defer frame.Close()
	if ok := cap.Read(&frame); !ok || frame.Empty() {
		return 0, 0, 0, 0, fmt.Errorf("failed to read frame for calibration")
	}

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(frame, &gray, gocv.ColorBGRToGray)
	gocv.GaussianBlur(gray, &gray, image.Pt(5, 5), 0, 0, gocv.BorderDefault)

	edges := gocv.NewMat()
	defer edges.Close()
	gocv.Canny(gray, &edges, 50, 150)

	contours := gocv.FindContours(edges, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	fw := float64(frame.Cols())
	fh := float64(frame.Rows())
	bestArea := 0.0

	for i := 0; i < contours.Size(); i++ {
		approx := gocv.ApproxPolyDP(contours.At(i), 4, true)
		if approx.Size() != 4 {
			approx.Close()
			continue
		}
		r := gocv.BoundingRect(contours.At(i))
		area := float64(r.Dx()*r.Dy()) / (fw * fh)
		if area > bestArea {
			bestArea = area
			x = float64(r.Min.X) / fw
			y = float64(r.Min.Y) / fh
			w = float64(r.Dx()) / fw
			h = float64(r.Dy()) / fh
		}
		approx.Close()
	}

	if bestArea < 0.05 {
		return 0, 0, 0, 0, fmt.Errorf("no suitable rectangle found (best area ratio: %.2f)", bestArea)
	}

	log.Printf("[tvmonitor] Auto-calibrated ROI: x=%.2f y=%.2f w=%.2f h=%.2f (area=%.0f%%)", x, y, w, h, bestArea*100)
	return x, y, w, h, nil
}
