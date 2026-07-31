package model

import (
	"math"
	"testing"
	"time"
)

func TestSeriesStatisticsSkipGaps(t *testing.T) {
	s := Series{Values: []float64{10, math.NaN(), 30, 20}}

	if got := s.Last(); got != 20 {
		t.Errorf("Last() = %v, want 20", got)
	}
	if got := s.Min(); got != 10 {
		t.Errorf("Min() = %v, want 10", got)
	}
	if got := s.Max(); got != 30 {
		t.Errorf("Max() = %v, want 30", got)
	}
	if got := s.Avg(); got != 20 {
		t.Errorf("Avg() = %v, want 20 (gaps excluded)", got)
	}
	if s.Empty() {
		t.Error("Empty() = true for a series with real samples")
	}
}

func TestEmptySeries(t *testing.T) {
	var s Series
	if !s.Empty() {
		t.Error("a series with no values should be empty")
	}
	if !math.IsNaN(s.Last()) || !math.IsNaN(s.Avg()) || !math.IsNaN(s.Min()) {
		t.Error("statistics over an empty series should be NaN")
	}

	gaps := Series{Values: []float64{math.NaN(), math.NaN()}}
	if !gaps.Empty() {
		t.Error("a series of only gaps should be empty")
	}
}

func TestServiceHealthy(t *testing.T) {
	tests := []struct {
		name string
		svc  Service
		want bool
	}{
		{"settled", Service{Desired: 2, Running: 2, RolloutState: "COMPLETED"}, true},
		{"no rollout state reported", Service{Desired: 1, Running: 1}, true},
		{"still converging", Service{Desired: 3, Running: 2}, false},
		{"pending tasks", Service{Desired: 2, Running: 2, Pending: 1}, false},
		{"rollout in flight", Service{Desired: 2, Running: 2, RolloutState: "IN_PROGRESS"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.svc.Healthy(); got != tc.want {
				t.Errorf("Healthy() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTaskAge(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)

	running := Task{StartedAt: start}
	if got := running.Age(); got < 2*time.Hour {
		t.Errorf("Age() = %v, want at least 2h", got)
	}

	// A stopped task's age is measured to when it stopped, not to now.
	stopped := Task{StartedAt: start, StoppedAt: start.Add(30 * time.Minute)}
	if got := stopped.Age(); got != 30*time.Minute {
		t.Errorf("Age() of a stopped task = %v, want 30m", got)
	}

	// Tasks that never started fall back to their creation time.
	created := Task{CreatedAt: start}
	if got := created.Age(); got < 2*time.Hour {
		t.Errorf("Age() from CreatedAt = %v, want at least 2h", got)
	}

	if got := (Task{}).Age(); got != 0 {
		t.Errorf("Age() with no timestamps = %v, want 0", got)
	}
}

func TestInstanceUtilization(t *testing.T) {
	i := Instance{CPURegistered: 4096, CPURemaining: 1024, MemRegistered: 8192, MemRemaining: 8192}

	if got := i.CPUUsedPct(); got != 75 {
		t.Errorf("CPUUsedPct() = %v, want 75", got)
	}
	if got := i.MemUsedPct(); got != 0 {
		t.Errorf("MemUsedPct() = %v, want 0", got)
	}
	// An unregistered instance must not divide by zero.
	if got := (Instance{}).CPUUsedPct(); got != 0 {
		t.Errorf("CPUUsedPct() with no capacity = %v, want 0", got)
	}
}

func TestCostGroupWindows(t *testing.T) {
	g := CostGroup{Points: []CostPoint{
		{Amount: 1}, {Amount: 2}, {Amount: 3}, {Amount: 4}, {Amount: 10},
	}}

	if got := g.Latest(); got != 10 {
		t.Errorf("Latest() = %v, want 10", got)
	}
	if got := g.Window(3); got != 17 {
		t.Errorf("Window(3) = %v, want 17", got)
	}
	// A window larger than the history sums everything available.
	if got := g.Window(99); got != 20 {
		t.Errorf("Window(99) = %v, want 20", got)
	}
	if got := g.Trend(); got != 150 {
		t.Errorf("Trend() = %v, want 150 (4 → 10)", got)
	}
	if got := (CostGroup{}).Latest(); got != 0 {
		t.Errorf("Latest() on an empty group = %v, want 0", got)
	}
	// A zero previous bucket must not produce an infinite trend.
	zero := CostGroup{Points: []CostPoint{{Amount: 0}, {Amount: 5}}}
	if got := zero.Trend(); got != 0 {
		t.Errorf("Trend() from a zero baseline = %v, want 0", got)
	}
}

func TestCostReportSortByTotal(t *testing.T) {
	r := CostReport{Groups: []CostGroup{
		{Key: "small", Total: 1},
		{Key: "large", Total: 100},
		{Key: "medium", Total: 10},
	}}
	r.SortByTotal()

	want := []string{"large", "medium", "small"}
	for i, key := range want {
		if r.Groups[i].Key != key {
			t.Fatalf("SortByTotal() produced %v, want %v", keys(r.Groups), want)
		}
	}
}

func TestClusterContainerInsights(t *testing.T) {
	enabled := Cluster{Settings: map[string]string{"containerInsights": "enabled"}}
	if got := enabled.ContainerInsights(); got != "enabled" {
		t.Errorf("ContainerInsights() = %q, want enabled", got)
	}
	if got := (Cluster{}).ContainerInsights(); got != "disabled" {
		t.Errorf("ContainerInsights() with no setting = %q, want disabled", got)
	}
}

func TestTaskDefName(t *testing.T) {
	if got := (TaskDef{Family: "my-app", Revision: 42}).Name(); got != "my-app:42" {
		t.Errorf("Name() = %q, want my-app:42", got)
	}
}

func keys(groups []CostGroup) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.Key
	}
	return out
}
