package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

// testPlan is a plan of three changes, for progress to count.
var testPlan = &engine.Plan{Changes: []engine.Change{
	{ID: "role/api", Action: engine.NoOp},
	{ID: "function/api", Action: engine.Update},
	{ID: "resource/orders", Action: engine.Create},
	{ID: "url/api", Action: engine.Create},
}}

// testProgress is progress written to a buffer, on a clock the test moves.
func testProgress(live bool) (*progress, *bytes.Buffer, *time.Time) {
	var out bytes.Buffer
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	p := newProgress(&out, testPlan)
	p.now = func() time.Time { return now }
	p.live = live
	p.width = func() int { return 40 }
	p.start = now
	return p, &out, &now
}

// TestProgressLines checks each finished node gets a line, padded to the widest id, that says
// what was done, failed or stopped, and that the summary counts them.
func TestProgressLines(t *testing.T) {
	tests := []struct {
		name    string
		events  []engine.Event
		want    []string
		summary string
	}{
		{
			name: "done",
			events: []engine.Event{
				{ID: "function/api", Action: engine.Update, Done: true, Duration: 1240 * time.Millisecond},
				{ID: "url/api", Action: engine.Create, Done: true, Duration: 12 * time.Millisecond},
			},
			want:    []string{"  ✓ function/api     updated in 1.2s", "  ✓ url/api          created in 12ms"},
			summary: "1 created, 1 updated",
		},
		{
			name: "failed and stopped",
			events: []engine.Event{
				{ID: "url/api", Action: engine.Create, Done: true, Err: errors.New("denied"), Duration: 3 * time.Second},
				{ID: "function/api", Action: engine.Update, Done: true, Err: fmt.Errorf("calling lambda: %w", context.Canceled), Duration: 2 * time.Minute},
			},
			want:    []string{"  ✗ url/api          create failed after 3s", "  ✗ function/api     update stopped after 2m"},
			summary: "1 failed, 1 stopped",
		},
		{
			name:    "nothing",
			summary: "nothing changed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, out, _ := testProgress(false)
			for _, ev := range tt.events {
				start := ev
				start.Done, start.Err = false, nil
				p.event(start)
				p.event(ev)
			}
			p.end()
			got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			if len(tt.want) == 0 && out.Len() == 0 {
				got = nil
			}
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			if s := p.summary(); s != tt.summary {
				t.Errorf("summary = %q, want %q", s, tt.summary)
			}
		})
	}
}

// TestProgressStillGoing checks that, without a terminal, a node that takes long says so once
// every progressNote, and nothing else is written while it runs.
func TestProgressStillGoing(t *testing.T) {
	tests := []struct {
		after time.Duration
		want  string
	}{
		{after: 10 * time.Second, want: ""},
		{after: 31 * time.Second, want: "  … resource/orders  still creating after 31s\n"},
		{after: 45 * time.Second, want: "  … resource/orders  still creating after 31s\n"},
		{after: 61 * time.Second, want: "  … resource/orders  still creating after 31s\n  … resource/orders  still creating after 1m1s\n"},
	}
	p, out, now := testProgress(false)
	start := *now
	p.event(engine.Event{ID: "resource/orders", Action: engine.Create})
	for _, tt := range tests {
		*now = start.Add(tt.after)
		p.tick()
		if out.String() != tt.want {
			t.Errorf("after %s, wrote %q, want %q", tt.after, out.String(), tt.want)
		}
	}
	if strings.Contains(out.String(), "\r") || strings.Contains(out.String(), "\x1b") {
		t.Errorf("wrote cursor control without a terminal: %q", out.String())
	}
}

// TestProgressStatusLine checks that on a terminal the status line is drawn in place, cut to the
// terminal's width, cleared before a finished node's line, and cleared at the end.
func TestProgressStatusLine(t *testing.T) {
	tests := []struct {
		name  string
		event engine.Event
		want  []string
	}{
		{
			name:  "a node starts",
			event: engine.Event{ID: "resource/orders", Action: engine.Create},
			want:  []string{"\r", "0 of 3", "resource/orders", "\x1b[K"},
		},
		{
			name:  "another starts",
			event: engine.Event{ID: "function/api", Action: engine.Update},
			want:  []string{"0 of 3", "resource/orders, func"},
		},
		{
			name:  "one finishes",
			event: engine.Event{ID: "resource/orders", Action: engine.Create, Done: true, Duration: time.Second},
			want:  []string{"\r\x1b[K  ✓ resource/orders  created in 1s\n", "1 of 3", "function/api"},
		},
	}
	p, out, _ := testProgress(true)
	for _, tt := range tests {
		out.Reset()
		p.event(tt.event)
		got := out.String()
		for _, want := range tt.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: wrote %q, which lacks %q", tt.name, got, want)
			}
		}
		status := got[strings.LastIndex(got, "\r")+1:]
		if w := ansi.StringWidth(strings.TrimSuffix(status, "\x1b[K")); w >= 40 {
			t.Errorf("%s: the status line is %d wide, on a terminal 40 wide", tt.name, w)
		}
	}
	out.Reset()
	p.end()
	if out.String() != "\r\x1b[K" {
		t.Errorf("end wrote %q, want the status line cleared", out.String())
	}
}

