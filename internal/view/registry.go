package view

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/ui"
)

// command is one entry in the resource registry.
type command struct {
	// Name is the canonical command, shown in the aliases view.
	Name string
	// Aliases are the short forms accepted at the ":" prompt.
	Aliases []string
	// Desc is the one-line description.
	Desc string
	// New builds the view. args are whatever followed the command.
	New func(app *ui.App, args []string) (ui.Component, error)
	// NeedsCluster rejects the command when no cluster is selected.
	NeedsCluster bool
}

// Registry maps commands and aliases onto views. It is the ":" prompt's
// dispatch table and the source of the aliases screen.
type Registry struct {
	app      *ui.App
	commands []command
	index    map[string]*command
}

// NewRegistry builds the command registry.
func NewRegistry(app *ui.App) *Registry {
	r := &Registry{app: app, index: map[string]*command{}}
	r.commands = []command{
		{
			Name:    "clusters",
			Aliases: []string{"cl", "cluster", "ns", "namespace"},
			Desc:    "ECS clusters in this region",
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return NewClusters(app), nil
			},
		},
		{
			Name:         "services",
			Aliases:      []string{"svc", "service", "deploy", "deployment"},
			Desc:         "Services in the active cluster",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return NewServices(app, app.Cluster()), nil
			},
		},
		{
			Name:         "tasks",
			Aliases:      []string{"ts", "task", "po", "pod", "pods"},
			Desc:         "Tasks in the active cluster",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				q := awsx.TaskQuery{Cluster: app.Cluster()}
				scope := app.Cluster()
				if len(args) > 0 {
					q.Service, scope = args[0], args[0]
				}
				return NewTasks(app, q, "tasks", scope), nil
			},
		},
		{
			Name:         "running",
			Aliases:      []string{"run"},
			Desc:         "Running tasks only",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				q := awsx.TaskQuery{Cluster: app.Cluster(), DesiredStatus: "RUNNING"}
				return NewTasks(app, q, "tasks", app.Cluster()+" · running"), nil
			},
		},
		{
			Name:         "stopped",
			Aliases:      []string{"stop", "failed"},
			Desc:         "Stopped tasks, newest first",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				q := awsx.TaskQuery{Cluster: app.Cluster(), DesiredStatus: "STOPPED"}
				return NewTasks(app, q, "tasks", app.Cluster()+" · stopped"), nil
			},
		},
		{
			Name:    "taskdefs",
			Aliases: []string{"td", "taskdef", "taskdefinition", "jobs"},
			Desc:    "Task definitions and revisions",
			New: func(app *ui.App, args []string) (ui.Component, error) {
				family := ""
				if len(args) > 0 {
					family = args[0]
				}
				return NewTaskDefs(app, family), nil
			},
		},
		{
			Name:         "instances",
			Aliases:      []string{"ci", "instance", "node", "nodes", "ec2"},
			Desc:         "Container instances (EC2 capacity)",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return NewInstances(app, app.Cluster()), nil
			},
		},
		{
			Name:         "capacity",
			Aliases:      []string{"cp", "providers", "provider"},
			Desc:         "Capacity providers",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return NewCapacityProviders(app, app.Cluster()), nil
			},
		},
		{
			Name:         "events",
			Aliases:      []string{"ev", "event"},
			Desc:         "Service events across the cluster",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				service := ""
				if len(args) > 0 {
					service = args[0]
				}
				return NewEvents(app, app.Cluster(), service), nil
			},
		},
		{
			Name:         "deployments",
			Aliases:      []string{"dp", "rollout", "rollouts"},
			Desc:         "Deployments of a service — :deployments <service>",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				if len(args) == 0 {
					return nil, fmt.Errorf("usage: :deployments <service>")
				}
				return NewDeployments(app, app.Cluster(), args[0]), nil
			},
		},
		{
			Name:         "metrics",
			Aliases:      []string{"mon", "monitor", "top", "cpu", "mem", "memory"},
			Desc:         "CPU and memory charts — :metrics [service]",
			NeedsCluster: true,
			New: func(app *ui.App, args []string) (ui.Component, error) {
				target := awsx.MetricTarget{Kind: "cluster", Cluster: app.Cluster()}
				if len(args) > 0 {
					target = awsx.MetricTarget{Kind: "service", Cluster: app.Cluster(), Name: args[0]}
				}
				return NewMetrics(app, target), nil
			},
		},
		{
			Name:    "cost",
			Aliases: []string{"$", "spend", "billing", "ce"},
			Desc:    "Cost Explorer spend for ECS",
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return NewCost(app, app.Cluster()), nil
			},
		},
		{
			Name:    "pulse",
			Aliases: []string{"pu", "dash", "dashboard"},
			Desc:    "Fleet dashboard: health, load and spend",
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return NewPulse(app), nil
			},
		},
		{
			Name:    "contexts",
			Aliases: []string{"ctx", "context", "profile", "profiles"},
			Desc:    "Switch AWS profile",
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return NewContexts(app), nil
			},
		},
		{
			Name:    "regions",
			Aliases: []string{"region"},
			Desc:    "Switch AWS region",
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return NewRegions(app), nil
			},
		},
		{
			Name:    "aliases",
			Aliases: []string{"alias", "a"},
			Desc:    "Every command and its aliases",
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return r.newAliases(), nil
			},
		},
		{
			Name:    "help",
			Aliases: []string{"h", "?"},
			Desc:    "Keyboard shortcuts",
			New: func(app *ui.App, args []string) (ui.Component, error) {
				return r.newHelp(), nil
			},
		},
	}

	for i := range r.commands {
		c := &r.commands[i]
		r.index[c.Name] = c
		for _, alias := range c.Aliases {
			r.index[alias] = c
		}
	}
	return r
}

