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
		read:      []string{"sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility", "sqs:GetQueueAttributes", "sqs:GetQueueUrl"},
		write:     []string{"sqs:SendMessage", "sqs:GetQueueAttributes", "sqs:GetQueueUrl"},
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
		read:      []string{"kinesis:GetRecords", "kinesis:GetShardIterator", "kinesis:DescribeStream", "kinesis:DescribeStreamSummary", "kinesis:ListShards", "kinesis:SubscribeToShard"},
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

func statement(effect string, actions []string, resources []any) map[string]any {
	return map[string]any{"Effect": effect, "Action": actions, "Resource": resources}
}

// permissions returns the role's inline policy, its managed policies, and the environment
// variables link adds.
func (b *builder) permissions(name string, fn config.Function) (map[string]any, []string, map[string]any, error) {
	statements := []any{statement("Allow",
		[]string{"logs:CreateLogStream", "logs:PutLogEvents"},
		[]any{engine.Ref(LogGroupID(name), "Arn")},
	)}

	for _, s := range fn.Permissions {
		statements = append(statements, statement(s.Effect, s.Actions, b.values(s.Resources)))
	}

	env := map[string]any{}
	for _, target := range slices.Sorted(maps.Keys(fn.Link)) {
		res := b.p.Resources[target]
		a, ok := links[res.Type]
		if !ok {
			return nil, nil, nil, fmt.Errorf("link[%s]: link does not know %s yet: give access with permissions instead", target, res.Type)
		}
		node := ResourceID(target)
		var actions []string
		switch fn.Link[target] {
		case "read":
			actions = a.read
		case "write":
			actions = a.write
		default:
			actions = slices.Concat(a.read, a.write)
		}
		slices.Sort(actions)
		actions = slices.Compact(actions)
		var resources []any
		if a.resources != nil {
			resources = a.resources(node)
		} else {
			// An ssm parameter's arn is not an attribute: build it from its name.
			resources = []any{engine.Join("arn:"+awsp.Partition(b.t.Region)+":ssm:"+b.t.Region+":"+b.t.Account+":parameter/", engine.Ref(node, "Name"))}
		}
		statements = append(statements, statement("Allow", actions, resources))
		for _, suffix := range slices.Sorted(maps.Keys(a.env)) {
			env[envName(target)+"_"+suffix] = engine.Ref(node, a.env[suffix])
		}
	}

	var managed []string
	if fn.VPC != nil {
		managed = append(managed, "arn:"+awsp.Partition(b.t.Region)+":iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole")
	}
	policy := map[string]any{"Version": "2012-10-17", "Statement": statements}
	return policy, managed, env, nil
}
