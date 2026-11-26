package cli

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"loganalyzer/internal/analysis"
	"loganalyzer/internal/parser"
)

const (
	maxTextWidth = 100
	timelineBar  = 40
)

// printer writes formatted text through a tabwriter and remembers the first
// write error so render functions can report it once at the end.
type printer struct {
	tw  *tabwriter.Writer
	err error
}

func newPrinter(w io.Writer) *printer {
	return &printer{tw: tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)}
}

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.tw, format, args...)
	}
}

func (p *printer) flush() error {
	if p.err != nil {
		return p.err
	}
	return p.tw.Flush()
}

func renderAnalyze(w io.Writer, r analysis.Report) error {
	p := newPrinter(w)
	writeSummary(p, r)
	writeLevels(p, r.Levels, r.Summary.ParsedLines)
	writeGroups(p, "Top error patterns", "PATTERN", r.TopPatterns, r.Summary.DistinctPatterns)
	writeGroups(p, "Top error messages", "MESSAGE", r.TopMessages, r.Summary.DistinctMessages)
	writeMalformed(p, r.Malformed)
	return p.flush()
}

func renderStats(w io.Writer, r analysis.Report) error {
	p := newPrinter(w)
	writeSummary(p, r)
	writeLevels(p, r.Levels, r.Summary.ParsedLines)
	writeSources(p, r.Sources)
	writeTimeline(p, r.Timeline)
	return p.flush()
}

func renderTopErrors(w io.Writer, out topErrorsOutput) error {
	p := newPrinter(w)
	p.printf("Total errors (ERROR+FATAL): %d, distinct %ss: %d\n", out.TotalErrors, out.GroupBy, out.Distinct)
	if out.Untracked > 0 {
		p.printf("Errors not grouped (distinct %s limit reached): %d\n", out.GroupBy, out.Untracked)
	}
	p.printf("\n")
	if len(out.Errors) == 0 {
		p.printf("No errors found.\n")
		return p.flush()
	}
	p.printf("#\tCOUNT\tSHARE\tFATAL\tFIRST SEEN\tLAST SEEN\t%s\n", strings.ToUpper(out.GroupBy))
	for i, g := range out.Errors {
		p.printf("%d\t%d\t%s\t%d\t%s\t%s\t%s\n",
			i+1, g.Count, percent(g.Share), g.FatalCount,
			formatTime(g.FirstSeen), formatTime(g.LastSeen), truncate(g.Key))
	}
	if out.GroupBy == groupByPattern {
		p.printf("\nExamples\n")
		for i, g := range out.Errors {
			p.printf("%d\t%s:%d\t%s\n", i+1, g.Example.Source, g.Example.Line, truncate(g.Example.Message))
		}
	}
	return p.flush()
}

func writeSummary(p *printer, r analysis.Report) {
	s := r.Summary
	p.printf("Summary\n")
	p.printf("  Files:\t%d\n", len(r.Sources))
	p.printf("  Total lines:\t%d\n", s.TotalLines)
	p.printf("  Parsed:\t%d\n", s.ParsedLines)
	p.printf("  Blank:\t%d\n", s.BlankLines)
	p.printf("  Malformed:\t%d (%s of all lines)\n", s.MalformedLines, percent(ratio(s.MalformedLines, s.TotalLines)))
	p.printf("  Errors:\t%d (%s of parsed lines)\n", s.ErrorCount, percent(s.ErrorRate))
	if !s.FirstTimestamp.IsZero() {
		p.printf("  Time range:\t%s .. %s (%s)\n",
			formatTime(s.FirstTimestamp), formatTime(s.LastTimestamp), s.LastTimestamp.Sub(s.FirstTimestamp))
	}
	p.printf("\n")
}

func writeLevels(p *printer, levels map[string]int64, parsed int64) {
	p.printf("Log levels\n")
	p.printf("  LEVEL\tCOUNT\tPERCENT\n")
	for _, l := range parser.Levels() {
		n := levels[l.String()]
		p.printf("  %s\t%d\t%s\n", l, n, percent(ratio(n, parsed)))
	}
	p.printf("\n")
}

func writeSources(p *printer, sources []analysis.SourceSummary) {
	p.printf("Files\n")
	p.printf("  FILE\tLINES\tERRORS\tMALFORMED\tBLANK\n")
	for _, s := range sources {
		p.printf("  %s\t%d\t%d\t%d\t%d\n", s.Source, s.TotalLines, s.ErrorCount, s.MalformedLines, s.BlankLines)
	}
	p.printf("\n")
}

func writeGroups(p *printer, title, keyHeader string, groups []analysis.ErrorGroup, distinct int) {
	p.printf("%s (showing %d of %d)\n", title, len(groups), distinct)
	if len(groups) == 0 {
		p.printf("  none\n\n")
		return
	}
	p.printf("  #\tCOUNT\tSHARE\tFATAL\t%s\n", keyHeader)
	for i, g := range groups {
		p.printf("  %d\t%d\t%s\t%d\t%s\n", i+1, g.Count, percent(g.Share), g.FatalCount, truncate(g.Key))
	}
	p.printf("\n")
}

func writeTimeline(p *printer, t *analysis.Timeline) {
	if t == nil {
		return
	}
	p.printf("Errors per %s (non-empty buckets only)\n", t.Bucket)
	if len(t.Buckets) == 0 {
		p.printf("  none\n\n")
		return
	}
	var peak int64
	for _, b := range t.Buckets {
		peak = max(peak, b.Errors)
	}
	for _, b := range t.Buckets {
		width := int(float64(b.Errors) / float64(peak) * timelineBar)
		p.printf("  %s\t%d\t%s\n", formatTime(b.Start), b.Errors, strings.Repeat("#", max(width, 1)))
	}
	p.printf("\n")
}

func writeMalformed(p *printer, m analysis.MalformedSummary) {
	p.printf("Malformed lines: %d\n", m.Count)
	if m.Count == 0 {
		return
	}
	reasons := slices.Sorted(maps.Keys(m.Reasons))
	slices.SortStableFunc(reasons, func(a, b string) int { return cmp.Compare(m.Reasons[b], m.Reasons[a]) })
	p.printf("  REASON\tCOUNT\n")
	for _, reason := range reasons {
		p.printf("  %s\t%d\n", reason, m.Reasons[reason])
	}
	p.printf("\n  Samples\n")
	for _, s := range m.Samples {
		p.printf("  %s:%d\t%s\t%q\n", s.Source, s.Line, s.Reason, truncate(s.Text))
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func percent(f float64) string { return fmt.Sprintf("%.2f%%", f*100) }

func ratio(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// truncate shortens s to maxTextWidth runes for table display.
func truncate(s string) string {
	if utf8.RuneCountInString(s) <= maxTextWidth {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxTextWidth-1]) + "…"
}