// Exec runs a command line from the ":" prompt.
func (r *Registry) Exec(line string) error {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return nil
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], ":"))
	args := fields[1:]

	switch name {
	case "q", "quit", "exit":
		r.app.Stop()
		return nil
	case "cluster", "use":
		// ":cluster prod" is the ECS equivalent of switching namespace.
		if len(args) == 0 {
			return fmt.Errorf("usage: :cluster <name>")
		}
		r.app.SetCluster(args[0])
		r.app.Flash().Infof("cluster set to %s", args[0])
		return r.Goto("services", nil)
	case "ctx", "context", "profile":
		if len(args) > 0 {
			switchContext(r.app, args[0], r.app.Region())
			return nil
		}
	case "region":
		if len(args) > 0 {
			switchContext(r.app, r.app.Profile(), args[0])
			return nil
		}
	}

	return r.Goto(name, args)
}

// Goto resolves a command name and pushes the resulting view.
func (r *Registry) Goto(name string, args []string) error {
	cmd, ok := r.index[strings.ToLower(name)]
	if !ok {
		return fmt.Errorf("unknown command %q — press ctrl-a for the alias list", name)
	}

	// Several commands accept a cluster as their first argument, which makes
	// ":svc prod" work without a separate ":cluster prod" step.
	if cmd.NeedsCluster && len(args) > 0 && isClusterArg(cmd.Name) {
		r.app.SetCluster(args[0])
		args = args[1:]
	}
	if cmd.NeedsCluster && r.app.Cluster() == "" {
		return fmt.Errorf("%s needs a cluster — pick one with :clusters or :cluster <name>", cmd.Name)
	}

	view, err := cmd.New(r.app, args)
	if err != nil {
		return err
	}
	r.app.Push(view)
	return nil
}

// isClusterArg reports whether a command's first argument names a cluster.
func isClusterArg(name string) bool {
	switch name {
	case "services", "instances", "capacity":
		return true
	default:
		return false
	}
}

