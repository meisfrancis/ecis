package ui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/meisfrancis/ecis/internal/model"
)

func TestSparklinePadsToWidth(t *testing.T) {
	got := Sparkline([]float64{1, 2, 3}, 8)
	if n := len([]rune(got)); n != 8 {
		t.Fatalf("Sparkline width = %d, want 8 (%q)", n, got)
	}
	if !strings.HasPrefix(got, "     ") {
		t.Errorf("short series should be right-aligned, got %q", got)
	}
}

func TestSparklineKeepsMostRecentSamples(t *testing.T) {
	// The last value is the largest, so it must render as a full block.
	got := []rune(Sparkline([]float64{9, 8, 7, 1, 2, 3}, 3))
	if len(got) != 3 {
		t.Fatalf("Sparkline width = %d, want 3", len(got))
	}
	if got[2] != '█' {
		t.Errorf("last rune = %q, want the tallest block", string(got[2]))
	}
	if got[0] != '▁' {
		t.Errorf("first rune = %q, want the shortest block", string(got[0]))
	}
}

func TestSparklineHandlesFlatAndEmptySeries(t *testing.T) {
	flat := Sparkline([]float64{5, 5, 5}, 3)
	if strings.TrimSpace(flat) == "" {
		t.Errorf("a flat series should still render, got %q", flat)
	}
	if got := Sparkline(nil, 4); got != "    " {
		t.Errorf("Sparkline(nil) = %q, want blanks", got)
	}
	gaps := Sparkline([]float64{math.NaN(), math.NaN()}, 2)
	if strings.TrimSpace(gaps) != "" {
		t.Errorf("an all-NaN series should render blank, got %q", gaps)
	}
}

func TestGaugeFillsProportionally(t *testing.T) {
	full := Gauge(100, 10)
	if strings.Count(full, "█") != 10 {
		t.Errorf("Gauge(100) = %q, want 10 filled cells", full)
	}
	empty := Gauge(0, 10)
	if strings.Count(empty, "░") != 10 {
		t.Errorf("Gauge(0) = %q, want 10 empty cells", empty)
	}
	half := Gauge(50, 10)
	if strings.Count(half, "█") != 5 {
		t.Errorf("Gauge(50) = %q, want 5 filled cells", half)
	}
	// Out-of-range values clamp rather than overflowing the bar.
	over := Gauge(150, 10)
	if strings.Count(over, "█") != 10 {
		t.Errorf("Gauge(150) = %q, want the bar clamped to full", over)
	}
	if unknown := Gauge(math.NaN(), 6); strings.Count(unknown, "░") != 6 {
		t.Errorf("Gauge(NaN) = %q, want an empty track", unknown)
	}
}

func TestLevelColorEscalates(t *testing.T) {
	tests := []struct {
		pct  float64
		want tcell.Color
	}{
		{10, ColorOK},
		{74.9, ColorOK},
		{80, ColorWarn},
		{95, ColorError},
		{-1, ColorMuted},
	}
	for _, tc := range tests {
		if got := LevelColor(tc.pct); got != tc.want {
			t.Errorf("LevelColor(%v) = %v, want %v", tc.pct, got, tc.want)
		}
	}
}

// drawChart renders a chart onto a simulation screen and returns the cells.
func drawChart(t *testing.T, c *Chart, w, h int) []rune {
	t.Helper()

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(w, h)

	c.SetRect(0, 0, w, h)
	c.Draw(screen)
	// GetContents reads the front buffer, which only picks up SetContent calls
	// once they have been flushed.
	screen.Show()

	cells, _, _ := screen.GetContents()
	out := make([]rune, 0, len(cells))
	for _, cell := range cells {
		out = append(out, cell.Runes...)
	}
	return out
}

func TestChartDrawsBrailleForData(t *testing.T) {
	now := time.Now()
	series := model.Series{Label: "CPU"}
	for i := 0; i < 60; i++ {
		series.Timestamps = append(series.Timestamps, now.Add(time.Duration(i)*time.Minute))
		series.Values = append(series.Values, float64(i%100))
	}

	c := NewChart().
		SetSeries(ChartSeries{Series: series, Color: ColorCPU, Fill: true}).
		SetRange(0, 100).
		SetWindow(now, now.Add(time.Hour))

	runes := drawChart(t, c, 80, 20)

	var braille int
	for _, r := range runes {
		if r >= 0x2800 && r <= 0x28ff {
			braille++
		}
	}
	if braille == 0 {
		t.Fatal("chart drew no braille cells for a populated series")
	}
}

func TestChartDrawsWithoutDataOrSpace(t *testing.T) {
	// An empty chart, a tiny rect and a NaN-only series must not panic: all
	// three happen in practice while CloudWatch is still catching up.
	drawChart(t, NewChart().SetNote("no data"), 40, 10)
	drawChart(t, NewChart(), 4, 2)
	drawChart(t, NewChart().SetSeries(ChartSeries{
		Series: model.Series{Label: "x", Values: []float64{math.NaN(), math.NaN()}},
		Color:  ColorCPU,
	}).SetAutoRange(), 40, 12)
}

func TestChartAutoRangeHandlesFlatSeries(t *testing.T) {
	c := NewChart().SetAutoRange().SetSeries(ChartSeries{
		Series: model.Series{Label: "flat", Values: []float64{7, 7, 7}},
		Color:  ColorCPU,
	})
	lo, hi := c.bounds()
	if !(lo < hi) {
		t.Errorf("bounds() = (%v, %v), want a non-empty range", lo, hi)
	}
}

func TestTruncateAddsEllipsis(t *testing.T) {
	if got := Truncate("abcdefgh", 4); got != "abc…" {
		t.Errorf("Truncate = %q, want %q", got, "abc…")
	}
	if got := Truncate("abc", 10); got != "abc" {
		t.Errorf("Truncate should leave short strings alone, got %q", got)
	}
	if got := Truncate("abc", 0); got != "" {
		t.Errorf("Truncate to zero = %q, want empty", got)
	}
}
