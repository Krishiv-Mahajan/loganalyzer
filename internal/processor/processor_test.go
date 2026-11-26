package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"loganalyzer/internal/analysis"
	"loganalyzer/internal/loggen"
	"loganalyzer/internal/parser"
	"loganalyzer/internal/reader"
)

func generate(t testing.TB, opts loggen.Options) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := loggen.Write(&buf, opts); err != nil {
		t.Fatalf("generate logs: %v", err)
	}
	return buf.Bytes()
}

// memSources returns fresh in-memory sources over the given contents. A new
// slice is needed for every Run because each Source wraps a single reader.
func memSources(contents map[string][]byte) []Source {
	var out []Source
	for name, data := range contents {
		out = append(out, ReaderSource(name, bytes.NewReader(data)))
	}
	return out
}

func reportJSON(t *testing.T, agg *analysis.Aggregator) string {
	t.Helper()
	b, err := json.Marshal(agg.Report(0))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestRunDeterministicAcrossWorkers is the main concurrency correctness
// test: the report must be identical for every worker/reader/batch
// configuration. Run it with -race to also check for data races.
func TestRunDeterministicAcrossWorkers(t *testing.T) {
	const linesPerFile = 5_000
	contents := map[string][]byte{
		"a.log": generate(t, loggen.Options{Lines: linesPerFile, Seed: 1, MalformedEvery: 97}),
		"b.log": generate(t, loggen.Options{Lines: linesPerFile, Seed: 2, MalformedEvery: 31}),
		"c.log": generate(t, loggen.Options{Lines: linesPerFile, Seed: 3}),
	}
	analysisOpts := analysis.Options{TimelineBucket: time.Minute}

	baseline, err := Run(context.Background(), Config{Workers: 1, Readers: 1, Analysis: analysisOpts}, memSources(contents))
	if err != nil {
		t.Fatalf("baseline run: %v", err)
	}
	want := reportJSON(t, baseline)

	r := baseline.Report(0)
	if r.Summary.TotalLines != 3*linesPerFile {
		t.Fatalf("TotalLines = %d, want %d", r.Summary.TotalLines, 3*linesPerFile)
	}
	if wantMalformed := int64(linesPerFile/97 + linesPerFile/31); r.Summary.MalformedLines != wantMalformed {
		t.Errorf("MalformedLines = %d, want %d", r.Summary.MalformedLines, wantMalformed)
	}

	configs := []Config{
		{Workers: 2, Readers: 1},
		{Workers: 4, Readers: 3},
		{Workers: 8, Readers: 2, Reader: reader.Options{BatchSize: 7}},
		{Workers: 16, Readers: 3, Reader: reader.Options{BatchSize: 1}, QueueSize: 1},
	}
	for _, cfg := range configs {
		cfg.Analysis = analysisOpts
		name := fmt.Sprintf("workers=%d,readers=%d,batch=%d", cfg.Workers, cfg.Readers, cfg.Reader.BatchSize)
		t.Run(name, func(t *testing.T) {
			agg, err := Run(context.Background(), cfg, memSources(contents))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := reportJSON(t, agg); got != want {
				t.Errorf("report differs from single-worker baseline")
			}
		})
	}
}

func TestRunMalformedInput(t *testing.T) {
	input := strings.Join([]string{
		"2024-01-15T10:00:00Z INFO ok",
		"",
		"   ",
		"total garbage",
		"2024-99-99T10:00:00Z ERROR bad date",
		"2024-01-15T10:00:01Z ERROR",
		"2024-01-15T10:00:02Z ERROR disk full on /dev/sda1",
		strings.Repeat("Z", 200),
		"\x00\xff binary",
		"2024-01-15T10:00:03Z FATAL kernel panic",
	}, "\n")

	cfg := Config{Workers: 3, Reader: reader.Options{MaxLineBytes: 100}}
	agg, err := Run(context.Background(), cfg, []Source{ReaderSource("mixed.log", strings.NewReader(input))})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := agg.Report(0)

	if r.Summary.TotalLines != 10 || r.Summary.ParsedLines != 3 || r.Summary.BlankLines != 2 || r.Summary.MalformedLines != 5 {
		t.Errorf("summary = %+v", r.Summary)
	}
	wantReasons := map[string]int64{
		parser.ErrUnknownLevel.Error():     2,
		parser.ErrInvalidTimestamp.Error(): 1,
		parser.ErrMissingMessage.Error():   1,
		ReasonLineTooLong:                  1,
	}
	for reason, n := range wantReasons {
		if r.Malformed.Reasons[reason] != n {
			t.Errorf("reason %q count = %d, want %d (all: %v)", reason, r.Malformed.Reasons[reason], n, r.Malformed.Reasons)
		}
	}
	if len(r.Malformed.Samples) != 5 || r.Malformed.Samples[0].Line != 4 {
		t.Errorf("samples = %+v", r.Malformed.Samples)
	}
	if r.Levels["ERROR"] != 1 || r.Levels["FATAL"] != 1 || r.Levels["INFO"] != 1 {
		t.Errorf("levels = %v", r.Levels)
	}
}

// endless is an infinite stream of valid log lines.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	const line = "2024-01-15T10:30:00Z ERROR connection reset by peer\n"
	n := 0
	for n+len(line) <= len(p) {
		n += copy(p[n:], line)
	}
	return n, nil
}

