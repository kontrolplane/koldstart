// Command koldstart deploys serverless applications to aws lambda. kdst is the same binary.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	name := cli.Name(os.Args[0])
	if err := cli.Root(name, tui.Run).ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s (see %s --help)\n", err, name)
		os.Exit(1)
	}
}
