// Package sim generates the synthetic telemetry of a small power distribution
// fleet: one feeder that suffers repeated voltage brownouts affecting all of
// its nodes, and a healthy control feeder.
package sim

import (
	"hash/fnv"
	"math"
	"sort"
	"time"
)

// Electrical constants of the simulated low voltage feeder.
const (
	NominalVoltage = 230.0 // V, feeder nominal
	MaxSagVolts    = 58.0  // V, sag at brownout depth 1.0 and coupling 1.0
	UnderVoltageV  = 207.0 // V, -10% threshold rule
	CriticalTempC  = 52.0  // deg C threshold rule
)

// Role describes the position of a node relative to the fault.
type Role string

const (
	// RolePrimary is the node reported as failing by the field technician.
	RolePrimary Role = "primary"
	// RoleNeighbor is a node on the same feeder, i.e. the correlation evidence.
	RoleNeighbor Role = "neighbor"
	// RoleBaseline is a node on a healthy feeder, i.e. the control group.
	RoleBaseline Role = "baseline"
)

// Node is one simulated device.
type Node struct {
	Serial   string
	Name     string
	Site     string
	Feeder   string
	Role     Role
	Coupling float64 // brownout sensitivity, 0 means unaffected
	Firmware string

	BaseVoltage float64
	BaseCurrent float64
	BaseTemp    float64

	// ID is the Cumulocity managed object ID, set during provisioning.
	ID string
	// GroupID is the ID of the owning device group.
	GroupID string

	seed uint64
}

// Affected reports whether the node reacts to brownouts at all.
func (n *Node) Affected() bool { return n.Coupling > 0 }

// Window is a single brownout episode on the affected feeder.
type Window struct {
	Start time.Time
	Dur   time.Duration
	Depth float64 // 0..1
	Cause string
}

// End returns the recovery time of the episode.
func (w Window) End() time.Time { return w.Start.Add(w.Dur) }

// Intensity returns the sag intensity at t: a fast collapse, a noisy plateau
// and a slower recovery ramp.
func (w Window) Intensity(t time.Time) float64 {
	if t.Before(w.Start) || !t.Before(w.End()) {
		return 0
	}
	p := t.Sub(w.Start).Seconds() / w.Dur.Seconds()
	var shape float64
	switch {
	case p < 0.08:
		shape = p / 0.08
	case p > 0.80:
		shape = (1 - p) / 0.20
	default:
		shape = 1
	}
	return w.Depth * shape
}

// Reading is one sample of the simulated sensors.
type Reading struct {
	Voltage float64
	Current float64
	Temp    float64
}

// BrownoutWindows returns the brownout episodes leading up to the fault
// timestamp. The failure looks intermittent: short, shallow dips first, then
// longer and deeper ones, ending in the reported outage at fault time.
func BrownoutWindows(fault time.Time, history time.Duration) []Window {
	all := []Window{
		{Start: fault.Add(-5*24*time.Hour - 3*time.Hour), Dur: 90 * time.Second, Depth: 0.34,
			Cause: "first unnoticed dip during evening peak"},
		{Start: fault.Add(-2*24*time.Hour - 40*time.Minute), Dur: 4 * time.Minute, Depth: 0.52,
			Cause: "dip coinciding with feeder peak load"},
		{Start: fault.Add(-26 * time.Hour), Dur: 7 * time.Minute, Depth: 0.71,
			Cause: "extended sag, first under-voltage alarms on neighbors"},
		{Start: fault.Add(-3 * time.Hour), Dur: 2 * time.Minute, Depth: 0.63,
			Cause: "precursor dip shortly before the outage"},
		{Start: fault, Dur: 18 * time.Minute, Depth: 1.0,
			Cause: "sustained brownout on the feeder, controllers brown-out reset"},
	}
	earliest := fault.Add(-history)
	out := make([]Window, 0, len(all))
	for _, w := range all {
		if !w.Start.Before(earliest) {
			out = append(out, w)
		}
	}
	return out
}

