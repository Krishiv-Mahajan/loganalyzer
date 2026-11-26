package analysis

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"loganalyzer/internal/parser"
)

var t0 = time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

func entry(offset time.Duration, level parser.Level, msg string) parser.Entry {
	return parser.Entry{Timestamp: t0.Add(offset), Level: level, Message: msg}
}

func loc(src string, line int64) Location { return Location{Source: src, Line: line} }

func TestAggregatorCounts(t *testing.T) {
	a := New(Options{})
	a.AddEntry(loc("a.log", 1), entry(0, parser.LevelInfo, "started"))
	a.AddEntry(loc("a.log", 2), entry(time.Second, parser.LevelWarn, "slow"))
	a.AddEntry(loc("a.log", 3), entry(2*time.Second, parser.LevelError, "db down"))
	a.AddBlank("a.log")
	a.AddMalformed(loc("a.log", 5), parser.ErrUnknownLevel.Error(), "garbage")
	a.AddEntry(loc("b.log", 1), entry(-time.Minute, parser.LevelFatal, "panic"))
	a.AddEntry(loc("b.log", 2), parser.Entry{Level: parser.LevelDebug, Message: "no timestamp"})

	r := a.Report(0)
	s := r.Summary
	if s.TotalLines != 7 || s.ParsedLines != 5 || s.BlankLines != 1 || s.MalformedLines != 1 {
		t.Errorf("line totals = %+v", s)
	}
	if s.ErrorCount != 2 {
		t.Errorf("ErrorCount = %d, want 2", s.ErrorCount)
	}
	if want := 2.0 / 5.0; s.ErrorRate != want {
		t.Errorf("ErrorRate = %v, want %v", s.ErrorRate, want)
	}
	if !s.FirstTimestamp.Equal(t0.Add(-time.Minute)) || !s.LastTimestamp.Equal(t0.Add(2*time.Second)) {
		t.Errorf("time range = %v .. %v", s.FirstTimestamp, s.LastTimestamp)
	}

	wantLevels := map[string]int64{"DEBUG": 1, "INFO": 1, "WARN": 1, "ERROR": 1, "FATAL": 1}
	for k, v := range wantLevels {
		if r.Levels[k] != v {
			t.Errorf("Levels[%s] = %d, want %d", k, r.Levels[k], v)
		}
	}

	if len(r.Sources) != 2 || r.Sources[0].Source != "a.log" || r.Sources[1].Source != "b.log" {
		t.Fatalf("sources = %+v", r.Sources)
	}
	if got := r.Sources[0]; got.TotalLines != 5 || got.BlankLines != 1 || got.MalformedLines != 1 || got.ErrorCount != 1 {
		t.Errorf("a.log summary = %+v", got)
	}
	if got := r.Sources[1]; got.TotalLines != 2 || got.ErrorCount != 1 || got.Levels["FATAL"] != 1 {
		t.Errorf("b.log summary = %+v", got)
	}

	if r.Malformed.Reasons[parser.ErrUnknownLevel.Error()] != 1 {
		t.Errorf("malformed reasons = %v", r.Malformed.Reasons)
	}
	if r.Timeline != nil {
		t.Errorf("timeline should be disabled by default, got %+v", r.Timeline)
	}
}

