package engine

import (
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
)

// TestDescribe checks koldstart's own values are written by what they stand for, a secret
// without its value.
func TestDescribe(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "string", in: "orders", want: "orders"},
		{name: "secret", in: SecretValue(config.Secret{Source: "ssm", Name: "/orders/key"}), want: "ssm:/orders/key"},
		{name: "secret field", in: SecretValue(config.Secret{Source: "secretsmanager", Name: "db", Key: "password"}), want: "secretsmanager:db#password"},
		{name: "output", in: StageOutput("billing", "prod", "url"), want: "output billing/prod.url"},
		{name: "ref", in: Ref("resource/table", "Arn"), want: "ref resource/table.Arn"},
		{name: "unknown", in: unknown(), want: "known after apply"},
		{name: "number", in: 256.0, want: "256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Describe(tt.in); got != tt.want {
				t.Errorf("Describe = %q, want %q", got, tt.want)
			}
		})
	}
}
