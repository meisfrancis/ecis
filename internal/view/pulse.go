package view

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

// Pulse is the dashboard: fleet counters, cluster CPU and memory history, the
// hottest services, and where the money is going — all on one screen.
type Pulse struct {
	*tview.Flex

	app *ui.App

	tiles    *tview.TextView
	cpu      *ui.Chart
	mem      *ui.Chart
	services *tview.TextView
	spend    *tview.TextView

	mu          sync.Mutex
	cancel      context.CancelFunc
	lastCost    *model.CostReport
	costFetched time.Time
}

// NewPulse builds the dashboard.
func NewPulse(app *ui.App) *Pulse {
	p := &Pulse{
		Flex:     tview.NewFlex().SetDirection(tview.FlexRow),
		app:      app,
		tiles:    newPanel(" Fleet "),
		cpu:      ui.NewChart(),
		mem:      ui.NewChart(),
		services: newPanel(" Busiest services "),
		spend:    newPanel(" Spend "),
	}

	p.cpu.SetRange(0, 100).SetUnit("%").SetLegend(false)
	p.mem.SetRange(0, 100).SetUnit("%").SetLegend(false)
	p.cpu.SetTitle(fmt.Sprintf(" [%s::b]CPU[-::-] ", ui.Hex(ui.ColorCPU)))
	p.mem.SetTitle(fmt.Sprintf(" [%s::b]Memory[-::-] ", ui.Hex(ui.ColorMemory)))

	charts := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(p.cpu, 0, 1, false).
		AddItem(p.mem, 0, 1, false)

	panels := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(p.services, 0, 1, false).
		AddItem(p.spend, 0, 1, false)

	p.SetBackgroundColor(ui.ColorBackground)
	p.AddItem(p.tiles, 7, 0, false).
		AddItem(charts, 0, 1, false).
		AddItem(panels, 0, 1, false)
	return p
}

func newPanel(title string) *tview.TextView {
	t := tview.NewTextView()
	t.SetDynamicColors(true).SetWrap(false).SetBackgroundColor(ui.ColorBackground)
	t.SetBorder(true).
		SetBorderColor(ui.ColorBorder).
		SetTitleColor(ui.ColorTitle).
		SetTitle(title)
	return t
}

// Name implements ui.Component.
func (p *Pulse) Name() string { return "pulse" }

// Title implements ui.Component.
func (p *Pulse) Title() string { return "Pulse" }

// Hints implements ui.Component.
func (p *Pulse) Hints() []ui.Hint {
	return []ui.Hint{
		{Key: ":cl", Desc: "Clusters"},
		{Key: ":svc", Desc: "Services"},
		{Key: ":cost", Desc: "Cost"},
		{Key: "ctrl-r", Desc: "Reload"},
	}
}

// Start begins refreshing the dashboard.
func (p *Pulse) Start() {
	p.mu.Lock()
	if p.cancel != nil {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()

	go p.loop(ctx)
}

// Stop halts refreshing.
func (p *Pulse) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
}

// Refresh implements ui.Refreshable.
func (p *Pulse) Refresh() { go p.fetch(context.Background()) }

func (p *Pulse) loop(ctx context.Context) {
	p.fetch(ctx)

	ticker := time.NewTicker(maxDuration(p.app.Config().Interval(), 10*time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.fetch(ctx)
		}
	}
}

func (p *Pulse) fetch(ctx context.Context) {
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	client := p.app.Client()
	cfg := p.app.Config()

	clusters, err := client.Clusters(reqCtx)
	if err != nil {
		p.app.QueueUpdateDraw(func() { p.app.Flash().Err(err) })
		return
	}

	cluster := p.app.Cluster()
	if cluster == "" && len(clusters) > 0 {
		cluster = clusters[0].Name
	}

	services, _ := client.Services(reqCtx, cluster)
	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, s.Name)
	}
	samples, _ := client.ServiceSamples(reqCtx, cluster, names, cfg.MetricPeriod())

	var charts *awsx.Charts
	if cluster != "" {
		charts, _ = client.GetCharts(reqCtx,
			awsx.MetricTarget{Kind: "cluster", Cluster: cluster},
			cfg.MetricWindow(), cfg.MetricPeriod())
	}

	report := p.costReport(reqCtx)
	if ctx.Err() != nil {
		return
	}

	p.app.QueueUpdateDraw(func() {
		p.renderTiles(clusters, services, cluster)
		p.renderCharts(charts)
		p.renderServices(services, samples)
		p.renderSpend(report)
	})
}

