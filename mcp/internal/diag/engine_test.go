package diag_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/c8y"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/diag"
)

var fault = time.Date(2026, 9, 21, 14, 5, 0, 0, time.UTC)

// device describes a fake asset of the fake tenant. A door sensor reports a
// completely different series and stands in for the unrelated equipment that
// real asset groups are full of.
type device struct {
	id      string
	name    string
	sagTo   float64 // voltage during the episode, 0 means no sag
	healthy bool
	door    bool
}

var fakeDevices = map[string]device{
	"1": {id: "1", name: "Power Node 01", sagTo: 178},
	"2": {id: "2", name: "Power Node 02", sagTo: 189},
	"3": {id: "3", name: "Power Node 03", healthy: true},
	"4": {id: "4", name: "Door Sensor 01", healthy: true, door: true},
	"5": {id: "5", name: "Door Sensor 02", healthy: true, door: true},
	"6": {id: "6", name: "Door Sensor 03", healthy: true, door: true},
}

// powerNodes is the homogeneous group; mixedGroup adds unrelated assets.
var (
	powerNodes = []string{"1", "2", "3"}
	mixedGroup = []string{"1", "2", "3", "4", "5", "6"}
)

func fakeTenant(t *testing.T, children []string) *httptest.Server {
	t.Helper()

	mo := func(d device) string {
		kind, thresholds := "sim_PowerNode", `,
 "sim_Thresholds":{"sim_Voltage.U":{"min":207,"max":253,"severity":"MAJOR","alarmType":"sim_UnderVoltage","unit":"V"}}`
		if d.door {
			kind, thresholds = "sim_DoorSensor", ""
		}
		return fmt.Sprintf(`{
 "id":%q,"name":%q,"type":%q,"c8y_IsDevice":{},
 "assetParents":{"references":[{"managedObject":{"id":"100","name":"SIM Substation Alpha"}}]},
 "c8y_Firmware":{"name":"powernode-fw","version":"1.4.2"},
 "c8y_Hardware":{"model":"SIM-PN-100","serialNumber":%q}%s
}`, d.id, d.name, kind, strings.ToLower(strings.ReplaceAll(d.name, " ", "-")), thresholds)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/inventory/managedObjects/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/inventory/managedObjects/")
		if strings.HasSuffix(id, "/childAssets") {
			refs := make([]string, 0, len(children))
			for _, c := range children {
				refs = append(refs, fmt.Sprintf(`{"managedObject":{"id":%q,"name":%q}}`,
					c, fakeDevices[c].name))
			}
			_, _ = fmt.Fprintf(w, `{"references":[%s]}`, strings.Join(refs, ","))
			return
		}
		d, ok := fakeDevices[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, mo(d))
	})

	mux.HandleFunc("/measurement/measurements", func(w http.ResponseWriter, r *http.Request) {
		d := fakeDevices[r.URL.Query().Get("source")]
		from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("dateFrom"))
		to, _ := time.Parse(time.RFC3339, r.URL.Query().Get("dateTo"))

		var items []string
		for ts := from; ts.Before(to); ts = ts.Add(time.Minute) {
			if d.door {
				items = append(items, fmt.Sprintf(`{"id":"m","time":%q,"type":"sim_DoorTelemetry",
 "source":{"id":%q},"sim_Door":{"state":{"value":0,"unit":""}}}`, ts.Format(time.RFC3339), d.id))
				continue
			}
			voltage, current := 230.0, 12.0
			if i := ts.Sub(from).Minutes(); int(i)%3 == 0 {
				voltage, current = 230.4, 12.2 // benign jitter
			}
			if !d.healthy && !ts.Before(fault) && ts.Before(fault.Add(10*time.Minute)) {
				voltage, current = d.sagTo, 18.5
			}
			items = append(items, fmt.Sprintf(`{"id":"m","time":%q,"type":"sim_PowerNodeTelemetry",
 "source":{"id":%q},"sim_Voltage":{"U":{"value":%.1f,"unit":"V"}},
 "sim_Current":{"I":{"value":%.1f,"unit":"A"}}}`, ts.Format(time.RFC3339), d.id, voltage, current))
		}
		_, _ = fmt.Fprintf(w, `{"measurements":[%s],"next":""}`, strings.Join(items, ","))
	})

	mux.HandleFunc("/alarm/alarms", func(w http.ResponseWriter, r *http.Request) {
		d := fakeDevices[r.URL.Query().Get("source")]
		if d.healthy {
			_, _ = fmt.Fprint(w, `{"alarms":[]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"alarms":[{"id":"a1","type":"sim_UnderVoltage","text":"Under-voltage",
 "severity":"MAJOR","status":"ACTIVE","count":1,"time":%q,"source":{"id":%q}}]}`,
			fault.Add(30*time.Second).Format(time.RFC3339), d.id)
	})

	mux.HandleFunc("/event/events", func(w http.ResponseWriter, r *http.Request) {
		d := fakeDevices[r.URL.Query().Get("source")]
		events := []string{fmt.Sprintf(`{"id":"e1","type":"sim_FirmwareUpdated",
 "text":"Firmware updated from 1.4.0 to 1.4.2","time":%q,"source":{"id":%q},
 "c8y_Firmware":{"version":"1.4.2","previousVersion":"1.4.0"}}`,
			fault.Add(-6*24*time.Hour).Format(time.RFC3339), d.id)}
		if d.id == "1" {
			events = append(events, fmt.Sprintf(`{"id":"e2","type":"sim_BrownoutReset",
 "text":"Controller restarted: brown-out detector tripped","time":%q,"source":{"id":%q}}`,
				fault.Add(4*time.Minute).Format(time.RFC3339), d.id))
		}
		_, _ = fmt.Fprintf(w, `{"events":[%s]}`, strings.Join(events, ","))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func analyse(t *testing.T, req diag.Request) *diag.Briefing {
	t.Helper()
	return analyseIn(t, req, powerNodes)
}

func analyseIn(t *testing.T, req diag.Request, children []string) *diag.Briefing {
	t.Helper()
	srv := fakeTenant(t, children)
	client, err := c8y.New(c8y.Config{BaseURL: srv.URL, Tenant: "t1", User: "u", Password: "p"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	b, err := diag.NewEngine(client).Analyze(context.Background(), req)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return b
}

func baseRequest() diag.Request {
	return diag.Request{
		Device: "1", FaultTime: fault, Before: 2 * time.Hour, After: 30 * time.Minute,
		IncludeNeighbors: true, MaxNeighbors: 6,
	}
}

func TestAnalyzeDetectsSharedInfrastructureFault(t *testing.T) {
	b := analyse(t, baseRequest())

	if got, want := b.Verdict.Scope, "shared-infrastructure"; got != want {
		t.Fatalf("scope = %q, want %q (headline: %s)", got, want, b.Verdict.Headline)
	}
	if b.Correlation == nil {
		t.Fatal("no correlation computed")
	}
	if b.Correlation.Affected != 1 || b.Correlation.Total != 2 {
		t.Fatalf("correlation = %d/%d affected, want 1/2", b.Correlation.Affected, b.Correlation.Total)
	}
	if b.Correlation.Comparable != 2 {
		t.Fatalf("comparable = %d, want 2 in a homogeneous group", b.Correlation.Comparable)
	}
	if b.Correlation.Series != "sim_Voltage.U" {
		t.Fatalf("correlated series = %q", b.Correlation.Series)
	}
	if b.Correlation.MaxOnsetSkew > 60 {
		t.Fatalf("onset skew = %.0fs, want simultaneous", b.Correlation.MaxOnsetSkew)
	}
	if b.Verdict.Confidence != "medium" && b.Verdict.Confidence != "high" {
		t.Fatalf("confidence = %q", b.Verdict.Confidence)
	}
}

func TestAnalyzeDetectsThresholdBreachOnTarget(t *testing.T) {
	b := analyse(t, baseRequest())

	var found bool
	for _, a := range b.Target.Anomalies {
		if a.Series == "sim_Voltage.U" && a.Kind == "threshold-breach" {
			found = true
			if a.Value > 180 {
				t.Errorf("extreme value = %.1f, want the sag minimum", a.Value)
			}
			if !a.Onset.Equal(fault) {
				t.Errorf("onset = %s, want %s", a.Onset, fault)
			}
		}
	}
	if !found {
		t.Fatalf("no threshold breach detected, anomalies: %+v", b.Target.Anomalies)
	}
}

func TestFirmwareIsRuledOut(t *testing.T) {
	b := analyse(t, baseRequest())

	joined := strings.ToLower(strings.Join(b.Verdict.RuledOut, " | "))
	if !strings.Contains(joined, "firmware") {
		t.Fatalf("firmware not assessed, ruled out: %v", b.Verdict.RuledOut)
	}
	if !strings.Contains(joined, "not correlated") && !strings.Contains(joined, "without symptoms") {
		t.Fatalf("firmware assessment is not conclusive: %v", b.Verdict.RuledOut)
	}
}

func TestRecommendationsAdviseAgainstSwap(t *testing.T) {
	b := analyse(t, baseRequest())

	joined := strings.ToLower(strings.Join(b.Recommendations, " | "))
	if !strings.Contains(joined, "do not replace") {
		t.Fatalf("expected advice against a device swap, got: %v", b.Recommendations)
	}
	if !strings.Contains(joined, "restart") {
		t.Fatalf("expected the reset events to be picked up, got: %v", b.Recommendations)
	}
}

func TestHealthyDeviceYieldsNoVerdictNoise(t *testing.T) {
	req := baseRequest()
	req.Device = "3"
	b := analyse(t, req)

	if b.Verdict.Scope != "inconclusive" {
		t.Fatalf("scope = %q, want inconclusive for a healthy device (%s)",
			b.Verdict.Scope, b.Verdict.Headline)
	}
	if len(b.Target.Anomalies) != 0 {
		t.Fatalf("healthy device reported anomalies: %+v", b.Target.Anomalies)
	}
}

// TestUnrelatedNeighboursDoNotDiluteCorrelation guards the ratio that decides
// between a site-wide fault and a device swap. The same disturbance on the same
// two power nodes must still read as shared infrastructure when the group also
// holds three door sensors, which report nothing that could confirm or refute
// it. Counting those as "unaffected" would flip the verdict to device-local and
// send a technician to replace a healthy unit.
func TestUnrelatedNeighboursDoNotDiluteCorrelation(t *testing.T) {
	b := analyseIn(t, baseRequest(), mixedGroup)

	c := b.Correlation
	if c == nil {
		t.Fatal("no correlation computed")
	}
	if c.Total != 5 {
		t.Fatalf("total neighbours = %d, want all 5 in the group", c.Total)
	}
	if c.Comparable != 2 {
		t.Fatalf("comparable = %d, want only the 2 power nodes reporting %s", c.Comparable, c.Series)
	}
	if c.Affected != 1 {
		t.Fatalf("affected = %d, want 1", c.Affected)
	}
	if got, want := b.Verdict.Scope, "shared-infrastructure"; got != want {
		t.Fatalf("scope = %q, want %q: unrelated assets diluted the ratio (headline: %s)",
			got, want, b.Verdict.Headline)
	}

	for _, n := range c.Neighbors {
		if strings.HasPrefix(n.Name, "Door") && n.Comparable {
			t.Errorf("%s reports no %s but was marked comparable", n.Name, c.Series)
		}
	}
}

// TestNoComparableNeighbourLowersConfidence covers the other half: neighbours
// that cannot speak to the series must not be read as evidence of isolation.
func TestNoComparableNeighbourLowersConfidence(t *testing.T) {
	req := baseRequest()
	b := analyseIn(t, req, []string{"1", "4", "5", "6"})

	if b.Verdict.Scope != "device-local" {
		t.Fatalf("scope = %q, want device-local", b.Verdict.Scope)
	}
	if b.Verdict.Confidence != "low" {
		t.Fatalf("confidence = %q, want low when nothing can refute a shared cause", b.Verdict.Confidence)
	}
	joined := strings.ToLower(strings.Join(b.Verdict.Reasoning, " | "))
	if !strings.Contains(joined, "neither be confirmed nor excluded") {
		t.Fatalf("reasoning does not flag the missing comparison: %v", b.Verdict.Reasoning)
	}
}

func TestBriefingIsJSONSerialisable(t *testing.T) {
	b := analyse(t, baseRequest())
	if _, err := json.Marshal(b); err != nil {
		t.Fatalf("marshal briefing: %v", err)
	}
}
