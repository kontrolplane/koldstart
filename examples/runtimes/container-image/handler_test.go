package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

// newRequest is a request of the function url.
func newRequest(method, path string) events.LambdaFunctionURLRequest {
	return events.LambdaFunctionURLRequest{
		RawPath: path,
		RequestContext: events.LambdaFunctionURLRequestContext{
			RequestID: "request-1",
			HTTP:      events.LambdaFunctionURLRequestContextHTTPDescription{Method: method, Path: path},
		},
	}
}

// TestHandle checks the status answers GET and HEAD of / alone, every answer with the headers
// that keep it from being cached or sniffed, and the request's id.
func TestHandle(t *testing.T) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		name        string
		method      string
		path        string
		status      int
		contentType string
		allow       string
		body        bool
	}{
		{name: "the status", method: "GET", path: "/", status: 200, contentType: "application/json", body: true},
		{name: "the status without its body", method: "HEAD", path: "/", status: 200, contentType: "application/json"},
		{name: "a post", method: "POST", path: "/", status: 405, contentType: "application/problem+json", allow: "GET, HEAD", body: true},
		{name: "a delete", method: "DELETE", path: "/", status: 405, contentType: "application/problem+json", allow: "GET, HEAD", body: true},
		{name: "another path", method: "GET", path: "/admin", status: 404, contentType: "application/problem+json", body: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := &server{version: "1.4.0", revision: "9f3c2ab", region: "eu-west-1", functionVersion: "$LATEST"}
			response, err := instance.handle(context.Background(), newRequest(tt.method, tt.path))
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", response.StatusCode, tt.status)
			}
			want := map[string]string{
				"content-type":              tt.contentType,
				"cache-control":             "no-store",
				"strict-transport-security": "max-age=63072000; includeSubDomains",
				"x-content-type-options":    "nosniff",
				"x-request-id":              "request-1",
			}
			for name, value := range want {
				if response.Headers[name] != value {
					t.Errorf("header %s = %q, want %q", name, response.Headers[name], value)
				}
			}
			if response.Headers["allow"] != tt.allow {
				t.Errorf("allow = %q, want %q", response.Headers["allow"], tt.allow)
			}
			if (response.Body != "") != tt.body {
				t.Errorf("body = %q, want one: %v", response.Body, tt.body)
			}
		})
	}
}

// TestStatus checks the status says what was deployed and where, and that only an instance's
// first answer is a cold start.
func TestStatus(t *testing.T) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	instance := &server{version: "1.4.0", revision: "9f3c2ab", region: "eu-west-1", functionVersion: "7"}
	tests := []struct {
		name string
		want status
	}{
		{name: "the first request", want: status{Version: "1.4.0", Revision: "9f3c2ab", Region: "eu-west-1", FunctionVersion: "7", ColdStart: true}},
		{name: "the second request", want: status{Version: "1.4.0", Revision: "9f3c2ab", Region: "eu-west-1", FunctionVersion: "7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := instance.handle(context.Background(), newRequest("GET", "/"))
			if err != nil {
				t.Fatal(err)
			}
			var got status
			if err := json.Unmarshal([]byte(response.Body), &got); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("status = %+v, want %+v", got, tt.want)
			}
		})
	}
}