// costReport reuses the last cost result until it goes stale, so the dashboard
// does not bill a Cost Explorer request on every tick.
func (p *Pulse) costReport(ctx context.Context) *model.CostReport {
	p.mu.Lock()
	cached, fetched := p.lastCost, p.costFetched
	p.mu.Unlock()

	if cached != nil && time.Since(fetched) < p.app.Config().CostInterval() {
		return cached
	}

	cfg := p.app.Config()
	report, err := p.app.Client().Cost(ctx, awsx.CostOptions{
		GroupBy:        awsx.GroupByService,
		Granularity:    "DAILY",
		Days:           7,
		ClusterTagKey:  cfg.Cost.ClusterTagKey,
		ServiceTagKey:  cfg.Cost.ServiceTagKey,
		IncludeCredits: cfg.Cost.IncludeCredits,
	})
	if err != nil {
		p.mu.Lock()
		p.costFetched = time.Now()
		p.mu.Unlock()
		return cached
	}

	p.mu.Lock()
	p.lastCost, p.costFetched = report, time.Now()
	p.mu.Unlock()
	return report
}

func (p *Pulse) renderTiles(clusters []model.Cluster, services []model.Service, cluster string) {
	var running, pending, instances, activeServices int
	for _, c := range clusters {
		running += c.RunningTasks
		pending += c.PendingTasks
		instances += c.Instances
		activeServices += c.ActiveServices
	}

	var degraded int
	for _, s := range services {
		if !s.Healthy() {
			degraded++
		}
	}

	degradedColor := ui.ColorOK
	if degraded > 0 {
		degradedColor = ui.ColorError
	}
	pendingColor := ui.ColorForeground
	if pending > 0 {
		pendingColor = ui.ColorWarn
	}

	strip := joinTiles(
		tile("CLUSTERS", strconv.Itoa(len(clusters)), ui.ColorPending),
		tile("SERVICES", strconv.Itoa(activeServices), ui.ColorPending),
		tile("RUNNING", strconv.Itoa(running), ui.ColorOK),
		tile("PENDING", strconv.Itoa(pending), pendingColor),
		tile("DEGRADED", strconv.Itoa(degraded), degradedColor),
		tile("INSTANCES", strconv.Itoa(instances), ui.ColorMuted),
	)

	scope := cluster
	if scope == "" {
		scope = "no cluster selected"
	}
	p.tiles.SetText(fmt.Sprintf("\n%s\n\n  [%s]focus: %s · %s · %s[-]",
		strip, ui.Hex(ui.ColorMuted), scope, p.app.Region(), p.app.Profile()))
}

// joinTiles lays multi-line tiles out side by side.
func joinTiles(tiles ...string) string {
	rows := make([]strings.Builder, 2)
	for _, t := range tiles {
		lines := strings.SplitN(t, "\n", 2)
		for i := range rows {
			if i < len(lines) {
				rows[i].WriteString(lines[i])
			}
		}
	}
	return rows[0].String() + "\n" + rows[1].String()
}

// tile renders one big-number counter for the fleet strip.
func tile(label, value string, color tcell.Color) string {
	return fmt.Sprintf("  [%s]%-9s[-]\n  [%s::b]%-9s[-::-]",
		ui.Hex(ui.ColorMuted), label, ui.Hex(color), value)
}

