package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/simulator/internal/c8y"
)

// LiveOptions configures the realtime phase.
type LiveOptions struct {
	Interval      time.Duration // sampling interval
	BrownoutEvery time.Duration // how often a fresh brownout is injected, 0 disables
	BrownoutDur   time.Duration
	Depth         float64
	UseMQTT       bool
	// DevicePassword is the CREDENTIALS value of the bulk registration CSV.
	// If set, each device authenticates as <tenant>/device_<serial> instead of
	// using the tenant user.
	DevicePassword string
}

// publisher abstracts the transport used during the live phase.
type publisher interface {
	Telemetry(ctx context.Context, n *Node, t time.Time, r Reading) error
	Alarm(ctx context.Context, n *Node, severity, alarmType, text string) error
	ClearAlarm(ctx context.Context, n *Node, alarmType string) error
	Event(ctx context.Context, n *Node, eventType, text string) error
	Close()
}

// Live streams telemetry in realtime and injects brownout episodes on the
// affected feeder so a demo can show a fault appearing live.
func Live(ctx context.Context, client *c8y.Client, nodes []*Node, opt LiveOptions) error {
	pub, err := newPublisher(client, nodes, opt)
	if err != nil {
		return err
	}
	defer pub.Close()

	sample := time.NewTicker(opt.Interval)
	defer sample.Stop()

	var trigger <-chan time.Time
	if opt.BrownoutEvery > 0 {
		t := time.NewTicker(opt.BrownoutEvery)
		defer t.Stop()
		trigger = t.C
	}

	var windows []Window
	raised := map[string]bool{}
	slog.Info("live phase started", "interval", opt.Interval.String(),
		"mqtt", opt.UseMQTT, "brownoutEvery", opt.BrownoutEvery.String())

	for {
		select {
		case <-ctx.Done():
			slog.Info("live phase stopped")
			return nil

		case <-trigger:
			w := Window{
				Start: time.Now(),
				Dur:   opt.BrownoutDur,
				Depth: opt.Depth,
				Cause: "injected live brownout on Feeder-A",
			}
			windows = append(windows, w)
			slog.Info("brownout injected", "start", w.Start.Format(time.RFC3339),
				"dur", w.Dur.String(), "depth", w.Depth)

		case now := <-sample.C:
			windows = prune(windows, now)
			for _, n := range nodes {
				r := n.Sample(now, windows)
				if err := pub.Telemetry(ctx, n, now, r); err != nil {
					slog.Warn("telemetry failed", "serial", n.Serial, "err", err)
					continue
				}
				if !n.Affected() {
					continue
				}
				switch {
				case r.Voltage < UnderVoltageV && !raised[n.Serial]:
					raised[n.Serial] = true
					text := fmt.Sprintf("Under-voltage: %.1f V (threshold %.0f V) on %s",
						r.Voltage, UnderVoltageV, n.Feeder)
					if err := pub.Alarm(ctx, n, voltageSeverity(r.Voltage), AlarmUnderVoltage, text); err != nil {
						slog.Warn("alarm failed", "serial", n.Serial, "err", err)
					}
					if n.Coupling*opt.Depth > 0.6 {
						if err := pub.Event(ctx, n, EventBrownoutReset,
							"Controller restarted: brown-out detector tripped during supply dip"); err != nil {
							slog.Warn("event failed", "serial", n.Serial, "err", err)
						}
					}
				case r.Voltage > UnderVoltageV+4 && raised[n.Serial]:
					raised[n.Serial] = false
					if err := pub.ClearAlarm(ctx, n, AlarmUnderVoltage); err != nil {
						slog.Warn("clear failed", "serial", n.Serial, "err", err)
					}
				}
			}
		}
	}
}

func prune(windows []Window, now time.Time) []Window {
	out := windows[:0]
	for _, w := range windows {
		if w.End().After(now) {
			out = append(out, w)
		}
	}
	return out
}

func newPublisher(client *c8y.Client, nodes []*Node, opt LiveOptions) (publisher, error) {
	if !opt.UseMQTT {
		return &restPublisher{client: client}, nil
	}
	devices := make(map[string]*c8y.Device, len(nodes))
	for _, n := range nodes {
		user, pass := client.MQTTUser(), client.Password()
		if opt.DevicePassword != "" {
			user, pass = client.DeviceUser(n.Serial), opt.DevicePassword
		}
		dev, err := c8y.Connect(client.Host(), user, pass, n.Serial)
		if err != nil {
			for _, d := range devices {
				d.Close()
			}
			return nil, err
		}
		devices[n.Serial] = dev
		slog.Info("mqtt connected", "serial", n.Serial, "user", user)
	}
	return &mqttPublisher{devices: devices}, nil
}

type restPublisher struct{ client *c8y.Client }

func (p *restPublisher) Telemetry(ctx context.Context, n *Node, t time.Time, r Reading) error {
	return p.client.CreateMeasurements(ctx, []c8y.Measurement{measurement(n, t, r)})
}

func (p *restPublisher) Alarm(ctx context.Context, n *Node, severity, alarmType, text string) error {
	_, err := p.client.CreateAlarm(ctx, c8y.Alarm{
		Source: c8y.Source{ID: n.ID}, Type: alarmType, Severity: severity,
		Status: "ACTIVE", Text: text, Time: time.Now(),
	})
	return err
}

func (p *restPublisher) ClearAlarm(context.Context, *Node, string) error {
	// Clearing by type requires a query; the live REST path leaves alarms
	// active and relies on the next episode to update the alarm count.
	return nil
}

func (p *restPublisher) Event(ctx context.Context, n *Node, eventType, text string) error {
	_, err := p.client.CreateEvent(ctx, c8y.Event{
		Source: c8y.Source{ID: n.ID}, Type: eventType, Text: text, Time: time.Now(),
	})
	return err
}

func (p *restPublisher) Close() {}

type mqttPublisher struct{ devices map[string]*c8y.Device }

func (p *mqttPublisher) dev(n *Node) (*c8y.Device, error) {
	d, ok := p.devices[n.Serial]
	if !ok {
		return nil, fmt.Errorf("mqtt: no connection for %s", n.Serial)
	}
	return d, nil
}

func (p *mqttPublisher) Telemetry(_ context.Context, n *Node, _ time.Time, r Reading) error {
	d, err := p.dev(n)
	if err != nil {
		return err
	}
	return errors.Join(
		d.Measurement(FragmentVoltage, SeriesVoltage, r.Voltage, "V"),
		d.Measurement(FragmentCurrent, SeriesCurrent, r.Current, "A"),
		d.Measurement(FragmentTemp, SeriesTemp, r.Temp, "C"),
	)
}

func (p *mqttPublisher) Alarm(_ context.Context, n *Node, severity, alarmType, text string) error {
	d, err := p.dev(n)
	if err != nil {
		return err
	}
	return d.Alarm(severity, alarmType, text)
}

func (p *mqttPublisher) ClearAlarm(_ context.Context, n *Node, alarmType string) error {
	d, err := p.dev(n)
	if err != nil {
		return err
	}
	return d.ClearAlarm(alarmType)
}

func (p *mqttPublisher) Event(_ context.Context, n *Node, eventType, text string) error {
	d, err := p.dev(n)
	if err != nil {
		return err
	}
	return d.Event(eventType, text)
}

func (p *mqttPublisher) Close() {
	for _, d := range p.devices {
		d.Close()
	}
}
