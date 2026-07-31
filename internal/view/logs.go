package view

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

// logSetupTimeout bounds the task-definition lookup that resolves a container's
// awslogs configuration.
const logSetupTimeout = 20 * time.Second

// logTarget describes what a log view should follow.
type logTarget struct {
	Cluster string
	TaskDef string
	Subject string
	// Containers restricts the tail; empty means every container.
	Containers []string
	// TaskIDs pins the tail to specific tasks. Empty follows every task of the
	// service, which is what makes a service-level log view useful.
	TaskIDs []string
}

// openLogs resolves the awslogs configuration and pushes a log view.
//
// The lookup needs an API call, so it runs off the UI goroutine and pushes the
// view when it completes; the flash bar covers the gap.
func openLogs(app *ui.App, t logTarget) {
	app.Flash().Infof("opening logs for %s…", t.Subject)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), logSetupTimeout)
		defer cancel()

		configs, err := app.Client().ContainerLogConfigs(ctx, t.TaskDef)
		if err != nil {
			app.QueueUpdateDraw(func() { app.Flash().Err(err) })
			return
		}

		group, streams, prefix, note := resolveLogSources(configs, t)
		if group == "" {
			app.QueueUpdateDraw(func() {
				app.Flash().Warnf("no awslogs driver configured for %s", t.Subject)
			})
			return
		}

		tail := app.Client().NewLogTail(group, streams, prefix,
			time.Duration(app.Config().LogSinceSeconds)*time.Second)
		multi := len(streams) != 1

		app.QueueUpdateDraw(func() {
			view := ui.NewLogView(app, "logs", t.Subject, tail, multi)
			app.Push(view)
			if note != "" {
				app.Flash().Warn(note)
			}
		})
	}()
}

// resolveLogSources turns container log configurations into a group plus either
// an explicit stream list or a stream prefix.
//
// ECS names awslogs streams "<prefix>/<container>/<task id>", so a known task
// yields exact stream names, while a service-wide tail follows the prefix.
func resolveLogSources(configs map[string]awsx.LogConfig, t logTarget) (group string, streams []string, prefix string, note string) {
	names := t.Containers
	if len(names) == 0 {
		for name := range configs {
			names = append(names, name)
		}
		sort.Strings(names)
	}

	var groups []string
	for _, name := range names {
		lc, ok := configs[name]
		if !ok || lc.Group == "" {
			continue
		}
		if !strings.EqualFold(lc.Driver, "awslogs") {
			note = fmt.Sprintf("container %s uses the %s log driver, which ecis cannot tail", name, lc.Driver)
			continue
		}
		if group == "" {
			group = lc.Group
		}
		if lc.Group != group {
			// Containers can log to different groups; one tail covers one group.
			groups = append(groups, lc.Group)
			continue
		}
		for _, taskID := range t.TaskIDs {
			streams = append(streams, awsx.LogStreamName(lc.StreamPrefix, name, taskID))
		}
		if len(t.TaskIDs) == 0 && prefix == "" {
			prefix = strings.TrimSuffix(awsx.LogStreamName(lc.StreamPrefix, name, ""), "/")
		}
	}

	if len(groups) > 0 && note == "" {
		note = fmt.Sprintf("containers span several log groups; showing %s only", group)
	}
	// When several containers share a group, following the whole group beats
	// picking one container's prefix arbitrarily.
	if len(streams) == 0 && len(names) > 1 {
		prefix = ""
	}
	return group, streams, prefix, note
}

// serviceLogTarget builds a log target following every task of a service.
func serviceLogTarget(svc model.Service) logTarget {
	return logTarget{
		Cluster: svc.Cluster,
		TaskDef: svc.TaskDefinition,
		Subject: svc.Name,
	}
}

// taskLogTarget builds a log target for one task's containers.
func taskLogTarget(task model.Task) logTarget {
	names := make([]string, 0, len(task.Containers))
	for _, c := range task.Containers {
		names = append(names, c.Name)
	}
	return logTarget{
		Cluster:    task.Cluster,
		TaskDef:    task.TaskDefinition,
		Subject:    task.ID,
		Containers: names,
		TaskIDs:    []string{task.ID},
	}
}

// containerLogTarget builds a log target for a single container.
func containerLogTarget(c model.Container, taskDef string) logTarget {
	return logTarget{
		Cluster:    c.Cluster,
		TaskDef:    taskDef,
		Subject:    c.TaskID + "/" + c.Name,
		Containers: []string{c.Name},
		TaskIDs:    []string{c.TaskID},
	}
}
