package desired

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
)

// access is what link grants on one type of resource.
type access struct {
	read, write []string
	// resources are the arns the statements name, from the resource's node.
	resources func(node string) []any
	// env are the attributes that become environment variables, by suffix.
	env map[string]string
}

// suffixARN ends the environment variable that holds a linked resource's arn, and attrArn is
// the attribute it is read from.
const (
	suffixARN  = "ARN"
	suffixName = "NAME"
	attrArn    = "Arn"
)

func arn(node string) []any { return []any{engine.Ref(node, attrArn)} }

// links are the types link knows. A type that is not here is refused rather than given *.
var links = map[string]access{
	"AWS::DynamoDB::Table": {
		read:  []string{"dynamodb:GetItem", "dynamodb:BatchGetItem", "dynamodb:Query", "dynamodb:Scan", "dynamodb:ConditionCheckItem", "dynamodb:DescribeTable"},
		write: []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem", "dynamodb:DescribeTable"},
		resources: func(node string) []any {
			return []any{engine.Ref(node, attrArn), engine.Join(engine.Ref(node, attrArn), "/index/*")}
		},
		env: map[string]string{suffixName: "TableName", suffixARN: attrArn},
	},
	"AWS::S3::Bucket": {
		read:  []string{"s3:GetObject", "s3:ListBucket"},
		write: []string{"s3:PutObject", "s3:DeleteObject", "s3:ListBucket"},
		resources: func(node string) []any {
			return []any{engine.Ref(node, attrArn), engine.Join(engine.Ref(node, attrArn), "/*")}
		},
		env: map[string]string{suffixName: "BucketName", suffixARN: attrArn},
	},
	"AWS::SQS::Queue": {
		read:      sqsRead,
		write:     sqsWrite,
		resources: arn,
		env:       map[string]string{"URL": "QueueUrl", suffixARN: attrArn},
	},
	"AWS::SNS::Topic": {
		read:      []string{"sns:GetTopicAttributes", "sns:ListSubscriptionsByTopic"},
		write:     []string{"sns:Publish"},
		resources: func(node string) []any { return []any{engine.Ref(node, "TopicArn")} },
		env:       map[string]string{suffixARN: "TopicArn"},
	},
	"AWS::Kinesis::Stream": {
		read:      kinesisRead,
		write:     []string{"kinesis:PutRecord", "kinesis:PutRecords", "kinesis:DescribeStreamSummary"},
		resources: arn,
		env:       map[string]string{suffixName: "Name", suffixARN: attrArn},
	},
	"AWS::SecretsManager::Secret": {
		read:      []string{"secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"},
		write:     []string{"secretsmanager:PutSecretValue", "secretsmanager:UpdateSecret"},
		resources: func(node string) []any { return []any{engine.Ref(node, "Id")} },
		env:       map[string]string{suffixARN: "Id"},
	},
	"AWS::SSM::Parameter": {
		read:  []string{"ssm:GetParameter", "ssm:GetParameters"},
		write: []string{"ssm:PutParameter"},
		env:   map[string]string{suffixName: "Name"},
	},
}

// envName turns a resource's name into the start of its environment variables.
func envName(name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}

// The actions a function needs to read a queue and a stream, for link and for events.
var (
	sqsRead     = []string{"sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility", "sqs:GetQueueAttributes", "sqs:GetQueueUrl"}
	sqsWrite    = []string{"sqs:SendMessage", "sqs:GetQueueAttributes", "sqs:GetQueueUrl"}
	kinesisRead = []string{"kinesis:GetRecords", "kinesis:GetShardIterator", "kinesis:DescribeStream", "kinesis:DescribeStreamSummary", "kinesis:ListShards", "kinesis:ListStreams", "kinesis:SubscribeToShard"}
	dynamoRead  = []string{"dynamodb:GetRecords", "dynamodb:GetShardIterator", "dynamodb:DescribeStream", "dynamodb:ListStreams"}
)

func statement(effect string, actions []string, resources []any) map[string]any {
	return map[string]any{"Effect": effect, "Action": actions, "Resource": resources}
}

// policy is an iam policy document of statements.
func policy(statements ...any) map[string]any {
	return map[string]any{"Version": "2012-10-17", "Statement": statements}
}

