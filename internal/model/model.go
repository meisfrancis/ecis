// Package model holds the domain types ecis renders. They are deliberately
// flattened views over the AWS SDK shapes: the UI never reaches into an SDK
// struct, and each type keeps its raw counterpart around for the describe/YAML
// views.
package model

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Cluster is an ECS cluster.
type Cluster struct {
	Name              string
	ARN               string
	Status            string
	ActiveServices    int
	RunningTasks      int
	PendingTasks      int
	Instances         int
	CapacityProviders []string
	Settings          map[string]string
	Statistics        map[string]string
	Tags              map[string]string
	Raw               any
}

// ContainerInsights reports whether the cluster streams enhanced metrics.
func (c Cluster) ContainerInsights() string {
	if v, ok := c.Settings["containerInsights"]; ok && v != "" {
		return v
	}
	return "disabled"
}

// Service is an ECS service.
type Service struct {
	Name           string
	ARN            string
	Cluster        string
	Status         string
	LaunchType     string
	TaskDefinition string
	Scheduling     string
	Desired        int
	Running        int
	Pending        int
	Deployments    int
	RolloutState   string
	RolloutReason  string
	PlatformVer    string
	CPU            string
	Memory         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Events         []Event
	Tags           map[string]string
	Raw            any
}

// Healthy reports whether the running count has caught up with the desired
// count and the newest deployment has settled.
func (s Service) Healthy() bool {
	if s.Desired != s.Running || s.Pending > 0 {
		return false
	}
	return s.RolloutState == "" || strings.EqualFold(s.RolloutState, "COMPLETED")
}

// Task is a running or stopped ECS task.
type Task struct {
	ID             string
	ARN            string
	Cluster        string
	Group          string
	Service        string
	TaskDefinition string
	Family         string
	Revision       string
	LastStatus     string
	DesiredStatus  string
	HealthStatus   string
	LaunchType     string
	CapacityProv   string
	PlatformVer    string
	AZ             string
	ContainerInst  string
	ConnectivityIP string
	CPU            string
	Memory         string
	StoppedReason  string
	StopCode       string
	ExecEnabled    bool
	CreatedAt      time.Time
	StartedAt      time.Time
	StoppedAt      time.Time
	Containers     []Container
	Tags           map[string]string
	Raw            any
}

// Age returns how long the task has been alive, measuring to the stop time for
// tasks that have already exited.
func (t Task) Age() time.Duration {
	start := t.StartedAt
	if start.IsZero() {
		start = t.CreatedAt
	}
	if start.IsZero() {
		return 0
	}
	if !t.StoppedAt.IsZero() {
		return t.StoppedAt.Sub(start)
	}
	return time.Since(start)
}

// Container is one container inside a task.
type Container struct {
	Name         string
	TaskID       string
	TaskARN      string
	Cluster      string
	Image        string
	ImageDigest  string
	LastStatus   string
	HealthStatus string
	ARN          string
	RuntimeID    string
	CPU          string
	Memory       string
	MemoryRes    string
	ExitCode     *int32
	Reason       string
	Ports        []string
	Networks     []string
	LogGroup     string
	LogStream    string
	LogRegion    string
	Raw          any
}

// TaskDef is one revision of a task definition.
type TaskDef struct {
	Family       string
	Revision     int
	ARN          string
	Status       string
	CPU          string
	Memory       string
	NetworkMode  string
	Requires     []string
	Containers   []string
	TaskRole     string
	ExecRole     string
	RegisteredAt time.Time
	RegisteredBy string
	Raw          any
}

// Name renders the familiar family:revision form.
func (t TaskDef) Name() string { return fmt.Sprintf("%s:%d", t.Family, t.Revision) }

// Instance is a container instance backing an EC2-launch-type cluster.
type Instance struct {
	ID             string
	ARN            string
	EC2ID          string
	Cluster        string
	Status         string
	AgentConnected bool
	AgentVersion   string
	DockerVersion  string
	RunningTasks   int
	PendingTasks   int
	CPURegistered  int
	CPURemaining   int
	MemRegistered  int
	MemRemaining   int
	AZ             string
	InstanceType   string
	CapacityProv   string
	RegisteredAt   time.Time
	Raw            any
}

// CPUUsedPct is the share of registered CPU units already reserved.
func (i Instance) CPUUsedPct() float64 { return pct(i.CPURegistered-i.CPURemaining, i.CPURegistered) }