// TestEndProgress checks the end of an apply says what it did on stdout, or on stderr how far a
// failed or stopped one got, and that a stopped one says to run it again rather than listing the
// calls ctrl+c cut short.
func TestEndProgress(t *testing.T) {
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name       string
		plan       *engine.Plan
		err        error
		stdout     string
		stderr     string
		wantErr    string
		wantNotErr string
	}{
		{name: "applied", plan: testPlan, stdout: "\ndeployed orders/dev in 0ms: 1 created\n"},
		{name: "no changes", plan: &engine.Plan{}},
		{name: "failed", plan: testPlan, err: applyFailed(context.Background(), errors.New("url/api: denied"), "deploy"),
			stderr: "\nfailed after 0ms: 1 created\n", wantErr: "url/api: denied\nwhat was done is in the state: deploy again to finish"},
		{name: "stopped", plan: testPlan, err: applyFailed(stopped, errors.New("url/api: context canceled"), "remove"),
			stderr: "\nstopped after 0ms: 1 created\n", wantErr: "stopped: what was done is in the state, remove again to finish", wantNotErr: "url/api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := testProgress(false)
			if tt.plan.HasChanges() {
				p.event(engine.Event{ID: "url/api", Action: engine.Create, Done: true})
			}
			var stdout, stderr bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			s := &stage{project: &project{Project: &config.Project{App: "orders", Stage: "dev"}}}
			err := s.endProgress(cmd, p, tt.plan, "deployed", tt.err)
			if stdout.String() != tt.stdout || stderr.String() != tt.stderr {
				t.Errorf("wrote %q on stdout and %q on stderr, want %q and %q", stdout.String(), stderr.String(), tt.stdout, tt.stderr)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("endProgress = %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("endProgress = %v, want %q", err, tt.wantErr)
			case tt.wantNotErr != "" && strings.Contains(err.Error(), tt.wantNotErr):
				t.Errorf("endProgress = %v, which lists %q", err, tt.wantNotErr)
			}
		})
	}
}

// TestColourWanted checks colour is only written to a terminal, and never with NO_COLOR set or
// a dumb terminal.
func TestColourWanted(t *testing.T) {
	tests := []struct {
		name     string
		terminal bool
		env      map[string]string
		want     bool
	}{
		{name: "terminal", terminal: true, env: map[string]string{"TERM": "xterm-256color"}, want: true},
		{name: "pipe", terminal: false, env: map[string]string{"TERM": "xterm-256color"}, want: false},
		{name: "NO_COLOR", terminal: true, env: map[string]string{"NO_COLOR": "1"}, want: false},
		{name: "dumb", terminal: true, env: map[string]string{"TERM": "dumb"}, want: false},
	}
	for _, tt := range tests {
		if got := colourWanted(tt.terminal, func(k string) string { return tt.env[k] }); got != tt.want {
			t.Errorf("%s: colourWanted = %v, want %v", tt.name, got, tt.want)
		}
	}
	if got := paletteFor(&bytes.Buffer{}).paint(sgrRed, "x"); got != "x" {
		t.Errorf("a buffer is painted: %q", got)
	}
}

// blockingReader is stdin nobody types on.
type blockingReader struct{ unblock chan struct{} }

// Read waits until the test ends.
func (r blockingReader) Read([]byte) (int, error) {
	<-r.unblock
	return 0, errors.New("closed")
}

