package diag

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/c8y"
)

// baselineGuard is the period before the fault that is excluded from the
// baseline, so a slow ramp into the fault does not poison the reference.
const baselineGuard = 10 * time.Minute

// onsetTolerance is how far apart two anomalies may start to still count as
// simultaneous.
const onsetTolerance = 3 * time.Minute

// summarise turns raw measurements into per series statistics and a
// downsampled chart series.
func summarise(ms []c8y.Measurement, fault time.Time, chartPoints int) (map[string]*SeriesStats, map[string][]Point) {
	bySeries := map[string][]Point{}
	units := map[string]string{}
	for _, m := range ms {
		for key, v := range m.Values {
			bySeries[key] = append(bySeries[key], Point{Time: m.Time.UTC(), Value: v})
			if u, ok := m.Units[key]; ok {
				units[key] = u
			}
		}
	}

	stats := map[string]*SeriesStats{}
	samples := map[string][]Point{}
	for key, pts := range bySeries {
		sort.Slice(pts, func(i, j int) bool { return pts[i].Time.Before(pts[j].Time) })

		s := &SeriesStats{
			Series: key, Unit: units[key], Count: len(pts),
			Min: math.MaxFloat64, Max: -math.MaxFloat64,
		}
		var sum float64
		for _, p := range pts {
			sum += p.Value
			if p.Value < s.Min {
				s.Min, s.MinTime = p.Value, p.Time
			}
			if p.Value > s.Max {
				s.Max, s.MaxTime = p.Value, p.Time
			}
		}
		s.Mean = round(sum/float64(len(pts)), 2)
		s.Min, s.Max = round(s.Min, 2), round(s.Max, 2)

		base := baselineValues(pts, fault)
		s.Baseline = round(median(base), 2)
		s.Spread = round(math.Max(1.4826*mad(base, median(base)), math.Abs(s.Baseline)*0.002+1e-6), 4)
		if d := medianDelta(pts); d > 0 {
			s.SampleRate = d.Round(time.Second).String()
		}

		stats[key] = s
		samples[key] = downsample(pts, chartPoints)
	}
	return stats, samples
}

func baselineValues(pts []Point, fault time.Time) []float64 {
	cut := fault.Add(-baselineGuard)
	var out []float64
	for _, p := range pts {
		if p.Time.Before(cut) {
			out = append(out, p.Value)
		}
	}
	if len(out) >= 10 {
		return out
	}
	// Not enough quiet data: fall back to the first third of the window.
	n := max(len(pts)/3, 1)
	out = out[:0]
	for _, p := range pts[:n] {
		out = append(out, p.Value)
	}
	return out
}

// anomalies detects threshold breaches and robust deviations from baseline.
func anomalies(stats map[string]*SeriesStats, samples map[string][]Point, rules []ThresholdRule, fault time.Time) []Anomaly {
	var out []Anomaly
	breached := map[string]bool{}

	for _, rule := range rules {
		s, ok := stats[rule.Series]
		if !ok {
			continue
		}
		pts := samples[rule.Series]
		if a, ok := breach(rule, s, pts); ok {
			out = append(out, a)
			breached[rule.Series] = true
		}
	}

	for key, s := range stats {
		if breached[key] {
			continue
		}
		if a, ok := deviation(s, samples[key], fault); ok {
			out = append(out, a)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if r := stronger(&out[i], &out[j]); r != 0 {
			return r > 0
		}
		return out[i].Onset.Before(out[j].Onset)
	})
	return out
}

// kindRank ranks classes of evidence. Breaching an operating limit that somebody
// configured on purpose is engineering-defined evidence; a z-score excursion is
// merely inferred from the signal itself, and is frequently a downstream
// consequence (a constant-power load draws more current when the supply sags).
// Root-cause ranking therefore prefers the rule-backed anomaly.
func kindRank(kind string) int {
	switch kind {
	case "threshold-breach":
		return 2
	case "deviation":
		return 1
	default:
		return 0
	}
}

