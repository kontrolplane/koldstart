package local

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// defaultRoute catches every request no other route matches; localName stands for the api and
// the account where there are none.
const (
	defaultRoute = "$default"
	localName    = "local"
)

// Route is an http route of the configuration, and the function it invokes.
type Route struct {
	Method   string
	Path     string
	Function string
}

// Key is the route key api gateway names the route by, such as GET /orders/{id}.
func (r Route) Key() string {
	if r.Path == defaultRoute {
		return defaultRoute
	}
	return r.Method + " " + r.Path
}

// match returns the route an http api picks for method and path, and the path parameters: a
// route of the method before ANY, more literal segments before parameters, a greedy {proxy+}
// last, and $default when nothing else matches.
func match(routes []Route, method, path string) (Route, map[string]string, bool) {
	type candidate struct {
		route  Route
		params map[string]string
		score  int
	}
	var found []candidate
	var fallback *Route
	for _, r := range routes {
		if r.Path == defaultRoute {
			fallback = &r
			continue
		}
		if r.Method != method && r.Method != "ANY" {
			continue
		}
		params, literal, ok := matchPath(r.Path, path)
		if !ok {
			continue
		}
		score := literal * 10
		if r.Method == method {
			score++
		}
		if strings.Contains(r.Path, "+}") {
			score -= 1000
		}
		found = append(found, candidate{route: r, params: params, score: score})
	}
	if len(found) == 0 {
		if fallback != nil {
			return *fallback, nil, true
		}
		return Route{}, nil, false
	}
	best := slices.MaxFunc(found, func(a, b candidate) int { return a.score - b.score })
	return best.route, best.params, true
}

// matchPath matches a path against a route's, returning the parameters and how many segments
// matched literally.
func matchPath(pattern, path string) (map[string]string, int, bool) {
	want := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	params := map[string]string{}
	literal := 0
	for i, seg := range want {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "+}") {
			if i >= len(got) {
				return nil, 0, false
			}
			params[strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "+}")] = strings.Join(got[i:], "/")
			return params, literal, true
		}
		if i >= len(got) {
			return nil, 0, false
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			if got[i] == "" {
				return nil, 0, false
			}
			params[strings.Trim(seg, "{}")] = got[i]
			continue
		}
		if seg != got[i] {
			return nil, 0, false
		}
		literal++
	}
	if len(got) != len(want) {
		return nil, 0, false
	}
	return params, literal, true
}

// event is the payload 2.0 event an http api hands a function for req.
func event(req *http.Request, route Route, params map[string]string, body []byte, now time.Time) map[string]any {
	headers := map[string]string{}
	for k, v := range req.Header {
		if strings.EqualFold(k, "Cookie") {
			continue
		}
		headers[strings.ToLower(k)] = strings.Join(v, ",")
	}
	headers["host"] = req.Host
	cookies := make([]string, 0, len(req.Cookies()))
	for _, c := range req.Cookies() {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	query := map[string]string{}
	for k, v := range req.URL.Query() {
		query[k] = strings.Join(v, ",")
	}
	ev := map[string]any{
		"version":        "2.0",
		"routeKey":       route.Key(),
		"rawPath":        req.URL.Path,
		"rawQueryString": req.URL.RawQuery,
		"headers":        headers,
		"requestContext": map[string]any{
			"accountId":    "000000000000",
			"apiId":        localName,
			"domainName":   req.Host,
			"domainPrefix": localName,
			"http": map[string]any{
				"method": req.Method, "path": req.URL.Path, "protocol": req.Proto,
				"sourceIp": strings.Split(req.RemoteAddr, ":")[0], "userAgent": req.UserAgent(),
			},
			"requestId": newID(),
			"routeKey":  route.Key(),
			"stage":     defaultRoute,
			"time":      now.UTC().Format("02/Jan/2006:15:04:05 -0700"),
			"timeEpoch": now.UnixMilli(),
		},
		"isBase64Encoded": false,
	}
	if len(cookies) > 0 {
		ev["cookies"] = cookies
	}
	if len(query) > 0 {
		ev["queryStringParameters"] = query
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

// respond writes what a function returned the way an http api does: a response object as it
// says, anything else as a 200 with the json, or a string's text.
func respond(w http.ResponseWriter, payload []byte) {
	var res struct {
		StatusCode      *int              `json:"statusCode"`
		Headers         map[string]string `json:"headers"`
		Cookies         []string          `json:"cookies"`
		Body            *string           `json:"body"`
		IsBase64Encoded bool              `json:"isBase64Encoded"`
	}
	if err := json.Unmarshal(payload, &res); err != nil || res.StatusCode == nil {
		w.Header().Set("Content-Type", "application/json")
		var s string
		if json.Unmarshal(payload, &s) == nil {
			_, _ = io.WriteString(w, s)
			return
		}
		_, _ = w.Write(payload)
		return
	}
	for k, v := range res.Headers {
		w.Header().Set(k, v)
	}
	for _, c := range res.Cookies {
		w.Header().Add("Set-Cookie", c)
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
		decoded, err := base64.StdEncoding.DecodeString(*res.Body)
		if err == nil {
			body = decoded
		}
	}
	_, _ = w.Write(body)
}

// Invoker runs a function by name.
type Invoker interface {
	Invoke(ctx context.Context, function string, payload []byte) ([]byte, error)
}

// Offline serves the http routes of a stage on this machine, each request an invocation of the
// function its route names.
type Offline struct {
	Routes  []Route
	Invoker Invoker
	// Log receives a line per request.
	Log io.Writer
}

// ServeHTTP implements http.Handler.
func (o *Offline) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	route, params, ok := match(o.Routes, req.Method, req.URL.Path)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		o.log(req, http.StatusNotFound, "", start)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 10<<20))
	if err != nil {
		http.Error(w, `{"message":"Request Entity Too Large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	payload, err := json.Marshal(event(req, route, params, body, start))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out, err := o.Invoker.Invoke(req.Context(), route.Function, payload)
	if err != nil {
		slog.Debug("invocation failed", "route", route.Key(), "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"Internal Server Error"}`)
		o.log(req, http.StatusInternalServerError, route.Function+": "+err.Error(), start)
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	respond(rec, out)
	o.log(req, rec.status, route.Function, start)
}

func (o *Offline) log(req *http.Request, status int, note string, start time.Time) {
	if o.Log == nil {
		return
	}
	line := fmt.Sprintf("%s %s %s %s", req.Method, req.URL.RequestURI(), strconv.Itoa(status), time.Since(start).Round(time.Millisecond))
	if note != "" {
		line += "  " + note
	}
	_, _ = fmt.Fprintln(o.Log, line)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
