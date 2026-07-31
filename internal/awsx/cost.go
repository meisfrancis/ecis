package awsx

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"

	"github.com/meisfrancis/ecis/internal/model"
)

// GroupBy selects how the cost views break spend down.
type GroupBy string

// The available cost breakdowns. The tag-based ones only return data once the
// corresponding cost allocation tag has been activated in the Billing console,
// which is why the estimator exists as a fallback.
const (
	GroupByAWSService GroupBy = "SERVICE"
	GroupByUsageType  GroupBy = "USAGE_TYPE"
	GroupByRegion     GroupBy = "REGION"
	GroupByAccount    GroupBy = "LINKED_ACCOUNT"
	GroupByCluster    GroupBy = "TAG_CLUSTER"
	GroupByService    GroupBy = "TAG_SERVICE"
	GroupByTotal      GroupBy = "TOTAL"
)

// GroupByCycle is the order the cost view cycles breakdowns in.
var GroupByCycle = []GroupBy{
	GroupByService, GroupByCluster, GroupByUsageType, GroupByAWSService, GroupByRegion, GroupByAccount, GroupByTotal,
}

// Label renders a breakdown for the UI.
func (g GroupBy) Label() string {
	switch g {
	case GroupByAWSService:
		return "AWS SERVICE"
	case GroupByUsageType:
		return "USAGE TYPE"
	case GroupByRegion:
		return "REGION"
	case GroupByAccount:
		return "ACCOUNT"
	case GroupByCluster:
		return "CLUSTER"
	case GroupByService:
		return "ECS SERVICE"
	default:
		return "TOTAL"
	}
}

// containerServices are the Cost Explorer SERVICE dimension values that carry
// container spend. Fargate bills under ECS; EC2-launch-type capacity bills
// under EC2 instead, so both are included when the scope is not ECS-only.
var (
	ecsServiceNames     = []string{"Amazon Elastic Container Service"}
	computeServiceNames = []string{
		"Amazon Elastic Container Service",
		"Amazon Elastic Compute Cloud - Compute",
		"EC2 - Other",
		"Amazon Elastic Container Registry (ECR)",
		"Amazon Elastic Container Service for Kubernetes",
	}
)

// CostOptions parameterises a cost query.
type CostOptions struct {
	GroupBy       GroupBy
	Granularity   string // DAILY or MONTHLY
	Days          int
	Cluster       string // restrict to one cluster via the cluster tag
	ClusterTagKey string
	ServiceTagKey string
	// Wide widens the SERVICE filter from ECS alone to the whole container
	// stack (EC2 capacity, ECR storage).
	Wide bool
	// IncludeCredits keeps credit and refund records in the totals.
	IncludeCredits bool
	// Metric is the Cost Explorer metric, defaulting to UnblendedCost.
	Metric string
}

func (o *CostOptions) normalize() {
	if o.Granularity == "" {
		o.Granularity = string(cetypes.GranularityDaily)
	}
	if o.Days <= 0 {
		o.Days = 30
	}
	if o.ClusterTagKey == "" {
		o.ClusterTagKey = "ecs:cluster-name"
	}
	if o.ServiceTagKey == "" {
		o.ServiceTagKey = "ecs:service-name"
	}
	if o.Metric == "" {
		o.Metric = "UnblendedCost"
	}
	if o.GroupBy == "" {
		o.GroupBy = GroupByService
	}
}