func TestTopGroups(t *testing.T) {
	a := New(Options{})
	// Pattern "timeout after <n>ms": 3 occurrences, one FATAL.
	a.AddEntry(loc("svc.log", 30), entry(3*time.Minute, parser.LevelError, "timeout after 30ms"))
	a.AddEntry(loc("svc.log", 10), entry(time.Minute, parser.LevelError, "timeout after 10ms"))
	a.AddEntry(loc("svc.log", 20), entry(2*time.Minute, parser.LevelFatal, "timeout after 30ms"))
	// Two single-occurrence patterns tie on count and are ordered by key.
	a.AddEntry(loc("svc.log", 40), entry(0, parser.LevelError, "zebra failed"))
	a.AddEntry(loc("svc.log", 50), entry(0, parser.LevelError, "alpha failed"))
	// Non-error levels never become groups.
	a.AddEntry(loc("svc.log", 60), entry(0, parser.LevelWarn, "timeout after 99ms"))

	r := a.Report(0)
	if r.Summary.DistinctPatterns != 3 || r.Summary.DistinctMessages != 4 {
		t.Errorf("distinct patterns/messages = %d/%d, want 3/4",
			r.Summary.DistinctPatterns, r.Summary.DistinctMessages)
	}

	top := r.TopPatterns[0]
	if top.Key != "timeout after <n>ms" || top.Count != 3 || top.FatalCount != 1 {
		t.Fatalf("top pattern = %+v", top)
	}
	if want := 3.0 / 5.0; top.Share != want {
		t.Errorf("Share = %v, want %v", top.Share, want)
	}
	if !top.FirstSeen.Equal(t0.Add(time.Minute)) || !top.LastSeen.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("first/last seen = %v / %v", top.FirstSeen, top.LastSeen)
	}
	if top.Example.Line != 10 || top.Example.Message != "timeout after 10ms" {
		t.Errorf("example should be the earliest line, got %+v", top.Example)
	}
	if r.TopPatterns[1].Key != "alpha failed" || r.TopPatterns[2].Key != "zebra failed" {
		t.Errorf("tie order = %q, %q", r.TopPatterns[1].Key, r.TopPatterns[2].Key)
	}

	if r.TopMessages[0].Key != "timeout after 30ms" || r.TopMessages[0].Count != 2 {
		t.Errorf("top message = %+v", r.TopMessages[0])
	}

	if limited := a.Report(2); len(limited.TopPatterns) != 2 || len(limited.TopMessages) != 2 {
		t.Errorf("topN=2 returned %d patterns, %d messages", len(limited.TopPatterns), len(limited.TopMessages))
	}
}

func TestTimeline(t *testing.T) {
	a := New(Options{TimelineBucket: time.Minute})
	a.AddEntry(loc("x", 1), entry(10*time.Second, parser.LevelError, "e"))
	a.AddEntry(loc("x", 2), entry(50*time.Second, parser.LevelFatal, "f"))
	a.AddEntry(loc("x", 3), entry(5*time.Minute, parser.LevelError, "e"))
	a.AddEntry(loc("x", 4), entry(5*time.Minute, parser.LevelInfo, "not an error"))
	a.AddEntry(loc("x", 5), parser.Entry{Level: parser.LevelError, Message: "no timestamp"})

	tl := a.Report(0).Timeline
	if tl == nil || tl.Bucket != "1m0s" {
		t.Fatalf("timeline = %+v", tl)
	}
	want := []TimelineBucket{{Start: t0, Errors: 2}, {Start: t0.Add(5 * time.Minute), Errors: 1}}
	if len(tl.Buckets) != len(want) {
		t.Fatalf("buckets = %+v, want %+v", tl.Buckets, want)
	}
	for i := range want {
		if !tl.Buckets[i].Start.Equal(want[i].Start) || tl.Buckets[i].Errors != want[i].Errors {
			t.Errorf("bucket %d = %+v, want %+v", i, tl.Buckets[i], want[i])
		}
	}
}

func TestTimestampsNormalizedToUTC(t *testing.T) {
	a := New(Options{})
	ist := time.FixedZone("IST", 5*3600+1800)
	a.AddEntry(loc("x", 1), parser.Entry{Timestamp: t0.In(ist), Level: parser.LevelError, Message: "e"})
	r := a.Report(0)
	if r.Summary.FirstTimestamp.Location() != time.UTC || r.TopPatterns[0].FirstSeen.Location() != time.UTC {
		t.Errorf("timestamps not in UTC: %v, %v", r.Summary.FirstTimestamp, r.TopPatterns[0].FirstSeen)
	}
}

func TestMalformedSamplesBounded(t *testing.T) {
	a := New(Options{MaxMalformedSamples: 3})
	long := strings.Repeat("x", 10_000)
	// Insert in reverse order to check that the earliest lines are kept.
	for i := int64(100); i >= 1; i-- {
		a.AddMalformed(loc("m.log", i), "bad", long)
	}
	r := a.Report(0)
	if r.Malformed.Count != 100 || len(r.Malformed.Samples) != 3 {
		t.Fatalf("count=%d samples=%d", r.Malformed.Count, len(r.Malformed.Samples))
	}
	for i, s := range r.Malformed.Samples {
		if s.Line != int64(i+1) {
			t.Errorf("sample %d is line %d, want %d", i, s.Line, i+1)
		}
		if len(s.Text) != maxSampleTextBytes {
			t.Errorf("sample text length = %d, want %d", len(s.Text), maxSampleTextBytes)
		}
	}
}

