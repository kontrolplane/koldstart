package cli

import (
	"bytes"
	"net"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/local"
)

// TestPrintServing checks offline says where it serves, lines the routes up with their
// functions, and says how to stop.
func TestPrintServing(t *testing.T) {
	routes := []local.Route{
		{Method: "GET", Path: "/orders/{id}", Function: "api"},
		{Method: "POST", Path: "/orders", Function: "api"},
		{Method: "ANY", Path: "$default", Function: "fallback"},
	}
	var out bytes.Buffer
	printServing(&out, "shop", "dev", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3000}, routes)
	want := `serving shop/dev on http://127.0.0.1:3000

  GET /orders/{id}  api
  POST /orders      api
  $default          fallback

a change to a source file rebuilds the functions; ctrl+c stops
`
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

// TestListen checks a port in use says to pass another --port.
func TestListen(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	port := taken.Addr().(*net.TCPAddr).Port
	tests := []struct {
		name     string
		port     int
		failWith string
	}{
		{name: "free", port: 0},
		{name: "in use", port: port, failWith: "is in use: stop what listens on it, or pass --port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := listen(tt.port)
			if tt.failWith == "" {
				if err != nil {
					t.Fatal(err)
				}
				_ = l.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.failWith) {
				t.Fatalf("listen = %v, want it to say %q", err, tt.failWith)
			}
		})
	}
}
