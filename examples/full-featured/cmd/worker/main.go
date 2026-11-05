// Command worker stores the orders the api queues in the orders table.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type worker struct {
	db    *dynamodb.Client
	table string
}

// handle stores each order of the batch, and reports the ones it could not, so only those are
// retried.
func (w worker) handle(ctx context.Context, batch events.SQSEvent) (events.SQSEventResponse, error) {
	var failed []events.SQSBatchItemFailure
	for _, msg := range batch.Records {
		if err := w.store(ctx, msg.Body); err != nil {
			slog.Error("storing an order", "message", msg.MessageId, "error", err)
			failed = append(failed, events.SQSBatchItemFailure{ItemIdentifier: msg.MessageId})
			continue
		}
		slog.Info("stored an order", "message", msg.MessageId)
	}
	return events.SQSEventResponse{BatchItemFailures: failed}, nil
}

func (w worker) store(ctx context.Context, body string) error {
	var order map[string]any
	if err := json.Unmarshal([]byte(body), &order); err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(order)
	if err != nil {
		return err
	}
	_, err = w.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(w.table), Item: item})
	return err
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("loading the aws config", "error", err)
		os.Exit(1)
	}
	lambda.Start(worker{db: dynamodb.NewFromConfig(cfg), table: os.Getenv("ORDERS_NAME")}.handle)
}
