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

// NewTaskDefs lists task definitions. With no family it shows the latest active
// revision of every family; drilling into one shows that family's revisions.
func NewTaskDefs(app *ui.App, family string) *Browser {
	columns := []ui.Column{
		colE("FAMILY"),
		colN("REVISION"),
		col("STATUS"),
		colN("CPU"),
		colN("MEM"),
		col("NETWORK"),
		col("REQUIRES"),
		colE("CONTAINERS"),
		colW("TASK-ROLE"),
		colW("EXEC-ROLE"),
		colW("REGISTERED-BY"),
		colA("AGE"),
	}

	kind := "TaskDefinitions"
	if family != "" {
		kind = "Revisions"
	}

	b := NewBrowser(app, "taskdefs", kind, columns, func(ctx context.Context) ([]ui.Row, error) {
		defs, err := app.Client().TaskDefs(ctx, family)
		if err != nil {
			return nil, err
		}

		rows := make([]ui.Row, 0, len(defs))
		for _, d := range defs {
			var r rowBuilder
			r.add(d.Family).
				addInt(d.Revision).
				addStatus(d.Status).
				add(dash(d.CPU)).
				add(dash(d.Memory)).
				add(dash(d.NetworkMode)).
				add(dash(strings.Join(d.Requires, ","))).
				add(dash(strings.Join(d.Containers, ","))).
				add(dash(d.TaskRole)).
				add(dash(d.ExecRole)).
				add(dash(d.RegisteredBy)).
				add(awsx.Since(d.RegisteredAt))
			rows = append(rows, r.build(d.ARN, d))
		}
		return rows, nil
	})

	b.Scope = func() string { return family }
	b.SortKeys = map[rune]string{
		'N': "FAMILY", 'R': "REVISION", 'S': "STATUS", 'A': "AGE", 'C': "CPU", 'M': "MEM",
	}
	b.SetHints([]ui.Hint{
		{Key: "t", Desc: "Tasks"},
		{Key: "d", Desc: "Describe"},
		{Key: "y", Desc: "YAML"},
	})

	b.OnEnter = func(row ui.Row) {
		d, ok := row.Ref.(model.TaskDef)
		if !ok {
			return
		}
		if family == "" {
			app.Push(NewTaskDefs(app, d.Family))
			return
		}
		app.Push(yamlView(app, "taskdef", d.Name(), d.Raw))
	}

	b.OnKey = func(evt *tcell.EventKey) *tcell.EventKey {
		row, ok := b.Selected()
		if !ok {
			return evt
		}
		d, ok := row.Ref.(model.TaskDef)
		if !ok {
			return evt
		}

		switch evt.Rune() {
		case 't':
			cluster := app.Cluster()
			if cluster == "" {
				app.Flash().Warn("select a cluster first")
				return nil
			}
			app.Push(NewTasks(app, awsx.TaskQuery{Cluster: cluster, Family: d.Family}, "tasks", d.Family))
			return nil
		case 'd':
			app.Push(describeTaskDef(app, d))
			return nil
		case 'y':
			app.Push(yamlView(app, "taskdef", d.Name(), d.Raw))
			return nil
		}
		return evt
	}
	return b
}

func describeTaskDef(app *ui.App, d model.TaskDef) ui.Component {
	text := describe("Task definition "+d.Name(), []section{
		{Name: "Overview", Fields: []field{
			{"Family", d.Family},
			{"Revision", strconv.Itoa(d.Revision)},
			{"ARN", d.ARN},
			{"Status", d.Status},
			{"Registered", formatTime(d.RegisteredAt)},
			{"Registered by", d.RegisteredBy},
		}},
		{Name: "Runtime", Fields: []field{
			{"CPU units", d.CPU},
			{"Memory (MiB)", d.Memory},
			{"Network mode", d.NetworkMode},
			{"Compatibilities", strings.Join(d.Requires, ", ")},
			{"Task role", d.TaskRole},
			{"Execution role", d.ExecRole},
		}},
		{Name: "Containers", Lines: d.Containers},
	})

	v := ui.NewDetails(app, "describe", d.Name())
	v.SetContent(text)
	return v
}

