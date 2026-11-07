// Command status answers its function url with the version it was built as and the region it
// runs in.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

// version is set by the Dockerfile from its VERSION build argument.
var version = "dev"

func handle(_ context.Context, _ events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
	body, err := json.Marshal(map[string]string{"version": version, "region": os.Getenv("AWS_REGION")})
	if err != nil {
		return events.LambdaFunctionURLResponse{}, fmt.Errorf("writing the response: %w", err)
	}
	return events.LambdaFunctionURLResponse{
		StatusCode: 200,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(body),
	}, nil
}

func main() {
	lambda.Start(handle)
}
