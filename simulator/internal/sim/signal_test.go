package sim

import (
	"testing"
	"time"
)

func fleet(t *testing.T) (primary, neighbor, baseline *Node) {
	t.Helper()
	nodes := BuildFleet(DefaultSpec())
	for _, n := range nodes {
		switch n.Role {
		case RolePrimary:
			primary = n
		case RoleNeighbor:
			if neighbor == nil {
				neighbor = n
			}
		case RoleBaseline:
			if baseline == nil {
				baseline = n
			}
		}
	}
	if primary == nil || neighbor == nil || baseline == nil {
		t.Fatal("default fleet is missing a role")
	}
	return primary, neighbor, baseline
}

func TestSampleDeterministic(t *testing.T) {
	primary, _, _ := fleet(t)
	at := time.Date(2025, 4, 1, 12, 0, 0, 0, time.UTC)
	if a, b := primary.Sample(at, nil), primary.Sample(at, nil); a != b {
		t.Fatalf("samples differ: %+v vs %+v", a, b)
	}
}

func TestBaselineStaysNominal(t *testing.T) {
	_, _, baseline := fleet(t)
	fault := time.Date(2025, 4, 1, 12, 0, 0, 0, time.UTC)
	windows := BrownoutWindows(fault, 7*24*time.Hour)
	for t0 := fault.Add(-7 * 24 * time.Hour); t0.Before(fault.Add(time.Hour)); t0 = t0.Add(30 * time.Second) {
		r := baseline.Sample(t0, windows)
		if r.Voltage < UnderVoltageV {
			t.Fatalf("baseline node sagged to %.1f V at %s", r.Voltage, t0)
		}
		if r.Temp > CriticalTempC {
			t.Fatalf("baseline node reached %.1f C at %s", r.Temp, t0)
		}
	}
}

func TestFaultEpisodeTripsThresholds(t *testing.T) {
	primary, neighbor, _ := fleet(t)
	fault := time.Date(2025, 4, 1, 12, 0, 0, 0, time.UTC)
	windows := BrownoutWindows(fault, 7*24*time.Hour)
	main := windows[len(windows)-1]

	worst := primary.MinVoltage(main, windows, 5*time.Second)
	if worst.Voltage >= UnderVoltageV {
		t.Fatalf("primary did not trip under-voltage: %.1f V", worst.Voltage)
	}
	if worst.Temp <= CriticalTempC {
		t.Fatalf("primary did not trip over-temperature: %.1f C", worst.Temp)
	}

	neighborWorst := neighbor.MinVoltage(main, windows, 5*time.Second)
	if neighborWorst.Voltage >= UnderVoltageV {
		t.Fatalf("neighbor did not corroborate the fault: %.1f V", neighborWorst.Voltage)
	}
	if neighborWorst.Voltage <= worst.Voltage {
		t.Fatalf("neighbor sagged deeper (%.1f V) than primary (%.1f V)",
			neighborWorst.Voltage, worst.Voltage)
	}
}

func TestSampleTimesDenseAroundEpisodes(t *testing.T) {
	fault := time.Date(2025, 4, 1, 12, 0, 0, 0, time.UTC)
	windows := BrownoutWindows(fault, 24*time.Hour)
	start, end := fault.Add(-24*time.Hour), fault.Add(30*time.Minute)
	times := SampleTimes(start, end, time.Minute, 5*time.Second, windows)

	if len(times) < 24*60 {
		t.Fatalf("expected at least the base grid, got %d samples", len(times))
	}
	for i := 1; i < len(times); i++ {
		if !times[i].After(times[i-1]) {
			t.Fatalf("sample times not strictly increasing at %d", i)
		}
	}
	var dense int
	main := windows[len(windows)-1]
	for _, ts := range times {
		if !ts.Before(main.Start) && ts.Before(main.End()) {
			dense++
		}
	}
	if want := int(main.Dur.Seconds() / 5 * 0.9); dense < want {
		t.Fatalf("expected ~%d high frequency samples in the fault episode, got %d", want, dense)
	}
}
