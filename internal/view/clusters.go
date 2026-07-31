package view

import (
	"context"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

// NewClusters builds the cluster list, which is the root view: picking a
// cluster here is the ECS equivalent of picking a namespace in k9s.
func NewClusters(app *ui.App) *Browser {
	columns := []ui.Column{
		colE("NAME"),
		col("STATUS"),
		colN("SERVICES"),
		colN("RUNNING"),
		colN("PENDING"),
		colN("INSTANCES"),
		colN("CPU"),
		colN("MEM"),
		col("INSIGHTS"),
		colW("PROVIDERS"),
		colW("ARN"),
	}

	b := NewBrowser(app, "clusters", "Clusters", columns, func(ctx context.Context) ([]ui.Row, error) {
		client := app.Client()
		clusters, err := client.Clusters(ctx)
		if err != nil {
			return nil, err
		}

		names := make([]string, 0, len(clusters))
		for _, c := range clusters {
			names = append(names, c.Name)
		}
		// Utilization is best-effort: a cluster without Container Insights
		// simply shows dashes rather than failing the whole listing.
		samples, _ := client.ClusterSamples(ctx, names, app.Config().MetricPeriod())

		rows := make([]ui.Row, 0, len(clusters))
		for _, c := range clusters {
			s := samples[c.Name]
			var r rowBuilder
			r.add(c.Name).
				addStatus(c.Status).
				addInt(c.ActiveServices).
				addC(strconv.Itoa(c.RunningTasks), ratioColor(c.RunningTasks, c.RunningTasks)).
				addInt(c.PendingTasks).
				addInt(c.Instances).
				addPct(s.CPU).
				addPct(s.Memory).
				addC(c.ContainerInsights(), insightsColor(c)).
				add(strings.Join(c.CapacityProviders, ",")).
				add(c.ARN)
			rows = append(rows, r.build(c.ARN, c))
		}
		return rows, nil
	})

	b.SortKeys = map[rune]string{
		'N': "NAME", 'S': "STATUS", 'C': "CPU", 'M': "MEM",
		'R': "RUNNING", 'P': "PENDING", 'V': "SERVICES",
	}
	b.SetHints([]ui.Hint{
		{Key: "t", Desc: "Tasks"},
		{Key: "i", Desc: "Instances"},
		{Key: "c", Desc: "Capacity"},
		{Key: "m", Desc: "Metrics"},
		{Key: "$", Desc: "Cost"},
		{Key: "d", Desc: "Describe"},
		{Key: "y", Desc: "YAML"},
	})

	b.OnEnter = func(row ui.Row) {
		c, ok := row.Ref.(model.Cluster)
		if !ok {
			return
		}
		app.SetCluster(c.Name)
		app.Push(NewServices(app, c.Name))
	}

	b.OnKey = func(evt *tcell.EventKey) *tcell.EventKey {
		row, ok := b.Selected()
		if !ok {
			return evt
		}
		c, ok := row.Ref.(model.Cluster)
		if !ok {
			return evt
		}

		switch evt.Rune() {
		case 't':
			app.SetCluster(c.Name)
			app.Push(NewTasks(app, awsx.TaskQuery{Cluster: c.Name}, "tasks", c.Name))
			return nil
		case 'i':
			app.SetCluster(c.Name)
			app.Push(NewInstances(app, c.Name))
			return nil
		case 'c':
			app.SetCluster(c.Name)
			app.Push(NewCapacityProviders(app, c.Name))
			return nil
		case 'm':
			app.SetCluster(c.Name)
			app.Push(NewMetrics(app, awsx.MetricTarget{Kind: "cluster", Cluster: c.Name}))
			return nil
		case '$':
			app.SetCluster(c.Name)
			app.Push(NewCost(app, c.Name))
			return nil
		case 'd':
			app.Push(describeCluster(app, c))
			return nil
		case 'y':
			app.Push(yamlView(app, "cluster", c.Name, c.Raw))
			return nil
		}
		return evt
	}
	return b
}

func insightsColor(c model.Cluster) tcell.Color {
	if awsx.ContainerInsightsEnabled(c) {
		return ui.ColorOK
	}
	return ui.ColorMuted
}

func describeCluster(app *ui.App, c model.Cluster) ui.Component {
	stats := make([]field, 0, len(c.Statistics))
	for _, f := range tagFields(c.Statistics) {
		stats = append(stats, f)
	}

	text := describe("Cluster "+c.Name, []section{
		{Name: "Overview", Fields: []field{
			{"Name", c.Name},
			{"ARN", c.ARN},
			{"Status", c.Status},
			{"Container Insights", c.ContainerInsights()},
			{"Capacity providers", strings.Join(c.CapacityProviders, ", ")},
		}},
		{Name: "Workload", Fields: []field{
			{"Active services", strconv.Itoa(c.ActiveServices)},
			{"Running tasks", strconv.Itoa(c.RunningTasks)},
			{"Pending tasks", strconv.Itoa(c.PendingTasks)},
			{"Container instances", strconv.Itoa(c.Instances)},
		}},
		{Name: "Statistics", Fields: stats},
		{Name: "Tags", Fields: tagFields(c.Tags)},
	})

	d := ui.NewDetails(app, "describe", c.Name)
	d.SetContent(text)
	return d
}

// yamlView renders any raw AWS shape as YAML.
func yamlView(app *ui.App, kind, subject string, raw any) ui.Component {
	d := ui.NewDetails(app, "yaml", subject)
	d.SetContent(toYAML(raw))
	return d
}
