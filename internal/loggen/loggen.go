// Package loggen generates deterministic synthetic log files for tests and
// benchmarks.
package loggen

import (
	"bufio"
	"fmt"
	"io"
	"math/rand/v2"
	"time"
)

// Options configures Write.
type Options struct {
	Lines int
	Seed  uint64
	// MalformedEvery inserts a malformed line every N lines; zero disables.
	MalformedEvery int
	// Start is the timestamp of the first line; zero means 2024-01-15 UTC.
	Start time.Time
}

var (
	infoMessages = []string{
		"request completed path=/api/users/%d status=200 duration=%dms",
		"user %d logged in",
		"cache refreshed with %d entries",
	}
	warnMessages = []string{
		"slow query took %dms on table orders",
		"retrying request to 10.0.%d.%d after timeout",
	}
	errorMessages = []string{
		"database connection to 10.0.%d.%d:5432 failed",
		"payment %d declined for user %d",
		"timeout after %dms calling inventory service",
		"failed to parse config file /etc/app/%d.yaml: line %d",
	}
	fatalMessages = []string{
		"out of memory: allocated %d MB of %d MB",
		"unrecoverable error in worker %d: exit code %d",
	}
	malformedLines = []string{
		"this is not a log line",
		"2024-13-45 99:99:99 ERROR impossible timestamp",
		"2024-01-15T10:30:00Z NOTICE unknown level",
		"2024-01-15T10:30:00Z ERROR",
	}
)

// Write writes opts.Lines lines to w. The output is fully determined by
// opts, so the same options always produce byte-identical output.
func Write(w io.Writer, opts Options) error {
	rng := rand.New(rand.NewPCG(opts.Seed, opts.Seed^0x9e3779b97f4a7c15))
	ts := opts.Start
	if ts.IsZero() {
		ts = time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	}

	bw := bufio.NewWriter(w)
	for i := 1; i <= opts.Lines; i++ {
		ts = ts.Add(time.Duration(rng.IntN(500)) * time.Millisecond)
		if opts.MalformedEvery > 0 && i%opts.MalformedEvery == 0 {
			if _, err := fmt.Fprintln(bw, malformedLines[rng.IntN(len(malformedLines))]); err != nil {
				return err
			}
			continue
		}

		var level string
		var templates []string
		switch p := rng.IntN(100); {
		case p < 70:
			level, templates = "INFO", infoMessages
		case p < 85:
			level, templates = "WARN", warnMessages
		case p < 98:
			level, templates = "ERROR", errorMessages
		default:
			level, templates = "FATAL", fatalMessages
		}
		msg := fmt.Sprintf(templates[rng.IntN(len(templates))], rng.IntN(1000), rng.IntN(1000))
		if _, err := fmt.Fprintf(bw, "%s %s %s\n", ts.Format(time.RFC3339Nano), level, msg); err != nil {
			return err
		}
	}
	return bw.Flush()
}
