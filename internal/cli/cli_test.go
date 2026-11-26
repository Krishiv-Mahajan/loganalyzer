package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loganalyzer/internal/analysis"
)

var (
	appLog    = filepath.Join("..", "..", "testdata", "app.log")
	workerLog = filepath.Join("..", "..", "testdata", "worker.log")
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runCtx(t, context.Background(), args...)
}

func runCtx(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Execute(ctx, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, s)
	}
	return v
}

func TestAnalyzeJSON(t *testing.T) {
	code, stdout, stderr := run(t, "analyze", "--json", "--workers", "3", appLog, workerLog)
	if code != ExitOK {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	r := decode[analysis.Report](t, stdout)

	s := r.Summary
	if s.TotalLines != 29 || s.ParsedLines != 24 || s.BlankLines != 1 || s.MalformedLines != 4 {
		t.Errorf("summary totals = %+v", s)
	}
	if s.ErrorCount != 12 || s.DistinctPatterns != 5 {
		t.Errorf("errors=%d distinct patterns=%d, want 12 and 5", s.ErrorCount, s.DistinctPatterns)
	}
	wantLevels := map[string]int64{"DEBUG": 1, "INFO": 8, "WARN": 3, "ERROR": 10, "FATAL": 2}
	for level, n := range wantLevels {
		if r.Levels[level] != n {
			t.Errorf("level %s = %d, want %d", level, r.Levels[level], n)
		}
	}

	top := r.TopPatterns[0]
	if top.Key != "database connection to <ip>:<n> failed: connection refused" || top.Count != 5 {
		t.Errorf("top pattern = %q x%d", top.Key, top.Count)
	}
	if top.Example.Source != appLog || top.Example.Line != 6 {
		t.Errorf("top pattern example = %+v, want %s:6", top.Example, appLog)
	}
	if r.TopMessages[0].Key != "database connection to 10.0.0.5:5432 failed: connection refused" || r.TopMessages[0].Count != 3 {
		t.Errorf("top message = %+v", r.TopMessages[0])
	}
	if len(r.Sources) != 2 {
		t.Errorf("sources = %+v", r.Sources)
	}
	if r.Timeline == nil || len(r.Timeline.Buckets) == 0 {
		t.Errorf("expected an error timeline, got %+v", r.Timeline)
	}
	// The default log level is warn, so the malformed-line warning is the
	// only diagnostic and it goes to stderr, keeping stdout valid JSON.
	if !strings.Contains(stderr, "skipped malformed lines") {
		t.Errorf("stderr missing malformed warning: %q", stderr)
	}
}

func TestAnalyzeText(t *testing.T) {
	code, stdout, stderr := run(t, "analyze", "--top", "2", appLog)
	if code != ExitOK {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	for _, want := range []string{
		"Summary",
		"Total lines:  20",
		"Top error patterns (showing 2 of 4)",
		"database connection to <ip>:<n> failed: connection refused",
		"Malformed lines: 3",
		"invalid timestamp",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q\n%s", want, stdout)
		}
	}
}

func TestStats(t *testing.T) {
	code, stdout, stderr := run(t, "stats", "--json", "--bucket", "1m", "-f", appLog, "-f", workerLog)
	if code != ExitOK {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	out := decode[statsOutput](t, stdout)
	if out.Summary.TotalLines != 29 || out.Levels["ERROR"] != 10 {
		t.Errorf("stats summary = %+v levels = %v", out.Summary, out.Levels)
	}
	if out.Timeline == nil || out.Timeline.Bucket != "1m0s" {
		t.Fatalf("timeline = %+v", out.Timeline)
	}
	var timelineErrors int64
	for _, b := range out.Timeline.Buckets {
		timelineErrors += b.Errors
	}
	if timelineErrors != out.Summary.ErrorCount {
		t.Errorf("timeline sums to %d errors, want %d", timelineErrors, out.Summary.ErrorCount)
	}
	if out.MalformedReasons["unrecognized log level"] != 2 {
		t.Errorf("malformed reasons = %v", out.MalformedReasons)
	}
	if strings.Contains(stdout, "top_patterns") {
		t.Error("stats JSON should not include error groups")
	}

	code, stdout, _ = run(t, "stats", appLog, workerLog)
	if code != ExitOK {
		t.Fatalf("text stats exit code %d", code)
	}
	for _, want := range []string{"Log levels", "Files", "Errors per 1m0s", appLog} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stats text missing %q\n%s", want, stdout)
		}
	}
}

