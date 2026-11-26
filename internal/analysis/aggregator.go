// Package analysis aggregates parsed log entries into statistics: per-level
// counts, error frequencies over time, recurring error messages, and
// normalized failure patterns.
//
// An Aggregator is deliberately not safe for concurrent use. Concurrent
// pipelines give each worker its own Aggregator and combine them with Merge
// once the workers finish, which avoids lock contention on the hot path.
// Reports are deterministic: the same input yields the same Report no matter
// how lines were partitioned across aggregators (as long as the MaxGroups
// limit is not exceeded).
package analysis

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"loganalyzer/internal/parser"
)

// Default option values.
const (
	DefaultMaxGroups           = 10_000
	DefaultMaxMalformedSamples = 20
	maxSampleTextBytes         = 256
)

// Options configures an Aggregator. Zero values select the defaults.
type Options struct {
	// TimelineBucket is the width of the buckets used for the error
	// timeline. Zero disables the timeline.
	TimelineBucket time.Duration
	// MaxGroups caps the number of distinct error messages and patterns
	// tracked, bounding memory on inputs with very high cardinality. Errors
	// beyond the cap are still counted but reported as "untracked".
	MaxGroups int
	// MaxMalformedSamples is the number of malformed lines kept as examples.
	MaxMalformedSamples int
}

func (o Options) withDefaults() Options {
	if o.MaxGroups <= 0 {
		o.MaxGroups = DefaultMaxGroups
	}
	if o.MaxMalformedSamples <= 0 {
		o.MaxMalformedSamples = DefaultMaxMalformedSamples
	}
	return o
}

// Location identifies a line within an input source.
type Location struct {
	Source string `json:"source"`
	Line   int64  `json:"line"`
}

func compareLocation(a, b Location) int {
	if c := strings.Compare(a.Source, b.Source); c != 0 {
		return c
	}
	return cmp.Compare(a.Line, b.Line)
}

type levelCounts [parser.NumLevels]int64

func (c *levelCounts) add(o *levelCounts) {
	for i := range c {
		c[i] += o[i]
	}
}

type sourceStats struct {
	levels    levelCounts
	blank     int64
	malformed int64
}

// group accumulates occurrences of one error message or pattern.
type group struct {
	count      int64
	fatal      int64
	first      time.Time
	last       time.Time
	example    Location
	exampleMsg string
}

func (g *group) observe(loc Location, e parser.Entry) {
	g.count++
	if e.Level == parser.LevelFatal {
		g.fatal++
	}
	g.observeTime(e.Timestamp, e.Timestamp)
	if compareLocation(loc, g.example) < 0 {
		g.example, g.exampleMsg = loc, e.Message
	}
}

func (g *group) observeTime(first, last time.Time) {
	if !first.IsZero() && (g.first.IsZero() || first.Before(g.first)) {
		g.first = first
	}
	if !last.IsZero() && last.After(g.last) {
		g.last = last
	}
}

func (g *group) merge(o *group) {
	g.count += o.count
	g.fatal += o.fatal
	g.observeTime(o.first, o.last)
	if compareLocation(o.example, g.example) < 0 {
		g.example, g.exampleMsg = o.example, o.exampleMsg
	}
}

// groupSet is a bounded map of error groups.
type groupSet struct {
	groups    map[string]*group
	untracked int64
}

func newGroupSet() groupSet { return groupSet{groups: make(map[string]*group)} }

func (s *groupSet) observe(key string, loc Location, e parser.Entry, limit int) {
	g, ok := s.groups[key]
	if !ok {
		if len(s.groups) >= limit {
			s.untracked++
			return
		}
		g = &group{example: loc, exampleMsg: e.Message}
		s.groups[key] = g
	}
	g.observe(loc, e)
}

func (s *groupSet) merge(o *groupSet, limit int) {
	s.untracked += o.untracked
	for key, og := range o.groups {
		if g, ok := s.groups[key]; ok {
			g.merge(og)
			continue
		}
		if len(s.groups) >= limit {
			s.untracked += og.count
			continue
		}
		cp := *og
		s.groups[key] = &cp
	}
}