// stronger reports whether a is better root-cause evidence than b:
// >0 if a wins, <0 if b wins, 0 if indistinguishable.
func stronger(a, b *Anomaly) int {
	if ra, rb := kindRank(a.Kind), kindRank(b.Kind); ra != rb {
		return ra - rb
	}
	if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
		return ra - rb
	}
	switch {
	case a.Sigma > b.Sigma:
		return 1
	case a.Sigma < b.Sigma:
		return -1
	}
	return 0
}

func breach(rule ThresholdRule, s *SeriesStats, pts []Point) (Anomaly, bool) {
	var (
		count     int
		onset     time.Time
		peak      time.Time
		extreme   float64
		direction string
		limit     float64
	)
	for _, p := range pts {
		var over bool
		switch {
		case rule.Min != nil && p.Value < *rule.Min:
			over, direction, limit = true, "drop", *rule.Min
		case rule.Max != nil && p.Value > *rule.Max:
			over, direction, limit = true, "spike", *rule.Max
		}
		if !over {
			continue
		}
		count++
		if onset.IsZero() {
			onset, peak, extreme = p.Time, p.Time, p.Value
		}
		if (direction == "drop" && p.Value < extreme) || (direction == "spike" && p.Value > extreme) {
			peak, extreme = p.Time, p.Value
		}
	}
	if count == 0 {
		return Anomaly{}, false
	}
	severity := rule.Severity
	if severity == "" {
		severity = "MAJOR"
	}
	unit := rule.Unit
	if unit == "" {
		unit = s.Unit
	}
	return Anomaly{
		Series: rule.Series, Kind: "threshold-breach", Direction: direction,
		Severity: severity, Onset: onset, Peak: peak, Value: round(extreme, 2),
		Baseline: s.Baseline, Threshold: &limit, Breaches: count,
		Sigma: round(math.Abs(extreme-s.Baseline)/s.Spread, 1),
		Description: fmt.Sprintf("%s %s to %.2f %s, breaching the %.2f %s limit in %d samples (baseline %.2f %s, rule from %s)",
			rule.Series, verb(direction), extreme, unit, limit, unit, count, s.Baseline, unit, rule.Origin),
	}, true
}

func deviation(s *SeriesStats, pts []Point, fault time.Time) (Anomaly, bool) {
	cut := fault.Add(-baselineGuard)
	var (
		onset   time.Time
		peak    time.Time
		extreme float64
		maxZ    float64
	)
	for _, p := range pts {
		if p.Time.Before(cut) {
			continue
		}
		z := math.Abs(p.Value-s.Baseline) / s.Spread
		if z >= 4 && onset.IsZero() {
			onset, peak, extreme, maxZ = p.Time, p.Time, p.Value, z
		}
		if z > maxZ {
			peak, extreme, maxZ = p.Time, p.Value, z
		}
	}
	relative := math.Abs(extreme-s.Baseline) / math.Max(math.Abs(s.Baseline), 1e-9)
	if onset.IsZero() || maxZ < 6 || relative < 0.02 {
		return Anomaly{}, false
	}
	direction := "spike"
	if extreme < s.Baseline {
		direction = "drop"
	}
	severity := "MINOR"
	switch {
	case maxZ >= 12:
		severity = "CRITICAL"
	case maxZ >= 8:
		severity = "MAJOR"
	}
	return Anomaly{
		Series: s.Series, Kind: "deviation", Direction: direction, Severity: severity,
		Onset: onset, Peak: peak, Value: round(extreme, 2), Baseline: s.Baseline,
		Sigma: round(maxZ, 1),
		Description: fmt.Sprintf("%s %s to %.2f %s, %.1f sigma off the %.2f %s baseline (%.1f%%)",
			s.Series, verb(direction), extreme, s.Unit, maxZ, s.Baseline, s.Unit, relative*100),
	}, true
}

