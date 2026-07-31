package awsx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// LogLine is one log event.
type LogLine struct {
	Timestamp time.Time
	Stream    string
	Message   string
}

// ErrNoLogGroup is returned when a container has no awslogs configuration, or
// its log group has not been created yet.
var ErrNoLogGroup = errors.New("no CloudWatch log group for this container")

// LogTail incrementally pulls new events from a log group. It is built on
// FilterLogEvents rather than GetLogEvents so one tail can follow every stream
// of a service at once, and it dedupes by event id because the API can replay
// events that share the boundary timestamp.
type LogTail struct {
	client *Client

	Group   string
	Streams []string
	Prefix  string
	Filter  string

	nextStart int64
	seen      map[string]struct{}
	seenOrder []string
	maxSeen   int
}

// NewLogTail starts a tail over a log group.
//
// streams pins the tail to specific log streams; prefix follows every stream
// under a prefix, which is how one view follows all tasks of a service.
func (c *Client) NewLogTail(group string, streams []string, prefix string, since time.Duration) *LogTail {
	return &LogTail{
		client:    c,
		Group:     group,
		Streams:   streams,
		Prefix:    prefix,
		nextStart: time.Now().Add(-since).UnixMilli(),
		seen:      map[string]struct{}{},
		maxSeen:   10000,
	}
}

// SetFilter installs a CloudWatch Logs filter pattern and rewinds the tail so
// the new pattern is applied to the history already on screen.
func (t *LogTail) SetFilter(pattern string, since time.Duration) {
	t.Filter = pattern
	t.Rewind(since)
}

// Rewind restarts the tail from a point in the past.
func (t *LogTail) Rewind(since time.Duration) {
	t.nextStart = time.Now().Add(-since).UnixMilli()
	t.seen = map[string]struct{}{}
	t.seenOrder = nil
}

// Next returns the log lines that have appeared since the previous call. A
// missing log group is reported as ErrNoLogGroup: for a task that has just
// started, the group often does not exist for a few seconds.
func (t *LogTail) Next(ctx context.Context, limit int32) ([]LogLine, error) {
	if t.Group == "" {
		return nil, ErrNoLogGroup
	}
	if limit <= 0 {
		limit = 1000
	}

	in := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(t.Group),
		StartTime:    aws.Int64(t.nextStart),
		Limit:        aws.Int32(limit),
	}
	if len(t.Streams) > 0 {
		in.LogStreamNames = t.Streams
	} else if t.Prefix != "" {
		in.LogStreamNamePrefix = aws.String(t.Prefix)
	}
	if t.Filter != "" {
		in.FilterPattern = aws.String(t.Filter)
	}

	var out []LogLine
	for {
		res, err := t.client.Logs.FilterLogEvents(ctx, in)
		if err != nil {
			var missing *logstypes.ResourceNotFoundException
			if errors.As(err, &missing) {
				return nil, ErrNoLogGroup
			}
			return nil, fmt.Errorf("filter log events: %w", err)
		}
		for _, e := range res.Events {
			id := aws.ToString(e.EventId)
			if _, dup := t.seen[id]; dup {
				continue
			}
			t.remember(id)

			ts := aws.ToInt64(e.Timestamp)
			if ts >= t.nextStart {
				// Resume from this event next time; the id set filters the replay.
				t.nextStart = ts
			}
			out = append(out, LogLine{
				Timestamp: time.UnixMilli(ts),
				Stream:    aws.ToString(e.LogStreamName),
				Message:   strings.TrimRight(aws.ToString(e.Message), "\n"),
			})
		}
		if res.NextToken == nil || len(out) >= int(limit) {
			break
		}
		in.NextToken = res.NextToken
	}
	return out, nil
}

// remember records an event id, evicting the oldest once the set is full so a
// long-lived tail does not grow without bound.
func (t *LogTail) remember(id string) {
	t.seen[id] = struct{}{}
	t.seenOrder = append(t.seenOrder, id)
	if len(t.seenOrder) <= t.maxSeen {
		return
	}
	drop := len(t.seenOrder) - t.maxSeen
	for _, old := range t.seenOrder[:drop] {
		delete(t.seen, old)
	}
	t.seenOrder = t.seenOrder[drop:]
}

// LogStreamName builds the awslogs stream name ECS uses for a container:
// "<prefix>/<container>/<task id>".
func LogStreamName(prefix, container, taskID string) string {
	if prefix == "" {
		return fmt.Sprintf("%s/%s", container, taskID)
	}
	return fmt.Sprintf("%s/%s/%s", prefix, container, taskID)
}

// LogGroupExists reports whether a log group is present, so a view can explain
// an empty screen instead of silently showing nothing.
func (c *Client) LogGroupExists(ctx context.Context, group string) bool {
	if group == "" {
		return false
	}
	res, err := c.Logs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(group),
		Limit:              aws.Int32(1),
	})
	if err != nil {
		return false
	}
	for _, g := range res.LogGroups {
		if aws.ToString(g.LogGroupName) == group {
			return true
		}
	}
	return false
}