// Cost runs a Cost Explorer query and returns a report ready for rendering.
//
// Cost Explorer charges per request, so callers should refresh this on a much
// slower cadence than the ECS tables.
func (c *Client) Cost(ctx context.Context, opts CostOptions) (*model.CostReport, error) {
	opts.normalize()

	end := time.Now().UTC().AddDate(0, 0, 1) // exclusive, and CE works in whole days
	start := end.AddDate(0, 0, -opts.Days-1)
	if strings.EqualFold(opts.Granularity, string(cetypes.GranularityMonthly)) {
		start = time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -monthsFor(opts.Days), 0)
	}

	in := &costexplorer.GetCostAndUsageInput{
		TimePeriod: &cetypes.DateInterval{
			Start: aws.String(start.Format("2006-01-02")),
			End:   aws.String(end.Format("2006-01-02")),
		},
		Granularity: cetypes.Granularity(strings.ToUpper(opts.Granularity)),
		Metrics:     []string{opts.Metric},
		Filter:      costFilter(opts),
	}
	if g := groupDefinition(opts); g != nil {
		in.GroupBy = []cetypes.GroupDefinition{*g}
	}

	report := &model.CostReport{
		GroupBy: opts.GroupBy.Label(),
		Start:   start,
		End:     end,
		Unit:    "USD",
	}

	groups := map[string]*model.CostGroup{}
	for {
		res, err := c.CE.GetCostAndUsage(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("get cost and usage: %w", err)
		}
		for _, byTime := range res.ResultsByTime {
			bStart, bEnd := parseInterval(byTime.TimePeriod)
			report.Buckets = append(report.Buckets, bStart)

			if len(byTime.Groups) == 0 {
				// Ungrouped query, or a bucket with no matching spend.
				amount, unit := metricValue(byTime.Total, opts.Metric)
				if unit != "" {
					report.Unit = unit
				}
				appendPoint(groups, "Total", bStart, bEnd, amount)
				continue
			}
			for _, g := range byTime.Groups {
				key := groupKey(g.Keys, opts)
				amount, unit := metricValue(g.Metrics, opts.Metric)
				if unit != "" {
					report.Unit = unit
				}
				appendPoint(groups, key, bStart, bEnd, amount)
			}
		}
		if res.NextPageToken == nil {
			break
		}
		in.NextPageToken = res.NextPageToken
	}

	// Every group needs a point per bucket so the sparklines line up even when a
	// service only started incurring spend part way through the window.
	sort.Slice(report.Buckets, func(i, j int) bool { return report.Buckets[i].Before(report.Buckets[j]) })
	report.Buckets = dedupeTimes(report.Buckets)
	for _, g := range groups {
		alignPoints(g, report.Buckets)
		g.Unit = report.Unit
		report.Total += g.Total
		report.Groups = append(report.Groups, *g)
	}
	report.SortByTotal()

	if f, err := c.forecast(ctx, opts); err == nil {
		report.Forecast = f
	}
	return report, nil
}

func monthsFor(days int) int {
	m := days / 30
	if m < 1 {
		return 1
	}
	if m > 12 {
		return 12
	}
	return m
}

func groupDefinition(opts CostOptions) *cetypes.GroupDefinition {
	switch opts.GroupBy {
	case GroupByTotal:
		return nil
	case GroupByCluster:
		return &cetypes.GroupDefinition{Type: cetypes.GroupDefinitionTypeTag, Key: aws.String(opts.ClusterTagKey)}
	case GroupByService:
		return &cetypes.GroupDefinition{Type: cetypes.GroupDefinitionTypeTag, Key: aws.String(opts.ServiceTagKey)}
	default:
		return &cetypes.GroupDefinition{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String(string(opts.GroupBy))}
	}
}

func costFilter(opts CostOptions) *cetypes.Expression {
	names := ecsServiceNames
	if opts.Wide {
		names = computeServiceNames
	}
	var exprs []cetypes.Expression
	exprs = append(exprs, cetypes.Expression{
		Dimensions: &cetypes.DimensionValues{
			Key:    cetypes.DimensionService,
			Values: names,
		},
	})
	if opts.Cluster != "" {
		exprs = append(exprs, cetypes.Expression{
			Tags: &cetypes.TagValues{
				Key:    aws.String(opts.ClusterTagKey),
				Values: []string{opts.Cluster},
			},
		})
	}
	if !opts.IncludeCredits {
		exprs = append(exprs, cetypes.Expression{
			Not: &cetypes.Expression{
				Dimensions: &cetypes.DimensionValues{
					Key:    cetypes.DimensionRecordType,
					Values: []string{"Credit", "Refund"},
				},
			},
		})
	}
	if len(exprs) == 1 {
		return &exprs[0]
	}
	return &cetypes.Expression{And: exprs}
}

// forecast projects spend to the end of the current month. Cost Explorer
// refuses to forecast without enough history, which is a normal condition
// rather than an error worth surfacing.
func (c *Client) forecast(ctx context.Context, opts CostOptions) (float64, error) {
	now := time.Now().UTC()
	// The forecast window must start today at the earliest.
	start := now.AddDate(0, 0, 1)
	end := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	if !start.Before(end) {
		return 0, fmt.Errorf("no forecast window left this month")
	}

	res, err := c.CE.GetCostForecast(ctx, &costexplorer.GetCostForecastInput{
		TimePeriod: &cetypes.DateInterval{
			Start: aws.String(start.Format("2006-01-02")),
			End:   aws.String(end.Format("2006-01-02")),
		},
		Metric:      cetypes.MetricUnblendedCost,
		Granularity: cetypes.GranularityMonthly,
		Filter:      costFilter(opts),
	})
	if err != nil {
		return 0, err
	}
	if res.Total == nil {
		return 0, nil
	}
	v, _ := strconv.ParseFloat(aws.ToString(res.Total.Amount), 64)
	return v, nil
}

