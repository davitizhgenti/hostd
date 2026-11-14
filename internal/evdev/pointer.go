package evdev

import (
	"math"
	"time"
)

// --- the pointer ---
//
// The IR camera sees the sensor bar's two lamps. Where the remote points is
// worked out from them in four steps, each against one kind of trouble:
//
//  1. the bar's two lamps are picked out of what the camera sees, and kept
//     apart from stray light (a candle, a window) by following the pair;
//  2. the pair's middle is turned back by the remote's roll and measured in
//     bar widths, so tilting the remote or stepping back does not move the
//     pointer: only where it is aimed does;
//  3. a calibration says how many bar widths the screen is and where its
//     middle lies (defaults fit a projector across a room; Calibrate
//     measures them);
//  4. a 1€ filter smooths it: heavily while the hand is nearly still (no
//     shake), lightly while it moves fast (no lag).

// Calibration maps the bar-width space of one remote to the screen: where
// the screen's middle is (CX, CY, in bar widths) and how many bar widths
// across and down it is (W, H). The zero value is the default.
type Calibration struct {
	CX, CY float64
	W, H   float64
}

const (
	defaultScreenW = 3.6 // bar widths across the screen
	defaultScreenH = 2.3 // ...and down it (16:10)
)

// Or returns the calibration with its defaults filled in.
func (c Calibration) Or() Calibration {
	if c.W <= 0 {
		c.W = defaultScreenW
	}
	if c.H <= 0 {
		c.H = defaultScreenH
	}
	return c
}

// Fit makes a calibration from the raw bar-width positions seen with the
// remote aimed at the screen's four corners: top left, top right, bottom
// left, bottom right. It is false when they cannot be a screen (too close
// together, or turned about).
func FitCalibration(tl, tr, bl, br [2]float64) (Calibration, bool) {
	// the bar moves left on the camera as the remote turns right, so the
	// screen's left corners have the larger u.
	w := (tl[0] + bl[0] - tr[0] - br[0]) / 2
	h := (bl[1] + br[1] - tl[1] - tr[1]) / 2
	if w < 0.5 || h < 0.3 || w > 20 || h > 20 {
		return Calibration{}, false
	}
	cx := (tl[0] + tr[0] + bl[0] + br[0]) / 4
	cy := (tl[1] + tr[1] + bl[1] + br[1]) / 4
	return Calibration{CX: cx, CY: cy, W: w, H: h}, true
}

// 1€ filter (Casiez, Roussel, Vogel): the cutoff rises with speed.
const (
	filterMin  = 1.0 // Hz: how still the pointer is held while the hand is
	filterBeta = 6.0 // how fast the cutoff rises with speed (per screen share per second)
	filterD    = 1.0 // Hz: the speed's own smoothing
)

type lowpass struct {
	v    float64
	have bool
}

func (l *lowpass) at(x, alpha float64) float64 {
	if !l.have {
		l.v, l.have = x, true
	}
	l.v += alpha * (x - l.v)
	return l.v
}

func alpha(cutoff, dt float64) float64 {
	tau := 1 / (2 * math.Pi * cutoff)
	return 1 / (1 + tau/dt)
}

type oneEuro struct {
	x, dx lowpass
	prev  float64
	have  bool
}

func (f *oneEuro) at(v, dt float64) float64 {
	if !f.have {
		f.have, f.prev = true, v
		f.x.at(v, 1)
		f.dx.at(0, 1)
		return v
	}
	speed := f.dx.at((v-f.prev)/dt, alpha(filterD, dt))
	f.prev = v
	return f.x.at(v, alpha(filterMin+filterBeta*math.Abs(speed), dt))
}

// A pointerTracker turns the IR dots into a position on the screen as
// shares (0..1), mirrored: the camera sees the bar move left as the remote
// turns right.
type pointerTracker struct {
	Cal Calibration

	fx, fy     oneEuro
	last       time.Time // the last report
	seen       time.Time // the last time the bar was seen
	have       bool
	mx, my     float64 // the bar's middle on the camera, as last followed
	sep        float64 // and its width
	x, y       float64
	rawU, rawV float64 // the bar's place in bar widths, as followed (for the calibration)
}

