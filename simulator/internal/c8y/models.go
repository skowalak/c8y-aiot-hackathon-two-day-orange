package c8y

import (
	"encoding/json"
	"time"
)

// Source references a managed object.
type Source struct {
	ID string `json:"id"`
}

// Value is a single measurement series value.
type Value struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
}

// Series is a set of named values belonging to one fragment.
type Series map[string]Value

// Measurement is a single point in time carrying one or more fragments.
type Measurement struct {
	Time      time.Time
	Type      string
	Source    string
	Fragments map[string]Series
}

// MarshalJSON flattens the fragments to top level keys as required by the
// Cumulocity measurement API.
func (m Measurement) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"time":   m.Time.UTC().Format(time.RFC3339Nano),
		"type":   m.Type,
		"source": Source{ID: m.Source},
	}
	for name, series := range m.Fragments {
		out[name] = series
	}
	return json.Marshal(out)
}

// Alarm mirrors the Cumulocity alarm API payload.
type Alarm struct {
	Source   Source         `json:"source"`
	Type     string         `json:"type"`
	Text     string         `json:"text"`
	Severity string         `json:"severity"`
	Status   string         `json:"status,omitempty"`
	Time     time.Time      `json:"time"`
	Extra    map[string]any `json:"-"`
}

func (a Alarm) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"source":   a.Source,
		"type":     a.Type,
		"text":     a.Text,
		"severity": a.Severity,
		"time":     a.Time.UTC().Format(time.RFC3339Nano),
	}
	if a.Status != "" {
		out["status"] = a.Status
	}
	for k, v := range a.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

// AlarmRef is an alarm as returned by a query, carrying the ID needed to
// update it.
type AlarmRef struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
}

// Event mirrors the Cumulocity event API payload.
type Event struct {
	Source Source         `json:"source"`
	Type   string         `json:"type"`
	Text   string         `json:"text"`
	Time   time.Time      `json:"time"`
	Extra  map[string]any `json:"-"`
}

func (e Event) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"source": e.Source,
		"type":   e.Type,
		"text":   e.Text,
		"time":   e.Time.UTC().Format(time.RFC3339Nano),
	}
	for k, v := range e.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

// Severity levels used by the alarm API.
const (
	SeverityCritical = "CRITICAL"
	SeverityMajor    = "MAJOR"
	SeverityMinor    = "MINOR"
	SeverityWarning  = "WARNING"
)