// gaps finds holes in the telemetry relative to the observed sampling rate.
func gaps(ms []c8y.Measurement, window Window) []Gap {
	if len(ms) < 3 {
		return nil
	}
	times := make([]Point, 0, len(ms))
	for _, m := range ms {
		times = append(times, Point{Time: m.Time.UTC()})
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Time.Before(times[j].Time) })

	step := medianDelta(times)
	if step <= 0 {
		return nil
	}
	limit := max(3*step, 90*time.Second)

	var out []Gap
	for i := 1; i < len(times); i++ {
		if d := times[i].Time.Sub(times[i-1].Time); d > limit {
			out = append(out, Gap{
				From: times[i-1].Time, To: times[i].Time,
				Duration: d.Round(time.Second).String(),
			})
		}
	}
	return out
}

// correlate compares the leading anomaly of the target with the neighbours.
func correlate(target *DeviceReport, neighbors []DeviceReport) *Correlation {
	lead := leadingAnomaly(target)
	if lead == nil || lead.Kind == "gap" {
		return nil
	}
	c := &Correlation{Series: lead.Series, Direction: lead.Direction, Total: len(neighbors)}

	for i := range neighbors {
		n := &neighbors[i]
		nc := NeighborCorrelation{
			Device: n.ID, Name: n.Name, SharedGroup: sharedGroup(target, n),
		}
		match := matchingAnomaly(n, lead.Series, lead.Direction)
		switch {
		case n.Error != "":
			nc.Note = "no data: " + n.Error
		case match == nil:
			if s, ok := n.Stats[lead.Series]; ok {
				nc.Comparable = true
				c.Comparable++
				nc.Baseline = s.Baseline
				nc.Value = extremeFor(s, lead.Direction)
				nc.Note = "series present, no anomaly detected"
			} else {
				nc.Note = "does not report " + lead.Series + ", not comparable"
			}
		default:
			nc.Comparable = true
			c.Comparable++
			skew := match.Onset.Sub(lead.Onset).Seconds()
			nc.OnsetSkewS = round(skew, 1)
			nc.Value = match.Value
			nc.Baseline = match.Baseline
			nc.Severity = match.Severity
			if math.Abs(skew) > onsetTolerance.Seconds() {
				nc.Note = fmt.Sprintf("same %s, but onset is %s apart and therefore not simultaneous",
					lead.Direction, time.Duration(math.Abs(skew))*time.Second)
				break
			}
			nc.Correlated = true
			c.Affected++
			if math.Abs(skew) > c.MaxOnsetSkew {
				c.MaxOnsetSkew = round(math.Abs(skew), 1)
			}
		}
		c.Neighbors = append(c.Neighbors, nc)
	}
	return c
}

func leadingAnomaly(d *DeviceReport) *Anomaly {
	var best *Anomaly
	for i := range d.Anomalies {
		a := &d.Anomalies[i]
		if a.Kind == "gap" {
			continue
		}
		if best == nil || stronger(a, best) > 0 {
			best = a
		}
	}
	return best
}

func matchingAnomaly(d *DeviceReport, series, direction string) *Anomaly {
	for i := range d.Anomalies {
		a := &d.Anomalies[i]
		if a.Series == series && a.Direction == direction {
			return a
		}
	}
	return nil
}