// Suggest returns the command names matching a prefix, for prompt completion.
func (r *Registry) Suggest(prefix string) []string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	for name := range r.index {
		if !strings.HasPrefix(name, prefix) || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) < len(out[j])
		}
		return out[i] < out[j]
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// newAliases builds the alias reference table.
func (r *Registry) newAliases() ui.Component {
	columns := []ui.Column{
		colE("COMMAND"),
		colE("ALIASES"),
		colE("DESCRIPTION"),
	}

	b := NewBrowser(r.app, "aliases", "Aliases", columns, func(ctx context.Context) ([]ui.Row, error) {
		rows := make([]ui.Row, 0, len(r.commands))
		for _, c := range r.commands {
			var rb rowBuilder
			rb.addC(c.Name, ui.ColorTitle).
				addC(strings.Join(c.Aliases, ", "), ui.ColorKey).
				add(c.Desc)
			rows = append(rows, rb.build(c.Name, c.Name))
		}
		return rows, nil
	})

	// The registry is static; there is nothing to poll for.
	b.Interval = time.Hour
	b.SortKeys = map[rune]string{'N': "COMMAND"}
	b.OnEnter = func(row ui.Row) {
		name, ok := row.Ref.(string)
		if !ok {
			return
		}
		// Replace rather than push: the alias list is a chooser, not a step in
		// the drill-down path.
		r.app.Pop()
		if err := r.Goto(name, nil); err != nil {
			r.app.Flash().Err(err)
		}
	}
	return b
}

// newHelp builds the keyboard reference.
func (r *Registry) newHelp() ui.Component {
	aliasLines := make([]string, 0, len(r.commands))
	for _, c := range r.commands {
		aliasLines = append(aliasLines, fmt.Sprintf("%-14s %-32s %s",
			":"+c.Name, strings.Join(c.Aliases, ", "), c.Desc))
	}

	sections := []section{
		{Name: "Global", Fields: []field{
			{":", "Open the command prompt"},
			{"/", "Filter the current view (regex or substring, ! to invert)"},
			{"?", "This help"},
			{"esc", "Clear the filter, or go back one view"},
			{"ctrl-a", "Alias list"},
			{"ctrl-r", "Force a reload"},
			{"ctrl-e", "Show or hide the header"},
			{"ctrl-c", "Quit (or :q)"},
		}},
		{Name: "Navigation", Fields: []field{
			{"j / k", "Down / up"},
			{"h / l", "Left / right"},
			{"g / G", "First / last row"},
			{"ctrl-f / ctrl-b", "Page down / page up"},
			{"enter", "Drill into the selected row"},
		}},
		{Name: "Tables", Fields: []field{
			{"space", "Mark a row and advance"},
			{"ctrl-\\", "Clear all marks"},
			{"ctrl-w", "Toggle wide columns"},
			{"shift-N", "Sort by name"},
			{"shift-C", "Sort by CPU"},
			{"shift-M", "Sort by memory"},
			{"shift-A", "Sort by age"},
			{"shift-S", "Sort by status"},
			{"(repeat)", "Press the same sort key again to reverse it"},
		}},
		{Name: "Services", Fields: []field{
			{"s", "Scale the service"},
			{"r", "Restart — force a new deployment"},
			{"l", "Tail logs from every task"},
			{"m", "CPU and memory charts"},
			{"e", "Service events"},
			{"p", "Deployments"},
			{"$", "Cost breakdown"},
			{"d / y", "Describe / YAML"},
		}},
		{Name: "Tasks and containers", Fields: []field{
			{"enter", "Containers of the task"},
			{"l", "Tail logs"},
			{"s", "Shell in via ECS Exec"},
			{"m", "Per-task charts"},
			{"ctrl-d", "Stop the marked tasks"},
			{"d / y", "Describe / YAML"},
		}},
		{Name: "Logs", Fields: []field{
			{"s", "Toggle autoscroll"},
			{"w", "Toggle wrapping"},
			{"t", "Toggle timestamps"},
			{"f", "Toggle fullscreen"},
			{"c", "Clear the buffer"},
			{"0 1 2 3 4 5", "Show all / 1m / 5m / 15m / 1h / 24h"},
			{"ctrl-s", "Save to a file"},
		}},
		{Name: "Charts", Fields: []field{
			{"1 2 3 4 5", "Window: 15m / 1h / 6h / 24h / 7d"},
			{"ctrl-r", "Reload from CloudWatch"},
		}},
		{Name: "Cost", Fields: []field{
			{"b", "Cycle the breakdown"},
			{"n", "Toggle daily / monthly granularity"},
			{"w", "Toggle ECS-only / all container spend"},
			{"e", "Toggle the local Fargate estimate"},
			{"enter", "Full history for the selected group"},
		}},
		{Name: "Commands", Lines: aliasLines},
	}

	d := ui.NewDetails(r.app, "help", "ecis "+r.app.Version())
	d.SetContent(describe("ecis — keyboard reference", sections))
	return d
}
