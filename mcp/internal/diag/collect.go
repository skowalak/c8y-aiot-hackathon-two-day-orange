package diag

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/c8y"
)

// Engine runs diagnostic analyses against a Cumulocity tenant.
type Engine struct {
	client *c8y.Client

	// MaxPoints caps the measurements fetched per device.
	MaxPoints int
	// ChartPoints caps the samples kept per series for rendering.
	ChartPoints int
	// UseSmartRules additionally queries the smart rule service.
	UseSmartRules bool
}

// NewEngine returns an engine with sane limits.
func NewEngine(client *c8y.Client) *Engine {
	return &Engine{client: client, MaxPoints: 20000, ChartPoints: 360}
}

func (r *Request) applyDefaults() {
	if r.FaultTime.IsZero() {
		r.FaultTime = time.Now()
	}
	if r.Before <= 0 {
		r.Before = 2 * time.Hour
	}
	if r.After <= 0 {
		r.After = 30 * time.Minute
	}
	if r.MaxNeighbors <= 0 {
		r.MaxNeighbors = 6
	}
	if r.AlarmLookback <= 0 {
		r.AlarmLookback = 7 * 24 * time.Hour
	}
}

// Analyze collects the evidence around the fault and derives the briefing.
func (e *Engine) Analyze(ctx context.Context, req Request) (*Briefing, error) {
	req.applyDefaults()
	window := Window{From: req.FaultTime.Add(-req.Before), To: req.FaultTime.Add(req.After)}
	if window.To.After(time.Now()) {
		window.To = time.Now()
	}

	target, err := e.client.ResolveDevice(ctx, req.Device)
	if err != nil {
		return nil, err
	}
	slog.Info("analysing device", "id", target.ID, "name", target.Name,
		"fault", req.FaultTime.Format(time.RFC3339))

	groups := groupsOf(target)
	var neighborMOs []*c8y.ManagedObject
	var notes []string
	if req.IncludeNeighbors {
		neighborMOs, notes = e.neighbors(ctx, target, req.MaxNeighbors)
	}

	all := append([]*c8y.ManagedObject{target}, neighborMOs...)
	reports := make([]DeviceReport, len(all))
	var wg sync.WaitGroup
	for i, mo := range all {
		wg.Add(1)
		go func(i int, mo *c8y.ManagedObject) {
			defer wg.Done()
			role := "neighbor"
			if i == 0 {
				role = "target"
			}
			reports[i] = e.collect(ctx, mo, role, window, req)
		}(i, mo)
	}
	wg.Wait()

	briefing := &Briefing{
		GeneratedAt: time.Now().UTC(),
		FaultTime:   req.FaultTime.UTC(),
		Symptom:     req.Symptom,
		Window:      window,
		Target:      reports[0],
		Neighbors:   reports[1:],
		Notes:       notes,
	}
	if len(groups) > 0 {
		briefing.Target.Groups = groups
	}

	briefing.FirmwareTimeline = e.firmwareTimeline(ctx, all, req)
	briefing.Correlation = correlate(&briefing.Target, briefing.Neighbors)
	briefing.Verdict = verdict(briefing)
	briefing.Recommendations = recommendations(briefing)
	return briefing, nil
}

// neighbors returns the sibling assets of the target in its groups.
func (e *Engine) neighbors(ctx context.Context, target *c8y.ManagedObject, limit int) ([]*c8y.ManagedObject, []string) {
	var notes []string
	seen := map[string]bool{target.ID: true}
	var out []*c8y.ManagedObject

	for _, parent := range target.AssetParents.References {
		children, err := e.client.ChildAssets(ctx, parent.ManagedObject.ID)
		if err != nil {
			notes = append(notes, fmt.Sprintf("could not read group %s: %v", parent.ManagedObject.ID, err))
			continue
		}
		for _, child := range children {
			if seen[child.ID] || len(out) >= limit {
				continue
			}
			seen[child.ID] = true
			mo, err := e.client.ManagedObject(ctx, child.ID)
			if err != nil {
				notes = append(notes, fmt.Sprintf("could not read asset %s: %v", child.ID, err))
				continue
			}
			if _, isDevice := mo.Fragments["c8y_IsDevice"]; !isDevice {
				continue
			}
			out = append(out, mo)
		}
	}
	if len(out) == 0 {
		notes = append(notes, "no neighbouring assets found; correlation is limited to the target device")
	}
	return out, notes
}

