package local

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var restRoutes = []Route{
	{Method: "GET", Path: "/orders/{id}", Function: "get", Stage: "dev"},
	{Method: "ANY", Path: "/", Function: "root", Stage: "dev"},
	{Method: "GET", Path: "/orders", Function: "http"},
}

// TestMatchREST checks a rest route is matched under its stage alone, its root at the stage, and
// an http route at its path, not under the stage.
func TestMatchREST(t *testing.T) {
	tests := []struct {
		method, path string
		want, under  string
		ok           bool
	}{
		{method: "GET", path: "/dev/orders/7", want: "get", under: "/orders/7", ok: true},
		{method: "POST", path: "/dev", want: "root", under: "/", ok: true},
		{method: "POST", path: "/dev/", want: "root", under: "/", ok: true},
		{method: "GET", path: "/development/orders/7"},
		{method: "GET", path: "/orders/7"},
		{method: "GET", path: "/dev/orders"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r, _, under, ok := matchREST(restRoutes, tt.method, tt.path)
			if ok != tt.ok || r.Function != tt.want || under != tt.under {
				t.Errorf("matchREST = %s %q %v, want %s %q %v", r.Function, under, ok, tt.want, tt.under, tt.ok)
			}
		})
	}
	if r := restRoutes[0]; r.Served() != "GET /dev/orders/{id}" || restRoutes[1].Served() != "ANY /dev" || restRoutes[2].Served() != "GET /orders" {
		t.Errorf("served at %s, %s and %s", r.Served(), restRoutes[1].Served(), restRoutes[2].Served())
	}
}

// TestOfflineREST checks a request to a rest route becomes the event of payload 1.0 the rest
// api's proxy integration hands its function, and an http route beside it still gets 2.0.
func TestOfflineREST(t *testing.T) {
	srv := httptest.NewServer(&Offline{Routes: restRoutes, Invoker: echo{}})
	defer srv.Close()
	get := func(path string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Add("X-Tag", "a")
		req.Header.Add("X-Tag", "b")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 201 || resp.Header.Get("X-Function") == "" {
			t.Fatalf("GET %s = %d %v", path, resp.StatusCode, resp.Header)
		}
		var out struct {
			Event map[string]any `json:"event"`
		}
		body, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out.Event
	}
	ev := get("/dev/orders/7?page=2&page=3")
	ctx, _ := ev["requestContext"].(map[string]any)
	tests := []struct {
		field string
		got   any
		want  any
	}{
		{field: "resource", got: ev["resource"], want: "/orders/{id}"},
		{field: "path", got: ev["path"], want: "/orders/7"},
		{field: "httpMethod", got: ev["httpMethod"], want: "GET"},
		{field: "pathParameters", got: ev["pathParameters"], want: map[string]any{"id": "7"}},
		{field: "queryStringParameters", got: ev["queryStringParameters"], want: map[string]any{"page": "3"}},
		{field: "multiValueQueryStringParameters", got: ev["multiValueQueryStringParameters"], want: map[string]any{"page": []any{"2", "3"}}},
		{field: "multiValueHeaders", got: ev["multiValueHeaders"].(map[string]any)["X-Tag"], want: []any{"a", "b"}},
		{field: "requestContext.path", got: ctx["path"], want: "/dev/orders/7"},
		{field: "requestContext.stage", got: ctx["stage"], want: "dev"},
		{field: "version", got: ev["version"], want: nil},
	}
	for _, tt := range tests {
		a, _ := json.Marshal(tt.got)
		b, _ := json.Marshal(tt.want)
		if string(a) != string(b) {
			t.Errorf("%s = %s, want %s", tt.field, a, b)
		}
	}
	if v := get("/orders")["version"]; v != "2.0" {
		t.Errorf("the http route's event is of version %v", v)
	}
}

// TestRespondREST checks what a function returns is written as the rest api writes it: the
// headers and their lists, and a 502 for what is not a response of the proxy integration, which
// an http api would take as a body.
func TestRespondREST(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		status  int
		body    string
		header  string
	}{
		{name: "a response", payload: `{"statusCode":202,"multiValueHeaders":{"Set-Cookie":["a=1","b=2"]},"body":"ok"}`, status: 202, body: "ok", header: "a=1,b=2"},
		{name: "base64", payload: `{"statusCode":200,"body":"aGk=","isBase64Encoded":true}`, status: 200, body: "hi"},
		{name: "not a response", payload: `{"hello":"world"}`, status: 502, body: `{"message":"Internal server error"}`},
		{name: "a string", payload: `"hello"`, status: 502, body: `{"message":"Internal server error"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			respondREST(w, []byte(tt.payload))
			if w.Code != tt.status || w.Body.String() != tt.body || strings.Join(w.Header().Values("Set-Cookie"), ",") != tt.header {
				t.Errorf("= %d %q %v, want %d %q", w.Code, w.Body.String(), w.Header(), tt.status, tt.body)
			}
		})
	}
}
