package awsp

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// TestRESTAuthorizerPatchOps checks an authorizer's update patches what changed alone, nothing
// when nothing did, and every field that is set when there is no prior, as after a crash.
func TestRESTAuthorizerPatchOps(t *testing.T) {
	token := RESTAuthorizerInputs{API: "a", Name: "token", Type: "TOKEN", URI: "u", Identity: "method.request.header.Authorization"}
	header := token
	header.Identity = "method.request.header.X-Token"
	tests := []struct {
		name         string
		prior, input RESTAuthorizerInputs
		want         string
	}{
		{name: "nothing changed", prior: token, input: token, want: ""},
		{name: "a header", prior: token, input: header, want: "replace /identitySource"},
		{name: "no prior", input: token, want: "replace /name,replace /type,replace /identitySource,replace /authorizerResultTtlInSeconds,replace /authorizerUri"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, op := range tt.input.patches(tt.prior) {
				got = append(got, string(op.Op)+" "+aws.ToString(op.Path))
			}
			if strings.Join(got, ",") != tt.want {
				t.Errorf("patches = %s, want %s", strings.Join(got, ","), tt.want)
			}
		})
	}
}
