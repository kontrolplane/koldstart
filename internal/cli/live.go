package cli

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscloudcontrol "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/kontrolplane/koldstart/internal/provider/awsp"
)

// MetricsWindow is how far back the metrics of a function go, and MetricsPeriod how long each of
// their points is.
const (
	MetricsWindow = time.Hour
	MetricsPeriod = time.Minute
)

// Metrics are a function's lambda metrics over MetricsWindow, a point for each MetricsPeriod,
// oldest first. A period without invocations is 0.
type Metrics struct {
	Start       time.Time
	Invocations []float64
	Errors      []float64
	Throttles   []float64
	// Duration is the average in milliseconds, and DurationP99 the 99th percentile.
	Duration    []float64
	DurationP99 []float64
	Concurrency []float64
}

// MetricsSummary is a function's metrics over MetricsWindow, summed.
type MetricsSummary struct {
	Invocations float64
	Errors      float64
	Throttles   float64
	// DurationP99 is the highest 99th percentile of a period, in milliseconds.
	DurationP99 float64
}

// FunctionState is what lambda says of a function now.
type FunctionState struct {
	State            string
	StateReason      string
	LastUpdate       string
	LastUpdateReason string
	LastModified     time.Time
	CodeSize         int64
	// CodeSHA256 is the hash of the code lambda runs, in hex, as the state keeps it.
	CodeSHA256 string
	Runtime    string
	Memory     int32
	Timeout    int32
}

// QueueDepth is how many messages a queue holds.
type QueueDepth struct {
	Visible  int
	InFlight int
	Delayed  int
}

// LogEvent is one line of a function's logs.
type LogEvent struct {
	Time    time.Time
	Message string
	// Stream is the log stream the line is in, one for each instance of the function.
	Stream string
}

// liveTypes are the cloud control types of koldstart's own, whose physical ids cloud control
// takes as their identifiers.
var liveTypes = map[string]string{
	awsp.TypeRole:         "AWS::IAM::Role",
	awsp.TypeLogGroup:     "AWS::Logs::LogGroup",
	awsp.TypeFunction:     "AWS::Lambda::Function",
	awsp.TypeHTTPAPI:      "AWS::ApiGatewayV2::Api",
	awsp.TypeDomain:       "AWS::ApiGatewayV2::DomainName",
	awsp.TypeSchedule:     "AWS::Scheduler::Schedule",
	awsp.TypeMapping:      "AWS::Lambda::EventSourceMapping",
	awsp.TypeSubscription: "AWS::SNS::Subscription",
}

// ErrNotLive is a resource whose type cloud control cannot read.
var ErrNotLive = errors.New("cloud control cannot read this type")

// Live reads a resource as cloud control sees it now: its properties, read only ones included.
func (c *Console) Live(ctx context.Context, ref StageRef, typ, physicalID string) (map[string]any, error) {
	if t, ok := liveTypes[typ]; ok {
		typ = t
	}
	if !strings.HasPrefix(typ, "AWS::") || physicalID == "" {
		return nil, ErrNotLive
	}
	client := awscloudcontrol.NewFromConfig(c.sess.in(ref.Region).cfg)
	out, err := client.GetResource(ctx, &awscloudcontrol.GetResourceInput{TypeName: &typ, Identifier: &physicalID})
	if err != nil {
		return nil, fmt.Errorf("reading %s %s: %w", typ, physicalID, err)
	}
	var props map[string]any
	if out.ResourceDescription != nil {
		if err := json.Unmarshal([]byte(aws.ToString(out.ResourceDescription.Properties)), &props); err != nil {
			return nil, fmt.Errorf("reading %s %s: %w", typ, physicalID, err)
		}
	}
	return props, nil
}

