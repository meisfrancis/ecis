package awsx

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/meisfrancis/ecis/internal/model"
)

// CloudWatch namespaces ecis reads.
//
// NamespaceECS is always populated for services; NamespaceInsights only exists
// when Container Insights is enabled on the cluster, and its per-task metrics
// additionally require enhanced observability. Every query degrades to "no
// data" rather than an error so the UI stays usable either way.
const (
	NamespaceECS      = "AWS/ECS"
	NamespaceInsights = "ECS/ContainerInsights"
)

// maxMetricQueries is the GetMetricData limit on queries per request.
const maxMetricQueries = 500

// Sample is a point-in-time utilization reading.
type Sample struct {
	// CPU and Memory are percentages, or NaN when CloudWatch had no data.
	CPU    float64
	Memory float64
	// CPUUnits and MemoryMB are absolute usage, available from Container
	// Insights only.
	CPUUnits float64
	MemoryMB float64
	// Source names the namespace the reading came from.
	Source string
}

// NoSample is the zero reading: everything unknown.
func NoSample() Sample {
	return Sample{CPU: math.NaN(), Memory: math.NaN(), CPUUnits: math.NaN(), MemoryMB: math.NaN()}
}

// OK reports whether the sample carries any usable number.
func (s Sample) OK() bool { return !math.IsNaN(s.CPU) || !math.IsNaN(s.Memory) }

// MetricQuery is one CloudWatch series request.
type MetricQuery struct {
	// Key groups results: every query sharing a key lands in the same bucket.
	Key       string
	Label     string
	Namespace string
	Name      string
	Stat      string
	Dims      map[string]string
}

func (q MetricQuery) dimensions() []cwtypes.Dimension {
	names := make([]string, 0, len(q.Dims))
	for k := range q.Dims {
		names = append(names, k)
	}
	sort.Strings(names)

	out := make([]cwtypes.Dimension, 0, len(names))
	for _, n := range names {
		out = append(out, cwtypes.Dimension{Name: aws.String(n), Value: aws.String(q.Dims[n])})
	}
	return out
}

// GetSeries runs a batch of metric queries and returns one series per query, in
// the order the queries were given. Series with no data come back empty rather
// than missing, so callers can index straight into the result.
func (c *Client) GetSeries(ctx context.Context, queries []MetricQuery, start, end time.Time, period time.Duration) ([]model.Series, error) {
	out := make([]model.Series, len(queries))
	for i, q := range queries {
		out[i] = model.Series{Label: q.Label}
	}
	if len(queries) == 0 {
		return out, nil
	}

	secs := int32(period.Seconds())
	if secs < 1 {
		secs = 60
	}

	for from := 0; from < len(queries); from += maxMetricQueries {
		to := from + maxMetricQueries
		if to > len(queries) {
			to = len(queries)
		}
		chunk := queries[from:to]

		mdq := make([]cwtypes.MetricDataQuery, 0, len(chunk))
		index := make(map[string]int, len(chunk))
		for i, q := range chunk {
			// GetMetricData ids must be unique and start with a lowercase letter.
			id := fmt.Sprintf("m%d", i)
			index[id] = from + i
			stat := q.Stat
			if stat == "" {
				stat = "Average"
			}
			mdq = append(mdq, cwtypes.MetricDataQuery{
				Id:    aws.String(id),
				Label: aws.String(q.Label),
				MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{
						Namespace:  aws.String(q.Namespace),
						MetricName: aws.String(q.Name),
						Dimensions: q.dimensions(),
					},
					Period: aws.Int32(secs),
					Stat:   aws.String(stat),
				},
				ReturnData: aws.Bool(true),
			})
		}

		in := &cloudwatch.GetMetricDataInput{
			MetricDataQueries: mdq,
			StartTime:         aws.Time(start),
			EndTime:           aws.Time(end),
			ScanBy:            cwtypes.ScanByTimestampAscending,
		}
		for {
			res, err := c.CW.GetMetricData(ctx, in)
			if err != nil {
				return nil, fmt.Errorf("get metric data: %w", err)
			}
			for _, r := range res.MetricDataResults {
				i, ok := index[aws.ToString(r.Id)]
				if !ok {
					continue
				}
				out[i].Timestamps = append(out[i].Timestamps, r.Timestamps...)
				out[i].Values = append(out[i].Values, r.Values...)
			}
			if res.NextToken == nil {
				break
			}
			in.NextToken = res.NextToken
		}
	}

	for i := range out {
		sortSeries(&out[i])
	}
	return out, nil
}