// verdict derives the conclusion from the collected evidence.
func verdict(b *Briefing) Verdict {
	lead := leadingAnomaly(&b.Target)
	if lead == nil {
		return Verdict{
			Scope: "inconclusive", Confidence: "low",
			Headline: fmt.Sprintf("No measurable anomaly on %s in the analysed window", deviceLabel(&b.Target)),
			Reasoning: []string{
				fmt.Sprintf("%d measurements analysed between %s and %s without threshold breach or significant deviation",
					b.Target.MeasurementsGot, ts(b.Window.From), ts(b.Window.To)),
				"widen the window with before/after, or verify the reported fault timestamp",
			},
		}
	}

	c := b.Correlation
	group := primaryGroup(b)
	restarts := countEvents(&b.Target, "restart", "reboot", "reset")

	// Only neighbours that report the leading series are evidence. Counting the
	// rest would let an unrelated door sensor in the same group vote against a
	// feeder-wide fault.
	var comparable int
	if c != nil {
		comparable = c.Comparable
	}

	if c != nil && comparable > 0 && c.Affected > 0 && c.Affected*2 >= comparable {
		v := Verdict{
			Scope: "shared-infrastructure",
			Headline: fmt.Sprintf("Shared upstream fault in %s: %d of %d comparable neighbouring assets show the same %s %s within %.0fs",
				group, c.Affected, comparable, c.Series, c.Direction, c.MaxOnsetSkew),
			Confidence: "medium",
		}
		if c.Affected >= 2 && c.MaxOnsetSkew <= 90 {
			v.Confidence = "high"
		}
		v.Reasoning = append(v.Reasoning,
			fmt.Sprintf("%s on %s: %s", strings.ToLower(lead.Kind), deviceLabel(&b.Target), lead.Description),
			fmt.Sprintf("the same %s occurs on %d of %d assets reporting %s in %s with at most %.0fs onset skew, which no device-local defect can produce",
				c.Direction, c.Affected, comparable, c.Series, group, c.MaxOnsetSkew))
		if unaffected := comparable - c.Affected; unaffected > 0 {
			v.Reasoning = append(v.Reasoning,
				fmt.Sprintf("%d comparable asset(s) stay nominal, so the disturbance is bounded to a part of the topology, not the whole tenant", unaffected))
		}
		if skipped := c.Total - comparable; skipped > 0 {
			v.Reasoning = append(v.Reasoning,
				fmt.Sprintf("%d further neighbour(s) do not report %s and were excluded from the ratio rather than counted as unaffected",
					skipped, c.Series))
		}
		if restarts > 0 {
			v.Reasoning = append(v.Reasoning,
				fmt.Sprintf("%d restart/reset event(s) on the target coincide with the disturbance, consistent with a supply side interruption rather than a software crash", restarts))
		}
		v.RuledOut = append(v.RuledOut,
			fmt.Sprintf("device hardware of %s: neighbouring devices with separate hardware fail at the same moment", deviceLabel(&b.Target)))
		v.RuledOut = append(v.RuledOut, firmwareAssessment(b, lead)...)
		return v
	}

	v := Verdict{
		Scope: "device-local",
		Headline: fmt.Sprintf("Fault isolated to %s: %s %s with no correlated neighbour",
			deviceLabel(&b.Target), lead.Series, lead.Direction),
		Confidence: "medium",
		Reasoning: []string{
			fmt.Sprintf("%s on %s: %s", strings.ToLower(lead.Kind), deviceLabel(&b.Target), lead.Description),
		},
	}
	switch {
	case c != nil && comparable > 0:
		v.Reasoning = append(v.Reasoning,
			fmt.Sprintf("none of the %d neighbouring assets in %s that report %s shows the same behaviour in the same window",
				comparable, group, c.Series))
	case c != nil && c.Total > 0:
		// Neighbours exist but none carries the series, so they cannot refute a
		// shared cause and the verdict must not pretend otherwise.
		v.Confidence = "low"
		v.Reasoning = append(v.Reasoning,
			fmt.Sprintf("none of the %d neighbouring assets in %s reports %s, so a shared cause could neither be confirmed nor excluded",
				c.Total, group, c.Series))
	default:
		v.Confidence = "low"
		v.Reasoning = append(v.Reasoning, "no neighbouring assets available, so a shared cause could not be excluded")
	}
	v.RuledOut = append(v.RuledOut, firmwareAssessment(b, lead)...)
	return v
}