// Function reads what lambda says of a function now, by its name in lambda.
func (c *Console) Function(ctx context.Context, ref StageRef, name string) (FunctionState, error) {
	out, err := c.sess.in(ref.Region).Lambda.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: &name})
	if err != nil {
		return FunctionState{}, fmt.Errorf("reading %s: %w", name, err)
	}
	f := FunctionState{
		State: string(out.State), StateReason: aws.ToString(out.StateReason),
		LastUpdate: string(out.LastUpdateStatus), LastUpdateReason: aws.ToString(out.LastUpdateStatusReason),
		CodeSize: out.CodeSize, Runtime: string(out.Runtime), Memory: aws.ToInt32(out.MemorySize), Timeout: aws.ToInt32(out.Timeout),
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000-0700", aws.ToString(out.LastModified)); err == nil {
		f.LastModified = t
	}
	if sum, err := base64.StdEncoding.DecodeString(aws.ToString(out.CodeSha256)); err == nil {
		f.CodeSHA256 = hex.EncodeToString(sum)
	}
	return f, nil
}

// Queue reads how many messages a queue holds, by its url.
func (c *Console) Queue(ctx context.Context, ref StageRef, url string) (QueueDepth, error) {
	out, err := sqs.NewFromConfig(c.sess.in(ref.Region).cfg).GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: &url,
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesDelayed,
		},
	})
	if err != nil {
		return QueueDepth{}, fmt.Errorf("reading the queue %s: %w", url, err)
	}
	n := func(name sqstypes.QueueAttributeName) int {
		v, _ := strconv.Atoi(out.Attributes[string(name)])
		return v
	}
	return QueueDepth{
		Visible:  n(sqstypes.QueueAttributeNameApproximateNumberOfMessages),
		InFlight: n(sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible),
		Delayed:  n(sqstypes.QueueAttributeNameApproximateNumberOfMessagesDelayed),
	}, nil
}

// The statistics the metrics are read as.
const (
	statSum = "Sum"
	statP99 = "p99"
)

// metricQueries are the lambda metrics read of each function, by their id prefix.
var metricQueries = []struct {
	id, name string
	stat     string
}{
	{"invocations", "Invocations", statSum},
	{"errors", "Errors", statSum},
	{"throttles", "Throttles", statSum},
	{"duration", "Duration", "Average"},
	{statP99, "Duration", statP99},
	{"concurrency", "ConcurrentExecutions", "Maximum"},
}

// Metrics reads the lambda metrics of functions over MetricsWindow, by their names in lambda, in
// one request.
func (c *Console) Metrics(ctx context.Context, ref StageRef, names []string) (map[string]Metrics, error) {
	end := time.Now().Truncate(MetricsPeriod)
	start := end.Add(-MetricsWindow)
	queries := make([]cwtypes.MetricDataQuery, 0, len(metricQueries)*len(names))
	for i, name := range names {
		for _, q := range metricQueries {
			queries = append(queries, cwtypes.MetricDataQuery{
				Id: aws.String(fmt.Sprintf("%s_%d", q.id, i)),
				MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{Namespace: aws.String("AWS/Lambda"), MetricName: aws.String(q.name),
						Dimensions: []cwtypes.Dimension{{Name: aws.String("FunctionName"), Value: aws.String(name)}}},
					Period: aws.Int32(int32(MetricsPeriod.Seconds())),
					Stat:   aws.String(q.stat),
				},
			})
		}
	}
	out := map[string]Metrics{}
	points := int(MetricsWindow / MetricsPeriod)
	for _, name := range names {
		out[name] = Metrics{Start: start, Invocations: make([]float64, points), Errors: make([]float64, points),
			Throttles: make([]float64, points), Duration: make([]float64, points), DurationP99: make([]float64, points), Concurrency: make([]float64, points)}
	}
	client := cloudwatch.NewFromConfig(c.sess.in(ref.Region).cfg)
	for chunk := range slices.Chunk(queries, 500) {
		p := cloudwatch.NewGetMetricDataPaginator(client, &cloudwatch.GetMetricDataInput{MetricDataQueries: chunk, StartTime: &start, EndTime: &end})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("reading the metrics of %s: %w", ref, err)
			}
			for _, r := range page.MetricDataResults {
				id, index, _ := strings.Cut(aws.ToString(r.Id), "_")
				i, err := strconv.Atoi(index)
				if err != nil || i >= len(names) {
					continue
				}
				m := out[names[i]]
				series := map[string][]float64{"invocations": m.Invocations, "errors": m.Errors, "throttles": m.Throttles,
					"duration": m.Duration, statP99: m.DurationP99, "concurrency": m.Concurrency}[id]
				for j, t := range r.Timestamps {
					if k := int(t.Sub(start) / MetricsPeriod); k >= 0 && k < points && j < len(r.Values) {
						series[k] = r.Values[j]
					}
				}
			}
		}
	}
	return out, nil
}

