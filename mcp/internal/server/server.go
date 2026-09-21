// Package server exposes the diagnostic engine as an MCP server over SSE and
// hosts the generated briefings.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/diag"
	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/report"
)

// Version is reported to MCP clients and by /health. The packaging script
// overrides it with the manifest version via -ldflags so that a deployed
// instance can be identified without guessing.
var Version = "dev"

// Options configures the MCP server.
type Options struct {
	Engine    *diag.Engine
	Publisher *Publisher
	// DefaultBefore/After bound the analysis window when the caller omits them.
	DefaultBefore time.Duration
	DefaultAfter  time.Duration
	MaxNeighbors  int
	// PathPrefix is the external mount point, for example /service/my-app. It
	// is only a fallback: a reverse proxy that sets X-Forwarded-Prefix wins.
	PathPrefix string
}

// AnalyzeInput is the argument set of analyze_device_failure.
type AnalyzeInput struct {
	Device           string `json:"device" jsonschema:"device to diagnose: Cumulocity managed object ID, serial/external ID or exact device name"`
	FaultTime        string `json:"faultTime,omitempty" jsonschema:"timestamp of the reported fault in RFC3339 (e.g. 2026-09-21T14:05:15Z); defaults to now"`
	Symptom          string `json:"symptom,omitempty" jsonschema:"what the reporter observed, quoted into the briefing"`
	BeforeMinutes    int    `json:"beforeMinutes,omitempty" jsonschema:"minutes of history to analyse before the fault (default 120)"`
	AfterMinutes     int    `json:"afterMinutes,omitempty" jsonschema:"minutes to analyse after the fault (default 30)"`
	IncludeNeighbors *bool  `json:"includeNeighbors,omitempty" jsonschema:"also analyse assets in the same device groups to correlate the fault (default true)"`
	MaxNeighbors     int    `json:"maxNeighbors,omitempty" jsonschema:"maximum number of neighbouring assets to compare (default 6)"`
	LookbackDays     int    `json:"lookbackDays,omitempty" jsonschema:"days of alarm, event and firmware history to consider for recurrence (default 7)"`
}

// VerdictOutput is the machine readable conclusion.
type VerdictOutput struct {
	Scope      string   `json:"scope" jsonschema:"shared-infrastructure, device-local or inconclusive"`
	Headline   string   `json:"headline"`
	Confidence string   `json:"confidence"`
	Reasoning  []string `json:"reasoning"`
	RuledOut   []string `json:"ruledOut,omitempty"`
}

// CorrelationOutput summarises the neighbour comparison.
type CorrelationOutput struct {
	Series            string   `json:"series"`
	Direction         string   `json:"direction"`
	AffectedNeighbors int      `json:"affectedNeighbors"`
	TotalNeighbors    int      `json:"totalNeighbors"`
	MaxOnsetSkewS     float64  `json:"maxOnsetSkewSeconds"`
	AffectedNames     []string `json:"affectedNames,omitempty"`
	UnaffectedNames   []string `json:"unaffectedNames,omitempty"`
}

// AnalyzeOutput is the structured result of analyze_device_failure.
type AnalyzeOutput struct {
	Device          string             `json:"device"`
	DeviceID        string             `json:"deviceId"`
	FaultTime       string             `json:"faultTime"`
	WindowFrom      string             `json:"windowFrom"`
	WindowTo        string             `json:"windowTo"`
	Verdict         VerdictOutput      `json:"verdict"`
	Correlation     *CorrelationOutput `json:"correlation,omitempty"`
	Anomalies       []string           `json:"anomalies,omitempty"`
	AlarmsInWindow  int                `json:"alarmsInWindow"`
	Measurements    int                `json:"measurements"`
	Recommendations []string           `json:"recommendations"`
	ReportURL       string             `json:"reportUrl,omitempty"`
	ReportBinaryURL string             `json:"reportBinaryUrl,omitempty"`
	Markdown        string             `json:"markdown" jsonschema:"the full diagnostic briefing as Markdown"`
}

