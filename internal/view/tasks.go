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

// NewTasks builds a task list. The query decides the scope: a whole cluster, a
// single service, one task definition family, or one container instance.
func NewTasks(app *ui.App, q awsx.TaskQuery, name, scope string) *Browser {
	columns := []ui.Column{
		colE("ID"),
		col("STATUS"),
		col("HEALTH"),
		colN("CPU"),
		colN("MEM"),
		colE("TASK-DEF"),
		col("SERVICE"),
		col("LAUNCH"),
		colW("CPU-RES"),
		colW("MEM-RES"),
		colW("IP"),
		colW("AZ"),
		colW("EXEC"),
		colW("STOPPED-REASON"),
		colA("AGE"),
	}

	b := NewBrowser(app, name, "Tasks", columns, func(ctx context.Context) ([]ui.Row, error) {
		client := app.Client()
		tasks, err := client.Tasks(ctx, q)
		if err != nil {
			return nil, err
		}

		ids := make([]string, 0, len(tasks))
		for _, t := range tasks {
			if strings.EqualFold(t.LastStatus, "RUNNING") {
				ids = append(ids, t.ID)
			}
		}
		// Per-task utilization needs Container Insights with enhanced
		// observability; without it these come back empty and render as dashes.
		samples, _ := client.TaskSamples(ctx, q.Cluster, ids, app.Config().MetricPeriod())

		rows := make([]ui.Row, 0, len(tasks))
		for _, t := range tasks {
			s := samples[t.ID]
			var r rowBuilder
			r.add(shortID(t.ID)).
				addStatus(t.LastStatus).
				addStatus(dash(t.HealthStatus)).
				addPct(s.CPU).
				addPct(s.Memory).
				add(dash(t.TaskDefinition)).
				add(dash(t.Service)).
				add(dash(launchText(t))).
				add(dash(t.CPU)).
				add(dash(t.Memory)).
				add(dash(t.ConnectivityIP)).
				add(dash(t.AZ)).
				addC(boolText(t.ExecEnabled), ui.StatusColor(boolText(t.ExecEnabled))).
				add(dash(t.StoppedReason)).
				add(awsx.Duration(t.Age()))
			rows = append(rows, r.build(t.ARN, t))
		}
		return rows, nil
	})

	b.Scope = func() string { return scope }
	b.SortKeys = map[rune]string{
		'N': "ID", 'S': "STATUS", 'C': "CPU", 'M': "MEM",
		'A': "AGE", 'H': "HEALTH", 'L': "LAUNCH", 'T': "TASK-DEF",
	}
	b.SetHints([]ui.Hint{
		{Key: "l", Desc: "Logs"},
		{Key: "s", Desc: "Shell"},
		{Key: "m", Desc: "Metrics"},
		{Key: "d", Desc: "Describe"},
		{Key: "y", Desc: "YAML"},
		{Key: "ctrl-d", Desc: "Stop task"},
	})

	b.OnEnter = func(row ui.Row) {
		t, ok := row.Ref.(model.Task)
		if !ok {
			return
		}
		app.Push(NewContainers(app, t))
	}

	b.OnKey = func(evt *tcell.EventKey) *tcell.EventKey {
		row, ok := b.Selected()
		if !ok {
			return evt
		}
		t, ok := row.Ref.(model.Task)
		if !ok {
			return evt
		}

		if evt.Key() == tcell.KeyCtrlD {
			stopTasks(app, b)
			return nil
		}

		switch evt.Rune() {
		case 'l':
			openLogs(app, taskLogTarget(t))
			return nil
		case 's':
			execInto(app, t, "")
			return nil
		case 'm':
			app.Push(NewMetrics(app, awsx.MetricTarget{Kind: "task", Cluster: t.Cluster, Name: t.ID}))
			return nil
		case 'd':
			app.Push(describeTask(app, t))
			return nil
		case 'y':
			app.Push(yamlView(app, "task", shortID(t.ID), t.Raw))
			return nil
		}
		return evt
	}
	return b
}

// launchText prefers the capacity provider name, which is more informative than
// a bare launch type on clusters that mix Fargate and Fargate Spot.
func launchText(t model.Task) string {
	if t.CapacityProv != "" {
		return t.CapacityProv
	}
	return t.LaunchType
}

