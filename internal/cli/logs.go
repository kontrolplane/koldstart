package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/desired"
)

func logsCommand(name string, opts *Options) *cobra.Command {
	var function, filter string
	var tail bool
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "print a function's logs, --tail to follow them",
		Long: `logs prints a function's logs of the last --since, and with --tail follows them as they come.
--filter takes a cloudwatch logs filter pattern, such as ERROR or "{ $.level = \"error\" }".`,
		Example: fmt.Sprintf(`  %[1]s logs -f api
  %[1]s logs -f api --tail --filter ERROR`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			if _, ok := s.Functions[function]; !ok {
				return fmt.Errorf("no function %q in %s", function, s.Path)
			}
			group := s.state.Resources[desired.LogGroupID(function)]
			if group == nil {
				return fmt.Errorf("%s is not deployed to %s: deploy it first", function, s.Stage)
			}
			start := time.Now().Add(-since)
			out := cmd.OutOrStdout()
			last, err := s.printLogs(ctx, out, group.PhysicalID, filter, start)
			if err != nil || !tail {
				return err
			}
			return s.tailLogs(ctx, out, group.Attributes["Arn"], group.PhysicalID, filter, last)
		},
	}
	cmd.Flags().StringVarP(&function, "function", "f", "", "the function whose logs to print")
	cmd.Flags().BoolVarP(&tail, "tail", "t", false, "follow the logs as they come")
	cmd.Flags().DurationVar(&since, "since", 10*time.Minute, "how far back to start")
	cmd.Flags().StringVar(&filter, "filter", "", "a cloudwatch logs filter pattern")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

func printEvent(w io.Writer, timestamp int64, message string) {
	t := time.UnixMilli(timestamp).Local().Format("15:04:05.000")
	_, _ = fmt.Fprintf(w, "%s  %s\n", t, strings.TrimRight(message, "\n"))
}

// printLogs prints the events of the group since start, and returns the time of the last one.
func (s *stage) printLogs(ctx context.Context, w io.Writer, group, filter string, start time.Time) (time.Time, error) {
	last := start
	in := &cloudwatchlogs.FilterLogEventsInput{LogGroupName: &group, StartTime: aws.Int64(start.UnixMilli())}
	if filter != "" {
		in.FilterPattern = &filter
	}
	p := cloudwatchlogs.NewFilterLogEventsPaginator(s.sess.Logs, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return last, fmt.Errorf("reading the logs of %s: %w", group, err)
		}
		for _, e := range page.Events {
			printEvent(w, aws.ToInt64(e.Timestamp), aws.ToString(e.Message))
			if t := time.UnixMilli(aws.ToInt64(e.Timestamp)); t.After(last) {
				last = t
			}
		}
	}
	return last, nil
}

// tailLogs follows the group with a live tail, starting again when a session ends, and polls
// where live tail is not there, as on an emulator.
func (s *stage) tailLogs(ctx context.Context, w io.Writer, arn, group, filter string, since time.Time) error {
	arn = strings.TrimSuffix(arn, ":*")
	for ctx.Err() == nil {
		in := &cloudwatchlogs.StartLiveTailInput{LogGroupIdentifiers: []string{arn}}
		if filter != "" {
			in.LogEventFilterPattern = &filter
		}
		out, err := s.sess.Logs.StartLiveTail(ctx, in)
		if err != nil {
			slog.Debug("no live tail, polling", "error", err)
			return s.pollLogs(ctx, w, group, filter, since)
		}
		stream := out.GetStream()
		for event := range stream.Events() {
			if update, ok := event.(*types.StartLiveTailResponseStreamMemberSessionUpdate); ok {
				for _, e := range update.Value.SessionResults {
					printEvent(w, aws.ToInt64(e.Timestamp), aws.ToString(e.Message))
				}
			}
		}
		_ = stream.Close()
	}
	return nil
}

func (s *stage) pollLogs(ctx context.Context, w io.Writer, group, filter string, since time.Time) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			last, err := s.printLogs(ctx, w, group, filter, since.Add(time.Millisecond))
			if err != nil {
				return err
			}
			since = last
		}
	}
}
