package view

import (
	"strings"
	"testing"
	"time"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

func TestResolveLogSourcesForOneTask(t *testing.T) {
	configs := map[string]awsx.LogConfig{
		"web": {Group: "/ecs/my-app", StreamPrefix: "ecs", Driver: "awslogs"},
	}
	target := logTarget{Containers: []string{"web"}, TaskIDs: []string{"abc123"}}

	group, streams, prefix, note := resolveLogSources(configs, target)
	if group != "/ecs/my-app" {
		t.Errorf("group = %q, want /ecs/my-app", group)
	}
	if len(streams) != 1 || streams[0] != "ecs/web/abc123" {
		t.Errorf("streams = %v, want [ecs/web/abc123]", streams)
	}
	if prefix != "" {
		t.Errorf("prefix = %q, want empty when exact streams are known", prefix)
	}
	if note != "" {
		t.Errorf("unexpected note %q", note)
	}
}

func TestResolveLogSourcesForAWholeService(t *testing.T) {
	configs := map[string]awsx.LogConfig{
		"web": {Group: "/ecs/my-app", StreamPrefix: "ecs", Driver: "awslogs"},
	}
	// No task ids: the tail should follow every task under the prefix.
	group, streams, prefix, _ := resolveLogSources(configs, logTarget{Containers: []string{"web"}})

	if group != "/ecs/my-app" {
		t.Errorf("group = %q, want /ecs/my-app", group)
	}
	if len(streams) != 0 {
		t.Errorf("streams = %v, want none for a service-wide tail", streams)
	}
	if prefix != "ecs/web" {
		t.Errorf("prefix = %q, want ecs/web", prefix)
	}
}

func TestResolveLogSourcesSkipsNonAWSLogsDrivers(t *testing.T) {
	configs := map[string]awsx.LogConfig{
		"sidecar": {Driver: "fluentbit", Group: "ignored"},
	}
	group, _, _, note := resolveLogSources(configs, logTarget{Containers: []string{"sidecar"}})

	if group != "" {
		t.Errorf("group = %q, want empty for an untailable driver", group)
	}
	if !strings.Contains(note, "fluentbit") {
		t.Errorf("note = %q, want it to name the unsupported driver", note)
	}
}

func TestResolveLogSourcesReportsSplitGroups(t *testing.T) {
	configs := map[string]awsx.LogConfig{
		"web":     {Group: "/ecs/web", StreamPrefix: "ecs", Driver: "awslogs"},
		"sidecar": {Group: "/ecs/sidecar", StreamPrefix: "ecs", Driver: "awslogs"},
	}
	group, _, _, note := resolveLogSources(configs, logTarget{TaskIDs: []string{"abc"}})

	if group == "" {
		t.Fatal("expected one of the groups to be selected")
	}
	if !strings.Contains(note, "several log groups") {
		t.Errorf("note = %q, want a warning about split log groups", note)
	}
}

func TestResolveLogSourcesFollowsGroupForMultipleContainers(t *testing.T) {
	configs := map[string]awsx.LogConfig{
		"web":     {Group: "/ecs/my-app", StreamPrefix: "ecs", Driver: "awslogs"},
		"sidecar": {Group: "/ecs/my-app", StreamPrefix: "ecs", Driver: "awslogs"},
	}
	// Containers sharing a group and no pinned task: follow the whole group
	// rather than picking one container's prefix arbitrarily.
	_, streams, prefix, _ := resolveLogSources(configs, logTarget{})

	if len(streams) != 0 {
		t.Errorf("streams = %v, want none", streams)
	}
	if prefix != "" {
		t.Errorf("prefix = %q, want the whole group", prefix)
	}
}

func TestTaskLogTargetCollectsContainers(t *testing.T) {
	task := model.Task{
		ID:             "abc123",
		Cluster:        "prod",
		TaskDefinition: "my-app:3",
		Containers: []model.Container{
			{Name: "web"},
			{Name: "sidecar"},
		},
	}
	target := taskLogTarget(task)

	if len(target.Containers) != 2 {
		t.Errorf("containers = %v, want both", target.Containers)
	}
	if len(target.TaskIDs) != 1 || target.TaskIDs[0] != "abc123" {
		t.Errorf("task ids = %v, want [abc123]", target.TaskIDs)
	}
	if target.TaskDef != "my-app:3" {
		t.Errorf("task definition = %q, want my-app:3", target.TaskDef)
	}
}

func TestServiceLogTargetFollowsEveryTask(t *testing.T) {
	target := serviceLogTarget(model.Service{Name: "web", Cluster: "prod", TaskDefinition: "my-app:3"})
	if len(target.TaskIDs) != 0 {
		t.Errorf("task ids = %v, want none so every task is followed", target.TaskIDs)
	}
	if target.Subject != "web" {
		t.Errorf("subject = %q, want web", target.Subject)
	}
}

func TestRolloutText(t *testing.T) {
	tests := []struct {
		name string
		svc  model.Service
		want string
	}{
		{"reported state wins", model.Service{RolloutState: "FAILED"}, "FAILED"},
		{"multiple deployments are in progress", model.Service{Deployments: 2}, "IN_PROGRESS"},
		{"settled service", model.Service{Deployments: 1, Desired: 2, Running: 2}, "COMPLETED"},
		{"converging service", model.Service{Deployments: 1, Desired: 2, Running: 1}, "PENDING"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rolloutText(tc.svc); got != tc.want {
				t.Errorf("rolloutText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShortImageDropsRegistryHost(t *testing.T) {
	tests := []struct{ in, want string }{
		{"123456789012.dkr.ecr.us-east-1.amazonaws.com/my-app:1.2.3", "my-app:1.2.3"},
		{"nginx:latest", "nginx:latest"},
		{"library/nginx:latest", "library/nginx:latest"},
	}
	for _, tc := range tests {
		if got := shortImage(tc.in); got != tc.want {
			t.Errorf("shortImage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShortIDTrims(t *testing.T) {
	if got := shortID("0123456789abcdefghij"); len(got) != 12 {
		t.Errorf("shortID = %q, want 12 characters", got)
	}
	if got := shortID("short"); got != "short" {
		t.Errorf("shortID(%q) = %q, want it unchanged", "short", got)
	}
}

func TestLaunchTextPrefersCapacityProvider(t *testing.T) {
	if got := launchText(model.Task{LaunchType: "FARGATE", CapacityProv: "FARGATE_SPOT"}); got != "FARGATE_SPOT" {
		t.Errorf("launchText = %q, want the capacity provider", got)
	}
	if got := launchText(model.Task{LaunchType: "EC2"}); got != "EC2" {
		t.Errorf("launchText = %q, want EC2", got)
	}
}

func TestWrapText(t *testing.T) {
	lines := wrapText("the quick brown fox jumps over the lazy dog", 12)
	if len(lines) < 3 {
		t.Fatalf("wrapText produced %v, want several lines", lines)
	}
	for _, line := range lines {
		if len(line) > 12 {
			t.Errorf("line %q exceeds the width", line)
		}
	}
	if got := wrapText("", 10); got != nil {
		t.Errorf("wrapText(\"\") = %v, want nil", got)
	}
}

func TestTrendText(t *testing.T) {
	up, _ := trendText(12)
	if !strings.HasPrefix(up, "↑") {
		t.Errorf("trendText(12) = %q, want an up arrow", up)
	}
	down, _ := trendText(-12)
	if !strings.HasPrefix(down, "↓") {
		t.Errorf("trendText(-12) = %q, want a down arrow", down)
	}
	flat, _ := trendText(0)
	if !strings.HasPrefix(flat, "→") {
		t.Errorf("trendText(0) = %q, want a flat arrow", flat)
	}
}

func TestRatioColor(t *testing.T) {
	if ratioColor(3, 3) != ui.ColorOK {
		t.Error("a fully converged service should be green")
	}
	if ratioColor(0, 3) != ui.ColorError {
		t.Error("a service with nothing running should be red")
	}
	if ratioColor(1, 3) != ui.ColorWarn {
		t.Error("a converging service should be amber")
	}
	if ratioColor(0, 0) != ui.ColorMuted {
		t.Error("a scaled-to-zero service should be muted")
	}
}

func TestUntaggedDetection(t *testing.T) {
	if !untagged(nil) {
		t.Error("a nil report counts as untagged")
	}
	if !untagged(&model.CostReport{}) {
		t.Error("an empty report counts as untagged")
	}
	onlyUntagged := &model.CostReport{Groups: []model.CostGroup{{Key: "(untagged)", Total: 10}}}
	if !untagged(onlyUntagged) {
		t.Error("a report with only the untagged bucket counts as untagged")
	}
	tagged := &model.CostReport{Groups: []model.CostGroup{
		{Key: "(untagged)", Total: 10},
		{Key: "web", Total: 5},
	}}
	if untagged(tagged) {
		t.Error("a report with a real group is not untagged")
	}
}

func TestEventColorFlagsProblems(t *testing.T) {
	if eventColor("service web was unable to place a task") != ui.ColorError {
		t.Error("placement failures should be red")
	}
	if eventColor("service web has reached a steady state") != ui.ColorOK {
		t.Error("steady state should be green")
	}
	if eventColor("service web is draining connections") != ui.ColorWarn {
		t.Error("draining should be amber")
	}
}

func TestDescribeRendersSections(t *testing.T) {
	out := describe("Service web", []section{
		{Name: "Overview", Fields: []field{{"Name", "web"}, {"Status", ""}}},
		{Name: "Empty"},
		{Name: "Events", Lines: []string{"steady state"}},
	})

	if !strings.Contains(out, "OVERVIEW") {
		t.Error("section headings should be upper-cased")
	}
	if !strings.Contains(out, "Status:") || !strings.Contains(out, "-") {
		t.Error("empty values should render as a dash")
	}
	if strings.Contains(out, "EMPTY") {
		t.Error("sections with no content should be omitted")
	}
	if !strings.Contains(out, "steady state") {
		t.Error("free-form lines should be rendered")
	}
}

func TestRegistryResolvesAliases(t *testing.T) {
	r := &Registry{index: map[string]*command{}}
	r.commands = []command{
		{Name: "services", Aliases: []string{"svc", "service"}},
		{Name: "tasks", Aliases: []string{"ts", "po"}},
	}
	for i := range r.commands {
		c := &r.commands[i]
		r.index[c.Name] = c
		for _, a := range c.Aliases {
			r.index[a] = c
		}
	}

	for _, alias := range []string{"svc", "service", "services"} {
		if got, ok := r.index[alias]; !ok || got.Name != "services" {
			t.Errorf("alias %q did not resolve to services", alias)
		}
	}

	suggestions := r.Suggest("s")
	if len(suggestions) == 0 {
		t.Fatal("Suggest returned nothing for a matching prefix")
	}
	// Shorter matches sort first so the most likely completion leads.
	if suggestions[0] != "svc" {
		t.Errorf("Suggest(\"s\") = %v, want the shortest match first", suggestions)
	}
	if got := r.Suggest(""); got != nil {
		t.Errorf("Suggest(\"\") = %v, want nil", got)
	}
	if got := r.Suggest("zzz"); len(got) != 0 {
		t.Errorf("Suggest(\"zzz\") = %v, want no matches", got)
	}
}

func TestIsClusterArg(t *testing.T) {
	if !isClusterArg("services") {
		t.Error(":services <cluster> should treat its argument as a cluster")
	}
	if isClusterArg("tasks") {
		t.Error(":tasks <service> takes a service, not a cluster")
	}
}

func TestMaxDuration(t *testing.T) {
	if got := maxDuration(time.Second, time.Minute); got != time.Minute {
		t.Errorf("maxDuration = %v, want the larger value", got)
	}
}
