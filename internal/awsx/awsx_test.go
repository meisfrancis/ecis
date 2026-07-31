package awsx

import (
	"math"
	"testing"
	"time"

	"github.com/meisfrancis/ecis/internal/model"
)

func TestShortARN(t *testing.T) {
	tests := []struct{ in, want string }{
		{"arn:aws:ecs:us-east-1:1234:service/prod/web", "web"},
		{"arn:aws:ecs:us-east-1:1234:task/prod/abc123", "abc123"},
		{"plain-name", "plain-name"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := ShortARN(tc.in); got != tc.want {
			t.Errorf("ShortARN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitTaskDef(t *testing.T) {
	family, rev := splitTaskDef("my-app:42")
	if family != "my-app" || rev != "42" {
		t.Errorf("splitTaskDef = (%q, %q), want (my-app, 42)", family, rev)
	}
	family, rev = splitTaskDef("my-app")
	if family != "my-app" || rev != "" {
		t.Errorf("splitTaskDef without revision = (%q, %q), want (my-app, \"\")", family, rev)
	}
}

func TestDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "n/a"},
		{-time.Second, "n/a"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},
		{5 * time.Minute, "5m"},
		{time.Hour + 30*time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{50 * time.Hour, "2d02h"},
		{48 * time.Hour, "2d"},
		{400 * 24 * time.Hour, "1y35d"},
	}
	for _, tc := range tests {
		if got := Duration(tc.in); got != tc.want {
			t.Errorf("Duration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSinceHandlesZeroTime(t *testing.T) {
	if got := Since(time.Time{}); got != "n/a" {
		t.Errorf("Since(zero) = %q, want n/a", got)
	}
}

func TestParseCPUUnits(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"512", 512},
		{"1024", 1024},
		{"1 vCPU", 1024},
		{".5 vcpu", 512},
		{"", 0},
		{"garbage", 0},
	}
	for _, tc := range tests {
		if got := parseCPUUnits(tc.in); got != tc.want {
			t.Errorf("parseCPUUnits(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseMemoryMiB(t *testing.T) {
	tests := []struct {
		in   string
		want float64
	}{
		{"2048", 2048},
		{"2GB", 2048},
		{"512 MB", 512},
		{"", 0},
		{"garbage", 0},
	}
	for _, tc := range tests {
		if got := parseMemoryMiB(tc.in); got != tc.want {
			t.Errorf("parseMemoryMiB(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTaskCostPerHour(t *testing.T) {
	rate := FargateRate{VCPUHour: 0.04, GBHour: 0.004}

	fargate := model.Task{LaunchType: "FARGATE", CPU: "1024", Memory: "2048", LastStatus: "RUNNING"}
	want := 1*0.04 + 2*0.004
	if got := TaskCostPerHour(fargate, rate); math.Abs(got-want) > 1e-9 {
		t.Errorf("TaskCostPerHour(fargate) = %v, want %v", got, want)
	}

	// Fargate Spot arrives as a capacity provider with an empty launch type.
	spot := model.Task{CapacityProv: "FARGATE_SPOT", CPU: "256", Memory: "512"}
	if got := TaskCostPerHour(spot, rate); got <= 0 {
		t.Errorf("TaskCostPerHour(spot) = %v, want a positive estimate", got)
	}

	// EC2 capacity is billed as instances, not per task.
	ec2 := model.Task{LaunchType: "EC2", CPU: "1024", Memory: "2048"}
	if got := TaskCostPerHour(ec2, rate); got != 0 {
		t.Errorf("TaskCostPerHour(ec2) = %v, want 0", got)
	}
}

func TestRateForFallsBackAndHonoursOverrides(t *testing.T) {
	known := RateFor("us-east-1", nil)
	if known.VCPUHour <= 0 || known.GBHour <= 0 {
		t.Fatalf("RateFor(us-east-1) = %+v, want populated rates", known)
	}
	if unknown := RateFor("mars-central-1", nil); unknown != defaultFargateRate {
		t.Errorf("RateFor(unknown region) = %+v, want the default rate", unknown)
	}

	override := FargateRate{VCPUHour: 1, GBHour: 2}
	if got := RateFor("us-east-1", map[string]FargateRate{"us-east-1": override}); got != override {
		t.Errorf("RateFor with override = %+v, want %+v", got, override)
	}
}

func TestEstimateReportGroupsRunningFargateTasks(t *testing.T) {
	tasks := []model.Task{
		{Service: "web", LaunchType: "FARGATE", CPU: "1024", Memory: "2048", LastStatus: "RUNNING", Cluster: "prod"},
		{Service: "web", LaunchType: "FARGATE", CPU: "1024", Memory: "2048", LastStatus: "RUNNING", Cluster: "prod"},
		{Service: "api", LaunchType: "FARGATE", CPU: "256", Memory: "512", LastStatus: "RUNNING", Cluster: "prod"},
		// Stopped tasks and EC2 tasks contribute nothing.
		{Service: "batch", LaunchType: "FARGATE", CPU: "4096", Memory: "8192", LastStatus: "STOPPED", Cluster: "prod"},
		{Service: "legacy", LaunchType: "EC2", CPU: "1024", Memory: "2048", LastStatus: "RUNNING", Cluster: "prod"},
	}

	report := EstimateReport(tasks, "us-east-1", nil, GroupByService)
	if !report.Estimated {
		t.Error("EstimateReport should mark the report as estimated")
	}
	if len(report.Groups) != 2 {
		t.Fatalf("groups = %d, want 2 (web, api); got %+v", len(report.Groups), report.Groups)
	}
	// Groups are sorted most expensive first, and web runs two of the larger tasks.
	if report.Groups[0].Key != "web" {
		t.Errorf("first group = %q, want web", report.Groups[0].Key)
	}
	if report.Groups[0].Total <= report.Groups[1].Total {
		t.Errorf("groups are not sorted by cost: %+v", report.Groups)
	}
	if math.Abs(report.Total-(report.Groups[0].Total+report.Groups[1].Total)) > 1e-9 {
		t.Errorf("report total %v does not match the sum of its groups", report.Total)
	}
}

func TestEstimateReportGroupsByCluster(t *testing.T) {
	tasks := []model.Task{
		{Service: "web", Cluster: "prod", LaunchType: "FARGATE", CPU: "512", Memory: "1024", LastStatus: "RUNNING"},
		{Service: "api", Cluster: "prod", LaunchType: "FARGATE", CPU: "512", Memory: "1024", LastStatus: "RUNNING"},
		{Service: "web", Cluster: "staging", LaunchType: "FARGATE", CPU: "512", Memory: "1024", LastStatus: "RUNNING"},
	}
	report := EstimateReport(tasks, "us-east-1", nil, GroupByCluster)
	if len(report.Groups) != 2 {
		t.Fatalf("groups = %d, want one per cluster", len(report.Groups))
	}
	if report.Groups[0].Key != "prod" {
		t.Errorf("first group = %q, want prod (two tasks)", report.Groups[0].Key)
	}
}

func TestAlignPointsFillsGaps(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 7, d, 0, 0, 0, 0, time.UTC) }
	buckets := []time.Time{day(1), day(2), day(3)}

	g := &model.CostGroup{
		Key:    "web",
		Points: []model.CostPoint{{Start: day(2), Amount: 5}},
	}
	alignPoints(g, buckets)

	if len(g.Points) != 3 {
		t.Fatalf("points = %d, want one per bucket", len(g.Points))
	}
	if g.Points[0].Amount != 0 || g.Points[2].Amount != 0 {
		t.Errorf("missing buckets should be zero-filled, got %+v", g.Points)
	}
	if g.Points[1].Amount != 5 {
		t.Errorf("existing bucket lost its amount: %+v", g.Points)
	}
}

func TestGroupKeyUnwrapsTags(t *testing.T) {
	opts := CostOptions{}
	tests := []struct {
		in   []string
		want string
	}{
		{[]string{"ecs:service-name$web"}, "web"},
		{[]string{"ecs:service-name$"}, "(untagged)"},
		{[]string{"Amazon Elastic Container Service"}, "Amazon Elastic Container Service"},
		{nil, "Total"},
	}
	for _, tc := range tests {
		if got := groupKey(tc.in, opts); got != tc.want {
			t.Errorf("groupKey(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDedupeTimes(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 7, d, 0, 0, 0, 0, time.UTC) }
	got := dedupeTimes([]time.Time{day(1), day(1), day(2), day(2), day(3)})
	if len(got) != 3 {
		t.Fatalf("dedupeTimes = %v, want 3 distinct days", got)
	}
}

func TestCostFilterCombinesClauses(t *testing.T) {
	// ECS-only with no cluster and credits excluded: service filter AND the
	// record-type exclusion.
	f := costFilter(CostOptions{})
	if f == nil || len(f.And) != 2 {
		t.Fatalf("costFilter = %+v, want two ANDed clauses", f)
	}

	// Including credits leaves a single clause, which must not be wrapped.
	f = costFilter(CostOptions{IncludeCredits: true})
	if f == nil || f.Dimensions == nil {
		t.Fatalf("costFilter with credits = %+v, want a bare dimension filter", f)
	}

	// A cluster scope adds the tag clause.
	f = costFilter(CostOptions{Cluster: "prod", ClusterTagKey: "ecs:cluster-name"})
	if f == nil || len(f.And) != 3 {
		t.Fatalf("costFilter with cluster = %+v, want three ANDed clauses", f)
	}
}

func TestRatioPct(t *testing.T) {
	if got := ratioPct(50, 200); got != 25 {
		t.Errorf("ratioPct(50, 200) = %v, want 25", got)
	}
	for _, tc := range [][2]float64{{math.NaN(), 100}, {50, math.NaN()}, {50, 0}} {
		if got := ratioPct(tc[0], tc[1]); !math.IsNaN(got) {
			t.Errorf("ratioPct(%v, %v) = %v, want NaN", tc[0], tc[1], got)
		}
	}
}

func TestSortSeriesOrdersByTimestamp(t *testing.T) {
	now := time.Now()
	s := model.Series{
		Timestamps: []time.Time{now.Add(2 * time.Minute), now, now.Add(time.Minute)},
		Values:     []float64{3, 1, 2},
	}
	sortSeries(&s)

	for i, want := range []float64{1, 2, 3} {
		if s.Values[i] != want {
			t.Fatalf("sortSeries produced %v, want ascending by time", s.Values)
		}
	}
}

func TestSortSeriesTruncatesMismatchedLengths(t *testing.T) {
	now := time.Now()
	s := model.Series{
		Timestamps: []time.Time{now, now.Add(time.Minute), now.Add(2 * time.Minute)},
		Values:     []float64{1},
	}
	sortSeries(&s)

	if len(s.Timestamps) != len(s.Values) {
		t.Errorf("sortSeries left %d timestamps and %d values", len(s.Timestamps), len(s.Values))
	}
}

func TestRatioSeriesAlignsOnTimestamp(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	used := model.Series{
		Timestamps: []time.Time{now, now.Add(time.Minute), now.Add(2 * time.Minute)},
		Values:     []float64{50, 25, 10},
	}
	reserved := model.Series{
		// The middle sample is missing, and there is an extra trailing one.
		Timestamps: []time.Time{now, now.Add(2 * time.Minute), now.Add(3 * time.Minute)},
		Values:     []float64{100, 100, 100},
	}

	got := ratioSeries(used, reserved, "CPU %")
	if len(got.Values) != 2 {
		t.Fatalf("ratioSeries = %v, want only the aligned samples", got.Values)
	}
	if got.Values[0] != 50 || got.Values[1] != 10 {
		t.Errorf("ratioSeries = %v, want [50 10]", got.Values)
	}
}

func TestLogStreamName(t *testing.T) {
	if got := LogStreamName("ecs", "web", "abc123"); got != "ecs/web/abc123" {
		t.Errorf("LogStreamName = %q, want ecs/web/abc123", got)
	}
	if got := LogStreamName("", "web", "abc123"); got != "web/abc123" {
		t.Errorf("LogStreamName without prefix = %q, want web/abc123", got)
	}
}

func TestLogTailEvictsOldEventIDs(t *testing.T) {
	tail := &LogTail{seen: map[string]struct{}{}, maxSeen: 3}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		tail.remember(id)
	}

	if len(tail.seen) != 3 {
		t.Fatalf("seen set holds %d ids, want the cap of 3", len(tail.seen))
	}
	if _, ok := tail.seen["a"]; ok {
		t.Error("oldest event id was not evicted")
	}
	if _, ok := tail.seen["e"]; !ok {
		t.Error("newest event id was evicted")
	}
}

func TestContainerInsightsEnabled(t *testing.T) {
	tests := []struct {
		setting string
		want    bool
	}{
		{"enabled", true},
		{"enhanced", true},
		{"ENABLED", true},
		{"disabled", false},
		{"", false},
	}
	for _, tc := range tests {
		c := model.Cluster{Settings: map[string]string{"containerInsights": tc.setting}}
		if got := ContainerInsightsEnabled(c); got != tc.want {
			t.Errorf("ContainerInsightsEnabled(%q) = %v, want %v", tc.setting, got, tc.want)
		}
	}
}

func TestExecCommandRequiresTooling(t *testing.T) {
	req := ExecRequest{Cluster: "prod", Task: "abc", Container: "web", Region: "us-east-1"}
	bin, args, err := req.ExecCommand()
	if err != nil {
		// Neither the AWS CLI nor the session manager plugin is installed here,
		// which must be reported rather than producing a broken command line.
		t.Skipf("exec tooling unavailable: %v", err)
	}
	if bin == "" {
		t.Fatal("ExecCommand returned an empty binary")
	}
	joined := args
	if len(joined) < 6 || joined[0] != "ecs" || joined[1] != "execute-command" {
		t.Errorf("ExecCommand args = %v, want an ecs execute-command invocation", args)
	}
}
