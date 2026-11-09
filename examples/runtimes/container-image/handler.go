package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
)

// status is what the function url answers with: what was deployed, and where it runs.
type status struct {
	Version         string `json:"version"`
	Revision        string `json:"revision"`
	Region          string `json:"region"`
	FunctionVersion string `json:"functionVersion"`
	ColdStart       bool   `json:"coldStart"`
}

// problem is an error as rfc 9457 describes one: what went wrong, in words the caller can act on.
type problem struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// server answers the function url with what one instance of the function knows of itself.
type server struct {
	version, revision, region, functionVersion string
	// served is false until the instance has answered its first request, its cold start.
	served bool
}

// handle answers GET and HEAD of / with the status, and anything else with a problem. Every
// answer carries the request's id, and is neither cached nor read as anything but json.
func (instance *server) handle(_ context.Context, request events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	method, path := request.RequestContext.HTTP.Method, request.RequestContext.HTTP.Path
	var response events.LambdaFunctionURLResponse
	var err error
	switch {
	case path != "/" && path != "":
		response, err = fail(http.StatusNotFound, "no "+path, "the status is at /")
	case method != http.MethodGet && method != http.MethodHead:
		response, err = fail(http.StatusMethodNotAllowed, method+" is not allowed", "the status takes GET and HEAD")
		response.Headers["allow"] = "GET, HEAD"
	default:
		response, err = reply(http.StatusOK, status{
			Version: instance.version, Revision: instance.revision, Region: instance.region,
			FunctionVersion: instance.functionVersion, ColdStart: !instance.served,
		})
		if method == http.MethodHead {
			response.Body = ""
		}
	}
	if err != nil {
		return events.LambdaFunctionURLResponse{}, err
	}
	response.Headers["x-request-id"] = request.RequestContext.RequestID
	slog.Info("answered a request", "method", method, "path", path, "status", response.StatusCode, "coldStart", !instance.served)
	instance.served = true
	return response, nil
}

// fail answers with statusCode and a problem of title and detail.
func fail(statusCode int, title, detail string) (events.LambdaFunctionURLResponse, error) {
	response, err := reply(statusCode, problem{Type: "about:blank", Status: statusCode, Title: title, Detail: detail})
	if err == nil {
		response.Headers["content-type"] = "application/problem+json"
	}
	return response, err
}

// reply answers with statusCode and body as json, and the headers every answer has.
func reply(statusCode int, body any) (events.LambdaFunctionURLResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return events.LambdaFunctionURLResponse{}, fmt.Errorf("writing the response: %w", err)
	}
	return events.LambdaFunctionURLResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"content-type":              "application/json",
			"cache-control":             "no-store",
			"strict-transport-security": "max-age=63072000; includeSubDomains",
			"x-content-type-options":    "nosniff",
		},
		Body: string(data),
	}, nil
}