// sortSeries orders samples oldest-first. Pagination can interleave pages, so
// the ScanBy hint alone is not enough to guarantee ordering.
func sortSeries(s *model.Series) {
	if len(s.Timestamps) != len(s.Values) {
		// Defensive: never let a malformed response desynchronise the pair.
		n := len(s.Timestamps)
		if len(s.Values) < n {
			n = len(s.Values)
		}
		s.Timestamps, s.Values = s.Timestamps[:n], s.Values[:n]
	}
	idx := make([]int, len(s.Timestamps))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return s.Timestamps[idx[a]].Before(s.Timestamps[idx[b]]) })

	ts := make([]time.Time, len(idx))
	vs := make([]float64, len(idx))
	for i, j := range idx {
		ts[i], vs[i] = s.Timestamps[j], s.Values[j]
	}
	s.Timestamps, s.Values = ts, vs
}

// latest returns the newest sample of a series, or NaN when it is empty.
func latest(s model.Series) float64 {
	if len(s.Values) == 0 {
		return math.NaN()
	}
	return s.Values[len(s.Values)-1]
}

// sampleWindow is how far back a snapshot query looks. CloudWatch publishes ECS
// metrics a minute or two behind real time, so a single period is not enough to
// reliably catch a datapoint.
func sampleWindow(period time.Duration) (time.Time, time.Time) {
	end := time.Now()
	return end.Add(-5 * period), end
}

// ServiceSamples returns the latest CPU and memory utilization for each named
// service, keyed by service name.
func (c *Client) ServiceSamples(ctx context.Context, cluster string, services []string, period time.Duration) (map[string]Sample, error) {
	out := map[string]Sample{}
	if cluster == "" || len(services) == 0 {
		return out, nil
	}

	var queries []MetricQuery
	for _, svc := range services {
		dims := map[string]string{"ClusterName": cluster, "ServiceName": svc}
		queries = append(queries,
			MetricQuery{Key: svc, Label: "cpu", Namespace: NamespaceECS, Name: "CPUUtilization", Stat: "Average", Dims: dims},
			MetricQuery{Key: svc, Label: "mem", Namespace: NamespaceECS, Name: "MemoryUtilization", Stat: "Average", Dims: dims},
		)
	}

	start, end := sampleWindow(period)
	series, err := c.GetSeries(ctx, queries, start, end, period)
	if err != nil {
		return nil, err
	}

	for _, svc := range services {
		out[svc] = NoSample()
	}
	for i, q := range queries {
		s := out[q.Key]
		switch q.Label {
		case "cpu":
			s.CPU = latest(series[i])
		case "mem":
			s.Memory = latest(series[i])
		}
		s.Source = NamespaceECS
		out[q.Key] = s
	}
	return out, nil
}

// ClusterSamples returns cluster-wide utilization. It prefers Container
// Insights (which reports utilized-versus-reserved across every launch type)
// and falls back to AWS/ECS, which only publishes cluster-level utilization for
// EC2 capacity.
func (c *Client) ClusterSamples(ctx context.Context, clusters []string, period time.Duration) (map[string]Sample, error) {
	out := map[string]Sample{}
	if len(clusters) == 0 {
		return out, nil
	}

	var queries []MetricQuery
	for _, cl := range clusters {
		dims := map[string]string{"ClusterName": cl}
		queries = append(queries,
			MetricQuery{Key: cl, Label: "ci_cpu_used", Namespace: NamespaceInsights, Name: "CpuUtilized", Stat: "Sum", Dims: dims},
			MetricQuery{Key: cl, Label: "ci_cpu_res", Namespace: NamespaceInsights, Name: "CpuReserved", Stat: "Sum", Dims: dims},
			MetricQuery{Key: cl, Label: "ci_mem_used", Namespace: NamespaceInsights, Name: "MemoryUtilized", Stat: "Sum", Dims: dims},
			MetricQuery{Key: cl, Label: "ci_mem_res", Namespace: NamespaceInsights, Name: "MemoryReserved", Stat: "Sum", Dims: dims},
			MetricQuery{Key: cl, Label: "ecs_cpu", Namespace: NamespaceECS, Name: "CPUUtilization", Stat: "Average", Dims: dims},
			MetricQuery{Key: cl, Label: "ecs_mem", Namespace: NamespaceECS, Name: "MemoryUtilization", Stat: "Average", Dims: dims},
		)
	}

	start, end := sampleWindow(period)
	series, err := c.GetSeries(ctx, queries, start, end, period)
	if err != nil {
		return nil, err
	}

	type raw struct {
		cpuUsed, cpuRes, memUsed, memRes, ecsCPU, ecsMem float64
	}
	acc := map[string]*raw{}
	for _, cl := range clusters {
		acc[cl] = &raw{math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN()}
	}
	for i, q := range queries {
		r := acc[q.Key]
		v := latest(series[i])
		switch q.Label {
		case "ci_cpu_used":
			r.cpuUsed = v
		case "ci_cpu_res":
			r.cpuRes = v
		case "ci_mem_used":
			r.memUsed = v
		case "ci_mem_res":
			r.memRes = v
		case "ecs_cpu":
			r.ecsCPU = v
		case "ecs_mem":
			r.ecsMem = v
		}
	}

	for cl, r := range acc {
		s := NoSample()
		s.Source = NamespaceInsights
		s.CPU = ratioPct(r.cpuUsed, r.cpuRes)
		s.Memory = ratioPct(r.memUsed, r.memRes)
		// CpuUtilized is reported in CPU units (1024 = 1 vCPU).
		if !math.IsNaN(r.cpuUsed) {
			s.CPUUnits = r.cpuUsed
		}
		if !math.IsNaN(r.memUsed) {
			s.MemoryMB = r.memUsed
		}
		if math.IsNaN(s.CPU) {
			s.CPU, s.Source = r.ecsCPU, NamespaceECS
		}
		if math.IsNaN(s.Memory) {
			s.Memory = r.ecsMem
		}
		out[cl] = s
	}
	return out, nil
}

