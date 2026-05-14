package tvmonitor

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"sort"
	"time"

	"github.com/r0n9/camkeep/constant"
	"gocv.io/x/gocv"
)

type Detector struct {
	config          constant.TVMonitorConfig
	perspectMat     gocv.Mat
	warpSize        image.Point
	roiPoints       [4]image.Point // pixel coords for drawing (TL, TR, BR, BL)
	onCount         int
	offCount        int
	lastStableState bool
	lastLaplacianVar float64
}

const debounceCount = 3

func NewDetector(cfg constant.TVMonitorConfig, frameWidth, frameHeight int) *Detector {
	d := &Detector{config: cfg}
	d.buildPerspectiveTransform(frameWidth, frameHeight)
	return d
}

// buildPerspectiveTransform builds a perspective transform matrix from the 4 ROI vertices.
func (d *Detector) buildPerspectiveTransform(w, h int) {
	tl := image.Pt(int(d.config.ROITopLeft[0]*float64(w)), int(d.config.ROITopLeft[1]*float64(h)))
	tr := image.Pt(int(d.config.ROITopRight[0]*float64(w)), int(d.config.ROITopRight[1]*float64(h)))
	br := image.Pt(int(d.config.ROIBottomRight[0]*float64(w)), int(d.config.ROIBottomRight[1]*float64(h)))
	bl := image.Pt(int(d.config.ROIBottomLeft[0]*float64(w)), int(d.config.ROIBottomLeft[1]*float64(h)))

	d.roiPoints = [4]image.Point{tl, tr, br, bl}

	// Target rectangle size: max of opposite edges
	dstW := int(math.Max(ptDist(tl, tr), ptDist(bl, br)))
	dstH := int(math.Max(ptDist(tl, bl), ptDist(tr, br)))
	if dstW <= 0 {
		dstW = 1
	}
	if dstH <= 0 {
		dstH = 1
	}
	d.warpSize = image.Pt(dstW, dstH)

	src := gocv.NewPoint2fVectorFromPoints([]gocv.Point2f{
		{X: float32(tl.X), Y: float32(tl.Y)},
		{X: float32(tr.X), Y: float32(tr.Y)},
		{X: float32(br.X), Y: float32(br.Y)},
		{X: float32(bl.X), Y: float32(bl.Y)},
	})
	dst := gocv.NewPoint2fVectorFromPoints([]gocv.Point2f{
		{X: 0, Y: 0},
		{X: float32(dstW), Y: 0},
		{X: float32(dstW), Y: float32(dstH)},
		{X: 0, Y: float32(dstH)},
	})
	d.perspectMat = gocv.GetPerspectiveTransform2f(src, dst)
	src.Close()
	dst.Close()
}

func ptDist(a, b image.Point) float64 {
	dx := float64(a.X - b.X)
	dy := float64(a.Y - b.Y)
	return math.Sqrt(dx*dx + dy*dy)
}

