package cloudcontrol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// schemaOf reads a schema of the cloudformation registry kept in testdata, as DescribeType
// returns it.
func schemaOf(t *testing.T, typ string) *Schema {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(typ, "::", "-")) + ".json"
	data, err := os.ReadFile(filepath.Join("testdata", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSchema(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// props decodes properties as pkl renders them, numbers as float64.
func props(t *testing.T, doc string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCheck checks properties against real schemas of the registry: what is right passes, with
// koldstart's own values left for apply, and each kind of mistake is reported at its path.
func TestCheck(t *testing.T) {
	ref := `{"$koldstart": "ref", "node": "resource/jobs", "attribute": "Arn"}`
	tests := []struct {
		name  string
		typ   string
		props string
		want  []string
	}{
		{
			name: "table", typ: "AWS::DynamoDB::Table",
			props: `{"BillingMode": "PAY_PER_REQUEST", "AttributeDefinitions": [{"AttributeName": "id", "AttributeType": "S"}],
				"KeySchema": [{"AttributeName": "id", "KeyType": "HASH"}], "StreamSpecification": {"StreamViewType": "NEW_IMAGE"}}`,
		},
		{
			name: "queue with a ref", typ: "AWS::SQS::Queue",
			props: `{"VisibilityTimeout": 60, "RedrivePolicy": {"deadLetterTargetArn": ` + ref + `, "maxReceiveCount": 5}}`,
		},
		{
			name: "bucket", typ: "AWS::S3::Bucket",
			props: `{"BucketName": "orders-uploads-dev", "VersioningConfiguration": {"Status": "Enabled"},
				"Tags": [{"Key": "team", "Value": "checkout"}]}`,
		},
		{
			name: "role with a policy document", typ: "AWS::IAM::Role",
			props: `{"AssumeRolePolicyDocument": {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}]}}`,
		},
		{
			name: "a misspelt property", typ: "AWS::DynamoDB::Table",
			props: `{"BillingMod": "PAY_PER_REQUEST", "KeySchema": [{"AttributeName": "id", "KeyType": "HASH"}]}`,
			want:  []string{"BillingMod: no such property: did you mean BillingMode?"},
		},
		{
			name: "a value it does not take", typ: "AWS::Lambda::Function",
			props: `{"Code": {"ZipFile": "x"}, "Role": "arn:aws:iam::123456789012:role/api", "Architectures": ["arm"]}`,
			want:  []string{`Architectures[0]: want one of "x86_64", "arm64", not "arm"`},
		},
		{
			name: "too many items", typ: "AWS::Lambda::Function",
			props: `{"Code": {"ZipFile": "x"}, "Role": "arn:aws:iam::123456789012:role/api", "Architectures": ["arm64", "x86_64"]}`,
			want:  []string{"Architectures: want at most 1 item, not 2"},
		},
		{
			name: "a pattern", typ: "AWS::Lambda::Function",
			props: `{"Code": {"ZipFile": "x"}, "Role": "api-role"}`,
			want:  []string{`Role: "api-role" does not match the pattern`},
		},
		{
			name: "a nested misspelling", typ: "AWS::DynamoDB::Table",
			props: `{"KeySchema": [{"AttributeName": "id", "KeyTyp": "HASH"}]}`,
			want:  []string{"KeySchema[0].KeyType: required, and not set", "KeySchema[0].KeyTyp: no such property: did you mean KeyType?"},
		},
		{
			name: "a string for a number", typ: "AWS::SQS::Queue",
			props: `{"VisibilityTimeout": "60"}`,
			want:  []string{`VisibilityTimeout: want integer, not "60"`},
		},
		{
			name: "a fraction for an integer", typ: "AWS::SQS::Queue",
			props: `{"DelaySeconds": 1.5}`,
			want:  []string{"DelaySeconds: want integer, not 1.5"},
		},
		{
			name: "an object for a list", typ: "AWS::DynamoDB::Table",
			props: `{"KeySchema": [{"AttributeName": "id", "KeyType": "HASH"}], "AttributeDefinitions": {"AttributeName": "id", "AttributeType": "S"}}`,
			want:  []string{"AttributeDefinitions: want array, not an object"},
		},
		{
			name: "a required property", typ: "AWS::DynamoDB::Table",
			props: `{"BillingMode": "PAY_PER_REQUEST"}`,
			want:  []string{"KeySchema: required, and not set"},
		},
		{
			name: "a property aws sets", typ: "AWS::SQS::Queue",
			props: `{"Arn": "arn:aws:sqs:eu-west-1:123456789012:jobs"}`,
			want:  []string{"Arn: aws sets Arn itself"},
		},
		{
			name: "an enum", typ: "AWS::S3::Bucket",
			props: `{"VersioningConfiguration": {"Status": "On"}}`,
			want:  []string{`VersioningConfiguration.Status: want one of "Enabled", "Suspended", not "On"`},
		},
		{
			name: "a ref anywhere", typ: "AWS::DynamoDB::Table",
			props: `{"KeySchema": ` + ref + `}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, p := range schemaOf(t, tt.typ).Check(props(t, tt.props)) {
				got = append(got, p.String())
			}
			if len(got) != len(tt.want) {
				t.Fatalf("problems:\n%s\nwant %d:\n%s", strings.Join(got, "\n"), len(tt.want), strings.Join(tt.want, "\n"))
			}
			for _, w := range tt.want {
				if !slices.ContainsFunc(got, func(g string) bool { return strings.HasPrefix(g, w) }) {
					t.Errorf("no problem starting %q in:\n%s", w, strings.Join(got, "\n"))
				}
			}
		})
	}
}

// TestCheckEverySchema runs the check over every schema in testdata with no properties, which
// must not panic or loop, and reports only the required ones.
func TestCheckEverySchema(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "schemas", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no schemas: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s, err := ParseSchema(string(data))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, p := range s.Check(map[string]any{}) {
			if !strings.HasSuffix(p.Message, "required, and not set") {
				t.Errorf("%s: %s", f, p)
			}
		}
	}
}
