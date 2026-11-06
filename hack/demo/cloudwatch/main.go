// Command cloudwatch writes what cloudwatch holds of a lambda function and floci does not, for the
// shop in hack/demo/shop: an hour of its metrics, and lambda's START, END and REPORT lines around
// the lines of each request in its log. hack/demo/seed.sh runs it with the profile it sets, once
// the shop is deployed and invoked.
package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

// traffic is how a function was invoked over the hour: calls a minute at the quietest and at the
// busiest, a failure every so many minutes, and how long a call takes in milliseconds.
type traffic struct {
	function      string
	low, high     int
	failEvery     int
	duration, p99 float64
}

// shop is the traffic of each function of dev and prod. The report runs once an hour.
var shop = []traffic{
	{function: "shop-prod-api", low: 18, high: 46, failEvery: 17, duration: 38, p99: 210},
	{function: "shop-prod-worker", low: 4, high: 12, duration: 120, p99: 480},
	{function: "shop-prod-report", duration: 2400, p99: 2400},
	{function: "shop-dev-api", low: 1, high: 9, failEvery: 11, duration: 22, p99: 160},
	{function: "shop-dev-worker", high: 4, failEvery: 23, duration: 95, p99: 310},
	{function: "shop-dev-report", duration: 1800, p99: 1800},
}

// wave is a value between low and high for minute m of the hour, rising and falling twice.
func wave(low, high, m int) int {
	step := m % 30
	if step > 15 {
		step = 30 - step
	}
	return low + (high-low)*step/15
}

// datum is one value of a metric of function at t.
func datum(function, metric string, t time.Time, value float64, unit cwtypes.StandardUnit) cwtypes.MetricDatum {
	return cwtypes.MetricDatum{
		MetricName: aws.String(metric),
		Dimensions: []cwtypes.Dimension{{Name: aws.String("FunctionName"), Value: aws.String(function)}},
		Timestamp:  aws.Time(t),
		Value:      aws.Float64(value),
		Unit:       unit,
	}
}

// minute is the metrics of f for minute m, at t.
func minute(f traffic, m int, t time.Time) []cwtypes.MetricDatum {
	calls := wave(f.low, f.high, m)
	if f.high == 0 && m == 45 {
		calls = 1
	}
	if calls == 0 {
		return nil
	}
	failed := 0
	if f.failEvery > 0 && m%f.failEvery == f.failEvery-1 {
		failed = 1 + calls/20
	}
	return []cwtypes.MetricDatum{
		datum(f.function, "Invocations", t, float64(calls), cwtypes.StandardUnitCount),
		datum(f.function, "Errors", t, float64(failed), cwtypes.StandardUnitCount),
		datum(f.function, "Throttles", t, 0, cwtypes.StandardUnitCount),
		datum(f.function, "Duration", t, f.duration+float64(m%7)*f.duration/10, cwtypes.StandardUnitMilliseconds),
		datum(f.function, "Duration", t, f.p99-float64(m%5)*f.p99/20, cwtypes.StandardUnitMilliseconds),
		datum(f.function, "ConcurrentExecutions", t, float64(1+calls/15), cwtypes.StandardUnitCount),
	}
}

// metrics writes the last hour of every function in shop, a request for each function.
func metrics(ctx context.Context, client *cloudwatch.Client) error {
	now := time.Now().Truncate(time.Minute)
	for _, f := range shop {
		data := make([]cwtypes.MetricDatum, 0, 60*6)
		for m := range 60 {
			data = append(data, minute(f, m, now.Add(time.Duration(m-60)*time.Minute+30*time.Second))...)
		}
		if _, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String("AWS/Lambda"), MetricData: data}); err != nil {
			return fmt.Errorf("writing the metrics of %s: %w", f.function, err)
		}
	}
	return nil
}

// request is what the log of a function holds of one request: the stream it is in, the times of
// its first and last lines, and whether lambda's report of it is there already.
type request struct {
	id          string
	stream      string
	first, last int64
	reported    bool
}

