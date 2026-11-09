// Command worker stores the orders the api queues in the orders table, once each.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// table is the orders table in dynamodb.
type table struct {
	client *dynamodb.Client
	name   string
}

// put stores placed unless an order with its id is stored already: a message sqs delivers
// twice, or an order placed again with the same idempotency key, is stored once.
func (database table) put(ctx context.Context, placed order) error {
	item, err := attributevalue.MarshalMap(placed)
	if err != nil {
		return fmt.Errorf("writing order %s as an item: %w", placed.ID, err)
	}
	_, err = database.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(database.name),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	if _, ok := errors.AsType[*types.ConditionalCheckFailedException](err); ok {
		return errStored
	}
	return err
}

// logLevel is the lowest level the function logs, applicationLogLevel, which lambda sets as
// AWS_LAMBDA_LOG_LEVEL. It is info without it, or with a level slog does not have.
func logLevel() slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(os.Getenv("AWS_LAMBDA_LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return level
}

// spelledOut names the line's message "message", where slog would name it "msg".
func spelledOut(_ []string, attribute slog.Attr) slog.Attr {
	if attribute.Key == slog.MessageKey {
		attribute.Key = "message"
	}
	return attribute
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(), ReplaceAttr: spelledOut})))
	configuration, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("loading the aws config", "error", err)
		os.Exit(1)
	}
	name := os.Getenv("ORDERS_NAME")
	if name == "" {
		slog.Error("ORDERS_NAME is not set: link the orders table in koldstart.pkl")
		os.Exit(1)
	}
	lambda.Start(worker{orders: table{client: dynamodb.NewFromConfig(configuration), name: name}}.handle)
}