// Summary sums metrics over their window.
func (m Metrics) Summary() MetricsSummary {
	var s MetricsSummary
	for i := range m.Invocations {
		s.Invocations += m.Invocations[i]
		s.Errors += m.Errors[i]
		s.Throttles += m.Throttles[i]
		s.DurationP99 = max(s.DurationP99, m.DurationP99[i])
	}
	return s
}

// LogsBetween reads a log group's events from since until until, the newest maxLogEvents of
// them, with a cloudwatch logs filter pattern when filter is set. A zero until reads up to now.
func (c *Console) LogsBetween(ctx context.Context, ref StageRef, group string, since, until time.Time, filter string) ([]LogEvent, error) {
	in := &cloudwatchlogs.FilterLogEventsInput{LogGroupName: &group, StartTime: aws.Int64(since.UnixMilli())}
	if !until.IsZero() {
		in.EndTime = aws.Int64(until.UnixMilli())
	}
	if filter != "" {
		in.FilterPattern = &filter
	}
	var out []LogEvent
	p := cloudwatchlogs.NewFilterLogEventsPaginator(c.sess.in(ref.Region).Logs, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("reading the logs of %s: %w", group, err)
		}
		for _, e := range page.Events {
			out = append(out, LogEvent{Time: time.UnixMilli(aws.ToInt64(e.Timestamp)), Message: strings.TrimRight(aws.ToString(e.Message), "\n"), Stream: aws.ToString(e.LogStreamName)})
		}
		if len(out) > maxLogEvents {
			out = slices.Delete(out, 0, len(out)-maxLogEvents)
		}
	}
	slices.SortStableFunc(out, func(a, b LogEvent) int { return a.Time.Compare(b.Time) })
	return out, nil
}

// ErrNoLiveTail is a log group that cannot be followed with a live tail, as on an emulator.
var ErrNoLiveTail = errors.New("no live tail")

// Tail follows a log group with a live tail, by its arn, calling fn with the lines of each update
// until ctx ends. It returns ErrNoLiveTail where a live tail cannot start.
func (c *Console) Tail(ctx context.Context, ref StageRef, groupARN string, fn func([]LogEvent)) error {
	groupARN = strings.TrimSuffix(groupARN, ":*")
	for ctx.Err() == nil {
		out, err := c.sess.in(ref.Region).Logs.StartLiveTail(ctx, &cloudwatchlogs.StartLiveTailInput{LogGroupIdentifiers: []string{groupARN}})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("%w: %w", ErrNoLiveTail, err)
		}
		stream := out.GetStream()
		for event := range stream.Events() {
			update, ok := event.(*logtypes.StartLiveTailResponseStreamMemberSessionUpdate)
			if !ok || len(update.Value.SessionResults) == 0 {
				continue
			}
			lines := make([]LogEvent, 0, len(update.Value.SessionResults))
			for _, e := range update.Value.SessionResults {
				lines = append(lines, LogEvent{Time: time.UnixMilli(aws.ToInt64(e.Timestamp)), Message: strings.TrimRight(aws.ToString(e.Message), "\n"), Stream: aws.ToString(e.LogStreamName)})
			}
			fn(lines)
		}
		_ = stream.Close()
	}
	return nil
}

// Drift lists the properties a resource was applied with whose live value differs, sorted.
func Drift(applied, live map[string]any) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(applied)) {
		if v, ok := live[k]; ok && !sameJSON(applied[k], v) {
			out = append(out, k)
		}
	}
	return out
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