const (
	pointerLost = 300 * time.Millisecond // without the bar this long, the remote points away
	minSep      = 12.0                   // closer than this, two dots are one lamp's glare
)

// Raw is where the bar is in bar widths, before calibration and a little
// smoothed, for the calibration to read; false while the bar is not seen.
func (t *pointerTracker) Raw(now time.Time) (u, v float64, ok bool) {
	return t.rawU, t.rawV, t.have && now.Sub(t.seen) < pointerLost
}

// pick chooses the bar's two lamps from the dots seen: with a pair being
// followed, the pair nearest it that is about as wide (stray light is
// neither where the bar was nor as wide as it); with none, the widest
// pair that lies more across than up.
func (t *pointerTracker) pick(dots [4]irDot) (a, b irDot, ok bool) {
	best := math.Inf(1)
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			p, q := dots[i], dots[j]
			if !p.Valid || !q.Valid {
				continue
			}
			dx, dy := float64(q.X-p.X), float64(q.Y-p.Y)
			sep := math.Hypot(dx, dy)
			if sep < minSep || math.Abs(dy) > math.Abs(dx)*1.5 {
				continue
			}
			var cost float64
			if t.have {
				mx, my := float64(p.X+q.X)/2, float64(p.Y+q.Y)/2
				if sep < t.sep*0.6 || sep > t.sep*1.6 || math.Hypot(mx-t.mx, my-t.my) > t.sep*1.5 {
					continue
				}
				cost = math.Hypot(mx-t.mx, my-t.my) + math.Abs(sep-t.sep)
			} else {
				cost = -sep
			}
			if cost < best {
				best, a, b, ok = cost, p, q, true
			}
		}
	}
	if ok && a.X > b.X {
		a, b = b, a
	}
	return a, b, ok
}

// Update takes a report's dots at time now and returns where the pointer
// is and whether it is on the screen. With fewer than two dots the position
// is held (one lamp alone would be off by half the bar); after pointerLost
// without the bar it is not shown.
func (t *pointerTracker) Update(dots [4]irDot, now time.Time) (x, y float64, shown bool) {
	if a, b, ok := t.pick(dots); ok {
		dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
		sep := math.Hypot(dx, dy)
		mx, my := float64(a.X+b.X)/2, float64(a.Y+b.Y)/2
		t.mx, t.my, t.sep = mx, my, sep
		// The middle's offset from the camera's centre, turned back by the
		// roll and measured in bar widths.
		ox, oy := mx-irWidth/2, my-irHeight/2
		c, s := dx/sep, dy/sep
		u := (ox*c + oy*s) / sep
		v := (-ox*s + oy*c) / sep
		if !t.have || now.Sub(t.seen) >= pointerLost {
			t.rawU, t.rawV = u, v
		} else {
			t.rawU += (u - t.rawU) * 0.3
			t.rawV += (v - t.rawV) * 0.3
		}
		cal := t.Cal.Or()
		tx := 0.5 - (u-cal.CX)/cal.W // mirrored: turning right moves the bar left on the camera
		ty := 0.5 + (v-cal.CY)/cal.H
		tx, ty = math.Min(1, math.Max(0, tx)), math.Min(1, math.Max(0, ty))
		if !t.have || now.Sub(t.seen) >= pointerLost {
			t.fx, t.fy = oneEuro{}, oneEuro{}
			t.last = now
		}
		dt := math.Min(0.1, math.Max(0.001, now.Sub(t.last).Seconds()))
		t.x, t.y = t.fx.at(tx, dt), t.fy.at(ty, dt)
		t.have, t.seen, t.last = true, now, now
	}
	shown = t.have && now.Sub(t.seen) < pointerLost
	if !shown {
		t.have = false
	}
	return t.x, t.y, shown
}

// A CalibrateEvent says how a calibration goes: State is "aim" (Step 0..3 is
// the corner to point at and press A: top left, top right, bottom left,
// bottom right), "done" (Fit is the result), "failed", "cancelled" or
// "reset".
type CalibrateEvent struct {
	State  string
	Step   int
	Fit    *Calibration
	Reason string
}

// Corners are the calibration's, in the order they are asked.
var Corners = [4]string{"top left", "top right", "bottom left", "bottom right"}

// A calibration is the corners found so far.
type calibration struct {
	at [4][2]float64
	n  int
}
