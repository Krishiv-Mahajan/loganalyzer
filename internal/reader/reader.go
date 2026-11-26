// Package reader streams lines from an io.Reader in fixed-size batches
// without loading the whole input into memory.
//
// Memory use per stream is bounded by the read buffer (MaxLineBytes) plus one
// batch of lines. Lines longer than MaxLineBytes are truncated and flagged
// rather than aborting the stream, so a single pathological line cannot stop
// the analysis of a multi-gigabyte file.
package reader

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
)

// Default option values.
const (
	DefaultBatchSize    = 512
	DefaultMaxLineBytes = 1 << 20 // 1 MiB
)

// Line is a single line of input without its line terminator.
type Line struct {
	// Number is the 1-based line number within the source.
	Number int64
	Text   string
	// Truncated is set when the line exceeded Options.MaxLineBytes; Text then
	// holds only the first MaxLineBytes bytes.
	Truncated bool
}

// Batch is a group of consecutive lines from one source. Sending lines in
// batches amortises channel synchronisation cost across many lines.
type Batch struct {
	Source string
	Lines  []Line
}

// Options configures Stream. Zero values select the defaults.
type Options struct {
	BatchSize    int
	MaxLineBytes int
}

func (o Options) withDefaults() Options {
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.MaxLineBytes <= 0 {
		o.MaxLineBytes = DefaultMaxLineBytes
	}
	return o
}

// Stream reads r line by line and sends the lines to out in batches tagged
// with source. It returns the number of lines read.
//
// Stream returns ctx.Err() if the context is cancelled before the input is
// exhausted, and a wrapped error if reading fails. Lines that were read before
// an error may or may not have been delivered. Stream never closes out.
func Stream(ctx context.Context, source string, r io.Reader, opts Options, out chan<- Batch) (int64, error) {
	opts = opts.withDefaults()
	// Two extra bytes leave room for a "\r\n" terminator so that a line of
	// exactly MaxLineBytes is not reported as truncated.
	br := bufio.NewReaderSize(r, opts.MaxLineBytes+2)

	var count int64
	batch := make([]Line, 0, opts.BatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case out <- Batch{Source: source, Lines: batch}:
		case <-ctx.Done():
			return ctx.Err()
		}
		batch = make([]Line, 0, opts.BatchSize)
		return nil
	}

	for {
		raw, err := br.ReadSlice('\n')
		line := Line{Number: count + 1}

		switch {
		case err == nil:
			line.Text = string(trimEOL(raw))
		case errors.Is(err, bufio.ErrBufferFull):
			// raw is only valid until the next read, so copy the prefix
			// before discarding the remainder of the line.
			line.Text = string(raw[:min(len(raw), opts.MaxLineBytes)])
			line.Truncated = true
			err = skipLine(br)
		case errors.Is(err, io.EOF):
			if len(raw) == 0 {
				return count, flush()
			}
			line.Text = string(trimEOL(raw))
		default:
			return count, fmt.Errorf("read %s: %w", source, err)
		}

		if len(line.Text) > opts.MaxLineBytes {
			line.Text = line.Text[:opts.MaxLineBytes]
			line.Truncated = true
		}

		count++
		batch = append(batch, line)
		if len(batch) == cap(batch) {
			if ferr := flush(); ferr != nil {
				return count, ferr
			}
		}

		switch {
		case err == nil:
		case errors.Is(err, io.EOF):
			return count, flush()
		default:
			return count, fmt.Errorf("read %s: %w", source, err)
		}
	}
}

// skipLine discards input up to and including the next '\n'. It returns nil,
// io.EOF, or the underlying read error.
func skipLine(br *bufio.Reader) error {
	for {
		_, err := br.ReadSlice('\n')
		if !errors.Is(err, bufio.ErrBufferFull) {
			return err
		}
	}
}

func trimEOL(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}
