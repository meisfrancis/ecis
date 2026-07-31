package view

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/config"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

// costSparkWidth is how many buckets the inline trend chart shows.
const costSparkWidth = 16

// Cost is the spend view. It reads Cost Explorer for actual spend and can fall
// back to a local Fargate estimate, which is what makes it useful on accounts
// that have never activated the ECS cost allocation tags.
type Cost struct {
	*Browser

	app     *ui.App
	cluster string

	mu          sync.Mutex
	groupBy     awsx.GroupBy
	granularity string
	wide        bool
	estimate    bool
	report      *model.CostReport
}

// NewCost builds the cost view, scoped to a cluster when one is given.
func NewCost(app *ui.App, cluster string) *Cost {
	c := &Cost{
		app:         app,
		cluster:     cluster,
		groupBy:     awsx.GroupByService,
		granularity: "DAILY",
	}

	columns := []ui.Column{
		colE("NAME"),
		colN("TOTAL"),
		colN("SHARE"),
		colN("LATEST"),
		colN("7-BUCKET"),
		col("TREND"),
		col("CHART"),
	}

	c.Browser = NewBrowser(app, "cost", "Cost", columns, c.load)
	// Cost Explorer charges per request, so this view refreshes far more slowly
	// than the ECS tables. Ctrl-R forces a reload when you want one now.
	c.Browser.Interval = app.Config().CostInterval()
	c.Browser.Scope = c.scope
	c.Browser.SortKeys = map[rune]string{
		'N': "NAME", 'T': "TOTAL", 'L': "LATEST", 'P': "SHARE",
	}
	c.Browser.SetHints([]ui.Hint{
		{Key: "b", Desc: "Breakdown"},
		{Key: "n", Desc: "Granularity"},
		{Key: "w", Desc: "Scope"},
		{Key: "e", Desc: "Estimate"},
		{Key: "ctrl-r", Desc: "Reload"},
	})
	c.Browser.OnKey = c.keys
	c.Browser.OnEnter = c.drillDown
	return c
}

func (c *Cost) scope() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	parts := []string{c.groupBy.Label()}
	if c.cluster != "" {
		parts = append(parts, c.cluster)
	}
	if c.estimate {
		parts = append(parts, "estimated")
	} else {
		parts = append(parts, strings.ToLower(c.granularity))
	}
	return strings.Join(parts, " · ")
}

func (c *Cost) load(ctx context.Context) ([]ui.Row, error) {
	c.mu.Lock()
	groupBy, granularity, wide, estimate := c.groupBy, c.granularity, c.wide, c.estimate
	c.mu.Unlock()

	cfg := c.app.Config()
	var (
		report *model.CostReport
		err    error
	)

	if estimate {
		report, err = c.estimateReport(ctx, groupBy)
	} else {
		report, err = c.app.Client().Cost(ctx, awsx.CostOptions{
			GroupBy:        groupBy,
			Granularity:    granularity,
			Days:           cfg.Cost.LookbackDays,
			Cluster:        c.cluster,
			ClusterTagKey:  cfg.Cost.ClusterTagKey,
			ServiceTagKey:  cfg.Cost.ServiceTagKey,
			Wide:           wide,
			IncludeCredits: cfg.Cost.IncludeCredits,
		})
		// An empty tag-based breakdown almost always means the cost allocation
		// tag was never activated. Falling back to the estimator keeps the view
		// informative instead of showing a blank table.
		if err == nil && cfg.Cost.Estimate && isTagBreakdown(groupBy) && untagged(report) {
			if est, estErr := c.estimateReport(ctx, groupBy); estErr == nil && len(est.Groups) > 0 {
				est.Note = fmt.Sprintf("Cost Explorer returned no %s breakdown — showing a local estimate. "+
					"Activate the %q cost allocation tag in Billing for real figures.",
					groupBy.Label(), tagKeyFor(groupBy, cfg))
				report = est
			}
		}
	}
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.report = report
	c.mu.Unlock()

	return c.rows(report), nil
}

func (c *Cost) rows(report *model.CostReport) []ui.Row {
	rows := make([]ui.Row, 0, len(report.Groups))
	for _, g := range report.Groups {
		share := 0.0
		if report.Total > 0 {
			share = g.Total / report.Total * 100
		}
		trend, trendColor := trendText(g.Trend())

		var r rowBuilder
		r.add(g.Key).
			addMoney(g.Total, report.Unit).
			addRaw(fmt.Sprintf("%s %5.1f%%", ui.Gauge(share, 10), share)).
			addMoney(g.Latest(), report.Unit).
			addMoney(g.Window(7), report.Unit).
			addC(trend, trendColor).
			addRaw(ui.Colorize(ui.Sparkline(g.Amounts(), costSparkWidth), ui.ColorCost))
		rows = append(rows, r.build(g.Key, g))
	}
	return rows
}

// estimateReport projects spend from the sizes of the running Fargate tasks.
func (c *Cost) estimateReport(ctx context.Context, groupBy awsx.GroupBy) (*model.CostReport, error) {
	clusters := []string{c.cluster}
	if c.cluster == "" {
		list, err := c.app.Client().Clusters(ctx)
		if err != nil {
			return nil, err
		}
		clusters = clusters[:0]
		for _, cl := range list {
			clusters = append(clusters, cl.Name)
		}
	}

	var tasks []model.Task
	for _, cluster := range clusters {
		if cluster == "" {
			continue
		}
		found, err := c.app.Client().Tasks(ctx, awsx.TaskQuery{Cluster: cluster, DesiredStatus: "RUNNING"})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, found...)
	}

	return awsx.EstimateReport(tasks, c.app.Region(), rateOverrides(c.app.Config()), groupBy), nil
}