// TestAsk checks an answer is accepted only when it is one asked for, and that ctrl+c and the end
// of input cancel without waiting for a line.
func TestAsk(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		accepted []string
		cancel   bool
		wantErr  bool
	}{
		{name: "yes", input: "y\n"},
		{name: "yes in capitals", input: " YES \n"},
		{name: "no", input: "n\n", wantErr: true},
		{name: "enter", input: "\n", wantErr: true},
		{name: "end of input", input: "", wantErr: true},
		{name: "the stage's name", input: "dev\n", accepted: []string{"dev"}},
		{name: "another name", input: "prod\n", accepted: []string{"dev"}, wantErr: true},
		{name: "ctrl+c", cancel: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var in io.Reader = strings.NewReader(tt.input)
			if tt.cancel {
				r := blockingReader{unblock: make(chan struct{})}
				defer close(r.unblock)
				in = r
				cancel()
			}
			var out bytes.Buffer
			err := ask(ctx, in, &out, "go on?", tt.accepted...)
			if (err != nil) != tt.wantErr {
				t.Errorf("ask = %v, want an error: %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "nothing was changed") {
				t.Errorf("the error %q does not say nothing was changed", err)
			}
		})
	}
}

// TestLockedError checks the error for a held lock says who holds it, doing what, since when,
// and the command that releases it.
func TestLockedError(t *testing.T) {
	now := time.Date(2026, 11, 5, 14, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		held state.LockInfo
		want string
	}{
		{
			name: "today",
			held: state.LockInfo{Who: "levi", Where: "laptop", Operation: "deploy", Since: now.Add(-3*time.Minute - 12*time.Second)},
			want: "orders/dev is locked: levi on laptop has been running deploy since 14:26:48, 3m12s ago\n" +
				"wait for it to finish, or, when it is gone, release the lock with kdst unlock --stage dev",
		},
		{
			name: "days ago",
			held: state.LockInfo{Who: "ci", Operation: "remove", Since: now.Add(-50 * time.Hour)},
			want: "orders/dev is locked: ci has been running remove since 2026-11-03 12:30:00, 50h ago\n" +
				"wait for it to finish, or, when it is gone, release the lock with kdst unlock --stage dev",
		},
	}
	for _, tt := range tests {
		if got := lockedError("orders", "dev", tt.held, "kdst unlock --stage dev", now).Error(); got != tt.want {
			t.Errorf("%s: lockedError =\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}
}

// TestLockHeld checks a lock taken in another time zone reads in the time zone of now, today's
// without its date, and a lock that leaves out who, what or when still reads as a sentence.
func TestLockHeld(t *testing.T) {
	amsterdam := time.FixedZone("CET", 3600)
	now := time.Date(2026, 11, 5, 0, 30, 0, 0, amsterdam)
	tests := []struct {
		name string
		held state.LockInfo
		want string
	}{
		{
			name: "taken in utc, today here",
			held: state.LockInfo{Who: "levi", Operation: "deploy", Since: time.Date(2026, 11, 4, 23, 20, 0, 0, time.UTC)},
			want: "levi has been running deploy since 00:20:00, 10m ago",
		},
		{
			name: "taken in utc, yesterday here",
			held: state.LockInfo{Who: "levi", Operation: "deploy", Since: time.Date(2026, 11, 4, 22, 50, 0, 0, time.UTC)},
			want: "levi has been running deploy since 2026-11-04 23:50:00, 40m ago",
		},
		{
			name: "nothing recorded",
			held: state.LockInfo{},
			want: "someone has held it, since a time it did not record",
		},
	}
	for _, tt := range tests {
		if got := LockHeld(tt.held, now); got != tt.want {
			t.Errorf("%s: LockHeld = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestUnlockHint checks the command that releases a lock keeps the flags that say where the
// state is.
func TestUnlockHint(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"deploy"}, want: "kdst unlock --stage dev"},
		{args: []string{"deploy", "--profile", "prod", "-c", "koldstart.prod.pkl", "--yes"}, want: "kdst unlock --stage dev --config koldstart.prod.pkl --profile prod"},
	}
	for _, tt := range tests {
		root := &cobra.Command{Use: "kdst"}
		root.PersistentFlags().StringP("config", "c", "koldstart.pkl", "")
		root.PersistentFlags().String("profile", "", "")
		root.PersistentFlags().String("region", "", "")
		var got string
		deploy := &cobra.Command{Use: "deploy", RunE: func(cmd *cobra.Command, _ []string) error {
			got = unlockHint(cmd, "dev")
			return nil
		}}
		deploy.Flags().Bool("yes", false, "")
		root.AddCommand(deploy)
		root.SetArgs(tt.args)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("unlockHint for %v = %q, want %q", tt.args, got, tt.want)
		}
	}
}