// TaskSamples returns per-task utilization. This needs Container Insights with
// enhanced observability; without it every task comes back as an empty sample
// and the tables fall back to showing reserved capacity.
func (c *Client) TaskSamples(ctx context.Context, cluster string, taskIDs []string, period time.Duration) (map[string]Sample, error) {
	out := map[string]Sample{}
	if cluster == "" || len(taskIDs) == 0 {
		return out, nil
	}

	var queries []MetricQuery
	for _, id := range taskIDs {
		dims := map[string]string{"ClusterName": cluster, "TaskId": id}
		queries = append(queries,
			MetricQuery{Key: id, Label: "cpu", Namespace: NamespaceInsights, Name: "TaskCpuUtilization", Stat: "Average", Dims: dims},
			MetricQuery{Key: id, Label: "mem", Namespace: NamespaceInsights, Name: "TaskMemoryUtilization", Stat: "Average", Dims: dims},
		)
	}

	start, end := sampleWindow(period)
	series, err := c.GetSeries(ctx, queries, start, end, period)
	if err != nil {
		return nil, err
	}

	for _, id := range taskIDs {
		out[id] = NoSample()
	}
	for i, q := range queries {
		s := out[q.Key]
		switch q.Label {
		case "cpu":
			s.CPU = latest(series[i])
		case "mem":
			s.Memory = latest(series[i])
		}
		s.Source = NamespaceInsights
		out[q.Key] = s
	}
	return out, nil
}

// ratioPct converts a used/reserved pair into a percentage.
func ratioPct(used, reserved float64) float64 {
	if math.IsNaN(used) || math.IsNaN(reserved) || reserved <= 0 {
		return math.NaN()
	}
	return used / reserved * 100
}

// MetricTarget names what a chart view is plotting.
type MetricTarget struct {
	Kind    string // "cluster", "service" or "task"
	Cluster string
	Name    string // service name or task id; empty for a cluster
}

// Title renders the target for a view title.
func (t MetricTarget) Title() string {
	if t.Name == "" {
		return t.Cluster
	}
	return t.Cluster + "/" + t.Name
}

// Charts is the series set backing the monitoring view.
type Charts struct {
	Target MetricTarget
	Start  time.Time
	End    time.Time
	Period time.Duration
	CPU    model.Series
	Memory model.Series
	CPUMax model.Series
	MemMax model.Series
	Extra  []model.Series
	Note   string
}