// shortID trims a task id to the first segment, which is unique enough to read
// while staying narrow.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// stopTasks stops every marked task, or the selected one when nothing is marked.
func stopTasks(app *ui.App, b *Browser) {
	if app.ReadOnly() {
		app.Flash().Warn("read-only mode: stopping tasks is disabled")
		return
	}
	rows := b.Marked()
	if len(rows) == 0 {
		return
	}

	tasks := make([]model.Task, 0, len(rows))
	for _, r := range rows {
		if t, ok := r.Ref.(model.Task); ok {
			tasks = append(tasks, t)
		}
	}
	if len(tasks) == 0 {
		return
	}

	subject := shortID(tasks[0].ID)
	if len(tasks) > 1 {
		subject = fmt.Sprintf("%d tasks", len(tasks))
	}
	msg := fmt.Sprintf("Stop %s?\n\nTasks owned by a service are replaced by the\nscheduler; standalone tasks are not.", subject)

	app.Confirm("Stop tasks", msg, func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			var failed int
			var lastErr error
			for _, t := range tasks {
				if err := app.Client().StopTask(ctx, t.Cluster, t.ARN, "Stopped from ecis"); err != nil {
					failed, lastErr = failed+1, err
				}
			}
			app.QueueUpdateDraw(func() {
				if lastErr != nil {
					app.Flash().Errf("%d of %d stops failed: %v", failed, len(tasks), lastErr)
				} else {
					app.Flash().Infof("stopped %s", subject)
				}
				b.ClearMarks()
				b.Refresh()
			})
		}()
	})
}

// execInto opens an ECS Exec session, suspending the TUI for the duration.
func execInto(app *ui.App, t model.Task, container string) {
	if app.ReadOnly() {
		app.Flash().Warn("read-only mode: exec is disabled")
		return
	}
	if !t.ExecEnabled {
		app.Flash().Warn("this task does not have execute-command enabled")
		return
	}
	if !strings.EqualFold(t.LastStatus, "RUNNING") {
		app.Flash().Warn("only running tasks can be exec'd into")
		return
	}

	req := awsx.ExecRequest{
		Cluster:   t.Cluster,
		Task:      t.ARN,
		Container: container,
		Profile:   app.Profile(),
		Region:    app.Region(),
	}
	bin, args, err := req.ExecCommand()
	if err != nil {
		app.Flash().Err(err)
		return
	}
	app.Shell(bin, args)
}

func describeTask(app *ui.App, t model.Task) ui.Component {
	containers := make([]string, 0, len(t.Containers))
	for _, c := range t.Containers {
		line := fmt.Sprintf("%-24s %-12s %s", c.Name, c.LastStatus, c.Image)
		if c.ExitCode != nil {
			line += fmt.Sprintf("  (exit %d)", *c.ExitCode)
		}
		if c.Reason != "" {
			line += "  " + c.Reason
		}
		containers = append(containers, line)
	}

	text := describe("Task "+t.ID, []section{
		{Name: "Overview", Fields: []field{
			{"ID", t.ID},
			{"ARN", t.ARN},
			{"Cluster", t.Cluster},
			{"Group", t.Group},
			{"Service", t.Service},
			{"Task definition", t.TaskDefinition},
		}},
		{Name: "State", Fields: []field{
			{"Last status", t.LastStatus},
			{"Desired status", t.DesiredStatus},
			{"Health", t.HealthStatus},
			{"Stop code", t.StopCode},
			{"Stopped reason", t.StoppedReason},
			{"Age", awsx.Duration(t.Age())},
		}},
		{Name: "Placement", Fields: []field{
			{"Launch type", launchText(t)},
			{"Platform version", t.PlatformVer},
			{"Availability zone", t.AZ},
			{"Container instance", t.ContainerInst},
			{"Private IP", t.ConnectivityIP},
			{"Exec enabled", boolText(t.ExecEnabled)},
		}},
		{Name: "Size", Fields: []field{
			{"CPU units", t.CPU},
			{"Memory (MiB)", t.Memory},
		}},
		{Name: "Timeline", Fields: []field{
			{"Created", formatTime(t.CreatedAt)},
			{"Started", formatTime(t.StartedAt)},
			{"Stopped", formatTime(t.StoppedAt)},
		}},
		{Name: "Containers", Lines: containers},
		{Name: "Tags", Fields: tagFields(t.Tags)},
	})

	d := ui.NewDetails(app, "describe", shortID(t.ID))
	d.SetContent(text)
	return d
}

