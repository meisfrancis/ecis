package view

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/gdamore/tcell/v2"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/model"
	"github.com/meisfrancis/ecis/internal/ui"
)

// runningApp starts the real tview event loop against a simulation screen and
// returns the app, the screen keys can be injected into, and a teardown.
//
// Driving the actual loop is the only way to catch the deadlock class these
// tests guard: tview's QueueUpdate blocks until the loop drains it, so any code
// that queues an update from the loop's own goroutine hangs the UI, and nothing
// short of running the loop will reveal it.
func runningApp(t *testing.T) (*ui.App, tcell.SimulationScreen, func()) {
	t.Helper()

	app := newTestApp(t)
	screen := tcell.NewSimulationScreen("UTF-8")
	// SetScreen initialises the screen itself when the loop has not started.
	app.SetScreen(screen)
	screen.SetSize(160, 45)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	// Wait for the loop to come up before anything is injected into it.
	if !responsive(app, 5*time.Second) {
		cancel()
		t.Fatal("event loop did not start")
	}

	return app, screen, func() {
		cancel()
		app.Stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("event loop did not shut down")
		}
	}
}

// responsive reports whether the event loop is still draining its queue. A
// deadlocked loop never runs the queued function, so this is a direct liveness
// probe rather than a proxy for one.
func responsive(app *ui.App, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		// Leaks if the loop is wedged, which is exactly the failure being
		// reported; the test fails loudly rather than hanging.
		app.QueueUpdate(func() {})
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// onUI runs f on the event loop and waits for it to finish.
func onUI(app *ui.App, f func()) { app.QueueUpdate(f) }

// eventually polls until cond holds or the deadline passes. Injected keys and
// queued updates arrive on separate channels, so there is no ordering guarantee
// between a keypress and a later probe.
func eventually(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// TestLogFilterKeepsUIResponsive reproduces the reported hang: pressing "/" in a
// log view froze the whole application.
//
// Opening the filter prompt seeds the input field, which fires the change
// handler, which called LogView.SetFilter — and that queued its repaint onto the
// event loop from inside the event loop.
func TestLogFilterKeepsUIResponsive(t *testing.T) {
	app, screen, teardown := runningApp(t)
	defer teardown()

	// A nil tail keeps the view offline; the filter is applied client-side and
	// never consults CloudWatch.
	logs := ui.NewLogView(app, "logs", "checkout/web", nil, false)
	onUI(app, func() { app.Push(logs) })

	if !responsive(app, 5*time.Second) {
		t.Fatal("event loop wedged after opening the log view")
	}

	screen.InjectKey(tcell.KeyRune, '/', tcell.ModNone)
	if !responsive(app, 5*time.Second) {
		t.Fatal("event loop deadlocked when the log filter prompt opened")
	}

	for _, r := range "timeout" {
		screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}

	// This is the deterministic signal: when the repaint is queued from the
	// loop's own goroutine the first keystroke wedges it, so the filter stalls
	// one character in and no later key is ever seen.
	if !eventually(func() bool { return logs.Filter() == "timeout" }, 5*time.Second) {
		t.Fatalf("log filter stalled at %q, want %q — the event loop stopped consuming keys",
			logs.Filter(), "timeout")
	}
	if !responsive(app, 5*time.Second) {
		t.Fatal("event loop deadlocked while typing a log filter")
	}

	// Esc clears the filter and closes the prompt without wedging either.
	screen.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	if !responsive(app, 5*time.Second) {
		t.Fatal("event loop deadlocked when the filter prompt was cancelled")
	}
	if !eventually(func() bool { return logs.Filter() == "" }, 5*time.Second) {
		t.Errorf("log filter = %q after Esc, want it cleared", logs.Filter())
	}
}

// TestLogViewKeysKeepUIResponsive covers the other log view keys that repaint,
// including the since-window keys that also rewind the tail.
func TestLogViewKeysKeepUIResponsive(t *testing.T) {
	app, screen, teardown := runningApp(t)
	defer teardown()

	logs := ui.NewLogView(app, "logs", "checkout/web", nil, false)
	onUI(app, func() { app.Push(logs) })

	for _, key := range []rune{'s', 't', 'c', 'w', 'g', 'G', '0', '1', '2', '3', '4', '5'} {
		screen.InjectKey(tcell.KeyRune, key, tcell.ModNone)
		if !responsive(app, 5*time.Second) {
			t.Fatalf("event loop deadlocked after pressing %q in the log view", key)
		}
	}

	// Ctrl-R routes through Refresh, which also used to queue from the loop.
	screen.InjectKey(tcell.KeyCtrlR, 0, tcell.ModNone)
	if !responsive(app, 5*time.Second) {
		t.Fatal("event loop deadlocked after Ctrl-R in the log view")
	}

	// The last window key pressed was "5", so the view settles on 24h once the
	// injected keys have drained.
	if !eventually(func() bool { return logs.Since() == 24*time.Hour }, 5*time.Second) {
		t.Errorf("Since() = %v, want the last pressed window (24h)", logs.Since())
	}
}

// TestTableFilterKeepsUIResponsive is the same probe for a table view, so the
// shared prompt plumbing stays covered.
func TestTableFilterKeepsUIResponsive(t *testing.T) {
	app, screen, teardown := runningApp(t)
	defer teardown()

	b := NewClusters(app)
	onUI(app, func() {
		app.Push(b)
		var r rowBuilder
		r.add("prod").addStatus("ACTIVE").addInt(4).addInt(12).addInt(0).addInt(0).
			addPct(10).addPct(20).add("enabled").add("FARGATE").add("arn:prod")
		b.Update([]ui.Row{r.build("arn:prod", model.Cluster{Name: "prod"})})
	})

	screen.InjectKey(tcell.KeyRune, '/', tcell.ModNone)
	for _, r := range "prod" {
		screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}

	// ui.Table is UI-goroutine-only, so its filter is read on the loop rather
	// than from the test goroutine.
	filter := func() string {
		var out string
		onUI(app, func() { out = b.Filter() })
		return out
	}
	if !eventually(func() bool { return filter() == "prod" }, 5*time.Second) {
		t.Fatalf("table filter stalled at %q, want %q", filter(), "prod")
	}
	if !responsive(app, 5*time.Second) {
		t.Fatal("event loop deadlocked while filtering a table")
	}
}

// TestYAMLKeyOpensViewWithoutCrashing reproduces the second reported bug: "y"
// killed the process outright.
//
// yaml.Marshal panicked on the embedded unexported marker struct that every AWS
// SDK shape carries, and tview re-panics after restoring the terminal, so the
// tool simply disappeared.
func TestYAMLKeyOpensViewWithoutCrashing(t *testing.T) {
	app, screen, teardown := runningApp(t)
	defer teardown()

	task := model.Task{
		ID:             "abc123def456",
		ARN:            "arn:aws:ecs:us-east-1:1:task/prod/abc123def456",
		Cluster:        "prod",
		LastStatus:     "RUNNING",
		TaskDefinition: "checkout:42",
		Raw:            sampleRawTask(),
	}

	b := NewTasks(app, awsx.TaskQuery{Cluster: "prod"}, "tasks", "prod")
	onUI(app, func() {
		app.Push(b)
		var r rowBuilder
		r.add(shortID(task.ID)).addStatus("RUNNING").addStatus("HEALTHY").
			addPct(12).addPct(34).add(task.TaskDefinition).add("checkout").
			add("FARGATE").add("1024").add("2048").add("10.0.0.5").
			add("us-east-1a").add("true").add("-").add("2h")
		b.Update([]ui.Row{r.build(task.ARN, task)})
	})

	depth := 0
	onUI(app, func() { depth = app.StackDepth() })

	screen.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)
	if !responsive(app, 5*time.Second) {
		t.Fatal("event loop died opening the YAML view")
	}

	var pushed int
	if !eventually(func() bool {
		onUI(app, func() { pushed = app.StackDepth() })
		return pushed > depth
	}, 5*time.Second) {
		t.Fatalf("stack depth = %d, want the YAML view pushed on top of %d", pushed, depth)
	}

	// Describe is the neighbouring key and shares the push path.
	screen.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	screen.InjectKey(tcell.KeyRune, 'd', tcell.ModNone)
	if !responsive(app, 5*time.Second) {
		t.Fatal("event loop died opening the describe view")
	}
}

// sampleRawTask builds an SDK task shape with the nesting that triggered the
// original panic: structs inside slices inside structs.
func sampleRawTask() ecstypes.Task {
	now := time.Now()
	return ecstypes.Task{
		TaskArn:           aws.String("arn:aws:ecs:us-east-1:1:task/prod/abc123def456"),
		ClusterArn:        aws.String("arn:aws:ecs:us-east-1:1:cluster/prod"),
		TaskDefinitionArn: aws.String("arn:aws:ecs:us-east-1:1:task-definition/checkout:42"),
		LastStatus:        aws.String("RUNNING"),
		DesiredStatus:     aws.String("RUNNING"),
		HealthStatus:      ecstypes.HealthStatusHealthy,
		LaunchType:        ecstypes.LaunchTypeFargate,
		Cpu:               aws.String("1024"),
		Memory:            aws.String("2048"),
		CreatedAt:         aws.Time(now.Add(-2 * time.Hour)),
		StartedAt:         aws.Time(now.Add(-2 * time.Hour)),
		Containers: []ecstypes.Container{{
			Name:            aws.String("web"),
			Image:           aws.String("1.dkr.ecr.us-east-1.amazonaws.com/checkout:1.2.3"),
			LastStatus:      aws.String("RUNNING"),
			ExitCode:        nil,
			NetworkBindings: []ecstypes.NetworkBinding{{ContainerPort: aws.Int32(8080)}},
			NetworkInterfaces: []ecstypes.NetworkInterface{{
				PrivateIpv4Address: aws.String("10.0.0.5"),
			}},
			ManagedAgents: []ecstypes.ManagedAgent{{
				Name:          ecstypes.ManagedAgentNameExecuteCommandAgent,
				LastStatus:    aws.String("RUNNING"),
				LastStartedAt: aws.Time(now),
			}},
		}},
		Attachments: []ecstypes.Attachment{{
			Type:   aws.String("ElasticNetworkInterface"),
			Status: aws.String("ATTACHED"),
			Details: []ecstypes.KeyValuePair{
				{Name: aws.String("privateIPv4Address"), Value: aws.String("10.0.0.5")},
			},
		}},
		Tags: []ecstypes.Tag{{Key: aws.String("env"), Value: aws.String("prod")}},
	}
}

// TestToYAMLOnSDKShapes is the direct guard for the crash: every shape reachable
// from a "y" keypress must render rather than panic.
func TestToYAMLOnSDKShapes(t *testing.T) {
	shapes := map[string]any{
		"task":              sampleRawTask(),
		"container":         sampleRawTask().Containers[0],
		"service":           ecstypes.Service{ServiceName: aws.String("checkout"), DesiredCount: 3},
		"cluster":           ecstypes.Cluster{ClusterName: aws.String("prod"), Status: aws.String("ACTIVE")},
		"taskDefinition":    ecstypes.TaskDefinition{Family: aws.String("checkout"), Revision: 42},
		"containerInstance": ecstypes.ContainerInstance{Ec2InstanceId: aws.String("i-123"), AgentConnected: true},
		"capacityProvider":  ecstypes.CapacityProvider{Name: aws.String("FARGATE")},
		"deployment":        ecstypes.Deployment{Id: aws.String("ecs-svc/1"), DesiredCount: 2},
	}

	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			out := toYAML(shape)

			if strings.Contains(out, "could not render YAML") {
				t.Fatalf("toYAML failed: %s", out)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatal("toYAML produced nothing")
			}
			// The marker struct that caused the panic must not appear.
			if strings.Contains(strings.ToLower(out), "smithy") {
				t.Errorf("SDK serde marker leaked into the output:\n%s", out)
			}
		})
	}
}

