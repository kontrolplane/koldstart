// Command koldstart deploys serverless applications to aws lambda. kdst is the same binary.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui"
)

// exitInterrupted is the exit code after ctrl+c or a SIGTERM, as a shell gives one that a signal
// stopped.
const exitInterrupted = 130

// main stops the command on ctrl+c, and on the SIGTERM a ci runner or docker stop sends, so a
// deploy records what it did and releases its lock. A second signal kills it.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	name := cli.Name(os.Args[0])
	err := cli.Root(name, tui.Run).ExecuteContext(ctx)
	interrupted := ctx.Err() != nil
	stop()
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "error: %s\n", err)
	if interrupted {
		os.Exit(exitInterrupted)
	}
	os.Exit(cli.ExitCode(err))
}
