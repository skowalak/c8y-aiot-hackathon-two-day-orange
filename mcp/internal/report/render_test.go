package report_test

import (
	"strings"
	"testing"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/diag"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/report"
)

var fault = time.Date(2026, 9, 21, 14, 5, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }

// briefing builds a small but complete result: a voltage sag on the target that
// a neighbour on the same feeder sees at the same instant, plus a firmware
// update that has to be ruled out.
func briefing() *diag.Briefing {
	samples := func(base, sag float64) []diag.Point {
		var pts []diag.Point
		for i := range 40 {
			t := fault.Add(time.Duration(i-30) * time.Minute)
			v := base
			if !t.Before(fault) {
				v = sag
			}
			pts = append(pts, diag.Point{Time: t, Value: v})
		}
		return pts
	}
	volts := func(id, name string, sag float64) diag.DeviceReport {
		return diag.DeviceReport{
			ID: id, Name: name, Type: "sim_PowerNode", Role: "neighbor",
			Groups: []string{"Feeder-A"}, Firmware: "1.4.2", Hardware: "rev-C",
			Thresholds: []diag.ThresholdRule{{
				Series: "sim_Voltage.U", Min: f64(207), Max: f64(253),
				Severity: "MAJOR", Unit: "V", Origin: "smart rule Under-voltage",
			}},
			Stats: map[string]*diag.SeriesStats{"sim_Voltage.U": {
				Series: "sim_Voltage.U", Unit: "V", Count: 40,
				Min: sag, Max: 231, Mean: 220, Baseline: 230, Spread: 1.2,
				MinTime: fault, MaxTime: fault.Add(-30 * time.Minute), SampleRate: "1m0s",
			}},
			Samples: map[string][]diag.Point{"sim_Voltage.U": samples(230, sag)},
			Anomalies: []diag.Anomaly{{
				Series: "sim_Voltage.U", Kind: "threshold-breach", Direction: "drop",
				Severity: "MAJOR", Onset: fault, Peak: fault, Value: sag, Baseline: 230,
				Sigma: 41.7, Threshold: f64(207), Breaches: 10,
				Description: "sim_Voltage.U dropped to 180.00 V, breaching the 207.00 V limit",
			}},
			Alarms: []diag.AlarmSummary{{
				ID: "a1", Type: "sim_UnderVoltage", Text: "Under-voltage", Severity: "MAJOR",
				Status: "ACTIVE", Count: 1, Time: fault, InWindow: true,
			}},
			Events: []diag.EventSummary{{
				ID: "e1", Type: "c8y_FirmwareUpdate", Text: "Firmware updated to 1.4.2",
				Time: fault.Add(-6 * 24 * time.Hour),
			}},
			MeasurementsGot: 40,
		}
	}

	target := volts("1", "Power Node 01", 180)
	target.Role = "target"
	neighbor := volts("2", "Power Node 02", 191)
	quiet := volts("5", "Power Node 05", 229)
	quiet.Groups = []string{"Feeder-B"}
	quiet.Anomalies = nil
	quiet.Alarms = nil

	return &diag.Briefing{
		GeneratedAt: fault.Add(time.Hour), FaultTime: fault,
		Symptom: "node went dark",
		Window:  diag.Window{From: fault.Add(-2 * time.Hour), To: fault.Add(30 * time.Minute)},
		Target:  target, Neighbors: []diag.DeviceReport{neighbor, quiet},
		Correlation: &diag.Correlation{
			Series: "sim_Voltage.U", Direction: "drop", Affected: 1, Total: 2, MaxOnsetSkew: 0,
			Neighbors: []diag.NeighborCorrelation{
				{Device: "2", Name: "Power Node 02", Correlated: true, Value: 191,
					Baseline: 230, Severity: "MAJOR", SharedGroup: "Feeder-A"},
				{Device: "5", Name: "Power Node 05", Note: "series present, no anomaly detected",
					Value: 229, Baseline: 230},
			},
		},
		FirmwareTimeline: []diag.FirmwareChange{{
			Device: "Power Node 01", Time: fault.Add(-6 * 24 * time.Hour),
			Version: "1.4.2", Previous: "1.4.0", Text: "Firmware updated",
		}},
		Verdict: diag.Verdict{
			Scope: "shared-infrastructure", Confidence: "high",
			Headline:  "Supply-side voltage sag on Feeder-A, not a defect of Power Node 01",
			Reasoning: []string{"sim_Voltage.U drops below the 207 V limit on 2 of 3 assets at the same instant"},
			RuledOut:  []string{"Firmware 1.4.2: installed 6 days before the fault & also running on unaffected assets"},
		},
		Recommendations: []string{"Inspect the Feeder-A supply, not the node"},
		Notes:           []string{"Baseline computed from the 2 h before the fault"},
	}
}

func TestHTMLIsSelfContainedAndEscaped(t *testing.T) {
	b := briefing()
	b.Symptom = `node "died" <script>alert(1)</script>`

	out, err := report.HTML(b)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	doc := string(out)

	if !strings.HasPrefix(strings.TrimSpace(doc), "<!DOCTYPE html>") {
		t.Error("not a standalone HTML document")
	}
	if strings.Contains(doc, "<script>alert(1)</script>") {
		t.Error("symptom text was not escaped")
	}
	// A shareable report must not depend on anything the tenant has to fetch.
	for _, forbidden := range []string{"<script", "src=\"http", "href=\"http"} {
		if strings.Contains(doc, forbidden) {
			t.Errorf("report references external resource or script: %q", forbidden)
		}
	}
	for _, want := range []string{
		"Supply-side voltage sag", "shared-infrastructure", "sim_Voltage.U",
		"Power Node 02", "Firmware 1.4.2", "<svg", "Inspect the Feeder-A supply",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("HTML is missing %q", want)
		}
	}
}

func TestHTMLChartsThePrimarySeries(t *testing.T) {
	doc, err := report.HTML(briefing())
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if n := strings.Count(string(doc), "<svg"); n == 0 {
		t.Fatal("no chart rendered")
	}
	if !strings.Contains(string(doc), `class="series"`) {
		t.Error("chart has no data trace")
	}
	if !strings.Contains(string(doc), `class="threshold"`) {
		t.Error("chart does not draw the 207 V limit")
	}
	if !strings.Contains(string(doc), `class="marker"`) {
		t.Error("chart does not mark the fault time")
	}
}

func TestMarkdownCarriesTheVerdict(t *testing.T) {
	md := report.Markdown(briefing())

	for _, want := range []string{
		"# ", "Supply-side voltage sag", "sim_Voltage.U", "Power Node 02",
		"| ", "Inspect the Feeder-A supply",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown is missing %q", want)
		}
	}
	if strings.Contains(md, "<svg") {
		t.Error("Markdown should not embed SVG charts")
	}
}

func TestRenderersToleratePartialBriefing(t *testing.T) {
	b := &diag.Briefing{
		GeneratedAt: fault, FaultTime: fault,
		Window: diag.Window{From: fault.Add(-time.Hour), To: fault},
		Target: diag.DeviceReport{ID: "9", Name: "Silent Node", Role: "target",
			Error: "no measurements in window"},
		Verdict: diag.Verdict{Scope: "inconclusive", Headline: "Not enough data",
			Confidence: "low"},
	}
	if _, err := report.HTML(b); err != nil {
		t.Fatalf("HTML on empty briefing: %v", err)
	}
	if md := report.Markdown(b); !strings.Contains(md, "Not enough data") {
		t.Error("Markdown lost the verdict")
	}
}
