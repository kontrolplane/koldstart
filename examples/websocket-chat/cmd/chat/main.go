// Command chat is a chat room on the stage's websocket api: it keeps the open connections in the
// connections table, and sends every message to each of them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	apitypes "github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type chat struct {
	db    *dynamodb.Client
	api   *apigatewaymanagementapi.Client
	table string
}

// handle stores a connection on $connect, forgets it on $disconnect, and sends the text of a
// send message to every connection.
func (c chat) handle(ctx context.Context, req events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	id := req.RequestContext.ConnectionID
	var err error
	switch req.RequestContext.RouteKey {
	case "$connect":
		_, err = c.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(c.table), Item: key(id)})
	case "$disconnect":
		_, err = c.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(c.table), Key: key(id)})
	case "send":
		err = c.broadcast(ctx, id, req.Body)
	}
	if err != nil {
		return events.APIGatewayProxyResponse{}, fmt.Errorf("%s of %s: %w", req.RequestContext.RouteKey, id, err)
	}
	return events.APIGatewayProxyResponse{StatusCode: 200}, nil
}

// broadcast sends the text of body, from the connection from, to every connection.
func (c chat) broadcast(ctx context.Context, from, body string) error {
	var msg struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		return fmt.Errorf("reading the message: %w", err)
	}
	data, err := json.Marshal(map[string]string{"from": from, "text": msg.Text})
	if err != nil {
		return fmt.Errorf("writing the message: %w", err)
	}
	pages := dynamodb.NewScanPaginator(c.db, &dynamodb.ScanInput{TableName: aws.String(c.table)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("listing the connections: %w", err)
		}
		for _, item := range page.Items {
			if to, ok := item["id"].(*types.AttributeValueMemberS); ok {
				c.send(ctx, to.Value, data)
			}
		}
	}
	return nil
}

// send posts data to the connection to, and forgets it when it is gone. A failure is logged
// rather than returned, so one connection cannot keep the message from the others.
func (c chat) send(ctx context.Context, to string, data []byte) {
	_, err := c.api.PostToConnection(ctx, &apigatewaymanagementapi.PostToConnectionInput{ConnectionId: aws.String(to), Data: data})
	if _, gone := errors.AsType[*apitypes.GoneException](err); gone {
		_, err = c.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(c.table), Key: key(to)})
	}
	if err != nil {
		slog.Error("sending a message", "to", to, "error", err)
	}
}

// key is the key of a connection in the table.
func key(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: id}}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("loading the aws config", "error", err)
		os.Exit(1)
	}
	api := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = aws.String(os.Getenv("WEBSOCKET_CALLBACK_URL"))
	})
	lambda.Start(chat{db: dynamodb.NewFromConfig(cfg), api: api, table: os.Getenv("CONNECTIONS_NAME")}.handle)
}
