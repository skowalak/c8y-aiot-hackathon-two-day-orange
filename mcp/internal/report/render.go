// Package report renders a diagnostic briefing as a self contained HTML
// document with inline SVG charts, or as Markdown for chat clients.
package report

import (
	"bytes"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/diag"
)

// maxCharts caps how many series are plotted.
const maxCharts = 4

type chartView struct {
	Series string
	Unit   string
	SVG    template.HTML
}

type view struct {
	B      *diag.Briefing
	Charts []chartView
	Title  string
}

// HTML renders the briefing as a standalone HTML document.
func HTML(b *diag.Briefing) ([]byte, error) {
	v := view{
		B:      b,
		Charts: charts(b),
		Title: fmt.Sprintf("Diagnostic briefing – %s – %s",
			label(&b.Target), b.FaultTime.UTC().Format("2006-01-02 15:04Z")),
	}
	var buf bytes.Buffer
	if err := htmlTemplate.Execute(&buf, v); err != nil {
		return nil, fmt.Errorf("report: render html: %w", err)
	}
	return buf.Bytes(), nil
}

func charts(b *diag.Briefing) []chartView {
	keys := make([]string, 0, len(b.Target.Stats))
	for k := range b.Target.Stats {
		keys = append(keys, k)
	}
	// Anomalous series first, then alphabetically.
	rank := map[string]int{}
	for _, a := range b.Target.Anomalies {
		rank[a.Series] = 1
	}
	sort.Slice(keys, func(i, j int) bool {
		if rank[keys[i]] != rank[keys[j]] {
			return rank[keys[i]] > rank[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > maxCharts {
		keys = keys[:maxCharts]
	}

	out := make([]chartView, 0, len(keys))
	for _, key := range keys {
		lines := []Line{{
			Label:   label(&b.Target),
			Points:  b.Target.Samples[key],
			Primary: true,
		}}
		for i := range b.Neighbors {
			n := &b.Neighbors[i]
			if pts, ok := n.Samples[key]; ok && len(pts) > 0 {
				lines = append(lines, Line{Label: n.Name, Points: pts})
			}
		}
		var limits []float64
		for _, r := range b.Target.Thresholds {
			if r.Series != key {
				continue
			}
			if r.Min != nil {
				limits = append(limits, *r.Min)
			}
			if r.Max != nil {
				limits = append(limits, *r.Max)
			}
		}
		unit := ""
		if s, ok := b.Target.Stats[key]; ok {
			unit = s.Unit
		}
		out = append(out, chartView{
			Series: key,
			Unit:   unit,
			SVG: SVG(key, unit, lines, limits, []Marker{
				{Time: b.FaultTime, Label: "reported fault"},
			}),
		})
	}
	return out
}

// Markdown renders the briefing as Markdown, suitable for a chat answer.
func Markdown(b *diag.Briefing) string {
	var s strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&s, format+"\n", args...) }

	p("# Diagnostic briefing: %s", label(&b.Target))
	p("")
	p("- **Fault timestamp**: %s", tsFull(b.FaultTime))
	p("- **Analysed window**: %s → %s", tsFull(b.Window.From), tsFull(b.Window.To))
	p("- **Generated**: %s", tsFull(b.GeneratedAt))
	if b.Symptom != "" {
		p("- **Reported symptom**: %s", b.Symptom)
	}
	if len(b.Target.Groups) > 0 {
		p("- **Groups**: %s", strings.Join(b.Target.Groups, ", "))
	}
	if b.Target.Firmware != "" {
		p("- **Firmware**: %s", b.Target.Firmware)
	}
	p("")
	p("## Verdict: %s", b.Verdict.Headline)
	p("")
	p("Scope `%s`, confidence `%s`.", b.Verdict.Scope, b.Verdict.Confidence)
	p("")
	for _, r := range b.Verdict.Reasoning {
		p("- %s", r)
	}
	if len(b.Verdict.RuledOut) > 0 {
		p("")
		p("**Considered and ruled out**")
		p("")
		for _, r := range b.Verdict.RuledOut {
			p("- %s", r)
		}
	}

	p("")
	p("## Recommended field steps")
	p("")
	for i, r := range b.Recommendations {
		p("%d. %s", i+1, r)
	}

	p("")
	p("## Evidence")
	p("")
	p("### Telemetry of %s", label(&b.Target))
	p("")
	p("| Series | Baseline | Min | Max | Samples | Rate |")
	p("| --- | --- | --- | --- | --- | --- |")
	for _, key := range sortedKeys(b.Target.Stats) {
		st := b.Target.Stats[key]
		p("| `%s` | %.2f %s | %.2f | %.2f | %d | %s |",
			key, st.Baseline, st.Unit, st.Min, st.Max, st.Count, st.SampleRate)
	}

	if len(b.Target.Anomalies) > 0 {
		p("")
		p("### Anomalies")
		p("")
		for _, a := range b.Target.Anomalies {
			p("- **%s** %s — %s", a.Severity, tsFull(a.Onset), a.Description)
		}
	}

	if c := b.Correlation; c != nil {
		p("")
		p("### Neighbour correlation on `%s` (%s)", c.Series, c.Direction)
		p("")
		p("%d of %d comparable neighbouring assets correlate, max onset skew %.0fs.",
			c.Affected, c.Comparable, c.MaxOnsetSkew)
		if skipped := c.Total - c.Comparable; skipped > 0 {
			p("")
			p("%d of the %d neighbours do not report `%s` and are excluded from the ratio.",
				skipped, c.Total, c.Series)
		}
		p("")
		p("| Asset | Comparable | Correlated | Onset skew | Extreme | Baseline | Group | Note |")
		p("| --- | --- | --- | --- | --- | --- | --- | --- |")
		for _, n := range c.Neighbors {
			p("| %s | %s | %s | %+.0fs | %.2f | %.2f | %s | %s |",
				n.Name, yesNo(n.Comparable), yesNo(n.Correlated), n.OnsetSkewS, n.Value, n.Baseline, n.SharedGroup, n.Note)
		}
	}

	if alarms := windowAlarms(b); len(alarms) > 0 {
		p("")
		p("### Alarms around the fault")
		p("")
		p("| Time | Asset | Severity | Status | Type | Text |")
		p("| --- | --- | --- | --- | --- | --- |")
		for _, a := range alarms {
			p("| %s | %s | %s | %s | `%s` | %s |",
				tsFull(a.Time), a.Device, a.Severity, a.Status, a.Type, a.Text)
		}
	}

	if events := windowEvents(b); len(events) > 0 {
		p("")
		p("### Events")
		p("")
		for _, e := range events {
			p("- %s `%s` on %s: %s", tsFull(e.Time), e.Type, e.Device, e.Text)
		}
	}

	if len(b.FirmwareTimeline) > 0 {
		p("")
		p("### Firmware change history")
		p("")
		for _, f := range b.FirmwareTimeline {
			p("- %s %s: %s", tsFull(f.Time), f.Device, f.Text)
		}
	}

	if len(b.Target.Thresholds) > 0 {
		p("")
		p("### Active threshold rules")
		p("")
		p("| Series | Min | Max | Severity | Origin |")
		p("| --- | --- | --- | --- | --- |")
		for _, r := range b.Target.Thresholds {
			p("| `%s` | %s | %s | %s | %s |",
				r.Series, num(r.Min), num(r.Max), r.Severity, r.Origin)
		}
	}

	if len(b.Notes) > 0 {
		p("")
		p("### Notes")
		p("")
		for _, n := range b.Notes {
			p("- %s", n)
		}
	}
	return s.String()
}

type alarmRow struct {
	diag.AlarmSummary
	Device string
}

type eventRow struct {
	diag.EventSummary
	Device string
}

func windowAlarms(b *diag.Briefing) []alarmRow {
	var out []alarmRow
	for _, d := range b.Devices() {
		for _, a := range d.Alarms {
			if a.InWindow {
				out = append(out, alarmRow{AlarmSummary: a, Device: d.Name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func windowEvents(b *diag.Briefing) []eventRow {
	var out []eventRow
	for _, d := range b.Devices() {
		for _, e := range d.Events {
			if e.Time.Before(b.Window.From) || e.Time.After(b.Window.To) {
				continue
			}
			out = append(out, eventRow{EventSummary: e, Device: d.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func sortedKeys(m map[string]*diag.SeriesStats) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func label(d *diag.DeviceReport) string {
	if d.Name != "" {
		return fmt.Sprintf("%s (%s)", d.Name, d.ID)
	}
	return d.ID
}

func tsFull(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05Z") }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func num(v *float64) string {
	if v == nil {
		return "–"
	}
	return fmt.Sprintf("%.2f", *v)
}

var htmlTemplate = template.Must(template.New("briefing").Funcs(template.FuncMap{
	"ts":           tsFull,
	"label":        label,
	"yesNo":        yesNo,
	"num":          num,
	"sortedKeys":   sortedKeys,
	"windowAlarms": windowAlarms,
	"windowEvents": windowEvents,
	"lower":        strings.ToLower,
	"join":         strings.Join,
	"sub":          func(a, b int) int { return a - b },
}).Parse(htmlSource))

const htmlSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{ .Title }}</title>
<style>
 :root { --fg:#1c2330; --muted:#61708a; --line:#dde3ec; --bg:#f6f8fb; --accent:#c0392b; }
 * { box-sizing: border-box; }
 body { margin:0; padding:32px; font:15px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
        color:var(--fg); background:var(--bg); }
 main { max-width: 1040px; margin:0 auto; }
 h1 { font-size:24px; margin:0 0 4px; }
 h2 { font-size:19px; margin:32px 0 10px; padding-bottom:6px; border-bottom:1px solid var(--line); }
 h3 { font-size:15px; margin:22px 0 8px; color:var(--muted); text-transform:uppercase; letter-spacing:.04em; }
 .sub { color:var(--muted); margin:0 0 20px; }
 .card { background:#fff; border:1px solid var(--line); border-radius:10px; padding:18px 20px; margin:16px 0; }
 .verdict { border-left:5px solid var(--accent); }
 .verdict.shared-infrastructure { --accent:#c0392b; }
 .verdict.device-local { --accent:#d68910; }
 .verdict.inconclusive { --accent:#7f8c8d; }
 .verdict h2 { border:0; margin:0 0 6px; font-size:20px; }
 .pill { display:inline-block; padding:2px 9px; border-radius:99px; font-size:12px; font-weight:600;
         background:#eef2f7; color:var(--muted); margin-right:6px; text-transform:uppercase; letter-spacing:.03em;}
 .pill.CRITICAL { background:#fdecea; color:#c0392b; }
 .pill.MAJOR { background:#fdf1e3; color:#b9770e; }
 .pill.MINOR { background:#fef9e7; color:#9a7d0a; }
 .pill.high { background:#eafaf1; color:#1e8449; }
 .pill.medium { background:#fdf1e3; color:#b9770e; }
 .pill.low { background:#fdecea; color:#c0392b; }
 table { border-collapse:collapse; width:100%; font-size:13.5px; }
 th, td { text-align:left; padding:7px 10px; border-bottom:1px solid var(--line); vertical-align:top; }
 th { color:var(--muted); font-weight:600; font-size:12px; text-transform:uppercase; letter-spacing:.03em; }
 tr.target td { background:#fff8f7; }
 code { font:12.5px/1.4 ui-monospace,SFMono-Regular,Menlo,monospace; background:#eef2f7; padding:1px 5px; border-radius:4px; }
 ul, ol { margin:8px 0; padding-left:22px; }
 li { margin:4px 0; }
 .chart { width:100%; height:auto; background:#fff; }
 .grid { stroke:#eef2f7; stroke-width:1; }
 .series { fill:none; stroke-linejoin:round; stroke-linecap:round; }
 .threshold { stroke:#e74c3c; stroke-dasharray:5 4; stroke-width:1; }
 .marker { stroke:#34495e; stroke-dasharray:3 3; stroke-width:1; }
 .tick { font-size:10px; fill:#8494ab; }
 .legend { font-size:11px; fill:#4a5568; }
 .axis { font-size:10px; fill:#8494ab; font-weight:600; }
 .marker-label, .threshold-label { fill:#7f8c8d; }
 footer { color:var(--muted); font-size:12px; margin-top:34px; }
</style>
</head>
<body>
<main>
 <h1>Diagnostic briefing – {{ label .B.Target }}</h1>
 <p class="sub">
  Fault {{ ts .B.FaultTime }} · window {{ ts .B.Window.From }} → {{ ts .B.Window.To }} ·
  generated {{ ts .B.GeneratedAt }}
  {{- with .B.Target.Groups }} · groups {{ join . ", " }}{{ end }}
  {{- with .B.Target.Firmware }} · firmware {{ . }}{{ end }}
  {{- with .B.Target.Hardware }} · {{ . }}{{ end }}
 </p>
 {{ with .B.Symptom }}<p class="card"><strong>Reported symptom.</strong> {{ . }}</p>{{ end }}

 <section class="card verdict {{ .B.Verdict.Scope }}">
  <h2>{{ .B.Verdict.Headline }}</h2>
  <p><span class="pill">{{ .B.Verdict.Scope }}</span>
     <span class="pill {{ .B.Verdict.Confidence }}">confidence {{ .B.Verdict.Confidence }}</span></p>
  <ul>{{ range .B.Verdict.Reasoning }}<li>{{ . }}</li>{{ end }}</ul>
  {{ with .B.Verdict.RuledOut }}
   <h3>Considered and ruled out</h3>
   <ul>{{ range . }}<li>{{ . }}</li>{{ end }}</ul>
  {{ end }}
 </section>

 <h2>Recommended field steps</h2>
 <ol class="card">{{ range .B.Recommendations }}<li>{{ . }}</li>{{ end }}</ol>

 {{ with .Charts }}
 <h2>Telemetry</h2>
 {{ range . }}
  <div class="card">
   <h3>{{ .Series }}{{ with .Unit }} [{{ . }}]{{ end }}</h3>
   {{ .SVG }}
  </div>
 {{ end }}
 {{ end }}

 <h2>Evidence</h2>

 <div class="card">
  <h3>Series statistics – {{ label .B.Target }}</h3>
  <table>
   <tr><th>Series</th><th>Baseline</th><th>Min</th><th>Max</th><th>Mean</th><th>Samples</th><th>Rate</th></tr>
   {{ $stats := .B.Target.Stats }}
   {{ range sortedKeys $stats }}{{ $s := index $stats . }}
   <tr><td><code>{{ $s.Series }}</code></td><td>{{ printf "%.2f" $s.Baseline }} {{ $s.Unit }}</td>
       <td>{{ printf "%.2f" $s.Min }}</td><td>{{ printf "%.2f" $s.Max }}</td>
       <td>{{ printf "%.2f" $s.Mean }}</td><td>{{ $s.Count }}</td><td>{{ $s.SampleRate }}</td></tr>
   {{ end }}
  </table>
 </div>

 {{ with .B.Target.Anomalies }}
 <div class="card">
  <h3>Detected anomalies</h3>
  <table>
   <tr><th>Severity</th><th>Onset</th><th>Kind</th><th>Description</th></tr>
   {{ range . }}
   <tr><td><span class="pill {{ .Severity }}">{{ .Severity }}</span></td>
       <td>{{ ts .Onset }}</td><td><code>{{ .Kind }}</code></td><td>{{ .Description }}</td></tr>
   {{ end }}
  </table>
 </div>
 {{ end }}

 {{ with .B.Correlation }}
 <div class="card">
  <h3>Neighbour correlation – <code>{{ .Series }}</code> {{ .Direction }}</h3>
  <p>{{ .Affected }} of {{ .Comparable }} comparable neighbouring assets show the same behaviour, max onset skew {{ printf "%.0f" .MaxOnsetSkew }} s.
   {{ if gt .Total .Comparable }}{{ sub .Total .Comparable }} of the {{ .Total }} neighbours do not report <code>{{ .Series }}</code> and are excluded from the ratio.{{ end }}</p>
  <table>
   <tr><th>Asset</th><th>Comparable</th><th>Correlated</th><th>Onset skew</th><th>Extreme</th><th>Baseline</th><th>Shared group</th><th>Note</th></tr>
   {{ range .Neighbors }}
   <tr><td>{{ .Name }}</td><td>{{ yesNo .Comparable }}</td><td>{{ yesNo .Correlated }}</td>
       <td>{{ if .Correlated }}{{ printf "%+.0f s" .OnsetSkewS }}{{ else }}–{{ end }}</td>
       <td>{{ printf "%.2f" .Value }}</td><td>{{ printf "%.2f" .Baseline }}</td>
       <td>{{ .SharedGroup }}</td><td>{{ .Note }}</td></tr>
   {{ end }}
  </table>
 </div>
 {{ end }}

 {{ with windowAlarms .B }}
 <div class="card">
  <h3>Alarms in the window</h3>
  <table>
   <tr><th>Time</th><th>Asset</th><th>Severity</th><th>Status</th><th>Type</th><th>Text</th></tr>
   {{ range . }}
   <tr><td>{{ ts .Time }}</td><td>{{ .Device }}</td>
       <td><span class="pill {{ .Severity }}">{{ .Severity }}</span></td>
       <td>{{ .Status }}</td><td><code>{{ .Type }}</code></td><td>{{ .Text }}</td></tr>
   {{ end }}
  </table>
 </div>
 {{ end }}

 {{ with windowEvents .B }}
 <div class="card">
  <h3>Events in the window</h3>
  <table>
   <tr><th>Time</th><th>Asset</th><th>Type</th><th>Text</th></tr>
   {{ range . }}
   <tr><td>{{ ts .Time }}</td><td>{{ .Device }}</td><td><code>{{ .Type }}</code></td><td>{{ .Text }}</td></tr>
   {{ end }}
  </table>
 </div>
 {{ end }}

 {{ with .B.FirmwareTimeline }}
 <div class="card">
  <h3>Firmware change history</h3>
  <table>
   <tr><th>Time</th><th>Asset</th><th>Version</th><th>Previous</th><th>Text</th></tr>
   {{ range . }}
   <tr><td>{{ ts .Time }}</td><td>{{ .Device }}</td><td>{{ .Version }}</td><td>{{ .Previous }}</td><td>{{ .Text }}</td></tr>
   {{ end }}
  </table>
 </div>
 {{ end }}

 {{ with .B.Target.Thresholds }}
 <div class="card">
  <h3>Active threshold rules</h3>
  <table>
   <tr><th>Series</th><th>Min</th><th>Max</th><th>Severity</th><th>Alarm type</th><th>Origin</th></tr>
   {{ range . }}
   <tr><td><code>{{ .Series }}</code></td><td>{{ num .Min }}</td><td>{{ num .Max }}</td>
       <td>{{ .Severity }}</td><td><code>{{ .AlarmType }}</code></td><td>{{ .Origin }}</td></tr>
   {{ end }}
  </table>
 </div>
 {{ end }}

 {{ with .B.Notes }}
 <div class="card"><h3>Notes</h3><ul>{{ range . }}<li>{{ . }}</li>{{ end }}</ul></div>
 {{ end }}

 <footer>
  Generated by the Cumulocity diagnostics MCP server ·
  {{ .B.Target.MeasurementsGot }} measurements analysed on the target,
  {{ len .B.Neighbors }} neighbouring assets compared.
 </footer>
</main>
</body>
</html>
`