func TestMaxGroups(t *testing.T) {
	a := New(Options{MaxGroups: 2})
	for i := range 5 {
		a.AddEntry(loc("x", int64(i)), entry(0, parser.LevelError, fmt.Sprintf("failure kind %c", 'a'+i)))
	}
	a.AddEntry(loc("x", 9), entry(0, parser.LevelError, "failure kind a"))
	s := a.Report(0).Summary
	if s.DistinctPatterns != 2 || s.UntrackedPatternErrors != 3 {
		t.Errorf("patterns tracked=%d untracked=%d, want 2/3", s.DistinctPatterns, s.UntrackedPatternErrors)
	}
	if s.DistinctMessages != 2 || s.UntrackedMessageErrors != 3 {
		t.Errorf("messages tracked=%d untracked=%d, want 2/3", s.DistinctMessages, s.UntrackedMessageErrors)
	}
	if s.ErrorCount != 6 {
		t.Errorf("ErrorCount = %d; untracked errors must still be counted", s.ErrorCount)
	}
}

type event struct {
	loc       Location
	entry     parser.Entry
	blank     bool
	malformed string
}

func sampleEvents() []event {
	var evs []event
	levels := parser.Levels()
	for i := range 500 {
		l := loc(fmt.Sprintf("src-%d.log", i%3), int64(i+1))
		switch {
		case i%37 == 0:
			evs = append(evs, event{loc: l, blank: true})
		case i%23 == 0:
			evs = append(evs, event{loc: l, malformed: fmt.Sprintf("junk %d", i)})
		default:
			lvl := levels[i%len(levels)]
			msg := fmt.Sprintf("operation %d failed on host-%d", i, i%4)
			evs = append(evs, event{loc: l, entry: entry(time.Duration(i)*time.Second, lvl, msg)})
		}
	}
	return evs
}

func apply(a *Aggregator, e event) {
	switch {
	case e.blank:
		a.AddBlank(e.loc.Source)
	case e.malformed != "":
		a.AddMalformed(e.loc, "bad line", e.malformed)
	default:
		a.AddEntry(e.loc, e.entry)
	}
}

func reportJSON(t *testing.T, a *Aggregator) string {
	t.Helper()
	b, err := json.Marshal(a.Report(0))
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(b)
}

// TestMergeEquivalence verifies that splitting input across several
// aggregators and merging them yields exactly the same report as a single
// aggregator. This is the property the concurrent pipeline relies on.
func TestMergeEquivalence(t *testing.T) {
	opts := Options{TimelineBucket: time.Minute, MaxMalformedSamples: 5}
	evs := sampleEvents()

	single := New(opts)
	for _, e := range evs {
		apply(single, e)
	}
	want := reportJSON(t, single)

	for _, parts := range []int{2, 3, 7} {
		t.Run(fmt.Sprintf("parts=%d", parts), func(t *testing.T) {
			aggs := make([]*Aggregator, parts)
			for i := range aggs {
				aggs[i] = New(opts)
			}
			// Interleave in a non-trivial order.
			for i, e := range evs {
				apply(aggs[(i*7)%parts], e)
			}
			merged := aggs[parts-1]
			for _, a := range aggs[:parts-1] {
				merged.Merge(a)
			}
			if got := reportJSON(t, merged); got != want {
				t.Errorf("merged report differs from single report\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestEmptyReport(t *testing.T) {
	r := New(Options{TimelineBucket: time.Hour}).Report(10)
	if r.Summary.ErrorRate != 0 || r.Summary.TotalLines != 0 {
		t.Errorf("empty summary = %+v", r.Summary)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal empty report: %v", err)
	}
	for _, field := range []string{`"top_patterns":[]`, `"samples":[]`, `"buckets":[]`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("empty report JSON missing %s: %s", field, b)
		}
	}
	if strings.Contains(string(b), "first_timestamp") {
		t.Errorf("zero timestamps should be omitted: %s", b)
	}
}
