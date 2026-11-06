// Package local runs functions on this machine the way lambda runs them: a process that asks
// an emulator of the lambda runtime api for the next event and posts back the result.
package local

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FunctionError is an invocation the function failed: the error it posted, or why it never
// answered.
type FunctionError struct {
	Type    string            `json:"errorType"`
	Message string            `json:"errorMessage"`
	Trace   []json.RawMessage `json:"stackTrace,omitempty"`
}

func (e *FunctionError) Error() string {
	return e.Type + ": " + e.Message
}

// Runtime is the runtime api of one function and the process that runs it. The process is
// started on the first invocation, kept for the next, and started again after it exits or times
// out.
type Runtime struct {
	// Name and ARN are what the function is told about itself, and Timeout how long an
	// invocation may take.
	Name    string
	ARN     string
	Timeout time.Duration
	// Command returns the process to run, told the runtime api is at addr.
	Command func(addr string) *exec.Cmd
	// Stop, when set, ends what the process started that outlives it, such as a container.
	Stop func()
	// Output receives the function's stdout and stderr.
	Output io.Writer

	mu       sync.Mutex
	invokeMu sync.Mutex
	listener net.Listener
	server   *http.Server
	next     chan *invocation
	pending  map[string]*invocation
	cmd      *exec.Cmd
	exited   chan struct{}
	initErr  *FunctionError
}

type invocation struct {
	id       string
	payload  []byte
	deadline time.Time
	done     chan outcome
}

type outcome struct {
	payload []byte
	err     error
}

// Start listens on a free port of 127.0.0.1.
func (r *Runtime) Start() error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("starting the runtime api: %w", err)
	}
	r.listener = l
	r.next = make(chan *invocation)
	r.pending = map[string]*invocation{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /2018-06-01/runtime/invocation/next", r.handleNext)
	mux.HandleFunc("POST /2018-06-01/runtime/invocation/{id}/response", r.handleResponse)
	mux.HandleFunc("POST /2018-06-01/runtime/invocation/{id}/error", r.handleError)
	mux.HandleFunc("POST /2018-06-01/runtime/init/error", r.handleInitError)
	r.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = r.server.Serve(l) }()
	return nil
}

// Addr is where the runtime api listens, as AWS_LAMBDA_RUNTIME_API takes it.
func (r *Runtime) Addr() string {
	return r.listener.Addr().String()
}

// Close stops the process and the runtime api.
func (r *Runtime) Close() error {
	r.stop()
	if r.server == nil {
		return nil
	}
	return r.server.Close()
}

// Restart stops the process, so the next invocation starts one with what is built now.
func (r *Runtime) Restart() {
	r.stop()
}

func (r *Runtime) stop() {
	r.mu.Lock()
	cmd, exited := r.cmd, r.exited
	r.cmd = nil
	r.mu.Unlock()
	if cmd == nil {
		return
	}
	if r.Stop != nil {
		r.Stop()
	}
	_ = cmd.Process.Kill()
	<-exited
}

// startProcess starts the function's process when none runs, and returns what closes when it exits.
func (r *Runtime) startProcess() (chan struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		return r.exited, nil
	}
	cmd := r.Command(r.Addr())
	cmd.Stdout, cmd.Stderr = r.Output, r.Output
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", r.Name, err)
	}
	r.cmd, r.exited, r.initErr = cmd, make(chan struct{}), nil
	exited := r.exited
	go func() {
		_ = cmd.Wait()
		r.mu.Lock()
		if r.cmd == cmd {
			r.cmd = nil
		}
		r.mu.Unlock()
		close(exited)
	}()
	return exited, nil
}

// traceID is an x-ray trace header of the form lambda passes, for code that reads it.
func traceID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return fmt.Sprintf("Root=1-%08x-%s;Sampled=0", time.Now().Unix(), hex.EncodeToString(b))
}

func requestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Invoke runs the function with payload, one invocation at a time as one lambda instance does,
// and returns what it answered.
func (r *Runtime) Invoke(ctx context.Context, payload []byte) ([]byte, error) {
	r.invokeMu.Lock()
	defer r.invokeMu.Unlock()
	if !json.Valid(payload) {
		return nil, errors.New("the event is not json")
	}
	exited, err := r.startProcess()
	if err != nil {
		return nil, err
	}
	inv := &invocation{id: requestID(), payload: payload, deadline: time.Now().Add(r.Timeout), done: make(chan outcome, 1)}
	timer := time.NewTimer(r.Timeout)
	defer timer.Stop()

	select {
	case r.next <- inv:
	case <-exited:
		return nil, r.exitError()
	case <-timer.C:
		r.stop()
		return nil, &FunctionError{Type: "Runtime.InitTimeout", Message: fmt.Sprintf("the function did not ask for an event within %s", r.Timeout)}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case out := <-inv.done:
		return out.payload, out.err
	case <-exited:
		return nil, r.exitError()
	case <-timer.C:
		r.stop()
		return nil, &FunctionError{Type: "Sandbox.Timedout", Message: fmt.Sprintf("Task timed out after %.2f seconds", r.Timeout.Seconds())}
	case <-ctx.Done():
		r.stop()
		return nil, ctx.Err()
	}
}

func (r *Runtime) exitError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initErr != nil {
		return r.initErr
	}
	return &FunctionError{Type: "Runtime.ExitError", Message: "the function's process exited"}
}

func (r *Runtime) handleNext(w http.ResponseWriter, req *http.Request) {
	select {
	case inv := <-r.next:
		r.mu.Lock()
		r.pending[inv.id] = inv
		r.mu.Unlock()
		h := w.Header()
		h.Set("Lambda-Runtime-Aws-Request-Id", inv.id)
		h.Set("Lambda-Runtime-Deadline-Ms", strconv.FormatInt(inv.deadline.UnixMilli(), 10))
		h.Set("Lambda-Runtime-Invoked-Function-Arn", r.ARN)
		h.Set("Lambda-Runtime-Trace-Id", traceID())
		h.Set("Content-Type", "application/json")
		_, _ = w.Write(inv.payload)
	case <-req.Context().Done():
	}
}

func (r *Runtime) take(id string) *invocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv := r.pending[id]
	delete(r.pending, id)
	return inv
}

func (r *Runtime) handleResponse(w http.ResponseWriter, req *http.Request) {
	inv := r.take(req.PathValue("id"))
	if inv == nil {
		http.Error(w, `{"errorType": "InvalidRequestID"}`, http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(req.Body)
	inv.done <- outcome{payload: body}
	w.WriteHeader(http.StatusAccepted)
}

// readError reads the error a function posted. Without a type in the body, the type is the one
// its header names, or Unhandled.
func readError(req *http.Request) *FunctionError {
	body, _ := io.ReadAll(req.Body)
	fe := &FunctionError{}
	if err := json.Unmarshal(body, fe); err != nil {
		fe = &FunctionError{Message: strings.TrimSpace(string(body))}
	}
	if fe.Type == "" {
		fe.Type = cmp.Or(req.Header.Get("Lambda-Runtime-Function-Error-Type"), "Unhandled")
	}
	return fe
}

func (r *Runtime) handleError(w http.ResponseWriter, req *http.Request) {
	inv := r.take(req.PathValue("id"))
	if inv == nil {
		http.Error(w, `{"errorType": "InvalidRequestID"}`, http.StatusBadRequest)
		return
	}
	fe := readError(req)
	payload, _ := json.Marshal(fe)
	inv.done <- outcome{payload: payload, err: fe}
	w.WriteHeader(http.StatusAccepted)
}

func (r *Runtime) handleInitError(w http.ResponseWriter, req *http.Request) {
	fe := readError(req)
	r.mu.Lock()
	r.initErr = fe
	r.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}
