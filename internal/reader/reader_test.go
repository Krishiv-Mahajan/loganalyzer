package reader

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// collect runs Stream on input and returns every batch it produced.
func collect(t *testing.T, input io.Reader, opts Options) ([]Batch, int64, error) {
	t.Helper()
	out := make(chan Batch)
	var batches []Batch
	done := make(chan struct{})
	go func() {
		defer close(done)
		for b := range out {
			batches = append(batches, b)
		}
	}()
	n, err := Stream(context.Background(), "test.log", input, opts, out)
	close(out)
	<-done
	return batches, n, err
}

func flatten(batches []Batch) []Line {
	var lines []Line
	for _, b := range batches {
		lines = append(lines, b.Lines...)
	}
	return lines
}

func TestStreamLines(t *testing.T) {
	input := "first\r\nsecond\n\nfourth without newline"
	batches, n, err := collect(t, strings.NewReader(input), Options{BatchSize: 2})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if n != 4 {
		t.Fatalf("line count = %d, want 4", n)
	}

	want := []Line{
		{Number: 1, Text: "first"},
		{Number: 2, Text: "second"},
		{Number: 3, Text: ""},
		{Number: 4, Text: "fourth without newline"},
	}
	got := flatten(batches)
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2", len(batches))
	}
	for _, b := range batches {
		if b.Source != "test.log" {
			t.Errorf("batch source = %q, want test.log", b.Source)
		}
	}
}

func TestStreamPartialFinalBatch(t *testing.T) {
	input := strings.Repeat("x\n", 5)
	batches, n, err := collect(t, strings.NewReader(input), Options{BatchSize: 2})
	if err != nil || n != 5 {
		t.Fatalf("Stream = %d, %v; want 5, nil", n, err)
	}
	sizes := []int{}
	for _, b := range batches {
		sizes = append(sizes, len(b.Lines))
	}
	if len(sizes) != 3 || sizes[0] != 2 || sizes[1] != 2 || sizes[2] != 1 {
		t.Errorf("batch sizes = %v, want [2 2 1]", sizes)
	}
}

func TestStreamEmptyInput(t *testing.T) {
	batches, n, err := collect(t, strings.NewReader(""), Options{})
	if err != nil || n != 0 || len(batches) != 0 {
		t.Fatalf("Stream on empty input = %d lines, %d batches, %v", n, len(batches), err)
	}
}

func TestStreamLongLines(t *testing.T) {
	const maxLen = 32
	exact := strings.Repeat("a", maxLen)
	tooLong := strings.Repeat("b", maxLen+1)
	huge := strings.Repeat("c", maxLen*10)
	input := exact + "\r\n" + tooLong + "\n" + huge + "\nafter\n" + huge

	lines := func() []Line {
		batches, n, err := collect(t, strings.NewReader(input), Options{MaxLineBytes: maxLen})
		if err != nil || n != 5 {
			t.Fatalf("Stream = %d, %v; want 5, nil", n, err)
		}
		return flatten(batches)
	}()

	want := []Line{
		{Number: 1, Text: exact},
		{Number: 2, Text: tooLong[:maxLen], Truncated: true},
		{Number: 3, Text: huge[:maxLen], Truncated: true},
		{Number: 4, Text: "after"},
		{Number: 5, Text: huge[:maxLen], Truncated: true},
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i+1, lines[i], want[i])
		}
	}
}

func TestStreamReadError(t *testing.T) {
	errBoom := errors.New("disk on fire")
	input := io.MultiReader(strings.NewReader("ok\n"), iotest.ErrReader(errBoom))
	_, _, err := collect(t, input, Options{})
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want wrapped %v", err, errBoom)
	}
	if !strings.Contains(err.Error(), "test.log") {
		t.Errorf("error %q does not mention the source", err)
	}
}

func TestStreamSmallReads(t *testing.T) {
	// OneByteReader forces bufio to assemble lines across many short reads.
	input := iotest.OneByteReader(strings.NewReader("alpha\nbeta\n"))
	batches, n, err := collect(t, input, Options{})
	if err != nil || n != 2 {
		t.Fatalf("Stream = %d, %v", n, err)
	}
	got := flatten(batches)
	if got[0].Text != "alpha" || got[1].Text != "beta" {
		t.Errorf("lines = %+v", got)
	}
}

// endless produces an infinite stream of log lines.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	const line = "2024-01-15T10:30:00Z INFO tick\n"
	n := 0
	for n+len(line) <= len(p) {
		n += copy(p[n:], line)
	}
	if n == 0 {
		n = copy(p, line)
	}
	return n, nil
}

func TestStreamCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan Batch) // nobody reads after the first batch

	errc := make(chan error, 1)
	go func() {
		_, err := Stream(ctx, "endless", endless{}, Options{BatchSize: 8}, out)
		errc <- err
	}()

	<-out // the stream is running
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stream did not stop after cancellation")
	}
}