// requests are the requests in the log of function, in the order their first lines were logged.
func requests(ctx context.Context, client *cloudwatchlogs.Client, function string) ([]*request, error) {
	var out []*request
	byID := map[string]*request{}
	p := cloudwatchlogs.NewFilterLogEventsPaginator(client, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String("/aws/lambda/" + function)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the log of %s: %w", function, err)
		}
		for _, e := range page.Events {
			id, reported := requestOf(aws.ToString(e.Message))
			if id == "" {
				continue
			}
			r := byID[id]
			if r == nil {
				r = &request{id: id, stream: aws.ToString(e.LogStreamName), first: aws.ToInt64(e.Timestamp)}
				byID[id] = r
				out = append(out, r)
			}
			r.last = max(r.last, aws.ToInt64(e.Timestamp))
			r.reported = r.reported || reported
		}
	}
	return out, nil
}

// requestOf is the request id of a line of a node function's log, written by the runtime after
// the time, and whether the line is one of lambda's own about it.
func requestOf(line string) (string, bool) {
	for _, prefix := range []string{"START RequestId: ", "END RequestId: ", "REPORT RequestId: "} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			id, _, _ := strings.Cut(rest, " ")
			return strings.TrimSpace(id), true
		}
	}
	fields := strings.Split(line, "\t")
	if len(fields) < 3 || len(fields[1]) != 36 {
		return "", false
	}
	return fields[1], false
}

// spread is a number from 0 to n-1 that stays the same for s, its fnv-1a hash modulo n.
func spread(s string, n uint32) int {
	h := uint32(2166136261)
	for i := range len(s) {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return int(h % n)
}

// reports writes lambda's lines around each request of function that has none: START before its
// first line, and END and REPORT after its last, the first request of each stream a cold start.
func reports(ctx context.Context, logs *cloudwatchlogs.Client, functions *lambda.Client, function string) error {
	conf, err := functions.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(function)})
	if err != nil {
		return fmt.Errorf("reading the configuration of %s: %w", function, err)
	}
	all, err := requests(ctx, logs, function)
	if err != nil {
		return err
	}
	started := map[string]bool{}
	byStream := map[string][]logtypes.InputLogEvent{}
	for _, r := range all {
		cold := !started[r.stream]
		started[r.stream] = true
		if r.reported {
			continue
		}
		duration := float64(r.last-r.first) + 1.5 + float64(spread(r.id, 2400))/100
		report := fmt.Sprintf("REPORT RequestId: %s\tDuration: %.2f ms\tBilled Duration: %d ms\tMemory Size: %d MB\tMax Memory Used: %d MB",
			r.id, duration, int(duration)+1, aws.ToInt32(conf.MemorySize), 64+spread(r.id, 24))
		if cold {
			report += fmt.Sprintf("\tInit Duration: %.2f ms", 150+float64(spread(r.id, 9000))/100)
		}
		byStream[r.stream] = append(byStream[r.stream],
			logtypes.InputLogEvent{Timestamp: aws.Int64(r.first), Message: aws.String("START RequestId: " + r.id + " Version: $LATEST")},
			logtypes.InputLogEvent{Timestamp: aws.Int64(r.last + 1), Message: aws.String("END RequestId: " + r.id)},
			logtypes.InputLogEvent{Timestamp: aws.Int64(r.last + 1), Message: aws.String(report)},
		)
	}
	for stream, events := range byStream {
		slices.SortStableFunc(events, func(a, b logtypes.InputLogEvent) int { return int(aws.ToInt64(a.Timestamp) - aws.ToInt64(b.Timestamp)) })
		if _, err := logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
			LogGroupName: aws.String("/aws/lambda/" + function), LogStreamName: aws.String(stream), LogEvents: events,
		}); err != nil {
			return fmt.Errorf("writing the reports of %s: %w", function, err)
		}
	}
	return nil
}

// run writes the metrics and the reports of every function in shop.
func run(ctx context.Context) error {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("loading the aws config: %w", err)
	}
	if err := metrics(ctx, cloudwatch.NewFromConfig(cfg)); err != nil {
		return err
	}
	logs, functions := cloudwatchlogs.NewFromConfig(cfg), lambda.NewFromConfig(cfg)
	for _, f := range shop {
		if err := reports(ctx, logs, functions, f.function); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
