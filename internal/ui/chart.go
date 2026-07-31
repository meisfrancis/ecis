package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/model"
)

// sparkRunes are the eighth-block characters used for inline sparklines.
var sparkRunes = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Sparkline renders values as a fixed-width inline chart, keeping the most
// recent width samples. Gaps (NaN) render as spaces.
func Sparkline(values []float64, width int) string {
	if width <= 0 || len(values) == 0 {
		return strings.Repeat(" ", max(width, 0))
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}

	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range values {
		if math.IsNaN(v) {
			continue
		}
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if math.IsInf(lo, 1) {
		return strings.Repeat(" ", width)
	}
	// A flat series should render as a mid-height line, not an empty one.
	if hi <= lo {
		hi = lo + 1
	}

	var b strings.Builder
	for i := 0; i < width-len(values); i++ {
		b.WriteRune(' ')
	}
	for _, v := range values {
		if math.IsNaN(v) {
			b.WriteRune(' ')
			continue
		}
		idx := int((v - lo) / (hi - lo) * float64(len(sparkRunes)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkRunes) {
			idx = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

// Gauge renders a percentage as a horizontal bar with a colour that escalates
// with the level. Values outside 0-100 clamp; NaN renders as an empty track.
func Gauge(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	if math.IsNaN(pct) {
		return Colorize(strings.Repeat("░", width), ColorMuted)
	}
	clamped := math.Max(0, math.Min(100, pct))
	filled := int(math.Round(clamped / 100 * float64(width)))
	return Colorize(strings.Repeat("█", filled), LevelColor(clamped)) +
		Colorize(strings.Repeat("░", width-filled), ColorMuted)
}

// FormatPct renders a percentage for a table cell, showing a dash for unknowns.
func FormatPct(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", v)
}

// braille dot bits, indexed by [column][row] within a 2x4 cell.
var brailleBits = [2][4]byte{
	{0x01, 0x02, 0x04, 0x40},
	{0x08, 0x10, 0x20, 0x80},
}

// ChartSeries is one plotted line.
type ChartSeries struct {
	Series model.Series
	Color  tcell.Color
	// Fill shades the area between the line and the baseline.
	Fill bool
}

// Chart is a braille line plot. Braille gives 2x4 sub-cell resolution, which is
// what makes a 60-point CloudWatch series legible in a handful of terminal rows.
type Chart struct {
	*tview.Box

	series  []ChartSeries
	unit    string
	min     float64
	max     float64
	autoMax bool
	note    string
	legend  bool
	start   time.Time
	end     time.Time
}

// NewChart returns an empty chart scaled 0-100 (the natural range for the
// utilization percentages it mostly plots).
func NewChart() *Chart {
	c := &Chart{
		Box:    tview.NewBox(),
		min:    0,
		max:    100,
		unit:   "%",
		legend: true,
	}
	c.SetBorder(true)
	c.SetBorderColor(ColorBorder)
	c.SetTitleColor(ColorTitle)
	c.SetBackgroundColor(ColorBackground)
	return c
}

// SetSeries replaces the plotted data.
func (c *Chart) SetSeries(series ...ChartSeries) *Chart {
	c.series = series
	return c
}

// SetUnit sets the axis unit suffix.
func (c *Chart) SetUnit(unit string) *Chart { c.unit = unit; return c }

// SetRange pins the y-axis.
func (c *Chart) SetRange(min, max float64) *Chart {
	c.min, c.max, c.autoMax = min, max, false
	return c
}

// SetAutoRange scales the y-axis to the data on every draw.
func (c *Chart) SetAutoRange() *Chart { c.autoMax = true; return c }

// SetWindow records the time span, used for the x-axis labels.
func (c *Chart) SetWindow(start, end time.Time) *Chart { c.start, c.end = start, end; return c }

// SetNote adds a caption under the legend, for explaining missing data.
func (c *Chart) SetNote(note string) *Chart { c.note = note; return c }

// SetLegend toggles the legend line.
func (c *Chart) SetLegend(on bool) *Chart { c.legend = on; return c }

// Draw renders the chart.
func (c *Chart) Draw(screen tcell.Screen) {
	c.Box.DrawForSubclass(screen, c)
	x, y, w, h := c.GetInnerRect()
	if w < 12 || h < 3 {
		return
	}

	legendRows := 0
	if c.legend && len(c.series) > 0 {
		legendRows = len(c.series)
	}
	if c.note != "" {
		legendRows++
	}
	axisRows := 1 // x-axis labels
	plotH := h - legendRows - axisRows
	if plotH < 2 {
		// Not enough room for both; the plot matters more than the legend.
		plotH, legendRows = h-axisRows, 0
		if plotH < 1 {
			return
		}
	}

	const gutter = 7 // width reserved for y-axis labels
	plotX, plotW := x+gutter, w-gutter
	if plotW < 4 {
		return
	}

	lo, hi := c.bounds()
	c.drawAxis(screen, x, y, gutter, plotH, lo, hi)
	c.drawPlot(screen, plotX, y, plotW, plotH, lo, hi)
	c.drawTimeAxis(screen, plotX, y+plotH, plotW)

	if legendRows > 0 {
		c.drawLegend(screen, x, y+plotH+axisRows, w)
	}
}

// bounds returns the y-axis range, widening to fit the data in auto mode.
func (c *Chart) bounds() (float64, float64) {
	if !c.autoMax {
		return c.min, c.max
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, s := range c.series {
		for _, v := range s.Series.Values {
			if math.IsNaN(v) {
				continue
			}
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	if math.IsInf(lo, 1) {
		return 0, 1
	}
	if hi <= lo {
		hi = lo + 1
	}
	// A little headroom keeps the peak off the border.
	return math.Min(0, lo), hi * 1.1
}

func (c *Chart) drawAxis(screen tcell.Screen, x, y, gutter, plotH int, lo, hi float64) {
	style := tcell.StyleDefault.Background(ColorBackground).Foreground(ColorMuted)
	for row := 0; row < plotH; row++ {
		// Top row is the maximum, bottom row the minimum.
		frac := 1 - float64(row)/float64(maxInt(plotH-1, 1))
		v := lo + (hi-lo)*frac
		label := ""
		switch row {
		case 0, plotH - 1, plotH / 2:
			label = fmt.Sprintf("%*s", gutter-1, formatAxis(v, c.unit))
		}
		for i, r := range label {
			screen.SetContent(x+i, y+row, r, nil, style)
		}
		screen.SetContent(x+gutter-1, y+row, '│', nil, style)
	}
}

func (c *Chart) drawTimeAxis(screen tcell.Screen, x, y, w int) {
	style := tcell.StyleDefault.Background(ColorBackground).Foreground(ColorMuted)
	for i := 0; i < w; i++ {
		screen.SetContent(x+i, y, '─', nil, style)
	}
	if c.start.IsZero() || c.end.IsZero() {
		return
	}
	left := c.start.Local().Format("15:04")
	right := c.end.Local().Format("15:04")
	drawString(screen, x, y, left, style)
	drawString(screen, x+w-len(right), y, right, style)
}

// drawPlot rasterises every series into a braille grid and blits it.
func (c *Chart) drawPlot(screen tcell.Screen, x, y, w, h int, lo, hi float64) {
	if hi <= lo {
		hi = lo + 1
	}
	dotsW, dotsH := w*2, h*4

	// One mask and colour per cell; later series win overlapping cells.
	mask := make([]byte, w*h)
	colors := make([]tcell.Color, w*h)
	for i := range colors {
		colors[i] = ColorForeground
	}

	set := func(dx, dy int, col tcell.Color) {
		if dx < 0 || dy < 0 || dx >= dotsW || dy >= dotsH {
			return
		}
		cx, cy := dx/2, dy/4
		idx := cy*w + cx
		mask[idx] |= brailleBits[dx%2][dy%4]
		colors[idx] = col
	}

	for _, s := range c.series {
		values := s.Series.Values
		if len(values) == 0 {
			continue
		}
		prevY := -1
		for dx := 0; dx < dotsW; dx++ {
			// Map the dot column onto a sample index.
			i := 0
			if dotsW > 1 {
				i = int(math.Round(float64(dx) / float64(dotsW-1) * float64(len(values)-1)))
			}
			v := values[i]
			if math.IsNaN(v) {
				prevY = -1
				continue
			}
			frac := (v - lo) / (hi - lo)
			dy := int(math.Round((1 - frac) * float64(dotsH-1)))
			dy = clampInt(dy, 0, dotsH-1)

			set(dx, dy, s.Color)
			if s.Fill {
				for f := dy + 1; f < dotsH; f++ {
					set(dx, f, s.Color)
				}
			} else if prevY >= 0 {
				// Bridge steep jumps so the line stays connected.
				step := 1
				if dy < prevY {
					step = -1
				}
				for f := prevY; f != dy; f += step {
					set(dx, f, s.Color)
				}
			}
			prevY = dy
		}
	}

	for cy := 0; cy < h; cy++ {
		for cx := 0; cx < w; cx++ {
			idx := cy*w + cx
			if mask[idx] == 0 {
				continue
			}
			style := tcell.StyleDefault.Background(ColorBackground).Foreground(colors[idx])
			screen.SetContent(x+cx, y+cy, rune(0x2800+int(mask[idx])), nil, style)
		}
	}
}

func (c *Chart) drawLegend(screen tcell.Screen, x, y, w int) {
	row := y
	for _, s := range c.series {
		style := tcell.StyleDefault.Background(ColorBackground).Foreground(s.Color)
		text := fmt.Sprintf("■ %-12s cur %s  min %s  avg %s  max %s",
			Truncate(s.Series.Label, 12),
			formatAxis(s.Series.Last(), c.unit),
			formatAxis(s.Series.Min(), c.unit),
			formatAxis(s.Series.Avg(), c.unit),
			formatAxis(s.Series.Max(), c.unit),
		)
		drawString(screen, x, row, Truncate(text, w), style)
		row++
	}
	if c.note != "" {
		style := tcell.StyleDefault.Background(ColorBackground).Foreground(ColorMuted)
		drawString(screen, x, row, Truncate(c.note, w), style)
	}
}

func drawString(screen tcell.Screen, x, y int, s string, style tcell.Style) {
	for i, r := range []rune(s) {
		screen.SetContent(x+i, y, r, nil, style)
	}
}

// formatAxis renders a value compactly enough for a 6-column axis gutter.
func formatAxis(v float64, unit string) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	abs := math.Abs(v)
	switch {
	case abs >= 1e9:
		return fmt.Sprintf("%.1fG", v/1e9)
	case abs >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case abs >= 1e4:
		return fmt.Sprintf("%.1fk", v/1e3)
	case unit == "%":
		return fmt.Sprintf("%.0f%%", v)
	case abs >= 100:
		return fmt.Sprintf("%.0f", v)
	case abs >= 1:
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.3f", v)
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func max(a, b int) int { return maxInt(a, b) }
