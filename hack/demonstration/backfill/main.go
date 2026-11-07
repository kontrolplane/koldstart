// Command backfill writes to floci what an aws account running the checkout service in
// hack/demonstration/checkout would hold and floci does not. hack/demonstration/seed.sh runs it with the profile it
// sets:
//
//	backfill parameters   the parameters the service reads, before it is deployed
//	backfill              once it is deployed and invoked: an hour of the metrics of each
//	                      function, lambda's START, END and REPORT lines around each request in
//	                      its log, and the lock of a deploy to staging that github actions has
//	                      not finished
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
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/kontrolplane/koldstart/internal/state"
)

// The app, its stages and its functions, as hack/demonstration/checkout deploys them.
const app = "checkout"

var (
	stages    = []string{"development", "staging", "production"}
	functions = []string{"storefront", "payments", "notify-customers", "stripe-webhook", "reconcile"}
)

// webhookSecret is the secret the stripe-webhook's saved events are signed with, the same on
// every stage, so they verify wherever they are sent. It signs nothing outside the demonstration.
//
//nolint:gosec
const webhookSecret = "whsec_5c2f8e1a9d3b7046e1f2"

// traffic is how a function was invoked over the last hour: calls a minute at the quietest and at
// the busiest, a failure every so many minutes, how long a call takes and its p99 in
// milliseconds, and a minute it was throttled in, -1 for none. A function with no calls at its
// busiest is invoked once, at minute 45.
type traffic struct {
	function      string
	low, high     int
	failEvery     int
	duration, p99 float64
	throttledAt   int
}

// hour is the traffic of each function that had any. Production takes the shop's customers, with a
// burst of throttling as a sale started; staging and development see the team's own requests.
// The nightly reconciliation runs at two in the morning, outside the last hour.
var hour = []traffic{
	{function: "checkout-production-storefront", low: 24, high: 52, failEvery: 13, duration: 41, p99: 236, throttledAt: 38},
	{function: "checkout-production-payments", low: 6, high: 14, failEvery: 9, duration: 420, p99: 1850, throttledAt: -1},
	{function: "checkout-production-notify-customers", low: 5, high: 12, duration: 64, p99: 148, throttledAt: -1},
	{function: "checkout-production-stripe-webhook", low: 5, high: 13, failEvery: 31, duration: 28, p99: 95, throttledAt: -1},
	{function: "checkout-staging-storefront", high: 4, failEvery: 19, duration: 48, p99: 260, throttledAt: -1},
	{function: "checkout-staging-payments", high: 2, duration: 400, p99: 910, throttledAt: -1},
	{function: "checkout-staging-notify-customers", high: 1, duration: 70, p99: 150, throttledAt: -1},
	{function: "checkout-development-storefront", high: 1, failEvery: 23, duration: 55, p99: 310, throttledAt: -1},
	{function: "checkout-development-payments", duration: 450, p99: 450, throttledAt: -1},
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
	failed, throttled := 0, 0
	if f.failEvery > 0 && m%f.failEvery == f.failEvery-1 {
		failed = 1 + calls/20
	}
	if f.throttledAt >= 0 && (m == f.throttledAt || m == f.throttledAt+1) {
		throttled = 3 + calls/10
	}
	return []cwtypes.MetricDatum{
		datum(f.function, "Invocations", t, float64(calls), cwtypes.StandardUnitCount),
		datum(f.function, "Errors", t, float64(failed), cwtypes.StandardUnitCount),
		datum(f.function, "Throttles", t, float64(throttled), cwtypes.StandardUnitCount),
		datum(f.function, "Duration", t, f.duration+float64(m%7)*f.duration/10, cwtypes.StandardUnitMilliseconds),
		datum(f.function, "Duration", t, f.p99-float64(m%5)*f.p99/20, cwtypes.StandardUnitMilliseconds),
		datum(f.function, "ConcurrentExecutions", t, float64(1+calls/15), cwtypes.StandardUnitCount),
	}
}

// metrics writes the last hour of every function in hour, a request for each function.
func metrics(ctx context.Context, client *cloudwatch.Client) error {
	now := time.Now().Truncate(time.Minute)
	for _, f := range hour {
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

// parameters puts what the service reads from parameter store under each stage: the secret its
// payment provider signs webhooks with, and where errors go once sentry is in.
func parameters(ctx context.Context, client *ssm.Client) error {
	for _, stage := range stages {
		values := map[string]string{
			"stripe-webhook-secret": webhookSecret,
			"sentry-dsn":            "https://4f1c9a2e7b3d4086@o451207.ingest.sentry.io/6312447",
		}
		for name, value := range values {
			_, err := client.PutParameter(ctx, &ssm.PutParameterInput{
				Name: aws.String("/" + app + "/" + stage + "/" + name), Value: aws.String(value),
				Type: ssmtypes.ParameterTypeSecureString, Overwrite: aws.Bool(true),
			})
			if err != nil {
				return fmt.Errorf("putting the parameter %s of %s: %w", name, stage, err)
			}
		}
	}
	return nil
}

// lock leaves the lock of a deploy to staging that github actions started a few minutes ago and
// has not finished, as a run that was cancelled leaves it.
func lock(ctx context.Context, cfg aws.Config) error {
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("reading the account: %w", err)
	}
	backend, err := state.Open(ctx, s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }), aws.ToString(ident.Account), cfg.Region)
	if err != nil {
		return err
	}
	return backend.Lock(ctx, app, "staging", state.LockInfo{
		Who:       "github-actions[bot]",
		Where:     "https://github.com/acme/checkout/actions/runs/11873520113",
		Operation: "deploy",
		Since:     time.Now().Add(-4 * time.Minute).UTC(),
	})
}

// run writes the parameters, or once the service is deployed and invoked, the metrics, the
// reports and the lock.
func run(ctx context.Context, args []string) error {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("loading the aws config: %w", err)
	}
	if slices.Equal(args, []string{"parameters"}) {
		return parameters(ctx, ssm.NewFromConfig(cfg))
	}
	if len(args) > 0 {
		return fmt.Errorf("backfill takes parameters, or nothing: not %s", strings.Join(args, " "))
	}
	if err := metrics(ctx, cloudwatch.NewFromConfig(cfg)); err != nil {
		return err
	}
	logs, client := cloudwatchlogs.NewFromConfig(cfg), lambda.NewFromConfig(cfg)
	for _, stage := range stages {
		for _, f := range functions {
			if err := reports(ctx, logs, client, app+"-"+stage+"-"+f); err != nil {
				return err
			}
		}
	}
	return lock(ctx, cfg)
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
