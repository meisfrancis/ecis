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

// NewEvents lists service events, newest first. With no service it merges the
// events of every service in the cluster, which is the fastest way to see what
// the scheduler is unhappy about.
func NewEvents(app *ui.App, cluster, service string) *Browser {
	columns := []ui.Column{
		colA("AGE"),
		col("SERVICE"),
		colE("MESSAGE"),
		colW("TIME"),
	}

	b := NewBrowser(app, "events", "Events", columns, func(ctx context.Context) ([]ui.Row, error) {
		events, err := app.Client().Events(ctx, cluster, service)
		if err != nil {
			return nil, err
		}

		rows := make([]ui.Row, 0, len(events))
		for _, e := range events {
			var r rowBuilder
			r.add(awsx.Since(e.CreatedAt)).
				add(e.Service).
				addC(e.Message, eventColor(e.Message)).
				add(e.CreatedAt.Local().Format("2006-01-02 15:04:05"))
			rows = append(rows, r.build(e.ID, e))
		}
		return rows, nil
	})

	scope := cluster
	if service != "" {
		scope = service
	}
	b.Scope = func() string { return scope }
	b.SortKeys = map[rune]string{'A': "AGE", 'N': "SERVICE"}

	b.OnEnter = func(row ui.Row) {
		e, ok := row.Ref.(model.Event)
		if !ok {
			return
		}
		d := ui.NewDetails(app, "event", e.Service)
		d.SetContent(describe("Service event", []section{
			{Name: "Event", Fields: []field{
				{"Service", e.Service},
				{"Time", formatTime(e.CreatedAt)},
				{"Age", awsx.Since(e.CreatedAt)},
				{"ID", e.ID},
			}},
			{Name: "Message", Lines: wrapText(e.Message, 100)},
		}))
		app.Push(d)
	}
	return b
}

// eventColor highlights the event messages that signal trouble. ECS event text
// is free-form, so this matches on the phrases the scheduler actually emits.
func eventColor(msg string) tcell.Color {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "unable to") ||
		strings.Contains(lower, "failed") ||
		strings.Contains(lower, "unhealthy") ||
		strings.Contains(lower, "error"):
		return ui.ColorError
	case strings.Contains(lower, "deregister") ||
		strings.Contains(lower, "draining") ||
		strings.Contains(lower, "stopping"):
		return ui.ColorWarn
	case strings.Contains(lower, "has reached a steady state") ||
		strings.Contains(lower, "registered") ||
		strings.Contains(lower, "has started"):
		return ui.ColorOK
	default:
		return ui.ColorForeground
	}
}

// wrapText breaks a long message into lines at word boundaries.
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var (
		lines []string
		line  strings.Builder
	)
	for _, w := range words {
		if line.Len() > 0 && line.Len()+1+len(w) > width {
			lines = append(lines, line.String())
			line.Reset()
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(w)
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	return lines
}

// NewDeployments lists the deployments of a service, which is where a stuck
// rollout shows itself.
func NewDeployments(app *ui.App, cluster, service string) *Browser {
	columns := []ui.Column{
		colE("ID"),
		col("STATUS"),
		colE("TASK-DEF"),
		colN("DESIRED"),
		colN("RUNNING"),
		colN("PENDING"),
		colN("FAILED"),
		col("ROLLOUT"),
		col("LAUNCH"),
		colE("REASON"),
		colA("AGE"),
	}

	b := NewBrowser(app, "deployments", "Deployments", columns, func(ctx context.Context) ([]ui.Row, error) {
		deployments, err := app.Client().Deployments(ctx, cluster, service)
		if err != nil {
			return nil, err
		}

		rows := make([]ui.Row, 0, len(deployments))
		for _, d := range deployments {
			failedColor := ui.ColorForeground
			if d.Failed > 0 {
				failedColor = ui.ColorError
			}

			var r rowBuilder
			r.add(d.ID).
				addStatus(d.Status).
				add(dash(d.TaskDefinition)).
				addInt(d.Desired).
				addC(strconv.Itoa(d.Running), ratioColor(d.Running, d.Desired)).
				addInt(d.Pending).
				addC(strconv.Itoa(d.Failed), failedColor).
				addStatus(dash(d.RolloutState)).
				add(dash(d.LaunchType)).
				add(dash(d.RolloutReason)).
				add(awsx.Since(d.CreatedAt))
			rows = append(rows, r.build(d.ID, d))
		}
		return rows, nil
	})

	b.Scope = func() string { return service }
	b.SortKeys = map[rune]string{'A': "AGE", 'S': "STATUS", 'R': "RUNNING", 'D': "DESIRED"}
	b.SetHints([]ui.Hint{{Key: "y", Desc: "YAML"}})

	b.OnKey = func(evt *tcell.EventKey) *tcell.EventKey {
		row, ok := b.Selected()
		if !ok {
			return evt
		}
		d, ok := row.Ref.(model.Deployment)
		if !ok {
			return evt
		}
		if evt.Rune() == 'y' {
			app.Push(yamlView(app, "deployment", d.ID, d.Raw))
			return nil
		}
		return evt
	}
	return b
}