// Sample computes the sensor reading of the node at t.
func (n *Node) Sample(t time.Time, windows []Window) Reading {
	load := loadFactor(t)

	voltage := n.BaseVoltage - 3.0*(load-1) + 1.1*valueNoise(n.seed^1, t, 7*time.Minute)
	current := n.BaseCurrent*load + 0.30*valueNoise(n.seed^2, t, 3*time.Minute)
	temp := n.BaseTemp + 5.5*(load-1) + 0.7*valueNoise(n.seed^3, t, 11*time.Minute)

	var sag, thermal float64
	for _, w := range windows {
		x := w.Intensity(t) * n.Coupling
		if x <= 0 {
			continue
		}
		flicker := 1 + 0.07*valueNoise(n.seed^4, t, 20*time.Second)
		if v := x * flicker; v > sag {
			sag = v
		}
		// Heating lags the sag by roughly two minutes.
		elapsed := t.Sub(w.Start).Seconds()
		if v := x * (1 - math.Exp(-elapsed/120)); v > thermal {
			thermal = v
		}
	}
	if sag > 0 {
		voltage -= MaxSagVolts * sag
		// Constant power loads draw more current at lower voltage.
		current += n.BaseCurrent * 0.55 * sag
		temp += 15 * thermal
	}

	return Reading{
		Voltage: round(voltage, 1),
		Current: round(current, 2),
		Temp:    round(temp, 1),
	}
}

// MinVoltage returns the deepest sample of the node inside the window.
func (n *Node) MinVoltage(w Window, windows []Window, step time.Duration) Reading {
	worst := Reading{Voltage: math.MaxFloat64}
	for t := w.Start; t.Before(w.End()); t = t.Add(step) {
		r := n.Sample(t, windows)
		if r.Voltage < worst.Voltage {
			worst = r
		}
	}
	return worst
}

// SampleTimes builds the sampling grid: a coarse base interval plus
// high frequency sampling around every brownout episode.
func SampleTimes(start, end time.Time, base, hf time.Duration, windows []Window) []time.Time {
	seen := make(map[int64]struct{})
	var out []time.Time
	add := func(t time.Time) {
		if t.Before(start) || t.After(end) {
			return
		}
		key := t.Unix()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	for t := start; !t.After(end); t = t.Add(base) {
		add(t)
	}
	const margin = 4 * time.Minute
	for _, w := range windows {
		for t := w.Start.Add(-margin); !t.After(w.End().Add(margin)); t = t.Add(hf) {
			add(t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// loadFactor models the diurnal load curve of a residential feeder with its
// evening peak around 19:00 local time.
func loadFactor(t time.Time) float64 {
	h := float64(t.Hour()) + float64(t.Minute())/60
	daily := math.Sin((h - 7) / 24 * 2 * math.Pi)
	evening := math.Exp(-(h - 19) * (h - 19) / 6)
	return 0.95 + 0.18*daily + 0.25*evening
}

// valueNoise is deterministic smooth noise in [-1,1] so repeated runs of the
// simulator produce the same history.
func valueNoise(seed uint64, t time.Time, period time.Duration) float64 {
	p := period.Seconds()
	x := float64(t.UTC().UnixNano()) / 1e9 / p
	i := math.Floor(x)
	f := x - i
	f = f * f * (3 - 2*f) // smoothstep
	a := unitNoise(seed, int64(i))
	b := unitNoise(seed, int64(i)+1)
	white := 0.15 * unitNoise(seed^0x9e37, t.Unix())
	return clamp(a+(b-a)*f+white, -1, 1)
}

func unitNoise(seed uint64, tick int64) float64 {
	x := seed ^ (uint64(tick) * 0x9e3779b97f4a7c15)
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11)/float64(1<<53)*2 - 1
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }

func round(v float64, digits int) float64 {
	f := math.Pow(10, float64(digits))
	return math.Round(v*f) / f
}