// Aggregator accumulates statistics for a stream of log lines.
type Aggregator struct {
	opts Options

	levels           levelCounts
	blank            int64
	malformed        int64
	malformedReasons map[string]int64
	malformedSamples []MalformedLine

	sources map[string]*sourceStats
	// Cache of the most recent source; consecutive lines almost always come
	// from the same source because input arrives in per-source batches.
	lastSource string
	lastStats  *sourceStats

	patterns groupSet
	messages groupSet

	timeline    map[int64]int64 // bucket start (Unix nanoseconds) -> errors
	first, last time.Time
}

// New returns an empty Aggregator.
func New(opts Options) *Aggregator {
	return &Aggregator{
		opts:             opts.withDefaults(),
		malformedReasons: make(map[string]int64),
		sources:          make(map[string]*sourceStats),
		patterns:         newGroupSet(),
		messages:         newGroupSet(),
		timeline:         make(map[int64]int64),
	}
}

func (a *Aggregator) source(name string) *sourceStats {
	if a.lastStats != nil && a.lastSource == name {
		return a.lastStats
	}
	s, ok := a.sources[name]
	if !ok {
		s = &sourceStats{}
		a.sources[name] = s
	}
	a.lastSource, a.lastStats = name, s
	return s
}

// AddEntry records a successfully parsed entry found at loc.
func (a *Aggregator) AddEntry(loc Location, e parser.Entry) {
	if !e.Timestamp.IsZero() {
		// Normalise to UTC so reports are independent of input offsets.
		e.Timestamp = e.Timestamp.UTC()
		if a.first.IsZero() || e.Timestamp.Before(a.first) {
			a.first = e.Timestamp
		}
		if e.Timestamp.After(a.last) {
			a.last = e.Timestamp
		}
	}

	a.levels[e.Level]++
	a.source(loc.Source).levels[e.Level]++

	if !e.Level.IsError() {
		return
	}
	a.messages.observe(e.Message, loc, e, a.opts.MaxGroups)
	a.patterns.observe(Normalize(e.Message), loc, e, a.opts.MaxGroups)
	if a.opts.TimelineBucket > 0 && !e.Timestamp.IsZero() {
		a.timeline[e.Timestamp.Truncate(a.opts.TimelineBucket).UnixNano()]++
	}
}

// AddBlank records an empty or whitespace-only line.
func (a *Aggregator) AddBlank(source string) {
	a.blank++
	a.source(source).blank++
}

// AddMalformed records a line that could not be parsed. reason should be a
// short, low-cardinality description such as a parser sentinel error message.
func (a *Aggregator) AddMalformed(loc Location, reason, text string) {
	a.malformed++
	a.source(loc.Source).malformed++
	a.malformedReasons[reason]++

	if len(text) > maxSampleTextBytes {
		text = text[:maxSampleTextBytes]
	}
	a.malformedSamples = append(a.malformedSamples, MalformedLine{
		Location: loc,
		Reason:   reason,
		// Clone so a small sample does not pin a large line in memory.
		Text: strings.Clone(text),
	})
	if len(a.malformedSamples) >= 2*a.opts.MaxMalformedSamples {
		a.malformedSamples = trimSamples(a.malformedSamples, a.opts.MaxMalformedSamples)
	}
}

// trimSamples keeps the n samples that appear first in input order.
func trimSamples(s []MalformedLine, n int) []MalformedLine {
	slices.SortFunc(s, func(x, y MalformedLine) int { return compareLocation(x.Location, y.Location) })
	if len(s) > n {
		clear(s[n:])
		s = s[:n]
	}
	return s
}

// Merge folds the statistics of other into a. other must not be used
// afterwards. Both aggregators should have been created with the same
// Options.
func (a *Aggregator) Merge(other *Aggregator) {
	a.levels.add(&other.levels)
	a.blank += other.blank
	a.malformed += other.malformed
	for reason, n := range other.malformedReasons {
		a.malformedReasons[reason] += n
	}
	a.malformedSamples = trimSamples(append(a.malformedSamples, other.malformedSamples...), a.opts.MaxMalformedSamples)

	for name, src := range other.sources {
		s := a.source(name)
		s.levels.add(&src.levels)
		s.blank += src.blank
		s.malformed += src.malformed
	}

	a.patterns.merge(&other.patterns, a.opts.MaxGroups)
	a.messages.merge(&other.messages, a.opts.MaxGroups)

	for bucket, n := range other.timeline {
		a.timeline[bucket] += n
	}
	if !other.first.IsZero() && (a.first.IsZero() || other.first.Before(a.first)) {
		a.first = other.first
	}
	if other.last.After(a.last) {
		a.last = other.last
	}
}
