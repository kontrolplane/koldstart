package local

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// REST reports whether the route is one of the rest api, served under its stage.
func (r Route) REST() bool { return r.Stage != "" }

// Served is the method and path the route is served at here: a rest route's under its stage, as
// GET /dev/orders, as the rest api's url has the stage in it.
func (r Route) Served() string {
	if !r.REST() {
		return r.Key()
	}
	return r.Method + " " + strings.TrimSuffix("/"+r.Stage+r.Path, "/")
}

// restPath is what path is under the stage of the rest routes, and whether it is under it.
func restPath(stage, path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/"+stage)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return "", false
	}
	if rest == "" {
		rest = "/"
	}
	return rest, true
}

// matchREST returns the rest route for method and path, under the stage of the rest routes, and
// the path parameters, as the rest api picks them.
func matchREST(routes []Route, method, path string) (Route, map[string]string, string, bool) {
	var rest []Route
	for _, r := range routes {
		if r.REST() {
			rest = append(rest, r)
		}
	}
	if len(rest) == 0 {
		return Route{}, nil, "", false
	}
	resource, ok := restPath(rest[0].Stage, path)
	if !ok {
		return Route{}, nil, "", false
	}
	r, params, ok := match(rest, method, resource)
	if !ok {
		return Route{}, nil, "", false
	}
	return r, params, resource, true
}

// lists turns repeated values, of headers or a query, into the two maps payload 1.0 has: the last
// value of each, and all of them.
func lists(values map[string][]string) (map[string]string, map[string][]string) {
	if len(values) == 0 {
		return nil, nil
	}
	last, all := map[string]string{}, map[string][]string{}
	for k, v := range values {
		last[k], all[k] = v[len(v)-1], v
	}
	return last, all
}

// restEvent is the payload 1.0 event the rest api's lambda proxy integration hands a function for
// req, whose path under the stage is resource's.
func restEvent(req *http.Request, route Route, params map[string]string, path string, body []byte, now time.Time) map[string]any {
	header := req.Header.Clone()
	header.Set("Host", req.Host)
	headers, multiHeaders := lists(header)
	query, multiQuery := lists(req.URL.Query())
	ev := map[string]any{
		"resource": route.Path, keyPath: path, "httpMethod": req.Method,
		"headers": headers, "multiValueHeaders": multiHeaders,
		"queryStringParameters": query, "multiValueQueryStringParameters": multiQuery,
		"pathParameters": nil, "stageVariables": nil, "body": nil, "isBase64Encoded": false,
		"requestContext": map[string]any{
			"accountId": "000000000000", "apiId": localName, "resourceId": localName, "resourcePath": route.Path,
			"httpMethod": req.Method, keyPath: req.URL.Path, "stage": route.Stage, "protocol": req.Proto,
			"domainName": req.Host, "requestId": requestID(), "extendedRequestId": requestID(),
			"requestTime": now.UTC().Format("02/Jan/2006:15:04:05 -0700"), "requestTimeEpoch": now.UnixMilli(),
			"identity": map[string]any{"sourceIp": strings.Split(req.RemoteAddr, ":")[0], "userAgent": req.UserAgent()},
		},
	}
	if len(params) > 0 {
		ev["pathParameters"] = params
	}
	if len(body) > 0 {
		if utf8.Valid(body) {
			ev["body"] = string(body)
		} else {
			ev["body"] = base64.StdEncoding.EncodeToString(body)
			ev["isBase64Encoded"] = true
		}
	}
	return ev
}

// respondREST writes what a function returned the way the rest api's proxy integration does: a
// response with a statusCode as it says, its headers and their lists, and a 502 for anything else.
func respondREST(w http.ResponseWriter, payload []byte) {
	var res struct {
		StatusCode        *int                `json:"statusCode"`
		Headers           map[string]string   `json:"headers"`
		MultiValueHeaders map[string][]string `json:"multiValueHeaders"`
		Body              *string             `json:"body"`
		IsBase64Encoded   bool                `json:"isBase64Encoded"`
	}
	if err := json.Unmarshal(payload, &res); err != nil || res.StatusCode == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"Internal server error"}`))
		return
	}
	for k, v := range res.MultiValueHeaders {
		for _, item := range v {
			w.Header().Add(k, item)
		}
	}
	for k, v := range res.Headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(*res.StatusCode)
	if res.Body == nil {
		return
	}
	body := []byte(*res.Body)
	if res.IsBase64Encoded {
		if decoded, err := base64.StdEncoding.DecodeString(*res.Body); err == nil {
			body = decoded
		}
	}
	_, _ = w.Write(body)
}
