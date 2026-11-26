package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loganalyzer/internal/analysis"
	"loganalyzer/internal/processor"
	"loganalyzer/internal/reader"
)

// globalOptions holds the persistent flags shared by all subcommands.
type globalOptions struct {
	files        []string
	workers      int
	readers      int
	batchSize    int
	maxLineBytes int
	json         bool
	logLevel     string
	logFormat    string
}

func (o *globalOptions) validate() error {
	var errs []error
	if o.workers < 1 {
		errs = append(errs, fmt.Errorf("--workers must be at least 1, got %d", o.workers))
	}
	if o.readers < 1 {
		errs = append(errs, fmt.Errorf("--readers must be at least 1, got %d", o.readers))
	}
	if o.batchSize < 1 {
		errs = append(errs, fmt.Errorf("--batch-size must be at least 1, got %d", o.batchSize))
	}
	if o.maxLineBytes < 1 {
		errs = append(errs, fmt.Errorf("--max-line-bytes must be at least 1, got %d", o.maxLineBytes))
	}
	return errors.Join(errs...)
}

// analysisRequest holds per-command analysis settings.
type analysisRequest struct {
	topN   int
	bucket time.Duration
}

// runAnalysis validates flags, runs the processing pipeline over the input
// files, and returns the resulting report.
func (o *globalOptions) runAnalysis(cmd *cobra.Command, args []string, req analysisRequest) (analysis.Report, error) {
	if err := o.validate(); err != nil {
		return analysis.Report{}, err
	}
	if req.bucket < 0 {
		return analysis.Report{}, fmt.Errorf("--bucket must not be negative, got %s", req.bucket)
	}
	logger, err := newLogger(cmd.ErrOrStderr(), o.logLevel, o.logFormat)
	if err != nil {
		return analysis.Report{}, err
	}

	paths := append(slices.Clone(o.files), args...)
	if len(paths) == 0 {
		return analysis.Report{}, errors.New(`no input files: pass paths as arguments or with --file ("-" for stdin)`)
	}
	sources, err := processor.FileSources(paths)
	if err != nil {
		return analysis.Report{}, err
	}

	cfg := processor.Config{
		Workers: o.workers,
		Readers: o.readers,
		Reader: reader.Options{
			BatchSize:    o.batchSize,
			MaxLineBytes: o.maxLineBytes,
		},
		Analysis: analysis.Options{TimelineBucket: req.bucket},
		Logger:   logger,
	}

	start := time.Now()
	agg, err := processor.Run(cmd.Context(), cfg, sources)
	if err != nil {
		return analysis.Report{}, err
	}
	report := agg.Report(req.topN)
	elapsed := time.Since(start)

	logger.Info("analysis complete",
		slog.Int("files", len(sources)),
		slog.Int64("lines", report.Summary.TotalLines),
		slog.Duration("elapsed", elapsed),
		slog.Float64("lines_per_sec", float64(report.Summary.TotalLines)/max(elapsed.Seconds(), 1e-9)),
	)
	if n := report.Summary.MalformedLines; n > 0 {
		logger.Warn("skipped malformed lines", slog.Int64("count", n), slog.Any("reasons", report.Malformed.Reasons))
	}
	return report, nil
}

func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid --log-level %q: want debug, info, warn, or error", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("invalid --log-format %q: want text or json", format)
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
