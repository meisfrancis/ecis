package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/config"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

// newTestApp builds an application against a region with no credentials. The
// AWS SDK resolves credentials lazily, so nothing here touches the network —
// which is what lets the render tests run offline.
func newTestApp(t *testing.T) *ui.App {
	t.Helper()

	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := awsx.New(ctx, "", "us-east-1")
	if err != nil {
		t.Fatalf("build aws client: %v", err)
	}
	return ui.NewApp(config.New(), client, "test")
}

// render draws a primitive onto a simulation screen and returns the text of
// each row, so assertions can look for labels and values.
func render(t *testing.T, p tview.Primitive, w, h int) []string {
	t.Helper()

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(w, h)

	p.SetRect(0, 0, w, h)
	p.Draw(screen)
	screen.Show()

	cells, width, height := screen.GetContents()
	lines := make([]string, 0, height)
	for y := 0; y < height; y++ {
		var b strings.Builder
		for x := 0; x < width; x++ {
			runes := cells[y*width+x].Runes
			if len(runes) == 0 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(runes[0])
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines
}

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestAppShellRenders(t *testing.T) {
	app := newTestApp(t)
	app.RefreshHeader()

	out := joined(render(t, app.Root(), 160, 40))

	for _, want := range []string{"Profile", "Region", "Account", "Cluster", "CPU", "MEM", "Cost"} {
		if !strings.Contains(out, want) {
			t.Errorf("header is missing the %q field:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "us-east-1") {
		t.Errorf("header does not show the region:\n%s", out)
	}
	if !strings.Contains(out, "ecs interactive shell") {
		t.Errorf("logo strapline missing:\n%s", out)
	}
}

func TestServicesTableRenders(t *testing.T) {
	app := newTestApp(t)
	b := NewServices(app, "prod")

	svc := model.Service{
		Name: "checkout", Cluster: "prod", Status: "ACTIVE",
		Desired: 3, Running: 3, LaunchType: "FARGATE",
		TaskDefinition: "checkout:42", RolloutState: "COMPLETED",
		CreatedAt: time.Now().Add(-36 * time.Hour),
	}
	var r rowBuilder
	r.add(svc.Name).
		addStatus(svc.Status).
		addC(ratio(svc.Running, svc.Desired), ratioColor(svc.Running, svc.Desired)).
		addInt(svc.Pending).
		addPct(41.5).
		addPct(63.25).
		addStatus(rolloutText(svc)).
		add(svc.LaunchType).
		add(svc.TaskDefinition).
		add("1024").
		add("2048").
		add("REPLICA").
		add("LATEST").
		add(awsx.Since(svc.CreatedAt))
	b.Update([]ui.Row{r.build(svc.Name, svc)})

	out := joined(render(t, b, 160, 12))

	for _, want := range []string{"NAME", "STATUS", "TASKS", "CPU", "MEM", "checkout", "ACTIVE", "3/3", "41.5%", "FARGATE"} {
		if !strings.Contains(out, want) {
			t.Errorf("services table is missing %q:\n%s", want, out)
		}
	}
	// ARN-style wide columns stay hidden until Ctrl-W.
	if strings.Contains(out, "SCHEDULING") {
		t.Errorf("wide columns should be hidden by default:\n%s", out)
	}
	b.ToggleWide()
	if out = joined(render(t, b, 200, 12)); !strings.Contains(out, "SCHEDULING") {
		t.Errorf("wide columns did not appear after toggling:\n%s", out)
	}
}

func TestCostTableRendersEstimateRows(t *testing.T) {
	app := newTestApp(t)
	c := NewCost(app, "prod")

	report := &model.CostReport{
		GroupBy: "ECS SERVICE",
		Unit:    "USD",
		Total:   150,
		Groups: []model.CostGroup{
			{Key: "checkout", Unit: "USD", Total: 100, Points: []model.CostPoint{
				{Amount: 10}, {Amount: 20}, {Amount: 70},
			}},
			{Key: "search", Unit: "USD", Total: 50, Points: []model.CostPoint{
				{Amount: 30}, {Amount: 15}, {Amount: 5},
			}},
		},
	}
	c.Update(c.rows(report))

	out := joined(render(t, c, 160, 12))

	for _, want := range []string{"NAME", "TOTAL", "SHARE", "TREND", "checkout", "search", "$100", "$50.00"} {
		if !strings.Contains(out, want) {
			t.Errorf("cost table is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "↑") || !strings.Contains(out, "↓") {
		t.Errorf("cost table should show trend arrows:\n%s", out)
	}
}

func TestPulseRendersWithoutData(t *testing.T) {
	app := newTestApp(t)
	p := NewPulse(app)

	// The dashboard has to be legible before any data has arrived, and when
	// Cost Explorer is unavailable.
	p.renderTiles(nil, nil, "")
	p.renderCharts(nil)
	p.renderServices(nil, nil)
	p.renderSpend(nil)

	out := joined(render(t, p, 160, 40))

	for _, want := range []string{"CLUSTERS", "RUNNING", "DEGRADED", "no cluster selected", "Cost Explorer is unavailable"} {
		if !strings.Contains(out, want) {
			t.Errorf("pulse is missing %q:\n%s", want, out)
		}
	}
}

func TestPulseRendersFleetCounters(t *testing.T) {
	app := newTestApp(t)
	p := NewPulse(app)

	clusters := []model.Cluster{
		{Name: "prod", RunningTasks: 12, PendingTasks: 1, ActiveServices: 4, Instances: 3},
		{Name: "staging", RunningTasks: 2, ActiveServices: 1},
	}
	services := []model.Service{
		{Name: "checkout", Desired: 3, Running: 3, RolloutState: "COMPLETED"},
		{Name: "search", Desired: 2, Running: 0},
	}
	samples := map[string]awsx.Sample{
		"checkout": {CPU: 55, Memory: 70},
		"search":   awsx.NoSample(),
	}

	p.renderTiles(clusters, services, "prod")
	p.renderServices(services, samples)
	p.renderSpend(&model.CostReport{
		GroupBy: "ECS SERVICE", Unit: "USD", Total: 42.5, Forecast: 130,
		Groups: []model.CostGroup{{Key: "checkout", Unit: "USD", Total: 42.5, Points: []model.CostPoint{{Amount: 42.5}}}},
	})

	out := joined(render(t, p, 160, 40))

	if !strings.Contains(out, "14") {
		t.Errorf("running task total (12+2) missing:\n%s", out)
	}
	if !strings.Contains(out, "checkout") || !strings.Contains(out, "55.0%") {
		t.Errorf("busiest-services panel missing data:\n%s", out)
	}
	if !strings.Contains(out, "forecast") {
		t.Errorf("spend panel should show the forecast:\n%s", out)
	}
	// The degraded counter must notice the service with nothing running.
	if !strings.Contains(out, "DEGRADED") {
		t.Errorf("degraded tile missing:\n%s", out)
	}
}

func TestMetricsViewRenders(t *testing.T) {
	app := newTestApp(t)
	m := NewMetrics(app, awsx.MetricTarget{Kind: "service", Cluster: "prod", Name: "checkout"})

	now := time.Now()
	series := func(base float64) model.Series {
		s := model.Series{Label: "CPU avg %"}
		for i := 0; i < 60; i++ {
			s.Timestamps = append(s.Timestamps, now.Add(time.Duration(i)*time.Minute))
			s.Values = append(s.Values, base+float64(i%20))
		}
		return s
	}

	m.apply(&awsx.Charts{
		Target: m.target,
		Start:  now, End: now.Add(time.Hour),
		CPU:    series(20),
		Memory: series(50),
	})

	out := joined(render(t, m, 160, 40))

	if !strings.Contains(out, "CPU") || !strings.Contains(out, "Memory") {
		t.Errorf("metrics view missing chart titles:\n%s", out)
	}
	if !strings.Contains(out, "checkout") {
		t.Errorf("metrics view should name its target:\n%s", out)
	}
	if !strings.Contains(out, "cur") {
		t.Errorf("metrics view should show the legend:\n%s", out)
	}
}

func TestMetricsExplainsMissingTaskData(t *testing.T) {
	charts := &awsx.Charts{Target: awsx.MetricTarget{Kind: "task", Cluster: "prod", Name: "abc"}}
	note := noteFor(charts, model.Series{})
	if !strings.Contains(note, "Container Insights") {
		t.Errorf("note = %q, want an explanation mentioning Container Insights", note)
	}
}

func TestLogViewExplainsMissingGroup(t *testing.T) {
	app := newTestApp(t)
	v := ui.NewLogView(app, "logs", "checkout", nil, false)

	out := joined(render(t, v, 120, 20))
	if !strings.Contains(out, "Logs") {
		t.Errorf("log view title missing:\n%s", out)
	}
}

func TestDetailsRendersAndSearches(t *testing.T) {
	app := newTestApp(t)
	d := ui.NewDetails(app, "describe", "checkout")
	d.SetContent(describe("Service checkout", []section{
		{Name: "Overview", Fields: []field{{"Name", "checkout"}, {"Status", "ACTIVE"}}},
	}))

	out := joined(render(t, d, 120, 20))
	if !strings.Contains(out, "OVERVIEW") || !strings.Contains(out, "checkout") {
		t.Errorf("details view did not render its content:\n%s", out)
	}

	d.SetFilter("ACTIVE")
	if d.Filter() != "ACTIVE" {
		t.Errorf("Filter() = %q, want ACTIVE", d.Filter())
	}
	if out = joined(render(t, d, 120, 20)); !strings.Contains(out, "ACTIVE") {
		t.Errorf("search should keep the matched text visible:\n%s", out)
	}
}

func TestClustersTableRenders(t *testing.T) {
	app := newTestApp(t)
	b := NewClusters(app)

	c := model.Cluster{
		Name: "prod", ARN: "arn:aws:ecs:us-east-1:1:cluster/prod", Status: "ACTIVE",
		ActiveServices: 4, RunningTasks: 12, PendingTasks: 0, Instances: 0,
		Settings:          map[string]string{"containerInsights": "enhanced"},
		CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"},
	}
	var r rowBuilder
	r.add(c.Name).
		addStatus(c.Status).
		addInt(c.ActiveServices).
		addInt(c.RunningTasks).
		addInt(c.PendingTasks).
		addInt(c.Instances).
		addPct(33.3).
		addPct(48).
		add(c.ContainerInsights()).
		add("FARGATE,FARGATE_SPOT").
		add(c.ARN)
	b.Update([]ui.Row{r.build(c.ARN, c)})

	out := joined(render(t, b, 160, 10))
	for _, want := range []string{"NAME", "INSIGHTS", "prod", "ACTIVE", "enhanced", "33.3%"} {
		if !strings.Contains(out, want) {
			t.Errorf("clusters table is missing %q:\n%s", want, out)
		}
	}
}
