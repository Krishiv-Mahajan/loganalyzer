package analysis

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"loganalyzer/internal/parser"
)

// Report is an immutable, JSON-serialisable snapshot of an Aggregator.
type Report struct {
	Summary     Summary          `json:"summary"`
	Levels      map[string]int64 `json:"levels"`
	Sources     []SourceSummary  `json:"sources"`
	TopPatterns []ErrorGroup     `json:"top_patterns"`
	TopMessages []ErrorGroup     `json:"top_messages"`
	Timeline    *Timeline        `json:"error_timeline,omitempty"`
	Malformed   MalformedSummary `json:"malformed"`
}

// Summary holds headline totals.
type Summary struct {
	TotalLines     int64 `json:"total_lines"`
	ParsedLines    int64 `json:"parsed_lines"`
	BlankLines     int64 `json:"blank_lines"`
	MalformedLines int64 `json:"malformed_lines"`
	// ErrorCount is the number of ERROR and FATAL entries.
	ErrorCount int64 `json:"error_count"`
	// ErrorRate is ErrorCount / ParsedLines, in the range [0, 1].
	ErrorRate        float64 `json:"error_rate"`
	DistinctPatterns int     `json:"distinct_patterns"`
	DistinctMessages int     `json:"distinct_messages"`
	// Untracked* count errors that were not attributed to a group because
	// the MaxGroups limit was reached.
	UntrackedPatternErrors int64     `json:"untracked_pattern_errors,omitempty"`
	UntrackedMessageErrors int64     `json:"untracked_message_errors,omitempty"`
	FirstTimestamp         time.Time `json:"first_timestamp,omitzero"`
	LastTimestamp          time.Time `json:"last_timestamp,omitzero"`
}

// SourceSummary holds per-input statistics.
type SourceSummary struct {
	Source         string           `json:"source"`
	TotalLines     int64            `json:"total_lines"`
	MalformedLines int64            `json:"malformed_lines"`
	BlankLines     int64            `json:"blank_lines"`
	ErrorCount     int64            `json:"error_count"`
	Levels         map[string]int64 `json:"levels"`
}

// ErrorGroup describes a recurring error message or failure pattern.
type ErrorGroup struct {
	// Key is the normalized pattern or the exact message, depending on the
	// list the group belongs to.
	Key        string `json:"key"`
	Count      int64  `json:"count"`
	FatalCount int64  `json:"fatal_count"`
	// Share is Count divided by the total number of errors, in [0, 1].
	Share     float64   `json:"share"`
	FirstSeen time.Time `json:"first_seen,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	Example   Example   `json:"example"`
}

// Example is a concrete occurrence of an error group: the earliest one in
// input order (by source name, then line number).
type Example struct {
	Location
	Message string `json:"message"`
}

// Timeline is the number of errors per time bucket. Only buckets that
// contain at least one error are listed.
type Timeline struct {
	Bucket  string           `json:"bucket"`
	Buckets []TimelineBucket `json:"buckets"`
}

// TimelineBucket is one bucket of the error timeline.
type TimelineBucket struct {
	Start  time.Time `json:"start"`
	Errors int64     `json:"errors"`
}

// MalformedSummary describes lines that could not be parsed.
type MalformedSummary struct {
	Count   int64            `json:"count"`
	Reasons map[string]int64 `json:"reasons"`
	Samples []MalformedLine  `json:"samples"`
}

// MalformedLine is a sample malformed line.
type MalformedLine struct {
	Location
	Reason string `json:"reason"`
	Text   string `json:"text"`
}

// Report builds a snapshot of the current statistics. topN limits the
// pattern and message lists; zero or a negative value means no limit.
func (a *Aggregator) Report(topN int) Report {
	parsed := sumLevels(&a.levels)
	errorCount := a.levels[parser.LevelError] + a.levels[parser.LevelFatal]

	r := Report{
		Summary: Summary{
			TotalLines:             parsed + a.blank + a.malformed,
			ParsedLines:            parsed,
			BlankLines:             a.blank,
			MalformedLines:         a.malformed,
			ErrorCount:             errorCount,
			ErrorRate:              ratio(errorCount, parsed),
			DistinctPatterns:       len(a.patterns.groups),
			DistinctMessages:       len(a.messages.groups),
			UntrackedPatternErrors: a.patterns.untracked,
			UntrackedMessageErrors: a.messages.untracked,
			FirstTimestamp:         a.first,
			LastTimestamp:          a.last,
		},
		Levels:      levelMap(&a.levels),
		Sources:     a.sourceSummaries(),
		TopPatterns: topGroups(a.patterns.groups, topN, errorCount),
		TopMessages: topGroups(a.messages.groups, topN, errorCount),
		Malformed: MalformedSummary{
			Count:   a.malformed,
			Reasons: maps.Clone(a.malformedReasons),
			Samples: trimSamples(slices.Clone(a.malformedSamples), a.opts.MaxMalformedSamples),
		},
	}
	if r.Malformed.Samples == nil {
		r.Malformed.Samples = []MalformedLine{}
	}
	if a.opts.TimelineBucket > 0 {
		r.Timeline = a.timelineReport()
	}
	return r
}

func (a *Aggregator) sourceSummaries() []SourceSummary {
	out := make([]SourceSummary, 0, len(a.sources))
	for name, s := range a.sources {
		parsed := sumLevels(&s.levels)
		out = append(out, SourceSummary{
			Source:         name,
			TotalLines:     parsed + s.blank + s.malformed,
			MalformedLines: s.malformed,
			BlankLines:     s.blank,
			ErrorCount:     s.levels[parser.LevelError] + s.levels[parser.LevelFatal],
			Levels:         levelMap(&s.levels),
		})
	}
	slices.SortFunc(out, func(x, y SourceSummary) int { return strings.Compare(x.Source, y.Source) })
	return out
}

func (a *Aggregator) timelineReport() *Timeline {
	t := &Timeline{
		Bucket:  a.opts.TimelineBucket.String(),
		Buckets: make([]TimelineBucket, 0, len(a.timeline)),
	}
	for start, n := range a.timeline {
		t.Buckets = append(t.Buckets, TimelineBucket{Start: time.Unix(0, start).UTC(), Errors: n})
	}
	slices.SortFunc(t.Buckets, func(x, y TimelineBucket) int { return x.Start.Compare(y.Start) })
	return t
}

// topGroups returns groups ordered by descending count, breaking ties by
// key so the output is deterministic.
func topGroups(groups map[string]*group, topN int, totalErrors int64) []ErrorGroup {
	out := make([]ErrorGroup, 0, len(groups))
	for key, g := range groups {
		out = append(out, ErrorGroup{
			Key:        key,
			Count:      g.count,
			FatalCount: g.fatal,
			Share:      ratio(g.count, totalErrors),
			FirstSeen:  g.first,
			LastSeen:   g.last,
			Example:    Example{Location: g.example, Message: g.exampleMsg},
		})
	}
	slices.SortFunc(out, func(x, y ErrorGroup) int {
		if c := cmp.Compare(y.Count, x.Count); c != 0 {
			return c
		}
		return strings.Compare(x.Key, y.Key)
	})
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}

// levelMap returns counts keyed by level name, including zero counts so
// consumers can rely on every level being present.
func levelMap(c *levelCounts) map[string]int64 {
	m := make(map[string]int64, parser.NumLevels-1)
	for _, l := range parser.Levels() {
		m[l.String()] = c[l]
	}
	return m
}

func sumLevels(c *levelCounts) int64 {
	var n int64
	for _, v := range c {
		n += v
	}
	return n
}

func ratio(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