func (p *Pulse) renderCharts(charts *awsx.Charts) {
	if charts == nil {
		p.cpu.SetSeries().SetNote("no cluster selected")
		p.mem.SetSeries().SetNote("no cluster selected")
		return
	}
	p.cpu.SetSeries(ui.ChartSeries{Series: charts.CPU, Color: ui.ColorCPU, Fill: true}).
		SetWindow(charts.Start, charts.End).
		SetNote(noteFor(charts, charts.CPU))
	p.mem.SetSeries(ui.ChartSeries{Series: charts.Memory, Color: ui.ColorMemory, Fill: true}).
		SetWindow(charts.Start, charts.End).
		SetNote(noteFor(charts, charts.Memory))
}

func (p *Pulse) renderServices(services []model.Service, samples map[string]awsx.Sample) {
	type entry struct {
		name string
		svc  model.Service
		cpu  float64
		mem  float64
	}
	entries := make([]entry, 0, len(services))
	for _, s := range services {
		sample := samples[s.Name]
		entries = append(entries, entry{s.Name, s, sample.CPU, sample.Memory})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].cpu, entries[j].cpu
		if math.IsNaN(a) {
			a = -1
		}
		if math.IsNaN(b) {
			b = -1
		}
		return a > b
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, " [%s::b]%-24s %-9s %-16s %-16s[-::-]\n",
		ui.Hex(ui.ColorHeader), "SERVICE", "TASKS", "CPU", "MEM")

	for i, e := range entries {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&sb, " %-24s [%s]%-9s[-] %s %s\n",
			tview.Escape(ui.Truncate(e.name, 24)),
			ui.Hex(ratioColor(e.svc.Running, e.svc.Desired)),
			ratio(e.svc.Running, e.svc.Desired),
			miniGauge(e.cpu),
			miniGauge(e.mem),
		)
	}
	if len(entries) == 0 {
		fmt.Fprintf(&sb, "\n [%s]No services in this cluster.[-]\n", ui.Hex(ui.ColorMuted))
	}
	p.services.SetText(sb.String())
}

// miniGauge renders a compact bar plus its percentage.
func miniGauge(pct float64) string {
	return fmt.Sprintf("%s %6s", ui.Gauge(pct, 8), ui.FormatPct(pct))
}

func (p *Pulse) renderSpend(report *model.CostReport) {
	var sb strings.Builder
	if report == nil {
		fmt.Fprintf(&sb, "\n [%s]Cost Explorer is unavailable.[-]\n", ui.Hex(ui.ColorMuted))
		fmt.Fprintf(&sb, " [%s]It needs ce:GetCostAndUsage and Cost Explorer enabled\n on the payer account.[-]\n",
			ui.Hex(ui.ColorMuted))
		p.spend.SetText(sb.String())
		return
	}

	fmt.Fprintf(&sb, " [%s::b]%-28s %12s %s[-::-]\n",
		ui.Hex(ui.ColorHeader), strings.ToUpper(report.GroupBy), "7 DAYS", "TREND")

	for i, g := range report.Groups {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&sb, " %-28s [%s]%12s[-] [%s]%s[-]\n",
			tview.Escape(ui.Truncate(g.Key, 28)),
			ui.Hex(ui.ColorCost),
			ui.FormatMoney(g.Total, report.Unit),
			ui.Hex(ui.ColorCost),
			ui.Sparkline(g.Amounts(), 12),
		)
	}

	fmt.Fprintf(&sb, "\n [%s]total %s over the last 7 days[-]",
		ui.Hex(ui.ColorMuted), ui.FormatMoney(report.Total, report.Unit))
	if report.Forecast > 0 {
		fmt.Fprintf(&sb, "\n [%s]forecast %s by month end[-]",
			ui.Hex(ui.ColorMuted), ui.FormatMoney(report.Forecast, report.Unit))
	}
	if report.Estimated {
		fmt.Fprintf(&sb, "\n [%s]estimated, not billed spend[-]", ui.Hex(ui.ColorWarn))
	}
	p.spend.SetText(sb.String())
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