// GetCharts loads the CPU and memory history for a target, plus whatever
// supporting series make sense for that level (task counts for a cluster,
// running/desired for a service, network I/O for a task).
func (c *Client) GetCharts(ctx context.Context, t MetricTarget, window, period time.Duration) (*Charts, error) {
	end := time.Now()
	start := end.Add(-window)

	out := &Charts{Target: t, Start: start, End: end, Period: period}

	var queries []MetricQuery
	switch t.Kind {
	case "service":
		dims := map[string]string{"ClusterName": t.Cluster, "ServiceName": t.Name}
		queries = []MetricQuery{
			{Label: "CPU avg %", Namespace: NamespaceECS, Name: "CPUUtilization", Stat: "Average", Dims: dims},
			{Label: "CPU max %", Namespace: NamespaceECS, Name: "CPUUtilization", Stat: "Maximum", Dims: dims},
			{Label: "MEM avg %", Namespace: NamespaceECS, Name: "MemoryUtilization", Stat: "Average", Dims: dims},
			{Label: "MEM max %", Namespace: NamespaceECS, Name: "MemoryUtilization", Stat: "Maximum", Dims: dims},
			{Label: "Running tasks", Namespace: NamespaceInsights, Name: "RunningTaskCount", Stat: "Average", Dims: dims},
			{Label: "Desired tasks", Namespace: NamespaceInsights, Name: "DesiredTaskCount", Stat: "Average", Dims: dims},
		}
	case "task":
		dims := map[string]string{"ClusterName": t.Cluster, "TaskId": t.Name}
		queries = []MetricQuery{
			{Label: "CPU avg %", Namespace: NamespaceInsights, Name: "TaskCpuUtilization", Stat: "Average", Dims: dims},
			{Label: "CPU max %", Namespace: NamespaceInsights, Name: "TaskCpuUtilization", Stat: "Maximum", Dims: dims},
			{Label: "MEM avg %", Namespace: NamespaceInsights, Name: "TaskMemoryUtilization", Stat: "Average", Dims: dims},
			{Label: "MEM max %", Namespace: NamespaceInsights, Name: "TaskMemoryUtilization", Stat: "Maximum", Dims: dims},
			{Label: "Net RX bytes", Namespace: NamespaceInsights, Name: "TaskNetworkRxBytes", Stat: "Average", Dims: dims},
			{Label: "Net TX bytes", Namespace: NamespaceInsights, Name: "TaskNetworkTxBytes", Stat: "Average", Dims: dims},
		}
		out.Note = "per-task metrics require Container Insights with enhanced observability"
	default:
		dims := map[string]string{"ClusterName": t.Cluster}
		queries = []MetricQuery{
			{Label: "CPU avg %", Namespace: NamespaceECS, Name: "CPUUtilization", Stat: "Average", Dims: dims},
			{Label: "CPU max %", Namespace: NamespaceECS, Name: "CPUUtilization", Stat: "Maximum", Dims: dims},
			{Label: "MEM avg %", Namespace: NamespaceECS, Name: "MemoryUtilization", Stat: "Average", Dims: dims},
			{Label: "MEM max %", Namespace: NamespaceECS, Name: "MemoryUtilization", Stat: "Maximum", Dims: dims},
			{Label: "Running tasks", Namespace: NamespaceInsights, Name: "RunningTaskCount", Stat: "Average", Dims: dims},
			{Label: "Pending tasks", Namespace: NamespaceInsights, Name: "PendingTaskCount", Stat: "Average", Dims: dims},
			{Label: "CPU utilized", Namespace: NamespaceInsights, Name: "CpuUtilized", Stat: "Sum", Dims: dims},
			{Label: "CPU reserved", Namespace: NamespaceInsights, Name: "CpuReserved", Stat: "Sum", Dims: dims},
			{Label: "MEM utilized", Namespace: NamespaceInsights, Name: "MemoryUtilized", Stat: "Sum", Dims: dims},
			{Label: "MEM reserved", Namespace: NamespaceInsights, Name: "MemoryReserved", Stat: "Sum", Dims: dims},
		}
	}

	series, err := c.GetSeries(ctx, queries, start, end, period)
	if err != nil {
		return nil, err
	}

	out.CPU, out.CPUMax = series[0], series[1]
	out.Memory, out.MemMax = series[2], series[3]
	out.Extra = series[4:]

	// AWS/ECS publishes cluster-level utilization for EC2 capacity only. On a
	// Fargate-only cluster those series are empty, so derive the percentages
	// from the Container Insights utilized/reserved pair instead.
	if t.Kind == "cluster" && out.CPU.Empty() && len(out.Extra) >= 6 {
		out.CPU = ratioSeries(out.Extra[2], out.Extra[3], "CPU %")
		out.Memory = ratioSeries(out.Extra[4], out.Extra[5], "MEM %")
		out.CPUMax, out.MemMax = model.Series{}, model.Series{}
		out.Extra = out.Extra[:2]
		out.Note = "derived from Container Insights utilized/reserved"
	}
	return out, nil
}

// ratioSeries divides two aligned series into a percentage series.
func ratioSeries(used, reserved model.Series, label string) model.Series {
	out := model.Series{Label: label, Unit: "Percent"}
	byTime := make(map[int64]float64, len(reserved.Timestamps))
	for i, ts := range reserved.Timestamps {
		byTime[ts.Unix()] = reserved.Values[i]
	}
	for i, ts := range used.Timestamps {
		res, ok := byTime[ts.Unix()]
		if !ok || res <= 0 {
			continue
		}
		out.Timestamps = append(out.Timestamps, ts)
		out.Values = append(out.Values, used.Values[i]/res*100)
	}
	return out
}

// ContainerInsightsEnabled reports whether a cluster streams Container Insights.
func ContainerInsightsEnabled(c model.Cluster) bool {
	v := strings.ToLower(c.ContainerInsights())
	return v == "enabled" || v == "enhanced"
}