// New builds the MCP server with the diagnostic tools registered.
func New(opt Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "c8y-diagnostics",
		Title:   "Cumulocity Root Cause & Diagnostics",
		Version: Version,
	}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:  "analyze_device_failure",
		Title: "Analyse a device failure",
		Description: strings.TrimSpace(`
Diagnose a reported device fault in Cumulocity IoT.

Collects the telemetry history around the fault timestamp, the alarm log, the
event and firmware change history and the active threshold rules of the device,
compares all of it against the neighbouring assets in the same device groups,
and derives a root cause verdict with recommended field technician steps.

Returns a structured summary plus a link to a rendered HTML diagnostic
briefing that can be shared with the field team.`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, analyzeHandler(opt))

	return s
}

func analyzeHandler(opt Options) mcp.ToolHandlerFor[AnalyzeInput, AnalyzeOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AnalyzeInput) (*mcp.CallToolResult, AnalyzeOutput, error) {
		req := diag.Request{
			Device:           strings.TrimSpace(in.Device),
			Symptom:          in.Symptom,
			Before:           opt.DefaultBefore,
			After:            opt.DefaultAfter,
			IncludeNeighbors: true,
			MaxNeighbors:     opt.MaxNeighbors,
		}
		if req.Device == "" {
			return nil, AnalyzeOutput{}, fmt.Errorf("device is required: pass a managed object ID, serial or device name")
		}
		if in.FaultTime != "" {
			t, err := ParseTime(in.FaultTime)
			if err != nil {
				return nil, AnalyzeOutput{}, err
			}
			req.FaultTime = t
		}
		if in.BeforeMinutes > 0 {
			req.Before = time.Duration(in.BeforeMinutes) * time.Minute
		}
		if in.AfterMinutes > 0 {
			req.After = time.Duration(in.AfterMinutes) * time.Minute
		}
		if in.IncludeNeighbors != nil {
			req.IncludeNeighbors = *in.IncludeNeighbors
		}
		if in.MaxNeighbors > 0 {
			req.MaxNeighbors = in.MaxNeighbors
		}
		if in.LookbackDays > 0 {
			req.AlarmLookback = time.Duration(in.LookbackDays) * 24 * time.Hour
		}

		briefing, err := opt.Engine.Analyze(ctx, req)
		if err != nil {
			return nil, AnalyzeOutput{}, fmt.Errorf("analysis failed: %w", err)
		}

		markdown := report.Markdown(briefing)
		out := AnalyzeOutput{
			Device:          briefing.Target.Name,
			DeviceID:        briefing.Target.ID,
			FaultTime:       briefing.FaultTime.Format(time.RFC3339),
			WindowFrom:      briefing.Window.From.UTC().Format(time.RFC3339),
			WindowTo:        briefing.Window.To.UTC().Format(time.RFC3339),
			Verdict:         VerdictOutput(briefing.Verdict),
			Correlation:     correlationOutput(briefing),
			Anomalies:       anomalyLines(briefing),
			AlarmsInWindow:  countWindowAlarms(briefing),
			Measurements:    briefing.Target.MeasurementsGot,
			Recommendations: briefing.Recommendations,
			Markdown:        markdown,
		}

		if html, err := report.HTML(briefing); err != nil {
			slog.Warn("html rendering failed", "err", err)
		} else if opt.Publisher != nil {
			links := opt.Publisher.Publish(ctx, briefing.Target.Name, html, map[string]any{
				"deviceId":  briefing.Target.ID,
				"device":    briefing.Target.Name,
				"faultTime": briefing.FaultTime.Format(time.RFC3339),
				"scope":     briefing.Verdict.Scope,
				"headline":  briefing.Verdict.Headline,
			})
			out.ReportURL, out.ReportBinaryURL = links.Shareable, links.Binary
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: chatSummary(out)}},
		}, out, nil
	}
}

