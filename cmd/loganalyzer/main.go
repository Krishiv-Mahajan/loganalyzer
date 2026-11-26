// Command loganalyzer analyzes large log files concurrently.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"loganalyzer/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
