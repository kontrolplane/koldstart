package cli

import (
	"context"
	"errors"
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

// The layouts of a log event's time: the time of day, and with the date when --since goes back a
// day or more.
const (
	timeLayout     = "15:04:05.000"
	dateTimeLayout = "2006-01-02 15:04:05.000"
)

func logsCommand(name string, opts *Options) *cobra.Command {
	var function, filter string
	var tail bool
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "print a function's logs, --tail to follow them",
		Long: `logs prints a function's logs of the last --since, 10m by default, in local time, the date too
when --since is 24h or more. --tail follows them as they come, until ctrl+c. --filter takes a
cloudwatch logs filter pattern, such as ERROR or "{ $.level = \"error\" }".`,
		Example: fmt.Sprintf(`  %[1]s logs -f api
  %[1]s logs -f api --tail
  %[1]s logs -f api --since 2h --filter ERROR`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if since <= 0 {
				return usage(cmd, fmt.Errorf("--since %s: want a duration back from now, such as 30m or 2h", since))
			}
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			if err := s.function(function); err != nil {
				return err
			}
			group := s.state.Resources[desired.LogGroupID(function)]
			if group == nil {
				return fmt.Errorf("%s is not deployed to %s: deploy it with %s deploy --stage %s", function, s.Stage, name, s.Stage)
			}
			p := &logPrinter{w: cmd.OutOrStdout(), layout: timeLayout}
			if since >= 24*time.Hour {
				p.layout = dateTimeLayout
			}
			start := time.Now().Add(-since)
			last, err := s.printLogs(ctx, p, group.PhysicalID, filter, start)
			if err != nil {
				return err
			}
			if !tail {
				if p.printed == 0 {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "no logs of %s in the last %s: --since looks further back, --tail follows them\n", function, Duration(since))
				}
				return nil
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "following the logs of %s, ctrl+c to stop\n", function)
			return s.tailLogs(ctx, p, group.Attributes["Arn"], group.PhysicalID, filter, last)
		},
	}
	cmd.Flags().StringVarP(&function, "function", "f", "", "the function whose logs to print")
	cmd.Flags().BoolVarP(&tail, "tail", "t", false, "follow the logs as they come")
	cmd.Flags().DurationVar(&since, "since", 10*time.Minute, "how far back to start")
	cmd.Flags().StringVar(&filter, "filter", "", "a cloudwatch logs filter pattern")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

// logPrinter writes log events one a line, the time first, and counts them.
type logPrinter struct {
	w       io.Writer
	layout  string
	printed int
}

// print writes an event in local time. The lines of a message after its first are indented
// under its first, so a stack trace reads as one event.
func (p *logPrinter) print(timestamp int64, message string) {
	t := time.UnixMilli(timestamp).Local().Format(p.layout)
	indent := "\n" + strings.Repeat(" ", len(t)+2)
	message = strings.ReplaceAll(strings.TrimRight(message, "\r\n"), "\n", indent)
	_, _ = fmt.Fprintf(p.w, "%s  %s\n", t, message)
	p.printed++
}

// printLogs prints the events of the group since start, and returns the time of the last one.
func (s *stage) printLogs(ctx context.Context, p *logPrinter, group, filter string, start time.Time) (time.Time, error) {
	last := start
	in := &cloudwatchlogs.FilterLogEventsInput{LogGroupName: &group, StartTime: aws.Int64(start.UnixMilli())}
	if filter != "" {
		in.FilterPattern = &filter
	}
	pages := cloudwatchlogs.NewFilterLogEventsPaginator(s.sess.Logs, in)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if errors.Is(err, context.Canceled) {
			return last, nil
		}
		if err != nil {
			return last, fmt.Errorf("reading the logs of %s: %w", group, err)
		}
		for _, e := range page.Events {
			p.print(aws.ToInt64(e.Timestamp), aws.ToString(e.Message))
			if t := time.UnixMilli(aws.ToInt64(e.Timestamp)); t.After(last) {
				last = t
			}
		}
	}
	return last, nil
}

// tailLogs follows the group with a live tail, starting again when a session ends, and polls
// where live tail is not there, as on an emulator, or when a session fails.
func (s *stage) tailLogs(ctx context.Context, p *logPrinter, arn, group, filter string, since time.Time) error {
	arn = strings.TrimSuffix(arn, ":*")
	for ctx.Err() == nil {
		in := &cloudwatchlogs.StartLiveTailInput{LogGroupIdentifiers: []string{arn}}
		if filter != "" {
			in.LogEventFilterPattern = &filter
		}
		out, err := s.sess.Logs.StartLiveTail(ctx, in)
		if err != nil {
			slog.Debug("no live tail, polling", "error", err)
			return s.pollLogs(ctx, p, group, filter, since)
		}
		stream := out.GetStream()
		for event := range stream.Events() {
			if update, ok := event.(*types.StartLiveTailResponseStreamMemberSessionUpdate); ok {
				for _, e := range update.Value.SessionResults {
					p.print(aws.ToInt64(e.Timestamp), aws.ToString(e.Message))
					if t := time.UnixMilli(aws.ToInt64(e.Timestamp)); t.After(since) {
						since = t
					}
				}
			}
		}
		err = stream.Err()
		_ = stream.Close()
		if err != nil && ctx.Err() == nil {
			slog.Debug("live tail ended, polling", "error", err)
			return s.pollLogs(ctx, p, group, filter, since)
		}
	}
	return nil
}

// pollLogs prints the events after since every two seconds, until ctx ends.
func (s *stage) pollLogs(ctx context.Context, p *logPrinter, group, filter string, since time.Time) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			last, err := s.printLogs(ctx, p, group, filter, since.Add(time.Millisecond))
			if err != nil {
				return err
			}
			since = last
		}
	}
}
