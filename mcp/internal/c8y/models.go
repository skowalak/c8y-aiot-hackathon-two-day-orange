package c8y

import (
	"encoding/json"
	"fmt"
	"time"
)

// Source references a managed object.
type Source struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Reference is an entry of a childAssets/assetParents collection.
type Reference struct {
	ManagedObject Source `json:"managedObject"`
}

// ReferenceCollection is an inventory reference collection.
type ReferenceCollection struct {
	References []Reference `json:"references"`
}

// ManagedObject is an inventory object with its custom fragments preserved.
type ManagedObject struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Type         string              `json:"type"`
	Owner        string              `json:"owner"`
	LastUpdated  time.Time           `json:"lastUpdated"`
	AssetParents ReferenceCollection `json:"assetParents"`
	ChildAssets  ReferenceCollection `json:"childAssets"`

	// Fragments holds every other top level property, e.g. c8y_Firmware.
	Fragments map[string]json.RawMessage `json:"-"`
}

var moReserved = map[string]bool{
	"id": true, "name": true, "type": true, "owner": true, "lastUpdated": true,
	"assetParents": true, "childAssets": true, "self": true, "creationTime": true,
	"childDevices": true, "childAdditions": true, "additionParents": true,
	"deviceParents": true,
}

func (m *ManagedObject) UnmarshalJSON(data []byte) error {
	type alias ManagedObject
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*m = ManagedObject(a)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Fragments = make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		if !moReserved[k] {
			m.Fragments[k] = v
		}
	}
	return nil
}

// Fragment decodes a fragment into out. It reports whether the fragment exists.
func (m *ManagedObject) Fragment(name string, out any) bool {
	raw, ok := m.Fragments[name]
	if !ok {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// Label returns a human readable device label.
func (m *ManagedObject) Label() string {
	if m.Name != "" {
		return fmt.Sprintf("%s (%s)", m.Name, m.ID)
	}
	return m.ID
}

// Measurement is a measurement with all of its series flattened to
// "fragment.series" keys.
type Measurement struct {
	Time   time.Time
	Type   string
	Source string
	Values map[string]float64
	Units  map[string]string
}

var measurementReserved = map[string]bool{
	"id": true, "self": true, "time": true, "type": true, "source": true,
}

func (m *Measurement) UnmarshalJSON(data []byte) error {
	var head struct {
		Time   time.Time `json:"time"`
		Type   string    `json:"type"`
		Source Source    `json:"source"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	m.Time, m.Type, m.Source = head.Time, head.Type, head.Source.ID

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Values = map[string]float64{}
	m.Units = map[string]string{}
	for fragment, rawFragment := range raw {
		if measurementReserved[fragment] {
			continue
		}
		var series map[string]struct {
			Value *float64 `json:"value"`
			Unit  string   `json:"unit"`
		}
		if err := json.Unmarshal(rawFragment, &series); err != nil {
			continue // not a measurement fragment
		}
		for name, v := range series {
			if v.Value == nil {
				continue
			}
			key := fragment + "." + name
			m.Values[key] = *v.Value
			if v.Unit != "" {
				m.Units[key] = v.Unit
			}
		}
	}
	return nil
}

// Alarm is an alarm record.
type Alarm struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Text         string    `json:"text"`
	Severity     string    `json:"severity"`
	Status       string    `json:"status"`
	Count        int       `json:"count"`
	Time         time.Time `json:"time"`
	CreationTime time.Time `json:"creationTime"`
	LastUpdated  time.Time `json:"lastUpdated"`
	Source       Source    `json:"source"`

	Fragments map[string]json.RawMessage `json:"-"`
}

var alarmReserved = map[string]bool{
	"id": true, "type": true, "text": true, "severity": true, "status": true,
	"count": true, "time": true, "creationTime": true, "lastUpdated": true,
	"source": true, "self": true, "history": true, "firstOccurrenceTime": true,
}

func (a *Alarm) UnmarshalJSON(data []byte) error {
	type alias Alarm
	var v alias
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*a = Alarm(v)
	a.Fragments = extraFragments(data, alarmReserved)
	return nil
}

// Event is an event record.
type Event struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Text         string    `json:"text"`
	Time         time.Time `json:"time"`
	CreationTime time.Time `json:"creationTime"`
	Source       Source    `json:"source"`

	Fragments map[string]json.RawMessage `json:"-"`
}

var eventReserved = map[string]bool{
	"id": true, "type": true, "text": true, "time": true, "creationTime": true,
	"source": true, "self": true, "lastUpdated": true,
}

func (e *Event) UnmarshalJSON(data []byte) error {
	type alias Event
	var v alias
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*e = Event(v)
	e.Fragments = extraFragments(data, eventReserved)
	return nil
}

func extraFragments(data []byte, reserved map[string]bool) map[string]json.RawMessage {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	out := make(map[string]json.RawMessage)
	for k, v := range raw {
		if !reserved[k] {
			out[k] = v
		}
	}
	return out
}
