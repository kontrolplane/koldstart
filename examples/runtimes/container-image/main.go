// Command status answers its function url with the version and revision it was built as, the
// region it runs in, and the version of the function.
package main

import (
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
)

// The build, set by the Dockerfile from its VERSION and REVISION build arguments.
var (
	version  = "0.0.0-development"
	revision = "unknown"
)

// level is the lowest level the function logs, applicationLogLevel, which lambda sets as
// AWS_LAMBDA_LOG_LEVEL. It is info without it, or with a level slog does not have.
func level() slog.Level {
	var parsed slog.Level
	if err := parsed.UnmarshalText([]byte(os.Getenv("AWS_LAMBDA_LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return parsed
}

// spelledOut names slog's message key "message" in place of its own "msg".
func spelledOut(_ []string, attribute slog.Attr) slog.Attr {
	if attribute.Key == slog.MessageKey {
		attribute.Key = "message"
	}
	return attribute
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level(), ReplaceAttr: spelledOut})))
	instance := &server{
		version:         version,
		revision:        revision,
		region:          os.Getenv("AWS_REGION"),
		functionVersion: os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"),
	}
	lambda.Start(instance.handle)
}
