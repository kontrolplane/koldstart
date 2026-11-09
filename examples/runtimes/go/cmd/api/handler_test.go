package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
)

// fakeStore keeps flags by name, as the table does, and fails every call when broken.
type fakeStore struct {
	flags  map[string]flag
	broken bool
}

func (fake *fakeStore) get(_ context.Context, name string) (flag, error) {
	if fake.broken {
		return flag{}, errors.New("dynamodb is down")
	}
	stored, ok := fake.flags[name]
	if !ok {
		return flag{}, errNotFound
	}
	return stored, nil
}

func (fake *fakeStore) put(_ context.Context, changed flag) error {
	if fake.broken {
		return errors.New("dynamodb is down")
	}
	stored, ok := fake.flags[changed.Name]
	if (changed.Version == 1 && ok) || (changed.Version > 1 && (!ok || stored.Version != changed.Version-1)) {
		return errConflict
	}
	fake.flags[changed.Name] = changed
	return nil
}

// now is the time every test runs at.
var now = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

// newRequest is a request of the http api to the route of the flag called name.
func newRequest(method, name, body string, headers map[string]string) httpRequest {
	if headers == nil {
		headers = map[string]string{}
	}
	return httpRequest{
		RouteKey:       method + " /flags/{name}",
		PathParameters: map[string]string{"name": name},
		Headers:        headers,
		Body:           body,
		RequestContext: events.APIGatewayV2HTTPRequestContext{RequestID: "request-1"},
	}
}

// TestHandle checks what each route answers, the etag of every flag it answers with, and the
// versions that keep two changes to one version of a flag from both being stored.
func TestHandle(t *testing.T) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	checkout := flag{Name: "new-checkout", Enabled: false, Description: "the new checkout page", Version: 3, UpdatedAt: "2026-11-01T09:00:00Z"}
	tests := []struct {
		name        string
		request     httpRequest
		broken      bool
		status      int
		contentType string
		etag        string
		stored      *flag
	}{
		{
			name:        "a flag",
			request:     newRequest("GET", "new-checkout", "", nil),
			status:      200,
			contentType: "application/json",
			etag:        `"3"`,
		},
		{
			name:        "a flag that is not there",
			request:     newRequest("GET", "dark-mode", "", nil),
			status:      404,
			contentType: "application/problem+json",
		},
		{
			name:        "a name that cannot be one",
			request:     newRequest("GET", "Dark_Mode", "", nil),
			status:      400,
			contentType: "application/problem+json",
		},
		{
			name:        "a new flag",
			request:     newRequest("PUT", "dark-mode", `{"enabled": true, "description": "the dark theme"}`, nil),
			status:      201,
			contentType: "application/json",
			etag:        `"1"`,
			stored:      &flag{Name: "dark-mode", Enabled: true, Description: "the dark theme", Version: 1, UpdatedAt: "2026-11-09T10:00:00Z"},
		},
		{
			name: "a new flag sent in base64",
			request: func() httpRequest {
				request := newRequest("PUT", "dark-mode", base64.StdEncoding.EncodeToString([]byte(`{"enabled": false}`)), nil)
				request.IsBase64Encoded = true
				return request
			}(),
			status:      201,
			contentType: "application/json",
			etag:        `"1"`,
		},
		{
			name:        "a flag that exists, without if-match",
			request:     newRequest("PUT", "new-checkout", `{"enabled": true}`, nil),
			status:      428,
			contentType: "application/problem+json",
			stored:      &checkout,
		},
		{
			name:        "a change to the version the flag is at",
			request:     newRequest("PUT", "new-checkout", `{"enabled": true, "description": "the new checkout page"}`, map[string]string{"if-match": `"3"`}),
			status:      200,
			contentType: "application/json",
			etag:        `"4"`,
			stored:      &flag{Name: "new-checkout", Enabled: true, Description: "the new checkout page", Version: 4, UpdatedAt: "2026-11-09T10:00:00Z"},
		},
		{
			name:        "a change to a version it is no longer at",
			request:     newRequest("PUT", "new-checkout", `{"enabled": true}`, map[string]string{"if-match": `"2"`}),
			status:      412,
			contentType: "application/problem+json",
			stored:      &checkout,
		},
		{
			name:        "an if-match that is not an etag",
			request:     newRequest("PUT", "new-checkout", `{"enabled": true}`, map[string]string{"if-match": "latest"}),
			status:      400,
			contentType: "application/problem+json",
		},
		{
			name:        "a body without enabled",
			request:     newRequest("PUT", "dark-mode", `{"description": "the dark theme"}`, nil),
			status:      400,
			contentType: "application/problem+json",
		},
		{
			name:        "a body that is not json",
			request:     newRequest("PUT", "dark-mode", "on", nil),
			status:      400,
			contentType: "application/problem+json",
		},
		{
			name:        "another route",
			request:     httpRequest{RouteKey: "DELETE /flags/{name}", PathParameters: map[string]string{"name": "dark-mode"}},
			status:      404,
			contentType: "application/problem+json",
		},
		{
			name:        "a table that fails",
			request:     newRequest("GET", "new-checkout", "", nil),
			broken:      true,
			status:      500,
			contentType: "application/problem+json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeStore{flags: map[string]flag{checkout.Name: checkout}, broken: tt.broken}
			service := api{flags: fake, now: func() time.Time { return now }}
			response, err := service.handle(context.Background(), tt.request)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tt.status {
				t.Errorf("status = %d, want %d: %s", response.StatusCode, tt.status, response.Body)
			}
			if got := response.Headers["content-type"]; got != tt.contentType {
				t.Errorf("content-type = %q, want %q", got, tt.contentType)
			}
			if got := response.Headers["etag"]; got != tt.etag {
				t.Errorf("etag = %q, want %q", got, tt.etag)
			}
			if tt.stored != nil {
				if got := fake.flags[tt.stored.Name]; got != *tt.stored {
					t.Errorf("stored %+v, want %+v", got, *tt.stored)
				}
			}
		})
	}
}

// TestHandleHeaders checks every answer names its request and is neither cached nor sniffed,
// and that a failure says nothing of its cause.
func TestHandleHeaders(t *testing.T) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	service := api{flags: &fakeStore{broken: true}, now: func() time.Time { return now }}
	response, err := service.handle(context.Background(), newRequest("GET", "dark-mode", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"x-request-id": "request-1", "cache-control": "no-store", "x-content-type-options": "nosniff"}
	for name, value := range want {
		if got := response.Headers[name]; got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	var answered problem
	if err := json.Unmarshal([]byte(response.Body), &answered); err != nil {
		t.Fatal(err)
	}
	if answered.Detail != "quote request request-1 when you report it" {
		t.Errorf("detail = %q, want one that names the request alone", answered.Detail)
	}
}
