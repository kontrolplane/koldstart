package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var routes = []Route{
	{Method: "GET", Path: "/orders/{id}", Function: "get"},
	{Method: "GET", Path: "/orders/latest", Function: "latest"},
	{Method: "ANY", Path: "/orders/{id}", Function: "any"},
	{Method: "POST", Path: "/orders", Function: "create"},
	{Method: "GET", Path: "/files/{proxy+}", Function: "files"},
	{Method: "ANY", Path: "$default", Function: "fallback"},
}

// TestMatch checks routes are picked as an http api picks them.
func TestMatch(t *testing.T) {
	tests := []struct {
		method, path string
		want         string
		params       map[string]string
	}{
		{method: "GET", path: "/orders/42", want: "get", params: map[string]string{"id": "42"}},
		{method: "GET", path: "/orders/latest", want: "latest"},
		{method: "DELETE", path: "/orders/42", want: "any", params: map[string]string{"id": "42"}},
		{method: "POST", path: "/orders", want: "create"},
		{method: "POST", path: "/orders/", want: "create"},
		{method: "GET", path: "/files/a/b/c.png", want: "files", params: map[string]string{"proxy": "a/b/c.png"}},
		{method: "GET", path: "/nothing/here", want: "fallback"},
		{method: "GET", path: "/orders", want: "fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r, params, ok := match(routes, tt.method, tt.path)
			if !ok || r.Function != tt.want {
				t.Fatalf("match = %v, %v, want %s", r.Function, ok, tt.want)
			}
			for k, v := range tt.params {
				if params[k] != v {
					t.Errorf("param %s = %q, want %q", k, params[k], v)
				}
			}
		})
	}
	if _, _, ok := match(routes[:4], "GET", "/nothing"); ok {
		t.Error("without $default, an unknown path matched")
	}
}

// echo answers with the event it was given, as a response with a status of its choosing.
type echo struct{ fail bool }

func (e echo) Invoke(_ context.Context, function string, payload []byte) ([]byte, error) {
	if e.fail {
		return nil, errors.New("crashed")
	}
	var ev map[string]any
	_ = json.Unmarshal(payload, &ev)
	body, _ := json.Marshal(map[string]any{"function": function, "event": ev})
	return json.Marshal(map[string]any{
		"statusCode": 201, "headers": map[string]string{"X-Function": function},
		"cookies": []string{"seen=1"}, "body": string(body),
	})
}

// TestOffline checks a request becomes the event an http api sends, and the response comes back.
func TestOffline(t *testing.T) {
	srv := httptest.NewServer(&Offline{Routes: routes, Invoker: echo{}})
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/orders/42?expand=items&expand=customer", nil)
	req.Header.Set("Authorization", "Bearer x")
	req.AddCookie(&http.Cookie{Name: "session", Value: "abc"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 201 || resp.Header.Get("X-Function") != "get" || !strings.Contains(resp.Header.Get("Set-Cookie"), "seen=1") {
		t.Errorf("response = %d %v", resp.StatusCode, resp.Header)
	}
	var got struct {
		Event struct {
			Version, RouteKey, RawPath, RawQueryString string
			Headers                                    map[string]string
			Cookies                                    []string
			QueryStringParameters                      map[string]string
			PathParameters                             map[string]string
			RequestContext                             struct {
				HTTP struct{ Method string } `json:"http"`
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	ev := got.Event
	if ev.Version != "2.0" || ev.RouteKey != "GET /orders/{id}" || ev.RawPath != "/orders/42" || ev.PathParameters["id"] != "42" {
		t.Errorf("event = %+v", ev)
	}
	if ev.QueryStringParameters["expand"] != "items,customer" || ev.Headers["authorization"] != "Bearer x" || len(ev.Cookies) != 1 || ev.RequestContext.HTTP.Method != "GET" {
		t.Errorf("event = %+v", ev)
	}

	failing := httptest.NewServer(&Offline{Routes: routes, Invoker: echo{fail: true}})
	defer failing.Close()
	resp, err = http.Get(failing.URL + "/orders/1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("a failing function = %d, want 500", resp.StatusCode)
	}
}

// TestOfflineLog checks each request is logged with its status and how long it took, never 0s,
// and with the function it invoked, why it failed, or that no route matched; the routes leave out
// $default, which matches everything.
func TestOfflineLog(t *testing.T) {
	tests := []struct {
		name    string
		invoker Invoker
		path    string
		want    string
	}{
		{name: "invoked", invoker: echo{}, path: "/orders/42", want: `^GET /orders/42 201 (<1ms|\d+ms)  get\n$`},
		{name: "failed", invoker: echo{fail: true}, path: "/orders/42", want: `^GET /orders/42 500 (<1ms|\d+ms)  get: crashed\n$`},
		{name: "no route", invoker: echo{}, path: "/nope", want: `^GET /nope 404 (<1ms|\d+ms)  no route\n$`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			o := &Offline{Routes: routes[:5], Invoker: tt.invoker, Log: &log}
			o.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tt.path, nil))
			if !regexp.MustCompile(tt.want).MatchString(log.String()) {
				t.Errorf("logged %q, want it to match %s", log.String(), tt.want)
			}
		})
	}
}

// TestRespond checks what a function returns becomes the response an http api sends.
func TestRespond(t *testing.T) {
	tests := []struct {
		payload    string
		status     int
		body, kind string
	}{
		{payload: `{"statusCode": 204}`, status: 204, body: ""},
		{payload: `{"statusCode": 200, "body": "aGk=", "isBase64Encoded": true, "headers": {"content-type": "text/plain"}}`, status: 200, body: "hi", kind: "text/plain"},
		{payload: `{"orders": [1, 2]}`, status: 200, body: `{"orders": [1, 2]}`, kind: "application/json"},
		{payload: `"plain"`, status: 200, body: "plain"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		respond(rec, []byte(tt.payload))
		if rec.Code != tt.status || rec.Body.String() != tt.body || (tt.kind != "" && rec.Header().Get("Content-Type") != tt.kind) {
			t.Errorf("respond(%s) = %d %q %q", tt.payload, rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
		}
	}
}

// TestWatch checks a change to a source is noticed, and one under node_modules or to a log is not.
func TestWatch(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"src/api.ts": "a", "node_modules/x/index.js": "a", "offline.log": "a"})
	changes := make(chan struct{}, 10)
	go Watch(t.Context(), root, 10*time.Millisecond, func() { changes <- struct{}{} })
	time.Sleep(50 * time.Millisecond)

	later := time.Now().Add(time.Second)
	_ = os.Chtimes(filepath.Join(root, "node_modules", "x", "index.js"), later, later)
	_ = os.Chtimes(filepath.Join(root, "offline.log"), later, later)
	select {
	case <-changes:
		t.Fatal("a change under node_modules, or to a log, was noticed")
	case <-time.After(100 * time.Millisecond):
	}
	_ = os.Chtimes(filepath.Join(root, "src", "api.ts"), later, later)
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("a change to the source was not noticed")
	}
}