// MemUsedPct is the share of registered memory already reserved.
func (i Instance) MemUsedPct() float64 { return pct(i.MemRegistered-i.MemRemaining, i.MemRegistered) }

func pct(used, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// Event is a service event.
type Event struct {
	ID        string
	Service   string
	Message   string
	CreatedAt time.Time
}

// Deployment is one deployment of a service.
type Deployment struct {
	ID             string
	Service        string
	Status         string
	TaskDefinition string
	Desired        int
	Running        int
	Pending        int
	Failed         int
	RolloutState   string
	RolloutReason  string
	LaunchType     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Raw            any
}

// CapacityProvider is a cluster capacity provider.
type CapacityProvider struct {
	Name               string
	ARN                string
	Status             string
	AutoScalingGroup   string
	ManagedScaling     string
	ManagedTermination string
	TargetCapacity     int
	UpdateStatus       string
	Tags               map[string]string
	Raw                any
}

// Series is a named CloudWatch time series.
type Series struct {
	Label      string
	Unit       string
	Timestamps []time.Time
	Values     []float64
}

// Last returns the most recent sample, or NaN when the series is empty.
func (s Series) Last() float64 {
	if len(s.Values) == 0 {
		return math.NaN()
	}
	return s.Values[len(s.Values)-1]
}

// Min, Max and Avg summarise the series, skipping gaps.
func (s Series) Min() float64 { return reduce(s.Values, math.Inf(1), math.Min) }
func (s Series) Max() float64 { return reduce(s.Values, math.Inf(-1), math.Max) }

// Avg returns the arithmetic mean of the samples.
func (s Series) Avg() float64 {
	var sum float64
	var n int
	for _, v := range s.Values {
		if math.IsNaN(v) {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

// Empty reports whether the series carries no usable samples.
func (s Series) Empty() bool {
	for _, v := range s.Values {
		if !math.IsNaN(v) {
			return false
		}
	}
	return true
}

func reduce(vs []float64, seed float64, fn func(a, b float64) float64) float64 {
	out := seed
	var seen bool
	for _, v := range vs {
		if math.IsNaN(v) {
			continue
		}
		out, seen = fn(out, v), true
	}
	if !seen {
		return math.NaN()
	}
	return out
}

// Utilization pairs the CPU and memory series for one entity.
type Utilization struct {
	Key    string
	CPU    Series
	Memory Series
	Extra  map[string]Series
}

// CostPoint is spend for one group over one time bucket.
type CostPoint struct {
	Start  time.Time
	End    time.Time
	Amount float64
}

// CostGroup is a cost breakdown row: one dimension value with its history.
type CostGroup struct {
	Key       string
	Unit      string
	Points    []CostPoint
	Total     float64
	Estimated bool
}

// Amounts returns just the numeric series, oldest first.
func (g CostGroup) Amounts() []float64 {
	out := make([]float64, len(g.Points))
	for i, p := range g.Points {
		out[i] = p.Amount
	}
	return out
}

// Window sums the most recent n buckets.
func (g CostGroup) Window(n int) float64 {
	var sum float64
	for i := len(g.Points) - 1; i >= 0 && n > 0; i, n = i-1, n-1 {
		sum += g.Points[i].Amount
	}
	return sum
}

// Latest returns the newest bucket's spend.
func (g CostGroup) Latest() float64 {
	if len(g.Points) == 0 {
		return 0
	}
	return g.Points[len(g.Points)-1].Amount
}

// Trend returns the percent change between the two most recent buckets.
func (g CostGroup) Trend() float64 {
	if len(g.Points) < 2 {
		return 0
	}
	prev := g.Points[len(g.Points)-2].Amount
	if prev == 0 {
		return 0
	}
	return (g.Points[len(g.Points)-1].Amount - prev) / prev * 100
}

// CostReport is a full cost breakdown plus its totals.
type CostReport struct {
	GroupBy   string
	Unit      string
	Start     time.Time
	End       time.Time
	Buckets   []time.Time
	Groups    []CostGroup
	Total     float64
	Forecast  float64
	Estimated bool
	Note      string
}

// SortByTotal orders the groups most expensive first.
func (r *CostReport) SortByTotal() {
	sort.SliceStable(r.Groups, func(i, j int) bool { return r.Groups[i].Total > r.Groups[j].Total })
}