func appendPoint(groups map[string]*model.CostGroup, key string, start, end time.Time, amount float64) {
	g, ok := groups[key]
	if !ok {
		g = &model.CostGroup{Key: key}
		groups[key] = g
	}
	g.Points = append(g.Points, model.CostPoint{Start: start, End: end, Amount: amount})
	g.Total += amount
}

// alignPoints pads a group so it has exactly one point per bucket.
func alignPoints(g *model.CostGroup, buckets []time.Time) {
	byBucket := make(map[int64]model.CostPoint, len(g.Points))
	for _, p := range g.Points {
		byBucket[p.Start.Unix()] = p
	}
	out := make([]model.CostPoint, 0, len(buckets))
	for _, b := range buckets {
		if p, ok := byBucket[b.Unix()]; ok {
			out = append(out, p)
			continue
		}
		out = append(out, model.CostPoint{Start: b, End: b, Amount: 0})
	}
	g.Points = out
}

func dedupeTimes(ts []time.Time) []time.Time {
	out := ts[:0]
	var last time.Time
	for _, t := range ts {
		if !t.Equal(last) {
			out = append(out, t)
			last = t
		}
	}
	return out
}

func parseInterval(d *cetypes.DateInterval) (time.Time, time.Time) {
	if d == nil {
		return time.Time{}, time.Time{}
	}
	start, _ := time.Parse("2006-01-02", aws.ToString(d.Start))
	end, _ := time.Parse("2006-01-02", aws.ToString(d.End))
	return start, end
}

func metricValue(m map[string]cetypes.MetricValue, metric string) (float64, string) {
	v, ok := m[metric]
	if !ok {
		for _, fallback := range m {
			v = fallback
			break
		}
	}
	amount, _ := strconv.ParseFloat(aws.ToString(v.Amount), 64)
	return amount, aws.ToString(v.Unit)
}

// groupKey renders a Cost Explorer group key. Tag groups arrive as "key$value"
// and an empty value means the resource carries no such tag.
func groupKey(keys []string, opts CostOptions) string {
	if len(keys) == 0 {
		return "Total"
	}
	key := keys[0]
	if i := strings.Index(key, "$"); i >= 0 {
		value := key[i+1:]
		if value == "" {
			return "(untagged)"
		}
		return value
	}
	return key
}

// EstimateReport builds a cost projection from running task sizes, for accounts
// where Cost Explorer cannot break spend down per service. Only Fargate tasks
// contribute: EC2-launch-type capacity is billed as EC2 instances regardless of
// how many tasks are packed onto them.
func EstimateReport(tasks []model.Task, region string, overrides map[string]FargateRate, groupBy GroupBy) *model.CostReport {
	rate := RateFor(region, overrides)
	now := time.Now()

	totals := map[string]float64{}
	for _, t := range tasks {
		if !strings.EqualFold(t.LastStatus, "RUNNING") {
			continue
		}
		hourly := TaskCostPerHour(t, rate)
		if hourly == 0 {
			continue
		}
		key := t.Service
		switch groupBy {
		case GroupByCluster:
			key = t.Cluster
		case GroupByUsageType:
			key = t.LaunchType
			if key == "" {
				key = t.CapacityProv
			}
		}
		if key == "" {
			key = t.Group
		}
		if key == "" {
			key = "(standalone tasks)"
		}
		totals[key] += hourly * HoursPerMonth
	}

	report := &model.CostReport{
		GroupBy:   groupBy.Label(),
		Unit:      "USD",
		Start:     now,
		End:       now.AddDate(0, 1, 0),
		Estimated: true,
		Note: fmt.Sprintf("projected monthly Fargate cost at %.5f/vCPU-hr + %.6f/GB-hr in %s (%d hrs/mo)",
			rate.VCPUHour, rate.GBHour, region, HoursPerMonth),
	}
	for key, total := range totals {
		report.Groups = append(report.Groups, model.CostGroup{
			Key:       key,
			Unit:      "USD",
			Total:     total,
			Estimated: true,
			Points:    []model.CostPoint{{Start: now, End: report.End, Amount: total}},
		})
		report.Total += total
	}
	report.SortByTotal()
	return report
}
