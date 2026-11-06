package pkl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReport checks pkl's report loses its banner and the frames in pkl's standard library, and
// names the files beside the module as the module's path does.
func TestReport(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	stderr := "–– Pkl Error ––\nExpected value of type `Int`, but got type `String`.\nValue: \"a\"\n\n" +
		"12 | memory = \"a\"\n              ^^^\nat koldstart#functions[\"api\"].memory (file://" + filepath.Join(wd, "orders", "koldstart.pkl") + ", line 12)\n\n" +
		"131 | renderer.renderDocument(value)\n      ^^^^\nat pkl.base#Module.output.text (https://github.com/apple/pkl/blob/0.32.2/stdlib/base.pkl#L131)\n"
	tests := []struct {
		name    string
		path    string
		want    string
		wantNot []string
	}{
		{name: "beside", path: filepath.Join("orders", "koldstart.pkl"), want: "(" + filepath.Join("orders", "koldstart.pkl") + ", line 12)",
			wantNot: []string{"Pkl Error", "pkl.base", "file://"}},
		{name: "here", path: "koldstart.pkl", want: "Expected value of type `Int`, but got type `String`.\nValue: \"a\"\n\n12 | memory",
			wantNot: []string{"Pkl Error", "renderDocument"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := report(stderr, tt.path)
			if !strings.Contains(got, tt.want) {
				t.Errorf("report =\n%s\nwant it to hold %q", got, tt.want)
			}
			for _, not := range tt.wantNot {
				if strings.Contains(got, not) {
					t.Errorf("report =\n%s\nwant nothing of %q", got, not)
				}
			}
		})
	}
}

// TestEvalErrorSays checks an evaluation error says to fix the module, or, when a package did
// not download, to check the machine is online.
func TestEvalErrorSays(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "wrong", message: "Missing `}` delimiter.", want: "pkl cannot evaluate koldstart.pkl: fix it where pkl points\n\nMissing"},
		{name: "download", message: "Received unexpected status code `404` when making request `GET https://example.com/x`.", want: "check this machine is online"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &EvalError{Path: "koldstart.pkl", Message: tt.message}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Error() = %q, want it to hold %q", err.Error(), tt.want)
			}
		})
	}
}

// TestFindNamedBinary checks a binary named in KOLDSTART_PKL is used, and one that does not exist
// says to set it to another or unset it.
func TestFindNamedBinary(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "pkl")
	if err := os.WriteFile(existing, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		binary  string
		wantErr string
	}{
		{name: "exists", binary: existing},
		{name: "missing", binary: filepath.Join(t.TempDir(), "nope"), wantErr: "which does not exist: set it to a pkl binary, or unset it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvBinary, tt.binary)
			got, err := Find(context.Background())
			switch {
			case tt.wantErr == "" && (err != nil || got != tt.binary):
				t.Errorf("Find = %q, %v, want %q", got, err, tt.binary)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("Find = %v, want an error with %q", err, tt.wantErr)
			}
		})
	}
}