// NewInstances lists the container instances backing an EC2-capacity cluster.
func NewInstances(app *ui.App, cluster string) *Browser {
	columns := []ui.Column{
		colE("EC2-ID"),
		col("STATUS"),
		col("AGENT"),
		colN("RUNNING"),
		colN("PENDING"),
		colN("CPU"),
		colN("MEM"),
		col("TYPE"),
		col("AZ"),
		colW("CAPACITY-PROVIDER"),
		colW("AGENT-VERSION"),
		colW("DOCKER"),
		colW("ID"),
		colA("AGE"),
	}

	b := NewBrowser(app, "instances", "ContainerInstances", columns, func(ctx context.Context) ([]ui.Row, error) {
		instances, err := app.Client().Instances(ctx, cluster)
		if err != nil {
			return nil, err
		}

		rows := make([]ui.Row, 0, len(instances))
		for _, i := range instances {
			var r rowBuilder
			r.add(dash(i.EC2ID)).
				addStatus(i.Status).
				addC(boolText(i.AgentConnected), ui.StatusColor(boolText(i.AgentConnected))).
				addInt(i.RunningTasks).
				addInt(i.PendingTasks).
				addPct(i.CPUUsedPct()).
				addPct(i.MemUsedPct()).
				add(dash(i.InstanceType)).
				add(dash(i.AZ)).
				add(dash(i.CapacityProv)).
				add(dash(i.AgentVersion)).
				add(dash(i.DockerVersion)).
				add(i.ID).
				add(awsx.Since(i.RegisteredAt))
			rows = append(rows, r.build(i.ARN, i))
		}
		return rows, nil
	})

	b.Scope = func() string { return cluster }
	b.SortKeys = map[rune]string{
		'N': "EC2-ID", 'S': "STATUS", 'C': "CPU", 'M': "MEM",
		'R': "RUNNING", 'A': "AGE", 'T': "TYPE",
	}
	b.SetHints([]ui.Hint{
		{Key: "t", Desc: "Tasks"},
		{Key: "d", Desc: "Describe"},
		{Key: "y", Desc: "YAML"},
	})

	b.OnEnter = func(row ui.Row) {
		i, ok := row.Ref.(model.Instance)
		if !ok {
			return
		}
		app.Push(NewTasks(app, awsx.TaskQuery{Cluster: cluster, Instance: i.ID}, "tasks", i.EC2ID))
	}

	b.OnKey = func(evt *tcell.EventKey) *tcell.EventKey {
		row, ok := b.Selected()
		if !ok {
			return evt
		}
		i, ok := row.Ref.(model.Instance)
		if !ok {
			return evt
		}

		switch evt.Rune() {
		case 't':
			app.Push(NewTasks(app, awsx.TaskQuery{Cluster: cluster, Instance: i.ID}, "tasks", i.EC2ID))
			return nil
		case 'd':
			app.Push(describeInstance(app, i))
			return nil
		case 'y':
			app.Push(yamlView(app, "instance", i.EC2ID, i.Raw))
			return nil
		}
		return evt
	}
	return b
}

func describeInstance(app *ui.App, i model.Instance) ui.Component {
	text := describe("Container instance "+i.EC2ID, []section{
		{Name: "Overview", Fields: []field{
			{"EC2 instance", i.EC2ID},
			{"Instance type", i.InstanceType},
			{"ARN", i.ARN},
			{"Cluster", i.Cluster},
			{"Status", i.Status},
			{"Availability zone", i.AZ},
			{"Capacity provider", i.CapacityProv},
			{"Registered", formatTime(i.RegisteredAt)},
		}},
		{Name: "Agent", Fields: []field{
			{"Connected", boolText(i.AgentConnected)},
			{"Agent version", i.AgentVersion},
			{"Docker version", i.DockerVersion},
		}},
		{Name: "Capacity", Fields: []field{
			{"Running tasks", strconv.Itoa(i.RunningTasks)},
			{"Pending tasks", strconv.Itoa(i.PendingTasks)},
			{"CPU registered", strconv.Itoa(i.CPURegistered)},
			{"CPU remaining", strconv.Itoa(i.CPURemaining)},
			{"Memory registered (MiB)", strconv.Itoa(i.MemRegistered)},
			{"Memory remaining (MiB)", strconv.Itoa(i.MemRemaining)},
		}},
	})

	v := ui.NewDetails(app, "describe", i.EC2ID)
	v.SetContent(text)
	return v
}

// NewCapacityProviders lists the capacity providers of a cluster.
func NewCapacityProviders(app *ui.App, cluster string) *Browser {
	columns := []ui.Column{
		colE("NAME"),
		col("STATUS"),
		colE("AUTO-SCALING-GROUP"),
		col("MANAGED-SCALING"),
		colN("TARGET"),
		col("TERMINATION-PROTECTION"),
		colW("UPDATE-STATUS"),
		colW("ARN"),
	}

	b := NewBrowser(app, "capacity", "CapacityProviders", columns, func(ctx context.Context) ([]ui.Row, error) {
		providers, err := app.Client().CapacityProviders(ctx, cluster)
		if err != nil {
			return nil, err
		}

		rows := make([]ui.Row, 0, len(providers))
		for _, p := range providers {
			target := "-"
			if p.TargetCapacity > 0 {
				target = strconv.Itoa(p.TargetCapacity) + "%"
			}
			var r rowBuilder
			r.add(p.Name).
				addStatus(p.Status).
				add(dash(p.AutoScalingGroup)).
				addStatus(dash(p.ManagedScaling)).
				add(target).
				addStatus(dash(p.ManagedTermination)).
				add(dash(p.UpdateStatus)).
				add(p.ARN)
			rows = append(rows, r.build(p.ARN, p))
		}
		return rows, nil
	})

	b.Scope = func() string { return cluster }
	b.SortKeys = map[rune]string{'N': "NAME", 'S': "STATUS", 'T': "TARGET"}
	b.SetHints([]ui.Hint{{Key: "y", Desc: "YAML"}})

	b.OnKey = func(evt *tcell.EventKey) *tcell.EventKey {
		row, ok := b.Selected()
		if !ok {
			return evt
		}
		p, ok := row.Ref.(model.CapacityProvider)
		if !ok {
			return evt
		}
		if evt.Rune() == 'y' {
			app.Push(yamlView(app, "capacity", p.Name, p.Raw))
			return nil
		}
		return evt
	}
	return b
}
