package view

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

// NewServices builds the service list for a cluster.
func NewServices(app *ui.App, cluster string) *Browser {
	columns := []ui.Column{
		colE("NAME"),
		col("STATUS"),
		colN("TASKS"),
		colN("PENDING"),
		colN("CPU"),
		colN("MEM"),
		col("ROLLOUT"),
		col("LAUNCH"),
		colE("TASK-DEF"),
		colW("CPU-RES"),
		colW("MEM-RES"),
		colW("SCHEDULING"),
		colW("PLATFORM"),
		colA("AGE"),
	}

	b := NewBrowser(app, "services", "Services", columns, func(ctx context.Context) ([]ui.Row, error) {
		client := app.Client()
		services, err := client.Services(ctx, cluster)
		if err != nil {
			return nil, err
		}

		names := make([]string, 0, len(services))
		for _, s := range services {
			names = append(names, s.Name)
		}
		samples, _ := client.ServiceSamples(ctx, cluster, names, app.Config().MetricPeriod())

		rows := make([]ui.Row, 0, len(services))
		for _, s := range services {
			sample := samples[s.Name]
			var r rowBuilder
			r.add(s.Name).
				addStatus(s.Status).
				addC(ratio(s.Running, s.Desired), ratioColor(s.Running, s.Desired)).
				addInt(s.Pending).
				addPct(sample.CPU).
				addPct(sample.Memory).
				addStatus(rolloutText(s)).
				add(dash(s.LaunchType)).
				add(dash(s.TaskDefinition)).
				add(dash(s.CPU)).
				add(dash(s.Memory)).
				add(dash(s.Scheduling)).
				add(dash(s.PlatformVer)).
				add(awsx.Since(s.CreatedAt))
			rows = append(rows, r.build(s.ARN, s))
		}
		return rows, nil
	})

	b.Scope = func() string { return cluster }
	b.SortKeys = map[rune]string{
		'N': "NAME", 'S': "STATUS", 'C': "CPU", 'M': "MEM",
		'A': "AGE", 'T': "TASKS", 'P': "PENDING", 'L': "LAUNCH",
	}
	b.SetHints([]ui.Hint{
		{Key: "l", Desc: "Logs"},
		{Key: "m", Desc: "Metrics"},
		{Key: "$", Desc: "Cost"},
		{Key: "e", Desc: "Events"},
		{Key: "p", Desc: "Deployments"},
		{Key: "s", Desc: "Scale"},
		{Key: "r", Desc: "Restart"},
		{Key: "d", Desc: "Describe"},
		{Key: "y", Desc: "YAML"},
	})

	b.OnEnter = func(row ui.Row) {
		s, ok := row.Ref.(model.Service)
		if !ok {
			return
		}
		app.Push(NewTasks(app, awsx.TaskQuery{Cluster: s.Cluster, Service: s.Name}, "tasks", s.Name))
	}

	b.OnKey = func(evt *tcell.EventKey) *tcell.EventKey {
		row, ok := b.Selected()
		if !ok {
			return evt
		}
		s, ok := row.Ref.(model.Service)
		if !ok {
			return evt
		}

		switch evt.Rune() {
		case 'l':
			openLogs(app, serviceLogTarget(s))
			return nil
		case 'm':
			app.Push(NewMetrics(app, awsx.MetricTarget{Kind: "service", Cluster: s.Cluster, Name: s.Name}))
			return nil
		case '$':
			app.Push(NewCost(app, s.Cluster))
			return nil
		case 'e':
			app.Push(NewEvents(app, s.Cluster, s.Name))
			return nil
		case 'p':
			app.Push(NewDeployments(app, s.Cluster, s.Name))
			return nil
		case 's':
			scaleService(app, b, s)
			return nil
		case 'r':
			restartService(app, b, s)
			return nil
		case 'd':
			app.Push(describeService(app, s))
			return nil
		case 'y':
			app.Push(yamlView(app, "service", s.Name, s.Raw))
			return nil
		}
		return evt
	}
	return b
}

// rolloutText renders the deployment state, falling back to a synthesised one
// for services that predate deployment circuit breakers.
func rolloutText(s model.Service) string {
	if s.RolloutState != "" {
		return s.RolloutState
	}
	if s.Deployments > 1 {
		return "IN_PROGRESS"
	}
	if s.Healthy() {
		return "COMPLETED"
	}
	return "PENDING"
}

func scaleService(app *ui.App, b *Browser, s model.Service) {
	if app.ReadOnly() {
		app.Flash().Warn("read-only mode: scaling is disabled")
		return
	}
	app.Input(fmt.Sprintf("Scale %s", s.Name), "Desired count", strconv.Itoa(s.Desired), func(value string) {
		desired, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || desired < 0 {
			app.Flash().Errf("invalid desired count %q", value)
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err := app.Client().Scale(ctx, s.Cluster, s.Name, int32(desired))
			app.QueueUpdateDraw(func() {
				if err != nil {
					app.Flash().Err(err)
					return
				}
				app.Flash().Infof("scaled %s to %d", s.Name, desired)
				b.Refresh()
			})
		}()
	})
}

func restartService(app *ui.App, b *Browser, s model.Service) {
	if app.ReadOnly() {
		app.Flash().Warn("read-only mode: restart is disabled")
		return
	}
	msg := fmt.Sprintf("Force a new deployment of %s?\n\nECS will replace all %d tasks using the current\ntask definition, honouring the deployment configuration.",
		s.Name, s.Running)
	app.Confirm("Restart service", msg, func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			err := app.Client().Restart(ctx, s.Cluster, s.Name)
			app.QueueUpdateDraw(func() {
				if err != nil {
					app.Flash().Err(err)
					return
				}
				app.Flash().Infof("forced a new deployment of %s", s.Name)
				b.Refresh()
			})
		}()
	})
}

func describeService(app *ui.App, s model.Service) ui.Component {
	events := make([]string, 0, len(s.Events))
	for i, e := range s.Events {
		if i >= 10 {
			break
		}
		events = append(events, fmt.Sprintf("%s  %s", e.CreatedAt.Local().Format("2006-01-02 15:04:05"), e.Message))
	}

	text := describe("Service "+s.Name, []section{
		{Name: "Overview", Fields: []field{
			{"Name", s.Name},
			{"ARN", s.ARN},
			{"Cluster", s.Cluster},
			{"Status", s.Status},
			{"Launch type", s.LaunchType},
			{"Platform version", s.PlatformVer},
			{"Scheduling strategy", s.Scheduling},
			{"Created", s.CreatedAt.Local().Format(time.RFC3339)},
		}},
		{Name: "Scale", Fields: []field{
			{"Desired", strconv.Itoa(s.Desired)},
			{"Running", strconv.Itoa(s.Running)},
			{"Pending", strconv.Itoa(s.Pending)},
			{"Deployments", strconv.Itoa(s.Deployments)},
		}},
		{Name: "Deployment", Fields: []field{
			{"Task definition", s.TaskDefinition},
			{"Reserved CPU", s.CPU},
			{"Reserved memory", s.Memory},
			{"Rollout state", s.RolloutState},
			{"Rollout reason", s.RolloutReason},
			{"Last updated", formatTime(s.UpdatedAt)},
		}},
		{Name: "Tags", Fields: tagFields(s.Tags)},
		{Name: "Recent events", Lines: events},
	})

	d := ui.NewDetails(app, "describe", s.Name)
	d.SetContent(text)
	return d
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format(time.RFC3339)
}
