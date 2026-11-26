package processor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// StdinName is the source name used for standard input.
const StdinName = "<stdin>"

// Source is a named input that can be opened for streaming.
type Source struct {
	Name string
	Open func() (io.ReadCloser, error)
}

// ReaderSource wraps an already-open reader as a Source. The reader is not
// closed by the pipeline.
func ReaderSource(name string, r io.Reader) Source {
	return Source{
		Name: name,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(r), nil },
	}
}

// FileSources validates paths and converts them into Sources. The path "-"
// denotes standard input. Validation happens up front so that a typo in the
// last of many paths is reported before any work is done.
func FileSources(paths []string) ([]Source, error) {
	if len(paths) == 0 {
		return nil, errors.New("no input files given")
	}

	sources := make([]Source, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	var errs []error
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			errs = append(errs, errors.New("empty input path"))
			continue
		}
		if seen[p] {
			errs = append(errs, fmt.Errorf("%s: given more than once", p))
			continue
		}
		seen[p] = true

		if p == "-" {
			sources = append(sources, ReaderSource(StdinName, os.Stdin))
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if info.IsDir() {
			errs = append(errs, fmt.Errorf("%s: is a directory", p))
			continue
		}
		path := p
		sources = append(sources, Source{
			Name: path,
			Open: func() (io.ReadCloser, error) { return os.Open(path) },
		})
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return sources, nil
}
