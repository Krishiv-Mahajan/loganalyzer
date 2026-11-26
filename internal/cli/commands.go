package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"loganalyzer/internal/analysis"
)

const (
	defaultTopN   = 10
	defaultBucket = time.Minute
)

func newAnalyzeCommand(opts *globalOptions) *cobra.Command {
	var req analysisRequest
	cmd := &cobra.Command{
		Use:   "analyze [file...]",
		Short: "Full report: summary, levels, top error patterns and messages, malformed lines",
		Example: `  loganalyzer analyze app.log worker.log
  loganalyzer analyze -f app.log --workers 8 --top 5 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTop(req.topN); err != nil {
				return err
			}
			report, err := opts.runAnalysis(cmd, args, req)
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return renderAnalyze(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().IntVarP(&req.topN, "top", "n", defaultTopN, "number of error patterns and messages to show (0 = all)")
	cmd.Flags().DurationVar(&req.bucket, "bucket", defaultBucket, "error timeline bucket width (0 disables the timeline)")
	return cmd
}

// statsOutput is the JSON document written by the stats command.
type statsOutput struct {
	Summary          analysis.Summary         `json:"summary"`
	Levels           map[string]int64         `json:"levels"`
	Sources          []analysis.SourceSummary `json:"sources"`
	Timeline         *analysis.Timeline       `json:"error_timeline,omitempty"`
	MalformedReasons map[string]int64         `json:"malformed_reasons"`
}

func newStatsCommand(opts *globalOptions) *cobra.Command {
	req := analysisRequest{topN: 1}
	cmd := &cobra.Command{
		Use:   "stats [file...]",
		Short: "Log-level counts, error rate, per-file statistics, and error frequency over time",
		Example: `  loganalyzer stats app.log
  loganalyzer stats -f a.log -f b.log --bucket 5m --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := opts.runAnalysis(cmd, args, req)
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), statsOutput{
					Summary:          report.Summary,
					Levels:           report.Levels,
					Sources:          report.Sources,
					Timeline:         report.Timeline,
					MalformedReasons: report.Malformed.Reasons,
				})
			}
			return renderStats(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().DurationVar(&req.bucket, "bucket", defaultBucket, "error timeline bucket width (0 disables the timeline)")
	return cmd
}

// Grouping modes for top-errors.
const (
	groupByPattern = "pattern"
	groupByMessage = "message"
)

// topErrorsOutput is the JSON document written by the top-errors command.
type topErrorsOutput struct {
	GroupBy     string                `json:"group_by"`
	TotalErrors int64                 `json:"total_errors"`
	Distinct    int                   `json:"distinct"`
	Untracked   int64                 `json:"untracked,omitempty"`
	Errors      []analysis.ErrorGroup `json:"errors"`
}

func newTopErrorsCommand(opts *globalOptions) *cobra.Command {
	var (
		req     analysisRequest
		groupBy string
	)
	cmd := &cobra.Command{
		Use:   "top-errors [file...]",
		Short: "Most frequent ERROR/FATAL patterns (or exact messages)",
		Example: `  loganalyzer top-errors app.log
  loganalyzer top-errors app.log --by message --top 20 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTop(req.topN); err != nil {
				return err
			}
			if groupBy != groupByPattern && groupBy != groupByMessage {
				return fmt.Errorf("--by must be %q or %q, got %q", groupByPattern, groupByMessage, groupBy)
			}
			report, err := opts.runAnalysis(cmd, args, req)
			if err != nil {
				return err
			}
			out := topErrorsOutput{GroupBy: groupBy, TotalErrors: report.Summary.ErrorCount}
			if groupBy == groupByPattern {
				out.Distinct = report.Summary.DistinctPatterns
				out.Untracked = report.Summary.UntrackedPatternErrors
				out.Errors = report.TopPatterns
			} else {
				out.Distinct = report.Summary.DistinctMessages
				out.Untracked = report.Summary.UntrackedMessageErrors
				out.Errors = report.TopMessages
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			return renderTopErrors(cmd.OutOrStdout(), out)
		},
	}
	cmd.Flags().IntVarP(&req.topN, "top", "n", defaultTopN, "number of entries to show (0 = all)")
	cmd.Flags().StringVar(&groupBy, "by", groupByPattern, `group errors by normalized "pattern" or exact "message"`)
	return cmd
}

func validateTop(n int) error {
	if n < 0 {
		return fmt.Errorf("--top must not be negative, got %d", n)
	}
	return nil
}