func TestTopErrors(t *testing.T) {
	code, stdout, stderr := run(t, "top-errors", "--json", "--by", "message", "-n", "1", appLog, workerLog)
	if code != ExitOK {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	out := decode[topErrorsOutput](t, stdout)
	if out.GroupBy != "message" || out.TotalErrors != 12 || len(out.Errors) != 1 {
		t.Fatalf("top-errors output = %+v", out)
	}
	if out.Errors[0].Count != 3 {
		t.Errorf("top message count = %d, want 3", out.Errors[0].Count)
	}

	code, stdout, _ = run(t, "top-errors", appLog)
	if code != ExitOK {
		t.Fatalf("text top-errors exit code %d", code)
	}
	for _, want := range []string{"Total errors (ERROR+FATAL): 8", "payment <n> declined for user <n>", "Examples", "app.log:9"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("top-errors text missing %q\n%s", want, stdout)
		}
	}
}

func TestFileFlagAndArgsCombined(t *testing.T) {
	code, stdout, stderr := run(t, "stats", "--json", "-f", appLog, workerLog)
	if code != ExitOK {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	out := decode[statsOutput](t, stdout)
	if len(out.Sources) != 2 || out.Summary.TotalLines != 29 {
		t.Errorf("sources = %+v, total = %d", out.Sources, out.Summary.TotalLines)
	}

	code, _, stderr = run(t, "stats", "-f", appLog, appLog)
	if code != ExitError || !strings.Contains(stderr, "more than once") {
		t.Errorf("duplicate input: code=%d stderr=%q", code, stderr)
	}
}

func TestStdinInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; r.Close() })

	go func() {
		w.WriteString("ERROR disk full\nINFO ok\nERROR disk full\n")
		w.Close()
	}()

	code, stdout, stderr := run(t, "top-errors", "--json", "-")
	if code != ExitOK {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	out := decode[topErrorsOutput](t, stdout)
	if out.TotalErrors != 2 || out.Errors[0].Example.Source != "<stdin>" {
		t.Errorf("stdin result = %+v", out)
	}
}

func TestDebugLogsGoToStderr(t *testing.T) {
	code, stdout, stderr := run(t, "stats", "--json", "--log-level", "debug", "--log-format", "json", appLog)
	if code != ExitOK {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	decode[statsOutput](t, stdout)
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("stderr line is not JSON: %q", line)
		}
	}
	for _, msg := range []string{`"msg":"reading source"`, `"msg":"processing finished"`, `"msg":"analysis complete"`} {
		if !strings.Contains(stderr, msg) {
			t.Errorf("stderr missing %s:\n%s", msg, stderr)
		}
	}
}

func TestCLIErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no input", []string{"analyze"}, "no input files"},
		{"missing file", []string{"stats", "does-not-exist.log"}, "does-not-exist.log"},
		{"directory input", []string{"stats", "."}, "is a directory"},
		{"zero workers", []string{"stats", "--workers", "0", appLog}, "--workers must be at least 1"},
		{"negative top", []string{"top-errors", "--top", "-1", appLog}, "--top must not be negative"},
		{"bad group by", []string{"top-errors", "--by", "level", appLog}, "--by must be"},
		{"bad log level", []string{"stats", "--log-level", "loud", appLog}, "invalid --log-level"},
		{"bad log format", []string{"stats", "--log-format", "xml", appLog}, "invalid --log-format"},
		{"negative bucket", []string{"stats", "--bucket", "-1m", appLog}, "--bucket must not be negative"},
		{"unknown flag", []string{"stats", "--nope", appLog}, "unknown flag"},
		{"unknown command", []string{"frobnicate"}, "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tt.args...)
			if code != ExitError {
				t.Errorf("exit code = %d, want %d", code, ExitError)
			}
			if !strings.Contains(stderr, tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.wantErr)
			}
			if stdout != "" {
				t.Errorf("stdout should be empty on error, got %q", stdout)
			}
		})
	}
}

func TestInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, stdout, stderr := runCtx(t, ctx, "analyze", appLog)
	if code != ExitInterrupted {
		t.Fatalf("exit code = %d, want %d (stderr %q)", code, ExitInterrupted, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "interrupted") {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestVersion(t *testing.T) {
	code, stdout, _ := run(t, "--version")
	if code != ExitOK || !strings.Contains(stdout, Version) {
		t.Errorf("--version: code=%d stdout=%q", code, stdout)
	}
}