// collect gathers and analyses the evidence of a single asset.
func (e *Engine) collect(ctx context.Context, mo *c8y.ManagedObject, role string, window Window, req Request) DeviceReport {
	rep := DeviceReport{
		ID:         mo.ID,
		Name:       mo.Name,
		Type:       mo.Type,
		Role:       role,
		Groups:     groupsOf(mo),
		Firmware:   firmwareVersion(mo),
		Hardware:   hardwareLabel(mo),
		Thresholds: thresholds(mo),
	}
	if e.UseSmartRules {
		rep.Thresholds = append(rep.Thresholds, e.smartRuleThresholds(ctx, mo.ID)...)
	}

	lookbackFrom := req.FaultTime.Add(-req.AlarmLookback)

	ms, err := e.client.Measurements(ctx, mo.ID, window.From, window.To, e.MaxPoints)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.MeasurementsGot = len(ms)
	rep.Stats, rep.Samples = summarise(ms, req.FaultTime, e.ChartPoints)
	rep.Gaps = gaps(ms, window)
	rep.Anomalies = anomalies(rep.Stats, rep.Samples, rep.Thresholds, req.FaultTime)
	for _, g := range rep.Gaps {
		rep.Anomalies = append(rep.Anomalies, Anomaly{
			Kind: "gap", Severity: "MAJOR", Onset: g.From,
			Description: fmt.Sprintf("telemetry gap of %s", g.Duration),
		})
	}

	if alarms, err := e.client.Alarms(ctx, mo.ID, lookbackFrom, window.To, 200); err == nil {
		for _, a := range alarms {
			rep.Alarms = append(rep.Alarms, AlarmSummary{
				ID: a.ID, Type: a.Type, Text: a.Text, Severity: a.Severity,
				Status: a.Status, Count: max(a.Count, 1), Time: a.Time.UTC(),
				InWindow: !a.Time.Before(window.From) && !a.Time.After(window.To),
			})
		}
		sort.Slice(rep.Alarms, func(i, j int) bool { return rep.Alarms[i].Time.After(rep.Alarms[j].Time) })
	} else {
		rep.Error = strings.TrimSpace(rep.Error + " " + err.Error())
	}

	if events, err := e.client.Events(ctx, mo.ID, lookbackFrom, window.To, 200); err == nil {
		for _, ev := range events {
			if ev.Time.Before(window.From) && !isFirmwareEvent(ev.Type) {
				continue // keep the window tight, except for firmware history
			}
			rep.Events = append(rep.Events, EventSummary{
				ID: ev.ID, Type: ev.Type, Text: ev.Text, Time: ev.Time.UTC(),
			})
		}
		sort.Slice(rep.Events, func(i, j int) bool { return rep.Events[i].Time.After(rep.Events[j].Time) })
	}
	return rep
}

func (e *Engine) smartRuleThresholds(ctx context.Context, deviceID string) []ThresholdRule {
	rules, err := e.client.SmartRules(ctx, deviceID)
	if err != nil {
		return nil
	}
	var out []ThresholdRule
	for _, r := range rules {
		name, _ := r["name"].(string)
		cfg, ok := r["config"].(map[string]any)
		if !ok {
			continue
		}
		series, _ := cfg["fragment"].(string)
		if s, ok := cfg["series"].(string); ok && series != "" {
			series += "." + s
		}
		rule := ThresholdRule{Series: series, Origin: "smart rule " + name}
		if v, ok := cfg["minValue"].(float64); ok {
			rule.Min = &v
		}
		if v, ok := cfg["maxValue"].(float64); ok {
			rule.Max = &v
		}
		if rule.Min != nil || rule.Max != nil {
			out = append(out, rule)
		}
	}
	return out
}

// firmwareTimeline merges the current firmware of every asset with the
// firmware related events found in the lookback period.
func (e *Engine) firmwareTimeline(ctx context.Context, mos []*c8y.ManagedObject, req Request) []FirmwareChange {
	var out []FirmwareChange
	from := req.FaultTime.Add(-req.AlarmLookback)
	to := req.FaultTime.Add(req.After)

	for _, mo := range mos {
		events, err := e.client.Events(ctx, mo.ID, from, to, 200)
		if err != nil {
			continue
		}
		for _, ev := range events {
			if !isFirmwareEvent(ev.Type) {
				continue
			}
			change := FirmwareChange{
				Device: label(mo), Time: ev.Time.UTC(), Text: ev.Text,
			}
			var fw struct {
				Version  string `json:"version"`
				Previous string `json:"previousVersion"`
			}
			if raw, ok := ev.Fragments["c8y_Firmware"]; ok {
				_ = json.Unmarshal(raw, &fw)
			}
			change.Version, change.Previous = fw.Version, fw.Previous
			out = append(out, change)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func isFirmwareEvent(t string) bool {
	return strings.Contains(strings.ToLower(t), "firmware")
}

func groupsOf(mo *c8y.ManagedObject) []string {
	var out []string
	for _, ref := range mo.AssetParents.References {
		if ref.ManagedObject.Name != "" {
			out = append(out, ref.ManagedObject.Name)
		} else {
			out = append(out, ref.ManagedObject.ID)
		}
	}
	return out
}

func label(mo *c8y.ManagedObject) string {
	if mo.Name != "" {
		return mo.Name
	}
	return mo.ID
}

func firmwareVersion(mo *c8y.ManagedObject) string {
	var fw struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if !mo.Fragment("c8y_Firmware", &fw) {
		return ""
	}
	if fw.Name != "" && fw.Version != "" {
		return fw.Name + " " + fw.Version
	}
	return fw.Version
}

func hardwareLabel(mo *c8y.ManagedObject) string {
	var hw struct {
		Model        string `json:"model"`
		Revision     string `json:"revision"`
		SerialNumber string `json:"serialNumber"`
	}
	if !mo.Fragment("c8y_Hardware", &hw) {
		return ""
	}
	parts := []string{hw.Model, hw.Revision, hw.SerialNumber}
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " / ")
}

// thresholds extracts threshold rules from inventory fragments. Any fragment
// that maps "fragment.series" keys to objects with min/max is accepted, so
// this is not tied to one naming convention.
func thresholds(mo *c8y.ManagedObject) []ThresholdRule {
	var out []ThresholdRule
	for name, raw := range mo.Fragments {
		var candidate map[string]struct {
			Min       *float64 `json:"min"`
			Max       *float64 `json:"max"`
			Severity  string   `json:"severity"`
			AlarmType string   `json:"alarmType"`
			Unit      string   `json:"unit"`
		}
		if err := json.Unmarshal(raw, &candidate); err != nil {
			continue
		}
		for series, rule := range candidate {
			if !strings.Contains(series, ".") || (rule.Min == nil && rule.Max == nil) {
				continue
			}
			out = append(out, ThresholdRule{
				Series: series, Min: rule.Min, Max: rule.Max,
				Severity: rule.Severity, AlarmType: rule.AlarmType,
				Unit: rule.Unit, Origin: "inventory " + name,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Series < out[j].Series })
	return out
}