// TVState returns true if the TV appears to be on in the given frame.
func (d *Detector) TVState(frame gocv.Mat) bool {
	warped := gocv.NewMat()
	defer warped.Close()
	gocv.WarpPerspective(frame, &warped, d.perspectMat, d.warpSize)

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(warped, &gray, gocv.ColorBGRToGray)
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
func (d *Detector) Close() {
	if !d.perspectMat.Empty() {
		d.perspectMat.Close()
	}
}

// DrawROI draws the ROI quadrilateral on a copy of the frame and returns JPEG bytes.
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

	pts := d.roiPoints
	thickness := 3
	gocv.Line(&annotated, pts[0], pts[1], boxColor, thickness)
	gocv.Line(&annotated, pts[1], pts[2], boxColor, thickness)
	gocv.Line(&annotated, pts[2], pts[3], boxColor, thickness)
	gocv.Line(&annotated, pts[3], pts[0], boxColor, thickness)

	labelPos := image.Pt(pts[0].X, pts[0].Y-8)
	if labelPos.Y < 16 {
		labelPos.Y = pts[0].Y + 20
	}
	gocv.PutText(&annotated, stateLabel, labelPos, gocv.FontHersheyPlain, 1.5, boxColor, 2)

	infoPos := image.Pt(pts[3].X, pts[3].Y+18)
	gocv.PutText(&annotated, fmt.Sprintf("LAP:%.1f", d.lastLaplacianVar), infoPos, gocv.FontHersheyPlain, 1.2, boxColor, 1)

	timePos := image.Pt(10, annotated.Rows()-10)
	gocv.PutText(&annotated, time.Now().Format("15:04:05"), timePos, gocv.FontHersheyPlain, 1.5, color.RGBA{R: 255, G: 255, B: 255, A: 200}, 2)

	buf, err := gocv.IMEncode(".jpg", annotated)
	if err != nil {
		return nil
	}
	defer buf.Close()
	return buf.GetBytes()
}

// AutoCalibrateROI attempts to detect the TV screen as the largest quadrilateral
// in the frame. Returns 4 ordered ROI vertices: top-left, top-right, bottom-right, bottom-left.
func AutoCalibrateROI(rtspURL string, fw, fh float64) (tl, tr, br, bl constant.ROIPoint, err error) {
	cap, err := gocv.OpenVideoCapture(rtspURL)
	if err != nil {
		return tl, tr, br, bl, fmt.Errorf("open stream for calibration: %w", err)
	}
	defer cap.Close()

	frame := gocv.NewMat()
	defer frame.Close()
	if ok := cap.Read(&frame); !ok || frame.Empty() {
		return tl, tr, br, bl, fmt.Errorf("failed to read frame for calibration")
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

	bestArea := 0.0
	var bestPoints []image.Point

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
			bestPoints = make([]image.Point, 4)
			for j := 0; j < 4; j++ {
				bestPoints[j] = approx.At(j)
			}
		}
		approx.Close()
	}

	if bestArea < 0.05 {
		return tl, tr, br, bl, fmt.Errorf("no suitable quadrilateral found (best area ratio: %.2f)", bestArea)
	}

	// Sort points: TL, TR, BR, BL
	sorted := orderQuadPoints(bestPoints)

	tl = constant.ROIPoint{float64(sorted[0].X) / fw, float64(sorted[0].Y) / fh}
	tr = constant.ROIPoint{float64(sorted[1].X) / fw, float64(sorted[1].Y) / fh}
	br = constant.ROIPoint{float64(sorted[2].X) / fw, float64(sorted[2].Y) / fh}
	bl = constant.ROIPoint{float64(sorted[3].X) / fw, float64(sorted[3].Y) / fh}

	log.Printf("[tvmonitor] Auto-calibrated ROI: TL=[%.2f,%.2f] TR=[%.2f,%.2f] BR=[%.2f,%.2f] BL=[%.2f,%.2f] (area=%.0f%%)",
		tl[0], tl[1], tr[0], tr[1], br[0], br[1], bl[0], bl[1], bestArea*100)
	return
}

// orderQuadPoints sorts 4 points into TL, TR, BR, BL order.
func orderQuadPoints(pts []image.Point) []image.Point {
	// Sort by Y, then split into top pair and bottom pair
	sorted := make([]image.Point, 4)
	copy(sorted, pts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Y < sorted[j].Y
	})

	top := sorted[:2]
	bottom := sorted[2:]

	// Top pair: smaller X = TL, larger X = TR
	if top[0].X > top[1].X {
		top[0], top[1] = top[1], top[0]
	}
	// Bottom pair: smaller X = BL, larger X = BR
	if bottom[0].X > bottom[1].X {
		bottom[0], bottom[1] = bottom[1], bottom[0]
	}

	return []image.Point{top[0], top[1], bottom[1], bottom[0]}
}
