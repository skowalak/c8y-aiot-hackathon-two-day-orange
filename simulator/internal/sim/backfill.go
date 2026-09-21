package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/simulator/internal/c8y"
)

// BackfillOptions configures the historical seeding run.
type BackfillOptions struct {
	Fault      time.Time
	History    time.Duration
	Interval   time.Duration // base sampling interval
	HFInterval time.Duration // sampling interval around brownout episodes
	Batch      int           // measurements per request
	Workers    int           // parallel nodes
	Spec       FleetSpec
	DryRun     bool
}

// Backfill writes the measurement history, the alarm history, the supply dip
// and reset events and the firmware change history for the whole fleet. All
// records are backdated, so a demo has a full week of evidence immediately.
func Backfill(ctx context.Context, client *c8y.Client, nodes []*Node, windows []Window, opt BackfillOptions) error {
	start := opt.Fault.Add(-opt.History)
	end := opt.Fault.Add(lastWindowTail(windows))
	if now := time.Now(); end.After(now) {
		end = now
	}
	times := SampleTimes(start, end, opt.Interval, opt.HFInterval, windows)
	slog.Info("backfill starting",
		"from", start.Format(time.RFC3339), "to", end.Format(time.RFC3339),
		"samplesPerNode", len(times), "nodes", len(nodes), "episodes", len(windows))

	if opt.DryRun {
		for _, w := range windows {
			n := Primary(nodes)
			worst := n.MinVoltage(w, windows, opt.HFInterval)
			slog.Info("episode (dry run)", "start", w.Start.Format(time.RFC3339),
				"dur", w.Dur.String(), "depth", w.Depth,
				"primaryMinVoltage", worst.Voltage, "primaryMaxTemp", worst.Temp, "cause", w.Cause)
		}
		return nil
	}

	sem := make(chan struct{}, max(opt.Workers, 1))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, n := range nodes {
		wg.Add(1)
		go func(n *Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := backfillNode(ctx, client, n, times, windows, opt); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("node %s: %w", n.Serial, err))
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func backfillNode(ctx context.Context, client *c8y.Client, n *Node, times []time.Time, windows []Window, opt BackfillOptions) error {
	batch := make([]c8y.Measurement, 0, opt.Batch)
	written := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := client.CreateMeasurements(ctx, batch); err != nil {
			return err
		}
		written += len(batch)
		batch = batch[:0]
		return nil
	}

	gaps := rebootGaps(n, windows)
	for _, t := range times {
		if inAnyGap(t, gaps) {
			continue
		}
		r := n.Sample(t, windows)
		batch = append(batch, measurement(n, t, r))
		if len(batch) >= opt.Batch {
			if err := flush(); err != nil {
				return fmt.Errorf("measurements: %w", err)
			}
		}
	}
	if err := flush(); err != nil {
		return fmt.Errorf("measurements: %w", err)
	}

	alarms, events, err := backfillIncidents(ctx, client, n, windows, opt)
	if err != nil {
		return err
	}
	slog.Info("node backfilled", "serial", n.Serial, "measurements", written,
		"alarms", alarms, "events", events)
	return nil
}

func backfillIncidents(ctx context.Context, client *c8y.Client, n *Node, windows []Window, opt BackfillOptions) (alarms, events int, err error) {
	if fw := firmwareEvent(n, windows, opt); fw != nil {
		if _, err := client.CreateEvent(ctx, *fw); err != nil {
			return alarms, events, fmt.Errorf("firmware event: %w", err)
		}
		events++
	}
	if !n.Affected() {
		return alarms, events, nil
	}

	last := len(windows) - 1
	for i, w := range windows {
		worst := n.MinVoltage(w, windows, opt.HFInterval)
		// The final episode is the reported fault and stays unresolved.
		ongoing := i == last

		if _, err := client.CreateEvent(ctx, c8y.Event{
			Source: c8y.Source{ID: n.ID},
			Type:   EventSupplyDip,
			Text: fmt.Sprintf("Supply voltage dipped to %.1f V for %s on %s",
				worst.Voltage, w.Dur, n.Feeder),
			Time: w.Start,
			Extra: map[string]any{"sim_SupplyDip": map[string]any{
				"minVoltageV": worst.Voltage,
				"durationS":   int(w.Dur.Seconds()),
				"feeder":      n.Feeder,
				"site":        n.Site,
				"cause":       w.Cause,
			}},
		}); err != nil {
			return alarms, events, fmt.Errorf("supply dip event: %w", err)
		}
		events++

		if worst.Voltage < UnderVoltageV {
			id, err := client.CreateAlarm(ctx, c8y.Alarm{
				Source:   c8y.Source{ID: n.ID},
				Type:     AlarmUnderVoltage,
				Severity: voltageSeverity(worst.Voltage),
				Status:   "ACTIVE",
				Time:     w.Start.Add(20 * time.Second),
				Text: fmt.Sprintf("Under-voltage: %.1f V (threshold %.0f V) on %s",
					worst.Voltage, UnderVoltageV, n.Feeder),
				Extra: map[string]any{"sim_Evidence": map[string]any{
					"minVoltageV":  worst.Voltage,
					"thresholdV":   UnderVoltageV,
					"feeder":       n.Feeder,
					"role":         string(n.Role),
					"episodeStart": w.Start.UTC().Format(time.RFC3339),
				}},
			})
			if err != nil {
				return alarms, events, fmt.Errorf("under-voltage alarm: %w", err)
			}
			alarms++
			if !ongoing {
				if err := client.UpdateAlarmStatus(ctx, id, "CLEARED"); err != nil {
					return alarms, events, fmt.Errorf("clear under-voltage alarm: %w", err)
				}
			}
		}

		if worst.Temp > CriticalTempC {
			id, err := client.CreateAlarm(ctx, c8y.Alarm{
				Source:   c8y.Source{ID: n.ID},
				Type:     AlarmOverTemp,
				Severity: c8y.SeverityCritical,
				Status:   "ACTIVE",
				Time:     w.Start.Add(w.Dur / 3),
				Text: fmt.Sprintf("Over-temperature: %.1f C (threshold %.0f C) while compensating supply dip",
					worst.Temp, CriticalTempC),
			})
			if err != nil {
				return alarms, events, fmt.Errorf("over-temperature alarm: %w", err)
			}
			alarms++
			if !ongoing {
				if err := client.UpdateAlarmStatus(ctx, id, "CLEARED"); err != nil {
					return alarms, events, fmt.Errorf("clear over-temperature alarm: %w", err)
				}
			}
		}

		if hasReset(n, w) {
			if _, err := client.CreateEvent(ctx, c8y.Event{
				Source: c8y.Source{ID: n.ID},
				Type:   EventBrownoutReset,
				Text:   "Controller restarted: brown-out detector tripped during supply dip",
				Time:   resetTime(w),
				Extra: map[string]any{"sim_Reset": map[string]any{
					"reason":       "brownout-detector",
					"uptimeResetS": 0,
					"feeder":       n.Feeder,
				}},
			}); err != nil {
				return alarms, events, fmt.Errorf("reset event: %w", err)
			}
			events++
		}
	}
	return alarms, events, nil
}

// firmwareEvent places a firmware change before the first episode. The
// versions do not line up with the fault onset, so the diagnostic engine can
// rule the update out as root cause.
func firmwareEvent(n *Node, windows []Window, opt BackfillOptions) *c8y.Event {
	if len(windows) == 0 {
		return nil
	}
	earliest := opt.Fault.Add(-opt.History)
	at := opt.Fault.Add(-6*24*time.Hour - 12*time.Hour)
	if at.Before(earliest) {
		at = earliest.Add(30 * time.Minute)
	}
	if !at.Before(windows[0].Start) {
		at = windows[0].Start.Add(-12 * time.Hour)
	}
	return &c8y.Event{
		Source: c8y.Source{ID: n.ID},
		Type:   EventFirmwareUpdate,
		Text: fmt.Sprintf("Firmware updated from %s to %s",
			opt.Spec.FirmwareOld, opt.Spec.Firmware),
		Time: at,
		Extra: map[string]any{"c8y_Firmware": map[string]any{
			"name":            "powernode-fw",
			"version":         opt.Spec.Firmware,
			"previousVersion": opt.Spec.FirmwareOld,
		}},
	}
}

func measurement(n *Node, t time.Time, r Reading) c8y.Measurement {
	return c8y.Measurement{
		Time:   t,
		Type:   MeasurementType,
		Source: n.ID,
		Fragments: map[string]c8y.Series{
			FragmentVoltage: {SeriesVoltage: {Value: r.Voltage, Unit: "V"}},
			FragmentCurrent: {SeriesCurrent: {Value: r.Current, Unit: "A"}},
			FragmentTemp:    {SeriesTemp: {Value: r.Temp, Unit: "C"}},
		},
	}
}

func voltageSeverity(v float64) string {
	switch {
	case v < 190:
		return c8y.SeverityCritical
	case v < 200:
		return c8y.SeverityMajor
	default:
		return c8y.SeverityMinor
	}
}

func hasReset(n *Node, w Window) bool { return w.Depth*n.Coupling > 0.6 }

func resetTime(w Window) time.Time {
	return w.Start.Add(time.Duration(float64(w.Dur) * 0.45))
}

type gap struct{ from, to time.Time }

// rebootGaps models the telemetry hole while a node reboots.
func rebootGaps(n *Node, windows []Window) []gap {
	var out []gap
	for _, w := range windows {
		if hasReset(n, w) {
			from := resetTime(w)
			out = append(out, gap{from: from, to: from.Add(75 * time.Second)})
		}
	}
	return out
}

func inAnyGap(t time.Time, gaps []gap) bool {
	for _, g := range gaps {
		if !t.Before(g.from) && t.Before(g.to) {
			return true
		}
	}
	return false
}

func lastWindowTail(windows []Window) time.Duration {
	var tail time.Duration
	for _, w := range windows {
		if w.Dur > tail {
			tail = w.Dur
		}
	}
	return tail + 10*time.Minute
}
