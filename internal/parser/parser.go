// Package parser converts raw log lines into structured entries.
//
// A line has the general shape
//
//	[timestamp] LEVEL message
//
// where the timestamp is optional and may be any of:
//
//	2024-01-15T10:30:00Z            (RFC 3339, optional fraction and offset)
//	2024-01-15T10:30:00.123         (ISO 8601 without offset, treated as UTC)
//	2024-01-15 10:30:00             (date and time, optional ".123" or ",123")
//	[2024-01-15 10:30:00]           (any of the above wrapped in brackets)
//
// The level token is case-insensitive and may be wrapped in brackets or
// followed by a colon ("[ERROR]", "error:"). Everything after the level is the
// message. Lines that do not match this shape are reported as malformed via
// one of the sentinel errors below; Parse never panics on arbitrary input.
package parser

import (
	"errors"
	"strings"
	"time"
)

// Sentinel errors describing why a line could not be parsed. Parse returns
// these values unwrapped so callers can compare or group by them cheaply.
var (
	ErrEmptyLine        = errors.New("empty line")
	ErrInvalidTimestamp = errors.New("invalid timestamp")
	ErrMissingLevel     = errors.New("missing log level")
	ErrUnknownLevel     = errors.New("unrecognized log level")
	ErrMissingMessage   = errors.New("missing message")
)

// Entry is a successfully parsed log line.
type Entry struct {
	// Timestamp is the zero time when the line carried no timestamp.
	Timestamp time.Time
	Level     Level
	Message   string
}

const (
	layoutDateTime = "2006-01-02 15:04:05"
	layoutISOLocal = "2006-01-02T15:04:05"
)

// Parse parses a single log line (without its trailing newline).
func Parse(line string) (Entry, error) {
	rest := strings.TrimSpace(line)
	if rest == "" {
		return Entry{}, ErrEmptyLine
	}

	var e Entry
	if startsWithTimestamp(rest) {
		ts, after, err := parseTimestamp(rest)
		if err != nil {
			return Entry{}, err
		}
		e.Timestamp = ts
		rest = after
	}

	token, rest := nextField(rest)
	if token == "" {
		return Entry{}, ErrMissingLevel
	}
	level, ok := ParseLevel(token)
	if !ok {
		return Entry{}, ErrUnknownLevel
	}
	e.Level = level

	e.Message = strings.TrimSpace(rest)
	if e.Message == "" {
		return Entry{}, ErrMissingMessage
	}
	return e, nil
}

// startsWithTimestamp reports whether s looks like it begins with a
// timestamp (a digit, optionally preceded by '['). Anything else is treated
// as a line without a timestamp.
func startsWithTimestamp(s string) bool {
	if s[0] == '[' {
		return len(s) > 1 && isDigit(s[1])
	}
	return isDigit(s[0])
}

// parseTimestamp parses the leading timestamp of s and returns it together
// with the remainder of the line.
func parseTimestamp(s string) (time.Time, string, error) {
	bracketed := s[0] == '['
	if bracketed {
		s = s[1:]
	}

	first, rest := nextField(s)
	if bracketed {
		if trimmed, ok := strings.CutSuffix(first, "]"); ok {
			ts, err := parseISO(trimmed)
			return ts, rest, err
		}
	} else if strings.IndexByte(first, 'T') >= 0 {
		ts, err := parseISO(first)
		return ts, rest, err
	}

	// Two-token "date time" form. Slice the original string instead of
	// concatenating so the common single-space case does not allocate.
	clock, rest := nextField(rest)
	if clock == "" {
		return time.Time{}, "", ErrInvalidTimestamp
	}
	if bracketed {
		var ok bool
		if clock, ok = strings.CutSuffix(clock, "]"); !ok {
			return time.Time{}, "", ErrInvalidTimestamp
		}
	}
	if len(s) < len(first)+1+len(clock) || s[len(first)] != ' ' {
		return time.Time{}, "", ErrInvalidTimestamp
	}
	ts, err := time.ParseInLocation(layoutDateTime, s[:len(first)+1+len(clock)], time.UTC)
	if err != nil {
		return time.Time{}, "", ErrInvalidTimestamp
	}
	return ts, rest, nil
}

// parseISO parses an RFC 3339 timestamp, falling back to ISO 8601 without a
// zone offset (interpreted as UTC).
func parseISO(s string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts, nil
	}
	if ts, err := time.ParseInLocation(layoutISOLocal, s, time.UTC); err == nil {
		return ts, nil
	}
	return time.Time{}, ErrInvalidTimestamp
}

// nextField skips leading blanks and returns the next blank-delimited field
// and everything after it.
func nextField(s string) (field, rest string) {
	i := 0
	for i < len(s) && isBlank(s[i]) {
		i++
	}
	j := i
	for j < len(s) && !isBlank(s[j]) {
		j++
	}
	return s[i:j], s[j:]
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
