// Command simulator provisions a small power distribution fleet in Cumulocity
// IoT and produces the telemetry, alarm and event history of an intermittent
// brownout fault on one feeder, with a healthy feeder as control group.
//
// It is the data source for the Automated Root Cause & Diagnostics MCP server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/simulator/internal/c8y"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/simulator/internal/sim"
)

type options struct {
	baseURL  string
	tenant   string
	user     string
	password string

	mode      string
	transport string

	fault      string
	history    string
	interval   time.Duration
	hfInterval time.Duration
	batch      int
	workers    int

	liveInterval  time.Duration
	brownoutEvery time.Duration
	brownoutDur   time.Duration
	brownoutDepth float64

	neighbors int
	baseline  int
	prefix    string

	registrationCSV string
	devicePassword  string

	dryRun  bool
	verbose bool
}

func main() {
	if err := run(); err != nil {
		slog.Error("simulator failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	opt := parseFlags()

	level := slog.LevelInfo
	if opt.verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	fault, err := parseFault(opt.fault)
	if err != nil {
		return err
	}
	history, err := parseDuration(opt.history)
	if err != nil {
		return fmt.Errorf("-history: %w", err)
	}

	if (opt.dryRun || opt.registrationCSV != "") && opt.baseURL == "" {
		opt.baseURL, opt.user, opt.password = "https://dry-run.invalid", "dry", "run"
	}
	client, err := c8y.New(c8y.Config{
		BaseURL:  opt.baseURL,
		Tenant:   opt.tenant,
		User:     opt.user,
		Password: opt.password,
	})
	if err != nil {
		return err
	}

	spec := sim.DefaultSpec()
	spec.Prefix = opt.prefix
	spec.Neighbors = opt.neighbors
	spec.Baseline = opt.baseline

	nodes := sim.BuildFleet(spec)
	windows := sim.BrownoutWindows(fault, history)

	if opt.registrationCSV != "" {
		return writeRegistrationCSV(opt, nodes, spec)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !opt.dryRun {
		if err := sim.Provision(ctx, client, nodes, spec); err != nil {
			return err
		}
	}

	if opt.mode == "backfill" || opt.mode == "both" {
		err := sim.Backfill(ctx, client, nodes, windows, sim.BackfillOptions{
			Fault:      fault,
			History:    history,
			Interval:   opt.interval,
			HFInterval: opt.hfInterval,
			Batch:      opt.batch,
			Workers:    opt.workers,
			Spec:       spec,
			DryRun:     opt.dryRun,
		})
		if err != nil {
			return err
		}
		summary(nodes, fault, windows)
	}

	if (opt.mode == "live" || opt.mode == "both") && !opt.dryRun {
		useMQTT := opt.transport == "mqtt" || opt.transport == "auto"
		return sim.Live(ctx, client, nodes, sim.LiveOptions{
			Interval:       opt.liveInterval,
			BrownoutEvery:  opt.brownoutEvery,
			BrownoutDur:    opt.brownoutDur,
			Depth:          opt.brownoutDepth,
			UseMQTT:        useMQTT,
			DevicePassword: opt.devicePassword,
		})
	}
	return nil
}

func writeRegistrationCSV(opt options, nodes []*sim.Node, spec sim.FleetSpec) error {
	password := opt.devicePassword
	if password == "" {
		password = "S1mN0de!" + time.Now().Format("2006")
		slog.Warn("no -device-password given, using a generated one", "password", password)
	}
	out := os.Stdout
	if opt.registrationCSV != "-" {
		f, err := os.Create(opt.registrationCSV)
		if err != nil {
			return fmt.Errorf("registration csv: %w", err)
		}
		defer func() { _ = f.Close() }()
		out = f
	}
	if err := sim.RegistrationCSV(out, nodes, spec, password); err != nil {
		return err
	}
	if opt.registrationCSV != "-" {
		slog.Info("registration csv written", "path", opt.registrationCSV, "devices", len(nodes))
	}
	return nil
}

func parseFlags() options {
	var opt options
	flag.StringVar(&opt.baseURL, "baseurl", os.Getenv("C8Y_BASEURL"), "tenant base URL, e.g. https://mytenant.eu-latest.cumulocity.com (env C8Y_BASEURL)")
	flag.StringVar(&opt.tenant, "tenant", os.Getenv("C8Y_TENANT"), "tenant ID, prefixed to the user name (env C8Y_TENANT)")
	flag.StringVar(&opt.user, "user", os.Getenv("C8Y_USER"), "user name (env C8Y_USER)")
	flag.StringVar(&opt.password, "password", os.Getenv("C8Y_PASSWORD"), "password (env C8Y_PASSWORD)")

	flag.StringVar(&opt.mode, "mode", "both", "backfill | live | both")
	flag.StringVar(&opt.transport, "transport", "auto", "live transport: auto (MQTT) | mqtt | rest")

	flag.StringVar(&opt.fault, "fault", "", "fault timestamp: RFC3339, relative (-2h) or empty for now")
	flag.StringVar(&opt.history, "history", "7d", "history span to seed before the fault, e.g. 7d or 36h")
	flag.DurationVar(&opt.interval, "interval", time.Minute, "base sampling interval of the backfill")
	flag.DurationVar(&opt.hfInterval, "hf-interval", 5*time.Second, "high frequency sampling interval around brownouts")
	flag.IntVar(&opt.batch, "batch", 200, "measurements per bulk request")
	flag.IntVar(&opt.workers, "workers", 4, "nodes seeded in parallel")

	flag.DurationVar(&opt.liveInterval, "live-interval", 10*time.Second, "live sampling interval")
	flag.DurationVar(&opt.brownoutEvery, "brownout-every", 15*time.Minute, "inject a live brownout this often, 0 disables")
	flag.DurationVar(&opt.brownoutDur, "brownout-dur", 3*time.Minute, "duration of an injected live brownout")
	flag.Float64Var(&opt.brownoutDepth, "brownout-depth", 0.85, "depth of an injected live brownout, 0..1")

	flag.IntVar(&opt.neighbors, "neighbors", 3, "neighbor nodes on the affected feeder")
	flag.IntVar(&opt.baseline, "baseline", 2, "healthy nodes on the control feeder")
	flag.StringVar(&opt.prefix, "prefix", "sim", "serial number prefix")

	flag.StringVar(&opt.registrationCSV, "registration-csv", "", "write a Cumulocity bulk device registration CSV to this path (- for stdout) and exit")
	flag.StringVar(&opt.devicePassword, "device-password", os.Getenv("C8Y_DEVICE_PASSWORD"), "device credentials from the bulk registration CSV; used for the live MQTT phase (env C8Y_DEVICE_PASSWORD)")
	flag.BoolVar(&opt.dryRun, "dry-run", false, "generate and log the scenario without calling Cumulocity")
	flag.BoolVar(&opt.verbose, "v", false, "debug logging")
	flag.Parse()
	return opt
}

var relativeRe = regexp.MustCompile(`^[-+]?\d+(\.\d+)?[a-z]+$`)

func parseFault(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().Add(-20 * time.Minute).Truncate(time.Second), nil
	}
	if relativeRe.MatchString(s) {
		d, err := parseDuration(s)
		if err != nil {
			return time.Time{}, fmt.Errorf("-fault: %w", err)
		}
		if d > 0 {
			d = -d
		}
		return time.Now().Add(d).Truncate(time.Second), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("-fault: expected RFC3339 or relative duration: %w", err)
	}
	return t, nil
}

// parseDuration extends time.ParseDuration with a day unit.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid day duration %q", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	return time.ParseDuration(s)
}

func summary(nodes []*sim.Node, fault time.Time, windows []sim.Window) {
	primary := sim.Primary(nodes)
	var b strings.Builder
	fmt.Fprintf(&b, "\nScenario ready\n")
	fmt.Fprintf(&b, "  fault timestamp : %s\n", fault.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "  primary device  : %s (id %s, serial %s)\n", primary.Name, primary.ID, primary.Serial)
	fmt.Fprintf(&b, "  episodes        : %d\n", len(windows))
	for _, w := range windows {
		fmt.Fprintf(&b, "    %s  %-6s depth %.2f  %s\n",
			w.Start.UTC().Format(time.RFC3339), w.Dur.String(), w.Depth, w.Cause)
	}
	fmt.Fprintf(&b, "  fleet:\n")
	for _, n := range nodes {
		fmt.Fprintf(&b, "    %-12s id %-8s %-9s coupling %.2f  %s / %s\n",
			n.Serial, n.ID, string(n.Role), n.Coupling, n.Site, n.Feeder)
	}
	fmt.Fprint(os.Stderr, b.String())
}