func TestRunCancellation(t *testing.T) {
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	sources := []Source{
		ReaderSource("endless-1", endless{}),
		ReaderSource("endless-2", endless{}),
		ReaderSource("never-started", strings.NewReader("INFO x\n")),
	}
	done := make(chan struct{})
	var (
		agg *analysis.Aggregator
		err error
	)
	go func() {
		defer close(done)
		agg, err = Run(ctx, Config{Workers: 4, Readers: 2}, sources)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the context deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if agg == nil || agg.Report(0).Summary.ErrorCount == 0 {
		t.Fatal("expected a partial aggregate with some processed lines")
	}

	// All pipeline goroutines must have exited. Allow a short grace period
	// for the runtime to reap them.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutine leak: %d before, %d after", before, after)
	}
}

func TestRunOpenError(t *testing.T) {
	errDenied := errors.New("permission denied")
	sources := []Source{
		ReaderSource("ok.log", strings.NewReader("INFO fine\n")),
		{Name: "secret.log", Open: func() (io.ReadCloser, error) { return nil, errDenied }},
	}
	_, err := Run(context.Background(), Config{Workers: 2}, sources)
	if !errors.Is(err, errDenied) || !strings.Contains(err.Error(), "secret.log") {
		t.Fatalf("error = %v, want wrapped %v mentioning secret.log", err, errDenied)
	}
}

func TestRunReadError(t *testing.T) {
	errIO := errors.New("input/output error")
	broken := io.MultiReader(strings.NewReader("INFO one\nINFO two\n"), iotest.ErrReader(errIO))
	_, err := Run(context.Background(), Config{Workers: 2}, []Source{ReaderSource("broken.log", broken)})
	if !errors.Is(err, errIO) {
		t.Fatalf("error = %v, want wrapped %v", err, errIO)
	}
}

func TestRunInvalidInput(t *testing.T) {
	if _, err := Run(context.Background(), Config{}, nil); err == nil {
		t.Error("expected error for no sources")
	}
	src := []Source{ReaderSource("x", strings.NewReader(""))}
	if _, err := Run(context.Background(), Config{Workers: -1}, src); err == nil {
		t.Error("expected error for negative workers")
	}
}

func TestRunReadsFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	data := "2024-01-15T10:00:00Z ERROR db timeout after 30s\n2024-01-15T10:00:05Z ERROR db timeout after 45s\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	sources, err := FileSources([]string{path})
	if err != nil {
		t.Fatalf("FileSources: %v", err)
	}
	agg, err := Run(context.Background(), Config{Workers: 2}, sources)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	top := agg.Report(1).TopPatterns
	if len(top) != 1 || top[0].Key != "db timeout after <n>s" || top[0].Count != 2 {
		t.Errorf("top patterns = %+v", top)
	}
}

func TestFileSources(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ok.log")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := FileSources(nil); err == nil {
		t.Error("expected error for empty path list")
	}
	if _, err := FileSources([]string{filepath.Join(dir, "missing.log")}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file error = %v, want os.ErrNotExist", err)
	}
	if _, err := FileSources([]string{dir}); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("directory error = %v", err)
	}
	if _, err := FileSources([]string{file, file}); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("duplicate error = %v", err)
	}
	srcs, err := FileSources([]string{file, "-"})
	if err != nil || len(srcs) != 2 || srcs[1].Name != StdinName {
		t.Errorf("FileSources(file, -) = %+v, %v", srcs, err)
	}
}

// BenchmarkRun measures end-to-end throughput for different worker counts
// on four generated files read from disk (served from the page cache after
// the first iteration).
func BenchmarkRun(b *testing.B) {
	const files, linesPerFile = 4, 100_000
	dir := b.TempDir()
	var paths []string
	var totalBytes int64
	for i := range files {
		data := generate(b, loggen.Options{Lines: linesPerFile, Seed: uint64(i + 1), MalformedEvery: 1000})
		p := filepath.Join(dir, fmt.Sprintf("bench-%d.log", i))
		if err := os.WriteFile(p, data, 0o600); err != nil {
			b.Fatal(err)
		}
		paths = append(paths, p)
		totalBytes += int64(len(data))
	}
	sources, err := FileSources(paths)
	if err != nil {
		b.Fatal(err)
	}

	for _, workers := range []int{1, 2, 4, 8, 16} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			cfg := Config{Workers: workers, Readers: files, Analysis: analysis.Options{TimelineBucket: time.Minute}}
			b.SetBytes(totalBytes)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Run(context.Background(), cfg, sources); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(files*linesPerFile)*float64(b.N)/b.Elapsed().Seconds(), "lines/s")
		})
	}
}
