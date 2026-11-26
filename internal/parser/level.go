package parser

import (
	"fmt"
	"strings"
)

// Level is the severity of a log entry.
type Level uint8

// Supported log levels, ordered by increasing severity. LevelUnknown is the
// zero value and is never produced for a successfully parsed entry.
const (
	LevelUnknown Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

// NumLevels is the number of Level values, including LevelUnknown. It is
// useful for sizing arrays indexed by Level.
const NumLevels = int(LevelFatal) + 1

var levelNames = [NumLevels]string{
	LevelUnknown: "UNKNOWN",
	LevelDebug:   "DEBUG",
	LevelInfo:    "INFO",
	LevelWarn:    "WARN",
	LevelError:   "ERROR",
	LevelFatal:   "FATAL",
}

// Levels returns all known levels (excluding LevelUnknown) in severity order.
func Levels() []Level {
	return []Level{LevelDebug, LevelInfo, LevelWarn, LevelError, LevelFatal}
}

// String returns the canonical upper-case name of the level.
func (l Level) String() string {
	if int(l) < NumLevels {
		return levelNames[l]
	}
	return fmt.Sprintf("Level(%d)", uint8(l))
}

// IsError reports whether the level represents a failure (ERROR or FATAL).
func (l Level) IsError() bool {
	return l == LevelError || l == LevelFatal
}

// MarshalText implements encoding.TextMarshaler so levels can be used as JSON
// object keys and values.
func (l Level) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

// ParseLevel converts a level token into a Level. Matching is
// case-insensitive and accepts the common aliases WARNING (WARN) and ERR
// (ERROR). Surrounding square brackets and a trailing colon are ignored, so
// "[error]" and "ERROR:" are both accepted.
func ParseLevel(token string) (Level, bool) {
	token = strings.TrimSuffix(token, ":")
	if len(token) >= 2 && token[0] == '[' && token[len(token)-1] == ']' {
		token = token[1 : len(token)-1]
	}
	switch {
	case strings.EqualFold(token, "INFO"):
		return LevelInfo, true
	case strings.EqualFold(token, "WARN"), strings.EqualFold(token, "WARNING"):
		return LevelWarn, true
	case strings.EqualFold(token, "ERROR"), strings.EqualFold(token, "ERR"):
		return LevelError, true
	case strings.EqualFold(token, "FATAL"):
		return LevelFatal, true
	case strings.EqualFold(token, "DEBUG"):
		return LevelDebug, true
	}
	return LevelUnknown, false
}
