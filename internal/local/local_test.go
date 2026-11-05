package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// syncBuffer is a buffer the function's output goroutines and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start builds fn of the project in root and starts its runtime api.
func start(t *testing.T, root string, fn config.Function, env map[string]string) (*Runtime, *syncBuffer) {
	t.Helper()
	var out syncBuffer
	l := Local{Root: root, Stage: "dev", Dir: t.TempDir()}
	f := Function{Name: "api", FunctionName: "orders-dev-api", ARN: "arn:aws:lambda:eu-west-1:123456789012:function:orders-dev-api", Config: fn, Region: "eu-west-1", Env: env, Output: &out}
	if err := l.Build(context.Background(), f); err != nil {
		t.Fatalf("Build: %v", err)
	}
	r, err := l.Runtime(f)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, &out
}

const nodeHandler = `
let calls = 0
export const handler = async (event, context) => {
  calls++
  if (event.kind === 'fail') throw new TypeError('no such order')
  if (event.kind === 'slow') await new Promise(r => setTimeout(r, 5000))
  console.log('handling', event.kind)
  return { calls, kind: event.kind, greeting: process.env.GREETING, name: context.functionName, left: context.getRemainingTimeInMillis() > 0 }
}
export const callback = (event, context, done) => { setTimeout(() => done(null, { via: 'callback' }), 10) }
`

// TestNodeRuntime runs a typescript handler as lambda would: warm between invocations, with
// its environment and context, errors returned as lambda reports them, and a timeout that stops
// the process and starts a new one for the next invocation.
func TestNodeRuntime(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node on the PATH")
	}
	root := t.TempDir()
	write(t, root, map[string]string{"src/api.ts": nodeHandler})
	fn := config.Function{Runtime: config.RuntimeNode22, Handler: "src/api.handler", Memory: 128, Timeout: 1}
	r, out := start(t, root, fn, map[string]string{"GREETING": "hello"})
	ctx := context.Background()

	var got struct {
		Calls    int
		Kind     string
		Greeting string
		Name     string
		Left     bool
	}
	for i := 1; i <= 2; i++ {
		payload, err := r.Invoke(ctx, []byte(`{"kind": "ok"}`))
		if err != nil {
			t.Fatalf("Invoke %d: %v\n%s", i, err, out)
		}
		_ = json.Unmarshal(payload, &got)
		if got.Calls != i || got.Greeting != "hello" || got.Name != "orders-dev-api" || !got.Left {
			t.Errorf("Invoke %d = %s", i, payload)
		}
	}
	if !strings.Contains(out.String(), "handling ok") {
		t.Errorf("the function's logs did not come through: %q", out)
	}

	_, err := r.Invoke(ctx, []byte(`{"kind": "fail"}`))
	if fe, ok := errors.AsType[*FunctionError](err); !ok || fe.Type != "TypeError" || fe.Message != "no such order" {
		t.Errorf("a throwing handler = %v", err)
	}

	_, err = r.Invoke(ctx, []byte(`{"kind": "slow"}`))
	if fe, ok := errors.AsType[*FunctionError](err); !ok || fe.Type != "Sandbox.Timedout" {
		t.Errorf("a handler slower than its timeout = %v", err)
	}
	payload, err := r.Invoke(ctx, []byte(`{"kind": "ok"}`))
	if err != nil {
		t.Fatalf("after a timeout: %v", err)
	}
	_ = json.Unmarshal(payload, &got)
	if got.Calls != 1 {
		t.Errorf("after a timeout the process was not started again: %s", payload)
	}

	fn.Handler = "src/api.callback"
	r2, _ := start(t, root, fn, nil)
	if payload, err := r2.Invoke(ctx, []byte(`{}`)); err != nil || string(payload) != `{"via":"callback"}` {
		t.Errorf("a callback handler = %s, %v", payload, err)
	}
}

// TestNodeInitError checks a module that fails to load is reported as lambda reports it.
func TestNodeInitError(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node on the PATH")
	}
	root := t.TempDir()
	write(t, root, map[string]string{"src/api.js": "throw new Error('no config')\nexports.handler = async () => 1\n"})
	r, _ := start(t, root, config.Function{Runtime: config.RuntimeNode22, Handler: "src/api.handler", Memory: 128, Timeout: 5}, nil)
	_, err := r.Invoke(context.Background(), []byte(`{}`))
	if fe, ok := errors.AsType[*FunctionError](err); !ok || !strings.Contains(fe.Message, "no config") {
		t.Errorf("a module that throws = %v", err)
	}
}

const goHandler = `package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func main() {
	api := "http://" + os.Getenv("AWS_LAMBDA_RUNTIME_API") + "/2018-06-01/runtime/invocation/"
	for {
		resp, err := http.Get(api + "next")
		if err != nil {
			os.Exit(1)
		}
		event, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		id := resp.Header.Get("Lambda-Runtime-Aws-Request-Id")
		body := fmt.Sprintf("{\"event\": %s, \"region\": %q}", event, os.Getenv("AWS_REGION"))
		r, err := http.Post(api+id+"/response", "application/json", strings.NewReader(body))
		if err == nil {
			_ = r.Body.Close()
		}
	}
}
`

// TestGoRuntime runs a go function, built for this machine, against the runtime api.
func TestGoRuntime(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on the PATH")
	}
	root := t.TempDir()
	write(t, root, map[string]string{"go.mod": "module example.com/worker\n\ngo 1.26\n", "cmd/worker/main.go": goHandler})
	r, out := start(t, root, config.Function{Runtime: config.RuntimeGo, Handler: "./cmd/worker", Memory: 128, Timeout: 5}, nil)
	payload, err := r.Invoke(context.Background(), []byte(`{"job": 1}`))
	if err != nil {
		t.Fatalf("Invoke: %v\n%s", err, out)
	}
	if string(payload) != `{"event": {"job": 1}, "region": "eu-west-1"}` {
		t.Errorf("Invoke = %s", payload)
	}
}

// TestInvokeRefusesBadJSON checks an event that is not json is refused before anything runs.
func TestInvokeRefusesBadJSON(t *testing.T) {
	r := &Runtime{}
	if _, err := r.Invoke(context.Background(), []byte(`{nope`)); err == nil {
		t.Error("Invoke with bad json: want an error")
	}
}
