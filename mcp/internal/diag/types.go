// Package diag is the diagnostic engine: it collects the telemetry, alarm,
// event and configuration context around a fault, correlates it across
// neighbouring assets and derives a verdict with recommended field steps.
package diag

import "time"

// Request describes one analysis run.
type Request struct {
	Device           string
	FaultTime        time.Time
	Before           time.Duration
	After            time.Duration
	IncludeNeighbors bool
	MaxNeighbors     int
	AlarmLookback    time.Duration
	Symptom          string
}

// Window is the analysed time range.
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Point is one sample of a series.
type Point struct {
	Time  time.Time `json:"t"`
	Value float64   `json:"v"`
}

// ThresholdRule is an active threshold rule found in inventory or smart rules.
type ThresholdRule struct {
	Series    string   `json:"series"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	Severity  string   `json:"severity,omitempty"`
	AlarmType string   `json:"alarmType,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	Origin    string   `json:"origin"` // inventory fragment or smart rule
}

// SeriesStats summarises one measurement series inside the window.
type SeriesStats struct {
	Series   string  `json:"series"`
	Unit     string  `json:"unit,omitempty"`
	Count    int     `json:"count"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Mean     float64 `json:"mean"`
	Baseline float64 `json:"baseline"`
	// Spread is the robust deviation (1.4826*MAD) of the baseline period.
	Spread     float64   `json:"spread"`
	MinTime    time.Time `json:"minTime"`
	MaxTime    time.Time `json:"maxTime"`
	SampleRate string    `json:"sampleRate,omitempty"`
}

// Anomaly is a detected deviation in one series.
type Anomaly struct {
	Series      string    `json:"series"`
	Kind        string    `json:"kind"` // threshold-breach, deviation, gap
	Direction   string    `json:"direction,omitempty"`
	Severity    string    `json:"severity"`
	Onset       time.Time `json:"onset"`
	Peak        time.Time `json:"peak,omitempty"`
	Value       float64   `json:"value"`
	Baseline    float64   `json:"baseline"`
	Sigma       float64   `json:"sigma,omitempty"`
	Threshold   *float64  `json:"threshold,omitempty"`
	Breaches    int       `json:"breaches,omitempty"`
	Description string    `json:"description"`
}

// Gap is a hole in the telemetry.
type Gap struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Duration string    `json:"duration"`
}

// AlarmSummary is a compacted alarm record.
type AlarmSummary struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Text     string    `json:"text"`
	Severity string    `json:"severity"`
	Status   string    `json:"status"`
	Count    int       `json:"count"`
	Time     time.Time `json:"time"`
	InWindow bool      `json:"inWindow"`
}

// EventSummary is a compacted event record.
type EventSummary struct {
	ID   string    `json:"id"`
	Type string    `json:"type"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// FirmwareChange is an entry of the firmware change history.
type FirmwareChange struct {
	Device   string    `json:"device"`
	Time     time.Time `json:"time"`
	Version  string    `json:"version"`
	Previous string    `json:"previous,omitempty"`
	Text     string    `json:"text"`
}

// DeviceReport is the evidence collected for one asset.
type DeviceReport struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Type            string                  `json:"type"`
	Role            string                  `json:"role"` // target | neighbor
	Groups          []string                `json:"groups,omitempty"`
	Firmware        string                  `json:"firmware,omitempty"`
	Hardware        string                  `json:"hardware,omitempty"`
	Thresholds      []ThresholdRule         `json:"thresholds,omitempty"`
	Stats           map[string]*SeriesStats `json:"stats,omitempty"`
	Samples         map[string][]Point      `json:"-"`
	Anomalies       []Anomaly               `json:"anomalies,omitempty"`
	Gaps            []Gap                   `json:"gaps,omitempty"`
	Alarms          []AlarmSummary          `json:"alarms,omitempty"`
	Events          []EventSummary          `json:"events,omitempty"`
	MeasurementsGot int                     `json:"measurements"`
	Error           string                  `json:"error,omitempty"`
}

// Anomalous reports whether the asset shows any anomaly.
func (d *DeviceReport) Anomalous() bool { return len(d.Anomalies) > 0 }

// NeighborCorrelation is the comparison of one neighbour with the target.
type NeighborCorrelation struct {
	Device      string  `json:"device"`
	Name        string  `json:"name"`
	Correlated  bool    `json:"correlated"`
	OnsetSkewS  float64 `json:"onsetSkewSeconds,omitempty"`
	Value       float64 `json:"value,omitempty"`
	Baseline    float64 `json:"baseline,omitempty"`
	Severity    string  `json:"severity,omitempty"`
	SharedGroup string  `json:"sharedGroup,omitempty"`
	Note        string  `json:"note,omitempty"`
}

// Correlation is the cross asset picture for the leading anomaly.
type Correlation struct {
	Series       string                `json:"series"`
	Direction    string                `json:"direction"`
	Affected     int                   `json:"affectedNeighbors"`
	Total        int                   `json:"totalNeighbors"`
	MaxOnsetSkew float64               `json:"maxOnsetSkewSeconds"`
	Neighbors    []NeighborCorrelation `json:"neighbors,omitempty"`
}

// Verdict is the engine's conclusion.
type Verdict struct {
	Scope      string   `json:"scope"` // shared-infrastructure | device-local | inconclusive
	Headline   string   `json:"headline"`
	Confidence string   `json:"confidence"`
	Reasoning  []string `json:"reasoning"`
	RuledOut   []string `json:"ruledOut,omitempty"`
}

// Briefing is the complete diagnostic result.
type Briefing struct {
	GeneratedAt      time.Time        `json:"generatedAt"`
	FaultTime        time.Time        `json:"faultTime"`
	Symptom          string           `json:"symptom,omitempty"`
	Window           Window           `json:"window"`
	Target           DeviceReport     `json:"target"`
	Neighbors        []DeviceReport   `json:"neighbors,omitempty"`
	Correlation      *Correlation     `json:"correlation,omitempty"`
	FirmwareTimeline []FirmwareChange `json:"firmwareTimeline,omitempty"`
	Verdict          Verdict          `json:"verdict"`
	Recommendations  []string         `json:"recommendations"`
	Notes            []string         `json:"notes,omitempty"`
}

// Devices returns target and neighbours in one slice.
func (b *Briefing) Devices() []*DeviceReport {
	out := []*DeviceReport{&b.Target}
	for i := range b.Neighbors {
		out = append(out, &b.Neighbors[i])
	}
	return out
}
