// Command api shortens links: POST /links stores a url under a new id, and GET /{id} redirects
// to it.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type api struct {
	db    *dynamodb.Client
	table string
}

// handle picks the route by its key, as the http api names it.
func (a api) handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	switch req.RouteKey {
	case "POST /links":
		return a.create(ctx, req)
	case "GET /{id}":
		return a.follow(ctx, req.PathParameters["id"])
	}
	return fail(http.StatusNotFound, "no route "+req.RouteKey)
}

// create stores the url of the body under a random id, refusing one that is taken.
func (a api) create(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	body := []byte(req.Body)
	if req.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			return fail(http.StatusBadRequest, "the body is not base64")
		}
		body = decoded
	}
	var link struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &link); err != nil {
		return fail(http.StatusBadRequest, `send {"url": "https://..."}`)
	}
	if u, err := url.Parse(link.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fail(http.StatusBadRequest, "url is not an http or https url")
	}
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	key := base64.RawURLEncoding.EncodeToString(id)
	_, err := a.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(a.table),
		Item: map[string]types.AttributeValue{
			"id":  &types.AttributeValueMemberS{Value: key},
			"url": &types.AttributeValueMemberS{Value: link.URL},
		},
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, fmt.Errorf("storing link %s: %w", key, err)
	}
	slog.Info("stored a link", "id", key)
	return reply(http.StatusCreated, map[string]string{"id": key, "url": link.URL})
}

// follow redirects to the url stored under id.
func (a api) follow(ctx context.Context, id string) (events.APIGatewayV2HTTPResponse, error) {
	out, err := a.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(a.table),
		Key:       map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: id}},
	})
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, fmt.Errorf("reading link %s: %w", id, err)
	}
	target, ok := out.Item["url"].(*types.AttributeValueMemberS)
	if !ok {
		return fail(http.StatusNotFound, "no link "+id)
	}
	return events.APIGatewayV2HTTPResponse{StatusCode: http.StatusFound, Headers: map[string]string{"location": target.Value}}, nil
}

// fail answers with status and {"error": message}.
func fail(status int, message string) (events.APIGatewayV2HTTPResponse, error) {
	return reply(status, map[string]string{"error": message})
}

// reply answers with status and body as json.
func reply(status int, body any) (events.APIGatewayV2HTTPResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, fmt.Errorf("writing the response: %w", err)
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(data),
	}, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("loading the aws config", "error", err)
		os.Exit(1)
	}
	lambda.Start(api{db: dynamodb.NewFromConfig(cfg), table: os.Getenv("LINKS_NAME")}.handle)
}