// rateOverrides converts the configured price overrides into the pricing type.
func rateOverrides(cfg *config.Config) map[string]awsx.FargateRate {
	if len(cfg.Cost.Rates) == 0 {
		return nil
	}
	out := make(map[string]awsx.FargateRate, len(cfg.Cost.Rates))
	for region, r := range cfg.Cost.Rates {
		out[region] = awsx.FargateRate{VCPUHour: r.VCPUHour, GBHour: r.GBHour}
	}
	return out
}

func isTagBreakdown(g awsx.GroupBy) bool {
	return g == awsx.GroupByCluster || g == awsx.GroupByService
}

func tagKeyFor(g awsx.GroupBy, cfg *config.Config) string {
	if g == awsx.GroupByCluster {
		return cfg.Cost.ClusterTagKey
	}
	return cfg.Cost.ServiceTagKey
}

// untagged reports whether a breakdown carries nothing but the "(untagged)"
// bucket, which is what an unactivated cost allocation tag looks like.
func untagged(r *model.CostReport) bool {
	if r == nil || len(r.Groups) == 0 {
		return true
	}
	for _, g := range r.Groups {
		if g.Key != "(untagged)" && g.Total > 0 {
			return false
		}
	}
	return true
}

// Title implements ui.Component, folding the totals into the heading.
func (c *Cost) Title() string { return "Cost" }

// Hints implements ui.Component.
func (c *Cost) Hints() []ui.Hint { return c.Browser.Hints() }

func (c *Cost) drillDown(row ui.Row) {
	g, ok := row.Ref.(model.CostGroup)
	if !ok {
		return
	}

	c.mu.Lock()
	report := c.report
	c.mu.Unlock()
	if report == nil {
		return
	}

	lines := make([]string, 0, len(g.Points))
	for i := len(g.Points) - 1; i >= 0; i-- {
		p := g.Points[i]
		lines = append(lines, fmt.Sprintf("%s  %12s", p.Start.Format("2006-01-02"), ui.FormatMoney(p.Amount, g.Unit)))
	}

	fields := []field{
		{"Breakdown", report.GroupBy},
		{"Window", fmt.Sprintf("%s → %s", report.Start.Format("2006-01-02"), report.End.Format("2006-01-02"))},
		{"Total", ui.FormatMoney(g.Total, g.Unit)},
		{"Latest bucket", ui.FormatMoney(g.Latest(), g.Unit)},
		{"Last 7 buckets", ui.FormatMoney(g.Window(7), g.Unit)},
		{"Change vs previous", fmt.Sprintf("%.1f%%", g.Trend())},
	}
	if g.Estimated {
		fields = append(fields, field{"Source", "local estimate — not billed spend"})
	} else {
		fields = append(fields, field{"Source", "AWS Cost Explorer (unblended)"})
	}

	sections := []section{
		{Name: g.Key, Fields: fields},
		{Name: "History", Lines: lines},
	}
	if report.Note != "" {
		sections = append(sections, section{Name: "Note", Lines: wrapText(report.Note, 90)})
	}

	d := ui.NewDetails(c.app, "cost", g.Key)
	d.SetContent(describe("Cost — "+g.Key, sections))
	c.app.Push(d)
}

func (c *Cost) keys(evt *tcell.EventKey) *tcell.EventKey {
	switch evt.Rune() {
	case 'b':
		c.cycleGroupBy()
		return nil
	case 'n':
		c.mu.Lock()
		if c.granularity == "DAILY" {
			c.granularity = "MONTHLY"
		} else {
			c.granularity = "DAILY"
		}
		granularity := c.granularity
		c.mu.Unlock()

		c.app.Flash().Infof("granularity %s", strings.ToLower(granularity))
		c.Refresh()
		return nil
	case 'w':
		c.mu.Lock()
		c.wide = !c.wide
		wide := c.wide
		c.mu.Unlock()

		if wide {
			c.app.Flash().Info("scope: all container spend (ECS, EC2 capacity, ECR)")
		} else {
			c.app.Flash().Info("scope: Amazon ECS only")
		}
		c.Refresh()
		return nil
	case 'e':
		c.mu.Lock()
		c.estimate = !c.estimate
		estimate := c.estimate
		c.mu.Unlock()

		if estimate {
			c.app.Flash().Warn("showing a local Fargate estimate, not billed spend")
		} else {
			c.app.Flash().Info("showing AWS Cost Explorer spend")
		}
		c.Refresh()
		return nil
	}
	return evt
}

func (c *Cost) cycleGroupBy() {
	c.mu.Lock()
	next := awsx.GroupByCycle[0]
	for i, g := range awsx.GroupByCycle {
		if g == c.groupBy {
			next = awsx.GroupByCycle[(i+1)%len(awsx.GroupByCycle)]
			break
		}
	}
	c.groupBy = next
	c.mu.Unlock()

	c.app.Flash().Infof("breakdown by %s", strings.ToLower(next.Label()))
	c.Refresh()
}

// Summary renders the totals line the pulse dashboard shows.
func (c *Cost) Summary() string {
	c.mu.Lock()
	report := c.report
	c.mu.Unlock()
	if report == nil {
		return ""
	}

	out := fmt.Sprintf("total %s over %s",
		ui.FormatMoney(report.Total, report.Unit),
		awsx.Duration(report.End.Sub(report.Start)))
	if report.Forecast > 0 {
		out += fmt.Sprintf(" · forecast %s to month end", ui.FormatMoney(report.Forecast, report.Unit))
	}
	return out
}
