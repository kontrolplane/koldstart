package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

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

func logsCommand(name string, opts *Options, c completer) *cobra.Command {
	var function, filter, output string
	var tail bool
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "print a function's logs, --tail to follow them",
		Long: `logs prints a function's logs of the last --since, 10m by default, in local time, the date too
when --since is 24h or more. --tail follows them as they come, until ctrl+c. --filter takes a
cloudwatch logs filter pattern, such as ERROR or "{ $.level = \"error\" }".

On a terminal, a line of json a structured logger wrote, such as powertools, pino, zap or
python's logging, is shown with its level and message first and its other fields faint after
them. Piped, every line is as the function wrote it.

-o json prints each event as one json object a line, its keys sorted, for jq: its time in utc,
its log stream, its message, and the fields of a message that is a json object:

  {"fields":{"level":"INFO","message":"order placed"},"message":"{\"level\":\"INFO\",\"message\":\"order placed\"}","stream":"2026/11/06/[$LATEST]0123abcd","timestamp":"2026-11-06T10:00:00.123Z"}`,
		Example: fmt.Sprintf(`  %[1]s logs -f api
  %[1]s logs -f api --tail
  %[1]s logs -f api --since 2h --filter ERROR
  %[1]s logs -f api -o json | jq -r 'select(.fields.level == "ERROR") | .message'`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(cmd, output); err != nil {
				return err
			}
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
			p := newLogPrinter(cmd.OutOrStdout(), output, since)
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
	functionFlag(cmd, &function, "the function whose logs to print", opts, c)
	cmd.Flags().BoolVarP(&tail, "tail", "t", false, "follow the logs as they come")
	cmd.Flags().DurationVar(&since, "since", 10*time.Minute, "how far back to start")
	cmd.Flags().StringVar(&filter, "filter", "", "a cloudwatch logs filter pattern")
	outputFlag(cmd, &output)
	return cmd
}

// logPrinter writes log events one a line, the time first, and counts them.
type logPrinter struct {
	w      io.Writer
	layout string
	// json writes each event as a json object a line instead, for a script.
	json bool
	// structured writes a line of json a logger wrote with its level and message first and its
	// other fields after them, as on a terminal.
	structured bool
	colour     palette
	printed    int
}

// newLogPrinter writes to w as -o says: json, or text, with the lines of json a logger wrote
// reworded on a terminal.
func newLogPrinter(w io.Writer, output string, since time.Duration) *logPrinter {
	f, ok := w.(*os.File)
	p := &logPrinter{w: w, layout: timeLayout, json: output == outputJSON, structured: ok && isTerminal(f), colour: paletteFor(w)}
	if since >= 24*time.Hour {
		p.layout = dateTimeLayout
	}
	return p
}

// logLine is an event as logs -o json writes it. Fields are those of a message that is a json
// object, as a structured logger writes one.
type logLine struct {
	Timestamp string         `json:"timestamp"`
	Stream    string         `json:"stream"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// jsonTimeLayout is the time of an event in logs -o json: rfc 3339 in utc, to the millisecond.
const jsonTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// print writes an event in local time. The lines of a message after its first are indented
// under its first, so a stack trace reads as one event.
func (p *logPrinter) print(timestamp int64, stream, message string) {
	message = strings.TrimRight(message, "\r\n")
	p.printed++
	if p.json {
		line := logLine{Timestamp: time.UnixMilli(timestamp).UTC().Format(jsonTimeLayout), Stream: stream, Message: message}
		if fields, ok := jsonObject(message); ok {
			line.Fields = fields
		}
		_ = writeJSONLine(p.w, line)
		return
	}
	t := time.UnixMilli(timestamp).Local().Format(p.layout)
	if p.structured {
		if s, ok := structuredLine(message, p.colour); ok {
			message = s
		}
	}
	indent := "\n" + strings.Repeat(" ", len(t)+2)
	message = strings.ReplaceAll(message, "\n", indent)
	_, _ = fmt.Fprintf(p.w, "%s  %s\n", t, message)
}

// jsonObject reads a message that is one json object, its numbers as they were written.
func jsonObject(message string) (map[string]any, bool) {
	if !strings.HasPrefix(strings.TrimSpace(message), "{") {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(message))
	dec.UseNumber()
	var fields map[string]any
	if dec.Decode(&fields) != nil || dec.More() {
		return nil, false
	}
	return fields, true
}

// The fields structured loggers write the message and the level in: powertools, pino, zap,
// python's logging as json, and lambda's own json format.
var (
	logMessageKeys = []string{"message", "msg"}
	logLevelKeys   = []string{"level", "levelname", "severity", "lvl"}
)

// field is one field of a line of json, in the order it was written.
type field struct {
	key   string
	value json.RawMessage
}

// structuredLine rewords a line of json a logger wrote for a person: its level, tinted, its
// message, and its other fields faint, as key=value in the order they were written. It returns
// false for a line that is not a json object with a message.
func structuredLine(line string, colour palette) (string, bool) {
	fields, ok := objectFields(line)
	if !ok {
		return "", false
	}
	message, level := -1, -1
	for i, f := range fields {
		switch key := strings.ToLower(f.key); {
		case message < 0 && slices.Contains(logMessageKeys, key):
			message = i
		case level < 0 && slices.Contains(logLevelKeys, key):
			level = i
		}
	}
	if message < 0 {
		return "", false
	}
	var b strings.Builder
	if level >= 0 {
		name := levelName(fields[level].value)
		b.WriteString(colour.paint(levelTone(name), padRight(name, 5)) + "  ")
	}
	b.WriteString(printable(fieldText(fields[message].value)))
	var rest []string
	for i, f := range fields {
		if i != message && i != level {
			rest = append(rest, fieldValue(f.key)+"="+shownValue(f.value))
		}
	}
	if len(rest) > 0 {
		b.WriteString("  " + colour.paint(sgrFaint, strings.Join(rest, " ")))
	}
	return b.String(), true
}

// objectFields reads the fields of a line that is one json object, in the order they were
// written.
func objectFields(line string) ([]field, bool) {
	if !strings.HasPrefix(strings.TrimSpace(line), "{") {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(line))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	var fields []field
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		fields = append(fields, field{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil || dec.More() {
		return nil, false
	}
	return fields, true
}

// pinoLevels are the names of pino's levels, which it writes as numbers.
var pinoLevels = map[string]string{"10": "TRACE", "20": "DEBUG", "30": "INFO", "40": "WARN", "50": "ERROR", "60": "FATAL"}

// levelName is a level as a word in capitals, pino's numbers too.
func levelName(v json.RawMessage) string {
	text := fieldText(v)
	if name, ok := pinoLevels[text]; ok {
		return name
	}
	return strings.ToUpper(printable(text))
}

// levelTone is the colour of a level: red for an error and worse, yellow for a warning, faint
// for debug and trace, and green for the rest.
func levelTone(level string) string {
	switch level {
	case "ERROR", "ERR", "FATAL", "CRITICAL", "PANIC", "ALERT", "EMERGENCY":
		return sgrRed
	case "WARN", "WARNING":
		return sgrYellow
	case "DEBUG", "TRACE":
		return sgrFaint
	}
	return sgrGreen
}

// fieldText is the value of a field as text: a string as it is, and anything else as compact
// json.
func fieldText(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return string(v)
	}
	return b.String()
}

// shownValue is the value of a field as key=value writes it: a string as fieldValue writes it,
// and anything else as compact json, which reads as it is.
func shownValue(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return fieldValue(s)
	}
	return printable(fieldText(v))
}

// fieldValue is a key or a string as key=value writes it: quoted when it is empty or holds a
// space, a quote, an = or a character that does not print.
func fieldValue(s string) string {
	if s != "" && !strings.ContainsAny(s, ` "=`) && !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return s
	}
	return strconv.Quote(s)
}

// printable writes the control characters of s, which a terminal would act on, as \u escapes,
// all but newlines and tabs.
func printable(s string) string {
	if !strings.ContainsFunc(s, isEscaped) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isEscaped(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isEscaped reports whether printable escapes r.
func isEscaped(r rune) bool {
	return unicode.IsControl(r) && r != '\n' && r != '\t'
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
			p.print(aws.ToInt64(e.Timestamp), aws.ToString(e.LogStreamName), aws.ToString(e.Message))
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
					p.print(aws.ToInt64(e.Timestamp), aws.ToString(e.LogStreamName), aws.ToString(e.Message))
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
