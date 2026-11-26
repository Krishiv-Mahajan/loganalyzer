// Package cli implements the loganalyzer command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"

	"loganalyzer/internal/processor"
	"loganalyzer/internal/reader"
)

// Version is the program version. It is overridden at build time with
// -ldflags "-X loganalyzer/internal/cli.Version=v1.2.3".
var Version = "dev"

// Exit codes returned by Execute.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitInterrupted = 130
)

// Execute runs the CLI with the given arguments and returns a process exit
// code. Cancelling ctx (for example on SIGINT) stops the analysis gracefully.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand(stdout, stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(stderr, "interrupted: analysis stopped before completion, no results written")
		return ExitInterrupted
	default:
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return ExitError
	}
}

// NewRootCommand builds the command tree. Output is written to stdout;
// diagnostics and logs to stderr.
func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	opts := &globalOptions{}
	root := &cobra.Command{
		Use:   "loganalyzer",
		Short: "Concurrent analyzer for large log files",
		Long: `loganalyzer streams one or more log files through a pool of worker
goroutines and reports log-level counts, error frequencies, and recurring
failure patterns.

Input files can be given as arguments or with --file; "-" reads stdin.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)

	pf := root.PersistentFlags()
	pf.StringArrayVarP(&opts.files, "file", "f", nil, `input log file, repeatable ("-" for stdin)`)
	pf.IntVarP(&opts.workers, "workers", "w", runtime.NumCPU(), "number of parsing worker goroutines")
	pf.IntVar(&opts.readers, "readers", processor.DefaultReaders, "maximum number of files read concurrently")
	pf.IntVar(&opts.batchSize, "batch-size", reader.DefaultBatchSize, "lines per batch sent to workers")
	pf.IntVar(&opts.maxLineBytes, "max-line-bytes", reader.DefaultMaxLineBytes, "lines longer than this are truncated and counted as malformed")
	pf.BoolVar(&opts.json, "json", false, "write results as JSON")
	pf.StringVar(&opts.logLevel, "log-level", "warn", "diagnostic log level: debug, info, warn, error")
	pf.StringVar(&opts.logFormat, "log-format", "text", "diagnostic log format: text or json")

	root.AddCommand(
		newAnalyzeCommand(opts),
		newStatsCommand(opts),
		newTopErrorsCommand(opts),
	)
	return root
}
