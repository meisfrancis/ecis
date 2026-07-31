package view

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

// metricWindows are the time ranges bound to the number keys.
var metricWindows = []struct {
	Key    rune
	Label  string
	Window time.Duration
	Period time.Duration
}{
	{'1', "15m", 15 * time.Minute, time.Minute},
	{'2', "1h", time.Hour, time.Minute},
	{'3', "6h", 6 * time.Hour, 5 * time.Minute},
	{'4', "24h", 24 * time.Hour, 15 * time.Minute},
	{'5', "7d", 7 * 24 * time.Hour, time.Hour},
}

// Metrics is the CloudWatch monitoring view: CPU and memory over time for a
// cluster, a service or a single task, plus whatever supporting series that
// level publishes.
type Metrics struct {
	*tview.Flex

	app    *ui.App
	target awsx.MetricTarget

	cpu   *ui.Chart
	mem   *ui.Chart
	extra *ui.Chart

	mu     sync.Mutex
	window time.Duration
	period time.Duration
	cancel context.CancelFunc
}

// NewMetrics builds the monitoring view for a target.
func NewMetrics(app *ui.App, target awsx.MetricTarget) *Metrics {
	m := &Metrics{
		Flex:   tview.NewFlex().SetDirection(tview.FlexRow),
		app:    app,
		target: target,
		cpu:    ui.NewChart(),
		mem:    ui.NewChart(),
		extra:  ui.NewChart(),
		window: app.Config().MetricWindow(),
		period: app.Config().MetricPeriod(),
	}

	m.cpu.SetRange(0, 100).SetUnit("%")
	m.mem.SetRange(0, 100).SetUnit("%")
	m.extra.SetAutoRange().SetUnit("")

	m.SetBackgroundColor(ui.ColorBackground)
	m.AddItem(m.cpu, 0, 1, false).
		AddItem(m.mem, 0, 1, false).
		AddItem(m.extra, 0, 1, false)

	m.SetInputCapture(m.keys)
	m.updateTitles()
	return m
}

// Name implements ui.Component.
func (m *Metrics) Name() string { return "metrics" }

// Title implements ui.Component.
func (m *Metrics) Title() string { return "Metrics" }

// Hints implements ui.Component.
func (m *Metrics) Hints() []ui.Hint {
	return []ui.Hint{
		{Key: "1", Desc: "15m"},
		{Key: "2", Desc: "1h"},
		{Key: "3", Desc: "6h"},
		{Key: "4", Desc: "24h"},
		{Key: "5", Desc: "7d"},
		{Key: "ctrl-r", Desc: "Reload"},
	}
}

// Start begins polling CloudWatch.
func (m *Metrics) Start() {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()

	go m.loop(ctx)
}

// Stop halts polling.
func (m *Metrics) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// Refresh implements ui.Refreshable.
func (m *Metrics) Refresh() { go m.fetch(context.Background()) }

func (m *Metrics) loop(ctx context.Context) {
	m.fetch(ctx)

	// Charts refresh on the CloudWatch period rather than the table rate: a
	// faster poll would just re-fetch the same datapoints.
	interval := m.period
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.fetch(ctx)
		}
	}
}

func (m *Metrics) fetch(ctx context.Context) {
	m.mu.Lock()
	window, period := m.window, m.period
	m.mu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	charts, err := m.app.Client().GetCharts(reqCtx, m.target, window, period)
	if ctx.Err() != nil {
		return
	}

	m.app.QueueUpdateDraw(func() {
		if err != nil {
			m.app.Flash().Err(err)
			return
		}
		m.apply(charts)
	})
}

func (m *Metrics) apply(c *awsx.Charts) {
	cpuSeries := []ui.ChartSeries{{Series: c.CPU, Color: ui.ColorCPU, Fill: true}}
	if !c.CPUMax.Empty() {
		cpuSeries = append(cpuSeries, ui.ChartSeries{Series: c.CPUMax, Color: ui.ColorWarn})
	}
	memSeries := []ui.ChartSeries{{Series: c.Memory, Color: ui.ColorMemory, Fill: true}}
	if !c.MemMax.Empty() {
		memSeries = append(memSeries, ui.ChartSeries{Series: c.MemMax, Color: ui.ColorWarn})
	}

	m.cpu.SetSeries(cpuSeries...).SetWindow(c.Start, c.End).SetNote(noteFor(c, c.CPU))
	m.mem.SetSeries(memSeries...).SetWindow(c.Start, c.End).SetNote(noteFor(c, c.Memory))

	extra := make([]ui.ChartSeries, 0, len(c.Extra))
	palette := []tcell.Color{ui.ColorOK, ui.ColorPending, ui.ColorWarn, ui.ColorCost}
	for i, s := range c.Extra {
		if s.Empty() {
			continue
		}
		extra = append(extra, ui.ChartSeries{Series: s, Color: palette[i%len(palette)]})
	}
	if len(extra) == 0 {
		// Keep the pane rather than collapsing the layout on every refresh.
		m.extra.SetSeries().SetNote("no supporting metrics published for this target")
	} else {
		m.extra.SetSeries(extra...).SetNote("")
	}
	m.extra.SetWindow(c.Start, c.End)

	m.updateTitles()
}

// noteFor explains an empty chart rather than leaving a blank pane.
func noteFor(c *awsx.Charts, s model.Series) string {
	if !s.Empty() {
		return c.Note
	}
	switch c.Target.Kind {
	case "task":
		return "no data — per-task metrics need Container Insights with enhanced observability"
	case "cluster":
		return "no data — cluster utilization needs EC2 capacity or Container Insights"
	default:
		return "no data in this window"
	}
}

func (m *Metrics) updateTitles() {
	m.mu.Lock()
	window, period := m.window, m.period
	m.mu.Unlock()

	scope := fmt.Sprintf("[%s](%s)[-]", ui.Hex(ui.ColorForeground), m.target.Title())
	suffix := fmt.Sprintf("[%s]%s / %s[-] ", ui.Hex(ui.ColorMuted),
		awsx.Duration(window), awsx.Duration(period))

	m.cpu.SetTitle(fmt.Sprintf(" [%s::b]CPU[-::-]%s %s", ui.Hex(ui.ColorCPU), scope, suffix))
	m.mem.SetTitle(fmt.Sprintf(" [%s::b]Memory[-::-]%s %s", ui.Hex(ui.ColorMemory), scope, suffix))
	m.extra.SetTitle(fmt.Sprintf(" [%s::b]Workload[-::-]%s %s", ui.Hex(ui.ColorTitle), scope, suffix))
}

func (m *Metrics) keys(evt *tcell.EventKey) *tcell.EventKey {
	for _, w := range metricWindows {
		if evt.Rune() != w.Key {
			continue
		}
		m.mu.Lock()
		m.window, m.period = w.Window, w.Period
		m.mu.Unlock()

		m.updateTitles()
		m.app.Flash().Infof("window %s", w.Label)
		m.Refresh()
		return nil
	}
	return evt
}