// firmwareAssessment checks whether a firmware change can explain the onset.
func firmwareAssessment(b *Briefing, lead *Anomaly) []string {
	if len(b.FirmwareTimeline) == 0 {
		return nil
	}
	var out []string
	for _, fw := range b.FirmwareTimeline {
		if !strings.EqualFold(fw.Device, b.Target.Name) && fw.Device != b.Target.ID {
			continue
		}
		age := lead.Onset.Sub(fw.Time)
		switch {
		case age < 0:
			continue
		case age <= 48*time.Hour:
			out = append(out, fmt.Sprintf(
				"firmware change to %s on %s is %s before the anomaly and cannot be excluded from the data alone",
				fw.Version, ts(fw.Time), age.Round(time.Minute)))
		default:
			out = append(out, fmt.Sprintf(
				"firmware change to %s on %s predates the anomaly by %s and is not correlated with its onset",
				fw.Version, ts(fw.Time), age.Round(time.Hour)))
		}
	}
	// Peers running the same version without symptoms are strong evidence.
	if b.Target.Firmware != "" {
		var same, healthy int
		for i := range b.Neighbors {
			n := &b.Neighbors[i]
			if n.Firmware == b.Target.Firmware {
				same++
				if !n.Anomalous() {
					healthy++
				}
			}
		}
		if healthy > 0 {
			out = append(out, fmt.Sprintf(
				"%d of %d peers run the identical firmware %s without symptoms",
				healthy, same, b.Target.Firmware))
		}
	}
	return out
}

// recommendations derives field technician steps from the verdict.
func recommendations(b *Briefing) []string {
	lead := leadingAnomaly(&b.Target)
	if lead == nil {
		return []string{
			"Confirm the reported fault timestamp with the reporter and re-run the analysis with a wider window.",
			"Verify that the device actually reported during the incident; check connectivity and required availability settings.",
		}
	}
	group := primaryGroup(b)
	unit := seriesUnit(&b.Target, lead.Series)
	kind := quantityKind(lead.Series, unit)

	var out []string
	if b.Verdict.Scope == "shared-infrastructure" {
		out = append(out,
			fmt.Sprintf("Do not replace %s: the same disturbance appears on %d neighbouring asset(s) at the same moment.",
				deviceLabel(&b.Target), b.Correlation.Affected))
		switch kind {
		case "voltage":
			out = append(out,
				fmt.Sprintf("Inspect the shared supply of %s: transformer tap setting, fuses and links, and the terminations in the distribution cabinet.", group),
				fmt.Sprintf("Measure at the busbar feeding %s during the same time of day (%s) and compare with the node readings in this briefing.",
					group, b.FaultTime.Format("15:04")),
				"Check for load growth or newly connected consumers on this circuit since the first episode listed below.",
				"Install a power quality logger for at least 7 days at the worst affected node and at the supply point.")
		case "current":
			out = append(out,
				fmt.Sprintf("Check the shared load path of %s for an overload or a partially failed conductor.", group),
				"Compare the load profile against the circuit rating during the peak period identified in the charts.")
		case "temperature":
			out = append(out,
				fmt.Sprintf("Inspect ventilation, filters and ambient conditions at %s: several assets heat up together.", group),
				"Thermal image the cabinet under load to locate the common heat source.")
		default:
			out = append(out,
				fmt.Sprintf("Inspect the infrastructure shared by the assets in %s (supply, network uplink, environment).", group))
		}
	} else {
		out = append(out,
			fmt.Sprintf("Inspect %s on site: terminations, wiring and sensor connections.", deviceLabel(&b.Target)),
			fmt.Sprintf("Capture the device log around %s and compare its configuration with an unaffected peer.", ts(lead.Onset)))
		if b.Target.Firmware != "" {
			out = append(out, fmt.Sprintf("Compare firmware %s with the version of the healthy peers and consider a rollback if the onset follows an update.", b.Target.Firmware))
		}
		out = append(out, "If the symptom recurs after inspection, swap the unit and keep the removed one for bench analysis.")
	}

	if countEvents(&b.Target, "restart", "reboot", "reset") > 0 {
		out = append(out, "Verify the restart cause on site (brown-out detector, watchdog) and check whether a backup supply or buffer capacitor is fitted.")
	}
	if len(b.Target.Gaps) > 0 {
		out = append(out, fmt.Sprintf("Expect %d telemetry gap(s) in the record; confirm on site that no data was lost due to a logger fault.", len(b.Target.Gaps)))
	}
	out = append(out, "Attach this briefing to the ticket and record the measured values so the next episode can be compared against it.")
	return out
}

