package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/local"
)

func offlineCommand(name string, opts *Options) *cobra.Command {
	var port int
	var yes bool
	cmd := &cobra.Command{
		Use:   "offline",
		Short: "serve the http routes on this machine",
		Long: `offline serves every http route of the stage on 127.0.0.1, each request an invocation of its
function on this machine, as invoke --local runs it. A change to a source file of the project
rebuilds the functions, and the next request runs the new code.`,
		Example: fmt.Sprintf("  %[1]s offline\n  %[1]s offline --port 8080", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			if err := confirmProd(cmd, s, yes); err != nil {
				return err
			}
			routes := s.routes()
			if len(routes) == 0 {
				return fmt.Errorf("no function in %s has an http route to serve: add a kdst.HttpRoute to its events, or run it with %s invoke --local", filepath.Base(s.Path), name)
			}
			return s.serve(ctx, cmd, routes, port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 3000, "the port to listen on")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "run against prod without asking")
	return cmd
}

// routes are the http routes of the stage's functions.
func (s *stage) routes() []local.Route {
	var routes []local.Route
	for _, fn := range slices.Sorted(maps.Keys(s.Functions)) {
		for _, e := range s.Functions[fn].Events {
			if e.Type == config.EventHTTPRoute {
				routes = append(routes, local.Route{Method: strings.ToUpper(e.Method), Path: e.Path, Function: fn})
			}
		}
	}
	return routes
}

// runtimes are the functions offline serves, each a warm process, rebuilt on a change.
type runtimes struct {
	mu         sync.Mutex
	byFunction map[string]*local.Runtime
	functions  []local.Function
}

func (r *runtimes) get(function string) *local.Runtime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byFunction[function]
}

// Invoke implements local.Invoker.
func (r *runtimes) Invoke(ctx context.Context, function string, payload []byte) ([]byte, error) {
	rt := r.get(function)
	if rt == nil {
		return nil, fmt.Errorf("%s is not running", function)
	}
	return rt.Invoke(ctx, payload)
}

// Close stops every function.
func (r *runtimes) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rt := range r.byFunction {
		_ = rt.Close()
	}
}

// startRuntimes builds and starts each function a route invokes, its output prefixed with its name.
func (s *stage) startRuntimes(ctx context.Context, cmd *cobra.Command, routes []local.Route) (*runtimes, error) {
	var mu sync.Mutex
	rts := &runtimes{byFunction: map[string]*local.Runtime{}}
	for _, r := range routes {
		if rts.byFunction[r.Function] != nil {
			continue
		}
		f, err := s.localFunction(ctx, r.Function, &prefixed{mu: &mu, w: cmd.ErrOrStderr(), prefix: "[" + r.Function + "] "})
		if err != nil {
			rts.Close()
			return nil, err
		}
		rt, err := s.runner().Start(ctx, f)
		if err != nil {
			rts.Close()
			return nil, err
		}
		rts.byFunction[r.Function] = rt
		rts.functions = append(rts.functions, f)
	}
	return rts, nil
}

// rebuild builds the functions again, and has each start a new process on its next request. It
// says which were rebuilt, and why any were not.
func (s *stage) rebuild(ctx context.Context, cmd *cobra.Command, rts *runtimes) {
	start := time.Now()
	var rebuilt []string
	for _, f := range rts.functions {
		if err := s.runner().Build(ctx, f); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "rebuilding %s: %v\n", f.Name, err)
			continue
		}
		rts.get(f.Name).Restart()
		rebuilt = append(rebuilt, f.Name)
	}
	if len(rebuilt) > 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "rebuilt %s in %s\n", strings.Join(rebuilt, ", "), time.Since(start).Round(100*time.Millisecond))
	}
}

// listen listens on 127.0.0.1:port, and says to pass another --port when it is taken.
func listen(port int) (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("port %d is in use: stop what listens on it, or pass --port", port)
	}
	if err != nil {
		return nil, fmt.Errorf("listening on port %d: %w", port, err)
	}
	return listener, nil
}

// printServing says where the routes are served, which function each invokes, and how to stop.
func printServing(w io.Writer, app, stage string, addr net.Addr, routes []local.Route) {
	_, _ = fmt.Fprintf(w, "serving %s/%s on http://%s\n\n", app, stage, addr)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range routes {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", r.Key(), r.Function)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w, "\na change to a source file rebuilds the functions; ctrl+c stops")
}

// serve serves the routes on 127.0.0.1:port until ctx ends. It listens before it builds, so a
// port in use fails at once.
func (s *stage) serve(ctx context.Context, cmd *cobra.Command, routes []local.Route, port int) error {
	listener, err := listen(port)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	rts, err := s.startRuntimes(ctx, cmd, routes)
	if err != nil {
		return err
	}
	defer rts.Close()

	printServing(cmd.OutOrStdout(), s.App, s.Stage, listener.Addr(), routes)
	srv := &http.Server{Handler: &local.Offline{Routes: routes, Invoker: rts, Log: cmd.ErrOrStderr()}, ReadHeaderTimeout: 10 * time.Second}
	go local.Watch(ctx, s.Root, 500*time.Millisecond, func() { s.rebuild(ctx, cmd, rts) })
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