// trust is a policy that lets a service assume a role, with conditions when there are any.
func trust(service string, conditions map[string]any) map[string]any {
	s := map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": service}, "Action": "sts:AssumeRole"}
	if conditions != nil {
		s["Condition"] = conditions
	}
	return policy(s)
}

// runtimeSecrets are what a function's role needs to read the secrets it reads itself, those
// with readAtRuntime. Parameters encrypted with a key of your own also need kms:Decrypt on it,
// through permissions.
func (b *builder) runtimeSecrets(fn config.Function) []any {
	var out []any
	for _, key := range slices.Sorted(maps.Keys(fn.Environment)) {
		s := fn.Environment[key].Secret
		if s == nil || !s.AtRuntime {
			continue
		}
		switch s.Source {
		case config.SourceSSM:
			arn := s.Name
			if !strings.HasPrefix(arn, "arn:") {
				arn = b.arnPrefix("ssm") + "parameter/" + strings.TrimPrefix(s.Name, "/")
			}
			out = append(out, statement("Allow", []string{"ssm:GetParameter", "ssm:GetParameters"}, []any{arn}))
		case config.SourceSecretsManager:
			arn := s.Name
			if !strings.HasPrefix(arn, "arn:") {
				arn = b.arnPrefix("secretsmanager") + "secret:" + s.Name + "-??????"
			}
			out = append(out, statement("Allow", []string{"secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"}, []any{arn}))
		}
	}
	return out
}

// permissions returns a function's role's inline policy and its managed policies: writing to its
// log group, its own statements, reading its event sources and runtime secrets, and what link
// gives it.
func (b *builder) permissions(name string, fn config.Function) (map[string]any, []string, error) {
	statements := []any{statement("Allow",
		[]string{"logs:CreateLogStream", "logs:PutLogEvents"},
		[]any{engine.Ref(LogGroupID(name), attrArn)},
	)}
	for _, s := range fn.Permissions {
		statements = append(statements, statement(s.Effect, s.Actions, b.values(s.Resources)))
	}
	statements = append(statements, b.sourceStatements(fn)...)
	statements = append(statements, b.runtimeSecrets(fn)...)
	linked, err := b.linkStatements(fn)
	if err != nil {
		return nil, nil, err
	}
	statements = append(statements, linked...)

	var managed []string
	if fn.VPC != nil {
		managed = append(managed, "arn:"+awsp.Partition(b.target.Region)+":iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole")
	}
	return policy(statements...), managed, nil
}

// linkAccess is what link knows of a resource a function links to, or an error for a type it
// does not know.
func (b *builder) linkAccess(target string) (access, error) {
	res := b.project.Resources[target]
	a, ok := links[res.Type]
	if !ok {
		return access{}, fmt.Errorf("link[%s]: link does not know %s yet: give access with permissions instead", target, res.Type)
	}
	return a, nil
}

// linkStatements are the statements link gives a function, one per resource. An ssm parameter's
// arn is not an attribute, so it is built from its name.
func (b *builder) linkStatements(fn config.Function) ([]any, error) {
	var out []any
	for _, target := range slices.Sorted(maps.Keys(fn.Link)) {
		a, err := b.linkAccess(target)
		if err != nil {
			return nil, err
		}
		var actions []string
		switch fn.Link[target] {
		case "read":
			actions = a.read
		case "write":
			actions = a.write
		default:
			actions = slices.Concat(a.read, a.write)
		}
		actions = slices.Compact(slices.Sorted(slices.Values(actions)))
		node := ResourceID(target)
		resources := []any{engine.Join(b.arnPrefix("ssm")+"parameter/", engine.Ref(node, "Name"))}
		if a.resources != nil {
			resources = a.resources(node)
		}
		out = append(out, statement("Allow", actions, resources))
	}
	return out, nil
}

// linkEnvironment are the variables link adds to a function: the names and arns of what it links.
func (b *builder) linkEnvironment(fn config.Function) (map[string]any, error) {
	env := map[string]any{}
	for _, target := range slices.Sorted(maps.Keys(fn.Link)) {
		a, err := b.linkAccess(target)
		if err != nil {
			return nil, err
		}
		for _, suffix := range slices.Sorted(maps.Keys(a.env)) {
			env[envName(target)+"_"+suffix] = engine.Ref(ResourceID(target), a.env[suffix])
		}
	}
	return env, nil
}
