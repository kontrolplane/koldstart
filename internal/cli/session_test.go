package cli

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// TestPathStyle checks buckets go in the path with an endpoint of an emulator, from the
// environment or a profile's endpoint_url, and in the host name without one.
func TestPathStyle(t *testing.T) {
	tests := []struct {
		name     string
		endpoint *string
		s3Env    string
		want     bool
	}{
		{name: "aws", want: false},
		{name: "an endpoint", endpoint: aws.String("http://127.0.0.1:4566"), want: true},
		{name: "an endpoint for s3", s3Env: "http://127.0.0.1:4566", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AWS_ENDPOINT_URL_S3", tt.s3Env)
			s := newSession(aws.Config{Region: "eu-west-1", BaseEndpoint: tt.endpoint}, "123456789012")
			if got := s.S3.Options().UsePathStyle; got != tt.want {
				t.Errorf("UsePathStyle = %v, want %v", got, tt.want)
			}
		})
	}
}
