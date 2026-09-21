package report

import (
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/diag"
)

// Line is one plotted asset series.
type Line struct {
	Label   string
	Points  []diag.Point
	Primary bool
}

// Marker is a labelled vertical line, e.g. the fault timestamp.
type Marker struct {
	Time  time.Time
	Label string
}

var palette = []string{"#c0392b", "#2980b9", "#27ae60", "#8e44ad", "#d35400", "#16a085", "#7f8c8d"}

const (
	chartW, chartH = 900.0, 260.0
	padL, padR     = 64.0, 150.0
	padT, padB     = 18.0, 28.0
)

// SVG renders a self contained line chart. The primary line is drawn bold, the
// threshold lines dashed, and markers as vertical lines.
func SVG(title, unit string, lines []Line, thresholds []float64, markers []Marker) template.HTML {
	var minT, maxT time.Time
	minV, maxV := math.MaxFloat64, -math.MaxFloat64
	for _, l := range lines {
		for _, p := range l.Points {
			if minT.IsZero() || p.Time.Before(minT) {
				minT = p.Time
			}
			if maxT.IsZero() || p.Time.After(maxT) {
				maxT = p.Time
			}
			minV, maxV = math.Min(minV, p.Value), math.Max(maxV, p.Value)
		}
	}
	if minT.IsZero() || maxT.Equal(minT) {
		return template.HTML(`<p class="nodata">no data</p>`) // #nosec G203 -- constant
	}
	for _, th := range thresholds {
		minV, maxV = math.Min(minV, th), math.Max(maxV, th)
	}
	span := maxV - minV
	if span == 0 {
		span = 1
	}
	minV, maxV = minV-span*0.12, maxV+span*0.12

	x := func(t time.Time) float64 {
		f := float64(t.Sub(minT)) / float64(maxT.Sub(minT))
		return padL + f*(chartW-padL-padR)
	}
	y := func(v float64) float64 {
		f := (v - minV) / (maxV - minV)
		return chartH - padB - f*(chartH-padT-padB)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`,
		chartW, chartH, template.HTMLEscapeString(title))

	// Horizontal grid with value labels.
	for i := range 5 {
		v := minV + (maxV-minV)*float64(i)/4
		yy := y(v)
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`,
			padL, yy, chartW-padR, yy)
		fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%.1f" text-anchor="end">%.1f</text>`,
			padL-8, yy+4, v)
	}
	fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.1f" text-anchor="end">%s</text>`,
		padL-8, padT+2, template.HTMLEscapeString(unit))

	// Time axis labels.
	for i := range 4 {
		t := minT.Add(time.Duration(float64(maxT.Sub(minT)) * float64(i) / 3))
		anchor := "middle"
		switch i {
		case 0:
			anchor = "start"
		case 3:
			anchor = "end"
		}
		fmt.Fprintf(&b, `<text class="tick" x="%.1f" y="%.1f" text-anchor="%s">%s</text>`,
			x(t), chartH-8, anchor, t.UTC().Format("15:04:05"))
	}

	for _, th := range thresholds {
		fmt.Fprintf(&b, `<line class="threshold" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`,
			padL, y(th), chartW-padR, y(th))
		fmt.Fprintf(&b, `<text class="tick threshold-label" x="%.1f" y="%.1f">%.1f limit</text>`,
			chartW-padR+4, y(th)+4, th)
	}

	for _, m := range markers {
		if m.Time.Before(minT) || m.Time.After(maxT) {
			continue
		}
		mx := x(m.Time)
		fmt.Fprintf(&b, `<line class="marker" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`,
			mx, padT, mx, chartH-padB)
		fmt.Fprintf(&b, `<text class="tick marker-label" x="%.1f" y="%.1f">%s</text>`,
			mx+4, padT+10, template.HTMLEscapeString(m.Label))
	}

	for i, l := range lines {
		if len(l.Points) == 0 {
			continue
		}
		color := palette[i%len(palette)]
		var path strings.Builder
		for j, p := range l.Points {
			cmd := "L"
			if j == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&path, "%s%.1f %.1f ", cmd, x(p.Time), y(p.Value))
		}
		width := 1.2
		if l.Primary {
			width = 2.4
		}
		fmt.Fprintf(&b, `<path class="series" d="%s" stroke="%s" stroke-width="%.1f"/>`,
			strings.TrimSpace(path.String()), color, width)

		ly := padT + 12 + float64(i)*16
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%.1f"/>`,
			chartW-padR+6, ly-4, chartW-padR+22, ly-4, color, width)
		fmt.Fprintf(&b, `<text class="legend" x="%.1f" y="%.1f">%s</text>`,
			chartW-padR+26, ly, template.HTMLEscapeString(trim(l.Label, 18)))
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String()) // #nosec G203 -- all interpolations are escaped above
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
