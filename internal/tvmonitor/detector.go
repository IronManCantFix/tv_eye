package tvmonitor

import (
	"fmt"
	"image"
	"log"

	"github.com/r0n9/camkeep/constant"
	"gocv.io/x/gocv"
)

type Detector struct {
	config   constant.TVMonitorConfig
	roiRect  image.Rectangle
	prevGray gocv.Mat
}

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
func (d *Detector) TVState(frame gocv.Mat) bool {
	roi := frame.Region(d.roiRect)
	defer roi.Close()

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	gocv.GaussianBlur(gray, &gray, image.Pt(5, 5), 0, 0, gocv.BorderDefault)

	mean := gray.Mean()
	brightness := mean.Val1
	tvOn := brightness > d.config.BrightnessThreshold

	// Frame-diff check (skip on first frame)
	if d.prevGray.Empty() {
		d.prevGray = gray.Clone()
		return tvOn
	}

	diff := gocv.NewMat()
	defer diff.Close()
	gocv.AbsDiff(gray, d.prevGray, &diff)

	stdDev := gocv.NewMat()
	meanDev := gocv.NewMat()
	defer stdDev.Close()
	defer meanDev.Close()
	gocv.MeanStdDev(diff, &meanDev, &stdDev)

	frameDiff := stdDev.GetFloatAt(0, 0)
	tvOn = tvOn && frameDiff > d.config.FrameDiffThreshold

	d.prevGray.Close()
	d.prevGray = gray.Clone()
	return tvOn
}

// Close releases gocv resources.
func (d *Detector) Close() {
	if !d.prevGray.Empty() {
		d.prevGray.Close()
	}
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
