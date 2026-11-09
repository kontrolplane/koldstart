// Command api reads and changes feature flags: GET /flags/{name} answers with a flag, and
// PUT /flags/{name} makes it or changes it, once for each version.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

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
	name := os.Getenv("FLAGS_NAME")
	if name == "" {
		slog.Error("FLAGS_NAME is not set: link the flags table in koldstart.pkl")
		os.Exit(1)
	}
	service := api{flags: table{client: dynamodb.NewFromConfig(configuration), name: name}, now: time.Now}
	lambda.Start(service.handle)
}