// NewContainers lists the containers of a task.
func NewContainers(app *ui.App, task model.Task) *Browser {
	columns := []ui.Column{
		colE("NAME"),
		col("STATUS"),
		col("HEALTH"),
		colE("IMAGE"),
		colN("CPU"),
		colN("MEM"),
		colN("EXIT"),
		colW("PORTS"),
		colW("IP"),
		colW("RUNTIME-ID"),
		colE("REASON"),
	}

	b := NewBrowser(app, "containers", "Containers", columns, func(ctx context.Context) ([]ui.Row, error) {
		// Re-read the task so container statuses stay live rather than frozen
		// at whatever they were when the row was selected.
		tasks, err := app.Client().Tasks(ctx, awsx.TaskQuery{Cluster: task.Cluster})
		if err != nil {
			return nil, err
		}
		current := task
		for _, t := range tasks {
			if t.ARN == task.ARN {
				current = t
				break
			}
		}

		rows := make([]ui.Row, 0, len(current.Containers))
		for _, c := range current.Containers {
			exit := "-"
			exitColor := ui.ColorMuted
			if c.ExitCode != nil {
				exit = strconv.Itoa(int(*c.ExitCode))
				exitColor = ui.ColorOK
				if *c.ExitCode != 0 {
					exitColor = ui.ColorError
				}
			}

			var r rowBuilder
			r.add(c.Name).
				addStatus(c.LastStatus).
				addStatus(dash(c.HealthStatus)).
				add(shortImage(c.Image)).
				add(dash(c.CPU)).
				add(dash(c.Memory)).
				addC(exit, exitColor).
				add(dash(strings.Join(c.Ports, ","))).
				add(dash(strings.Join(c.Networks, ","))).
				add(dash(c.RuntimeID)).
				add(dash(c.Reason))
			rows = append(rows, r.build(c.ARN+c.Name, c))
		}
		return rows, nil
	})

	b.Scope = func() string { return shortID(task.ID) }
	b.SortKeys = map[rune]string{'N': "NAME", 'S': "STATUS", 'H': "HEALTH", 'E': "EXIT"}
	b.SetHints([]ui.Hint{
		{Key: "l", Desc: "Logs"},
		{Key: "s", Desc: "Shell"},
		{Key: "d", Desc: "Describe"},
		{Key: "y", Desc: "YAML"},
	})

	b.OnEnter = func(row ui.Row) {
		c, ok := row.Ref.(model.Container)
		if !ok {
			return
		}
		openLogs(app, containerLogTarget(c, task.TaskDefinition))
	}

	b.OnKey = func(evt *tcell.EventKey) *tcell.EventKey {
		row, ok := b.Selected()
		if !ok {
			return evt
		}
		c, ok := row.Ref.(model.Container)
		if !ok {
			return evt
		}

		switch evt.Rune() {
		case 'l':
			openLogs(app, containerLogTarget(c, task.TaskDefinition))
			return nil
		case 's':
			execInto(app, task, c.Name)
			return nil
		case 'd':
			app.Push(describeContainer(app, c))
			return nil
		case 'y':
			app.Push(yamlView(app, "container", c.Name, c.Raw))
			return nil
		}
		return evt
	}
	return b
}

// shortImage drops the registry host, which is the same for every container in
// most accounts and costs a lot of column width.
func shortImage(image string) string {
	if i := strings.Index(image, "/"); i > 0 && strings.Contains(image[:i], ".") {
		return image[i+1:]
	}
	return image
}

func describeContainer(app *ui.App, c model.Container) ui.Component {
	exit := "-"
	if c.ExitCode != nil {
		exit = strconv.Itoa(int(*c.ExitCode))
	}

	text := describe("Container "+c.Name, []section{
		{Name: "Overview", Fields: []field{
			{"Name", c.Name},
			{"Task", c.TaskID},
			{"Cluster", c.Cluster},
			{"Image", c.Image},
			{"Image digest", c.ImageDigest},
			{"Runtime ID", c.RuntimeID},
		}},
		{Name: "State", Fields: []field{
			{"Last status", c.LastStatus},
			{"Health", c.HealthStatus},
			{"Exit code", exit},
			{"Reason", c.Reason},
		}},
		{Name: "Resources", Fields: []field{
			{"CPU units", c.CPU},
			{"Memory (MiB)", c.Memory},
			{"Memory reservation", c.MemoryRes},
		}},
		{Name: "Network", Fields: []field{
			{"Ports", strings.Join(c.Ports, ", ")},
			{"Addresses", strings.Join(c.Networks, ", ")},
		}},
	})

	d := ui.NewDetails(app, "describe", c.Name)
	d.SetContent(text)
	return d
}
