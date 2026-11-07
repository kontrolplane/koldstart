package cli

import (
	"bytes"
	"maps"
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/state"
)

// TestWriteOutputs checks outputs are sorted with their values lined up, after a deploy under a
// heading, and as json one object with nothing escaped.
func TestWriteOutputs(t *testing.T) {
	outputs := map[string]string{"httpApiUrl": "https://abc.execute-api.eu-west-1.amazonaws.com", "table": "orders-dev", "q": "https://x?a=1&b=2"}
	tests := []struct {
		name  string
		write func(*bytes.Buffer)
		want  string
	}{
		{
			name:  "text",
			write: func(b *bytes.Buffer) { writeOutputs(b, "", outputs) },
			want:  "httpApiUrl = https://abc.execute-api.eu-west-1.amazonaws.com\nq          = https://x?a=1&b=2\ntable      = orders-dev\n",
		},
		{
			name:  "after a deploy",
			write: func(b *bytes.Buffer) { printOutputs(b, map[string]string{"a": "1", "bcd": "2"}) },
			want:  "\noutputs:\n  a   = 1\n  bcd = 2\n",
		},
		{
			name:  "none after a deploy",
			write: func(b *bytes.Buffer) { printOutputs(b, nil) },
			want:  "",
		},
		{
			name: "json",
			write: func(b *bytes.Buffer) {
				_ = writeOutputsJSON(b, map[string]string{"q": "https://x?a=1&b=2", "a": "<1>"})
			},
			want: "{\n  \"a\": \"<1>\",\n  \"q\": \"https://x?a=1&b=2\"\n}\n",
		},
		{
			name:  "json of none",
			write: func(b *bytes.Buffer) { _ = writeOutputsJSON(b, nil) },
			want:  "{}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			tt.write(&out)
			if out.String() != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", out.String(), tt.want)
			}
		})
	}
}

// TestURLOutputs checks the outputs a deploy adds are read from the state: the url of the rest
// api's stage from its api and its stage, the id of its key, and none of an api the state does not
// hold.
func TestURLOutputs(t *testing.T) {
	tests := []struct {
		name      string
		resources map[string]*state.Resource
		want      map[string]string
	}{
		{name: "nothing", resources: map[string]*state.Resource{}, want: map[string]string{}},
		{
			name: "a rest api with a key",
			resources: map[string]*state.Resource{
				desired.RESTAPIID:   {Attributes: map[string]string{"ApiEndpoint": "https://abc.execute-api.eu-west-1.amazonaws.com"}},
				desired.RESTStageID: {Attributes: map[string]string{"StageName": "dev"}},
				desired.RESTKeyID:   {PhysicalID: "k1"},
			},
			want: map[string]string{config.OutputRESTAPIURL: "https://abc.execute-api.eu-west-1.amazonaws.com/dev", config.OutputRESTAPIKeyID: "k1"},
		},
		{
			name:      "a rest api not deployed yet",
			resources: map[string]*state.Resource{desired.RESTAPIID: {Attributes: map[string]string{"ApiEndpoint": "https://abc"}}},
			want:      map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stage{project: &project{Project: &config.Project{}}}
			got := s.urlOutputs(&state.State{Resources: tt.resources})
			if !maps.Equal(got, tt.want) {
				t.Errorf("urlOutputs = %v, want %v", got, tt.want)
			}
		})
	}
}
