// Package processor runs the concurrent analysis pipeline:
//
//	sources ──► reader pool ──► chan reader.Batch ──► worker pool ──► merge
//	            (Readers)        (bounded buffer)      (Workers)
//
// Readers stream each source in batches of lines. Workers parse lines and
// update a private analysis.Aggregator, so the hot path takes no locks. When
// the input is exhausted (or the run is cancelled) the channel is closed,
// workers drain it and exit, and their partial aggregates are merged.
package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"loganalyzer/internal/analysis"
	"loganalyzer/internal/parser"
	"loganalyzer/internal/reader"
)

// ReasonLineTooLong is the malformed-line reason recorded for lines that
// exceeded the reader's maximum line length.
const ReasonLineTooLong = "line too long"

// DefaultReaders is the default number of sources read concurrently.
const DefaultReaders = 4

// Config configures Run. Zero values select defaults.
type Config struct {
	// Workers is the number of parsing goroutines. Default: runtime.NumCPU().
	Workers int
	// Readers is the maximum number of sources read concurrently.
	// Default: DefaultReaders.
	Readers int
	// QueueSize is the capacity, in batches, of the channel between readers
	// and workers. Default: 2 * Workers.
	QueueSize int
	Reader    reader.Options
	Analysis  analysis.Options
	// Logger receives structured diagnostic events. Default: discard.
	Logger *slog.Logger
}

func (c Config) withDefaults() (Config, error) {
	if c.Workers < 0 || c.Readers < 0 || c.QueueSize < 0 {
		return c, fmt.Errorf("invalid config: workers=%d readers=%d queue=%d must not be negative",
			c.Workers, c.Readers, c.QueueSize)
	}
	if c.Workers == 0 {
		c.Workers = runtime.NumCPU()
	}
	if c.Readers == 0 {
		c.Readers = DefaultReaders
	}
	if c.QueueSize == 0 {
		c.QueueSize = 2 * c.Workers
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

// Run analyses all sources and returns the merged aggregate.
//
// If ctx is cancelled, or a source fails to open or read, Run stops reading,
// lets the workers finish the batches already queued, and returns the
// partial aggregate together with the first error encountered. All
// goroutines started by Run have exited when it returns.
func Run(ctx context.Context, cfg Config, sources []Source) (*analysis.Aggregator, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, errors.New("no sources to process")
	}

	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		errOnce  sync.Once
		firstErr error
	)
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	batches := make(chan reader.Batch, cfg.QueueSize)

	partials := make([]*analysis.Aggregator, cfg.Workers)
	var workers sync.WaitGroup
	for i := range partials {
		agg := analysis.New(cfg.Analysis)
		partials[i] = agg
		workers.Go(func() { consume(batches, agg) })
	}

	jobs := make(chan Source)
	var readers sync.WaitGroup
	for range min(cfg.Readers, len(sources)) {
		readers.Go(func() {
			for src := range jobs {
				if err := readSource(ctx, cfg, src, batches); err != nil {
					fail(err)
				}
			}
		})
	}

feed:
	for _, src := range sources {
		select {
		case jobs <- src:
		case <-ctx.Done():
			// Sources remain unread, so the result is partial even if no
			// reader observed the cancellation.
			fail(ctx.Err())
			break feed
		}
	}
	close(jobs)
	readers.Wait()
	close(batches)
	workers.Wait()

	merged := partials[0]
	for _, p := range partials[1:] {
		merged.Merge(p)
	}

	cfg.Logger.Info("processing finished",
		slog.Int("sources", len(sources)),
		slog.Int("workers", cfg.Workers),
		slog.Duration("elapsed", time.Since(start)),
		slog.Any("error", firstErr),
	)
	return merged, firstErr
}

func readSource(ctx context.Context, cfg Config, src Source, out chan<- reader.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	log := cfg.Logger.With(slog.String("source", src.Name))
	start := time.Now()
	log.Debug("reading source")

	rc, err := src.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", src.Name, err)
	}
	defer func() {
		if cerr := rc.Close(); cerr != nil {
			log.Warn("closing source failed", slog.Any("error", cerr))
		}
	}()

	lines, err := reader.Stream(ctx, src.Name, rc, cfg.Reader, out)
	log.Debug("finished source",
		slog.Int64("lines", lines),
		slog.Duration("elapsed", time.Since(start)),
		slog.Any("error", err),
	)
	return err
}

// consume parses every line of every batch into agg until batches is closed.
func consume(batches <-chan reader.Batch, agg *analysis.Aggregator) {
	for b := range batches {
		for _, ln := range b.Lines {
			loc := analysis.Location{Source: b.Source, Line: ln.Number}
			if ln.Truncated {
				agg.AddMalformed(loc, ReasonLineTooLong, ln.Text)
				continue
			}
			e, err := parser.Parse(ln.Text)
			switch {
			case err == nil:
				agg.AddEntry(loc, e)
			case errors.Is(err, parser.ErrEmptyLine):
				agg.AddBlank(b.Source)
			default:
				agg.AddMalformed(loc, err.Error(), ln.Text)
			}
		}
	}
}