// chatSummary is what the agent sees first: verdict, evidence, link.
func chatSummary(out AnalyzeOutput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\n", out.Verdict.Headline)
	fmt.Fprintf(&b, "Scope `%s`, confidence `%s`, window %s → %s.\n\n",
		out.Verdict.Scope, out.Verdict.Confidence, out.WindowFrom, out.WindowTo)
	for _, r := range out.Verdict.Reasoning {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	if len(out.Verdict.RuledOut) > 0 {
		b.WriteString("\nRuled out:\n")
		for _, r := range out.Verdict.RuledOut {
			fmt.Fprintf(&b, "- %s\n", r)
		}
	}
	b.WriteString("\nRecommended field steps:\n")
	for i, r := range out.Recommendations {
		fmt.Fprintf(&b, "%d. %s\n", i+1, r)
	}
	if out.ReportURL != "" {
		fmt.Fprintf(&b, "\nFull briefing with charts: %s\n", out.ReportURL)
	}
	if out.ReportBinaryURL != "" && out.ReportBinaryURL != out.ReportURL {
		fmt.Fprintf(&b, "Stored in Cumulocity: %s\n", out.ReportBinaryURL)
	}
	return b.String()
}

func correlationOutput(b *diag.Briefing) *CorrelationOutput {
	c := b.Correlation
	if c == nil {
		return nil
	}
	out := &CorrelationOutput{
		Series: c.Series, Direction: c.Direction,
		AffectedNeighbors: c.Affected, TotalNeighbors: c.Total,
		MaxOnsetSkewS: c.MaxOnsetSkew,
	}
	for _, n := range c.Neighbors {
		if n.Correlated {
			out.AffectedNames = append(out.AffectedNames, n.Name)
		} else {
			out.UnaffectedNames = append(out.UnaffectedNames, n.Name)
		}
	}
	return out
}

func anomalyLines(b *diag.Briefing) []string {
	var out []string
	for _, a := range b.Target.Anomalies {
		out = append(out, fmt.Sprintf("%s %s: %s",
			a.Severity, a.Onset.UTC().Format(time.RFC3339), a.Description))
	}
	return out
}

func countWindowAlarms(b *diag.Briefing) int {
	var n int
	for _, d := range b.Devices() {
		for _, a := range d.Alarms {
			if a.InWindow {
				n++
			}
		}
	}
	return n
}

// ParseTime accepts an absolute timestamp or an offset relative to now, so
// both "2026-09-21T14:05:15Z" and the "-3h" a technician would say work.
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		if d, err := time.ParseDuration(s); err == nil {
			return time.Now().UTC().Add(d), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse faultTime %q: use RFC3339 (2026-09-21T14:05:15Z) or an offset from now (-3h)", s)
}

// Handler builds the HTTP surface: the MCP SSE endpoint, the hosted reports
// and a health check.
func Handler(opt Options) http.Handler {
	srv := New(opt)
	sse := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	prefixed := withForwardedPrefix(sse, opt.PathPrefix)

	mux := http.NewServeMux()
	mux.Handle("/sse", prefixed)
	mux.Handle("/sse/", prefixed)
	if opt.Publisher != nil {
		mux.Handle("/reports/", opt.Publisher.Handler())
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"UP","version":%q}`, Version)
	})
	return logging(mux)
}

// withForwardedPrefix restores the mount point that a reverse proxy stripped.
// Cumulocity exposes a microservice at /service/<name> but forwards the request
// with that prefix removed. The SSE transport derives the POST endpoint it
// advertises to the client from the request URL, so without the prefix the
// client would post its messages to the tenant root and get a 404.
func withForwardedPrefix(next http.Handler, fallback string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := r.Header.Get("X-Forwarded-Prefix")
		if prefix == "" {
			prefix = fallback
		}
		prefix = strings.TrimRight(prefix, "/")
		if prefix != "" && !strings.HasPrefix(r.URL.Path, prefix+"/") {
			r = r.Clone(r.Context())
			r.URL.Path = prefix + r.URL.Path
		}
		next.ServeHTTP(w, r)
	})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/health" {
			slog.Debug("http", "method", r.Method, "path", r.URL.Path,
				"dur", time.Since(start).Round(time.Millisecond).String())
		}
	})
}