func countEvents(d *DeviceReport, needles ...string) int {
	var n int
	for _, ev := range d.Events {
		hay := strings.ToLower(ev.Type + " " + ev.Text)
		for _, needle := range needles {
			if strings.Contains(hay, needle) {
				n++
				break
			}
		}
	}
	return n
}

func sharedGroup(a, b *DeviceReport) string {
	for _, g := range a.Groups {
		for _, h := range b.Groups {
			if g == h {
				return g
			}
		}
	}
	return ""
}

func primaryGroup(b *Briefing) string {
	if len(b.Target.Groups) > 0 {
		return b.Target.Groups[0]
	}
	return "the asset group"
}

func seriesUnit(d *DeviceReport, series string) string {
	if s, ok := d.Stats[series]; ok {
		return s.Unit
	}
	return ""
}

// quantityKind guesses the physical quantity from unit and series name, which
// drives the wording of the recommendations.
func quantityKind(series, unit string) string {
	s := strings.ToLower(series)
	switch {
	case unit == "V" || strings.Contains(s, "volt"):
		return "voltage"
	case unit == "A" || strings.Contains(s, "current"):
		return "current"
	case unit == "C" || unit == "°C" || strings.Contains(s, "temp"):
		return "temperature"
	default:
		return "generic"
	}
}

func extremeFor(s *SeriesStats, direction string) float64 {
	if direction == "drop" {
		return s.Min
	}
	return s.Max
}

func deviceLabel(d *DeviceReport) string {
	if d.Name != "" {
		return fmt.Sprintf("%s (%s)", d.Name, d.ID)
	}
	return d.ID
}

func severityRank(s string) int {
	switch strings.ToUpper(s) {
	case "CRITICAL":
		return 4
	case "MAJOR":
		return 3
	case "MINOR":
		return 2
	case "WARNING":
		return 1
	default:
		return 0
	}
}

func verb(direction string) string {
	if direction == "drop" {
		return "dropped"
	}
	return "rose"
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05Z") }

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func mad(v []float64, med float64) float64 {
	if len(v) == 0 {
		return 0
	}
	dev := make([]float64, len(v))
	for i, x := range v {
		dev[i] = math.Abs(x - med)
	}
	return median(dev)
}

func medianDelta(pts []Point) time.Duration {
	if len(pts) < 2 {
		return 0
	}
	deltas := make([]float64, 0, len(pts)-1)
	for i := 1; i < len(pts); i++ {
		deltas = append(deltas, pts[i].Time.Sub(pts[i-1].Time).Seconds())
	}
	return time.Duration(median(deltas) * float64(time.Second))
}

// downsample reduces a series to at most n points while keeping the extremes
// of every bucket, so spikes survive.
func downsample(pts []Point, n int) []Point {
	if n <= 0 || len(pts) <= n {
		return pts
	}
	buckets := n / 2
	size := float64(len(pts)) / float64(buckets)
	out := make([]Point, 0, n)
	for i := range buckets {
		lo := int(float64(i) * size)
		hi := min(int(float64(i+1)*size), len(pts))
		if lo >= hi {
			continue
		}
		minP, maxP := pts[lo], pts[lo]
		for _, p := range pts[lo:hi] {
			if p.Value < minP.Value {
				minP = p
			}
			if p.Value > maxP.Value {
				maxP = p
			}
		}
		if minP.Time.Before(maxP.Time) {
			out = append(out, minP, maxP)
		} else {
			out = append(out, maxP, minP)
		}
	}
	return out
}

func round(v float64, digits int) float64 {
	f := math.Pow(10, float64(digits))
	return math.Round(v*f) / f
}