func TestToYAMLRendersValuesAndPrunesEmpties(t *testing.T) {
	out := toYAML(sampleRawTask())

	for _, want := range []string{"LastStatus: RUNNING", "Cpu:", "Containers:", "privateIPv4Address"} {
		if !strings.Contains(out, want) {
			t.Errorf("YAML is missing %q:\n%s", want, out)
		}
	}
	// Unset optional fields are dropped: an AWS describe is mostly nils, and a
	// screen of "null" buries what matters.
	if strings.Contains(out, "null") {
		t.Errorf("unset fields should be pruned, not rendered as null:\n%s", out)
	}
}

func TestPrunePreservesMeaningfulZeroValues(t *testing.T) {
	in := map[string]any{
		"ExitCode":     float64(0),
		"DesiredCount": float64(0),
		"Enabled":      false,
		"Missing":      nil,
		"UnsetEnum":    "",
		"EmptyList":    []any{},
		"EmptyMap":     map[string]any{},
		"Nested":       map[string]any{"Keep": "yes", "Drop": nil},
	}

	pruned, ok := prune(in).(map[string]any)
	if !ok {
		t.Fatalf("prune returned %T, want a map", prune(in))
	}

	// false and 0 are real values in an ECS describe, not absence.
	for _, key := range []string{"ExitCode", "DesiredCount", "Enabled"} {
		if _, present := pruned[key]; !present {
			t.Errorf("prune dropped %q, which carries meaning", key)
		}
	}
	for _, key := range []string{"Missing", "UnsetEnum", "EmptyList", "EmptyMap"} {
		if _, present := pruned[key]; present {
			t.Errorf("prune kept unset key %q", key)
		}
	}

	nested, ok := pruned["Nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested map was dropped: %+v", pruned)
	}
	if _, present := nested["Drop"]; present {
		t.Error("prune did not recurse into nested maps")
	}
}

func TestToYAMLRecoversFromUnencodableValues(t *testing.T) {
	// A channel cannot be encoded; the view must report it, not crash.
	out := toYAML(struct{ Ch chan int }{Ch: make(chan int)})
	if !strings.Contains(out, "could not render YAML") {
		t.Errorf("toYAML = %q, want a rendered error", out)
	}
}
