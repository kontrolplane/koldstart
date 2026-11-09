package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
)

// namePattern is the shape of a flag's name: what fits a path and reads well in code.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// maximumDescription is the longest description a flag takes, in bytes.
const maximumDescription = 512

// httpRequest and httpResponse are a request and a response of the http api, payload 2.0.
type (
	httpRequest  = events.APIGatewayV2HTTPRequest
	httpResponse = events.APIGatewayV2HTTPResponse
)

// change is the body of a PUT: what a flag becomes.
type change struct {
	Enabled     *bool  `json:"enabled"`
	Description string `json:"description"`
}

// problem is an error as rfc 9457 describes one: what went wrong, in words the caller can act on.
type problem struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// api reads and changes feature flags. A change names the version it was made to in if-match,
// as the etag of the flag it read, so two changes made to one version cannot both be stored.
type api struct {
	flags store
	now   func() time.Time
}

// handle picks the route by its key, answers a failure with a 500 that names the request, and
// logs every request it answers.
func (service api) handle(ctx context.Context, request httpRequest) (httpResponse, error) {
	started := service.now()
	requestID := request.RequestContext.RequestID
	name := request.PathParameters["name"]
	var response httpResponse
	var err error
	switch {
	case request.RouteKey != "GET /flags/{name}" && request.RouteKey != "PUT /flags/{name}":
		response, err = fail(http.StatusNotFound, "no route "+request.RouteKey, "")
	case !namePattern.MatchString(name):
		response, err = fail(http.StatusBadRequest, "the name cannot be used", "a name is 1 to 64 lower case letters, digits and -, starting with a letter")
	case request.RouteKey == "GET /flags/{name}":
		response, err = service.read(ctx, name)
	default:
		response, err = service.write(ctx, name, request)
	}
	if err != nil {
		slog.Error("answering a request", "route", request.RouteKey, "requestId", requestID, "error", err)
		response, err = fail(http.StatusInternalServerError, "something went wrong on our side", "quote request "+requestID+" when you report it")
		if err != nil {
			return httpResponse{}, err
		}
	}
	response.Headers["x-request-id"] = requestID
	slog.Info("answered a request", "route", request.RouteKey, "status", response.StatusCode, "milliseconds", service.now().Sub(started).Milliseconds())
	return response, nil
}

// read answers with the flag called name and its version as its etag.
func (service api) read(ctx context.Context, name string) (httpResponse, error) {
	stored, err := service.flags.get(ctx, name)
	if errors.Is(err, errNotFound) {
		return fail(http.StatusNotFound, "no flag "+name, "a PUT without if-match makes it")
	}
	if err != nil {
		return httpResponse{}, err
	}
	return reply(http.StatusOK, stored)
}

// write makes the flag called name, or changes it when if-match names the version it is at.
func (service api) write(ctx context.Context, name string, request httpRequest) (httpResponse, error) {
	body := request.Body
	if request.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return fail(http.StatusBadRequest, "the body is not base64", "")
		}
		body = string(decoded)
	}
	var wanted change
	if err := json.Unmarshal([]byte(body), &wanted); err != nil || wanted.Enabled == nil {
		return fail(http.StatusBadRequest, "the flag cannot be stored", `send {"enabled": true or false, "description": "..."}`)
	}
	if len(wanted.Description) > maximumDescription {
		return fail(http.StatusBadRequest, "the description is too long", fmt.Sprintf("send at most %d bytes", maximumDescription))
	}
	version := 1
	if match := request.Headers["if-match"]; match != "" {
		previous, err := strconv.Atoi(strings.Trim(match, `"`))
		if err != nil || previous < 1 {
			return fail(http.StatusBadRequest, "if-match is not an etag of this api", `send the etag a GET answered with, such as "3"`)
		}
		version = previous + 1
	}
	changed := flag{
		Name: name, Enabled: *wanted.Enabled, Description: wanted.Description,
		Version: version, UpdatedAt: service.now().UTC().Format(time.RFC3339),
	}
	switch err := service.flags.put(ctx, changed); {
	case errors.Is(err, errConflict) && version == 1:
		return fail(http.StatusPreconditionRequired, "flag "+name+" exists", "GET it, and send its etag in if-match to change it")
	case errors.Is(err, errConflict):
		return fail(http.StatusPreconditionFailed, "flag "+name+" changed since that version", "GET it again, and make the change to the version it is at")
	case err != nil:
		return httpResponse{}, err
	}
	slog.Info("stored a flag", "flag", name, "enabled", changed.Enabled, "version", changed.Version)
	if version == 1 {
		return reply(http.StatusCreated, changed)
	}
	return reply(http.StatusOK, changed)
}

// fail answers with statusCode and a problem of title and detail.
func fail(statusCode int, title, detail string) (httpResponse, error) {
	response, err := reply(statusCode, problem{Type: "about:blank", Status: statusCode, Title: title, Detail: detail})
	if err == nil {
		response.Headers["content-type"] = "application/problem+json"
	}
	return response, err
}

// reply answers with statusCode and body as json, and the headers every answer has; a flag's
// version is its etag.
func reply(statusCode int, body any) (httpResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return httpResponse{}, fmt.Errorf("writing the response: %w", err)
	}
	headers := map[string]string{
		"content-type":           "application/json",
		"cache-control":          "no-store",
		"x-content-type-options": "nosniff",
	}
	if stored, ok := body.(flag); ok {
		headers["etag"] = `"` + strconv.Itoa(stored.Version) + `"`
	}
	return httpResponse{StatusCode: statusCode, Headers: headers, Body: string(data)}, nil
}
