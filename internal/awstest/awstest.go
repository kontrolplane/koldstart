// Package awstest connects tests to the aws emulator that make test-emulator starts.
package awstest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// EnvEndpoint is the emulator's endpoint. Without it, emulator tests skip.
const EnvEndpoint = "KOLDSTART_TEST_ENDPOINT"

// Region is the region emulator tests use.
const Region = "eu-west-1"

// Emulator returns a config for the emulator, or skips the test when there is none.
func Emulator(t *testing.T) aws.Config {
	t.Helper()
	endpoint := os.Getenv(EnvEndpoint)
	if endpoint == "" {
		t.Skipf("no emulator: run make emulator test-emulator, or set %s", EnvEndpoint)
	}
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(Region),
		config.WithBaseEndpoint(endpoint),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("loading the emulator's config: %v", err)
	}
	return cfg
}

// Get requests a url aws gave out, such as an http api's endpoint. The emulator answers for
// every host on its own address, so a url of an aws host goes there with its host in the Host
// header.
func Get(t *testing.T, rawURL string) (*http.Response, error) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(u.Hostname(), ".amazonaws.com") {
		endpoint, err := url.Parse(os.Getenv(EnvEndpoint))
		if err != nil {
			return nil, err
		}
		req.URL.Scheme, req.URL.Host = endpoint.Scheme, endpoint.Host
		req.Host = u.Hostname()
	}
	return http.DefaultClient.Do(req)
}

// Name returns a name no other test run uses, so tests can share one emulator.
func Name(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}
