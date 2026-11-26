package parser

import (
	"errors"
	"testing"
	"time"
)

func mustTime(t *testing.T, layout, value string) time.Time {
	t.Helper()
	ts, err := time.Parse(layout, value)
	if err != nil {
		t.Fatalf("bad test timestamp %q: %v", value, err)
	}
	return ts
}

func TestParseValid(t *testing.T) {
	base := mustTime(t, time.RFC3339, "2024-01-15T10:30:00Z")
	withMillis := base.Add(123 * time.Millisecond)

	tests := []struct {
		name string
		line string
		want Entry
	}{
		{
			name: "rfc3339 utc",
			line: "2024-01-15T10:30:00Z ERROR database connection failed",
			want: Entry{Timestamp: base, Level: LevelError, Message: "database connection failed"},
		},
		{
			name: "rfc3339 with fraction and offset",
			line: "2024-01-15T16:00:00.123+05:30 WARN disk usage high",
			want: Entry{Timestamp: withMillis, Level: LevelWarn, Message: "disk usage high"},
		},
		{
			name: "iso without offset is utc",
			line: "2024-01-15T10:30:00 INFO started",
			want: Entry{Timestamp: base, Level: LevelInfo, Message: "started"},
		},
		{
			name: "date and time",
			line: "2024-01-15 10:30:00 INFO server started on port 8080",
			want: Entry{Timestamp: base, Level: LevelInfo, Message: "server started on port 8080"},
		},
		{
			name: "date and time with comma millis",
			line: "2024-01-15 10:30:00,123 FATAL out of memory",
			want: Entry{Timestamp: withMillis, Level: LevelFatal, Message: "out of memory"},
		},
		{
			name: "date and time with dot millis",
			line: "2024-01-15 10:30:00.123 ERROR boom",
			want: Entry{Timestamp: withMillis, Level: LevelError, Message: "boom"},
		},
		{
			name: "bracketed date time and level",
			line: "[2024-01-15 10:30:00] [ERROR] request failed",
			want: Entry{Timestamp: base, Level: LevelError, Message: "request failed"},
		},
		{
			name: "bracketed rfc3339",
			line: "[2024-01-15T10:30:00Z] WARN slow query",
			want: Entry{Timestamp: base, Level: LevelWarn, Message: "slow query"},
		},
		{
			name: "no timestamp",
			line: "ERROR something broke",
			want: Entry{Level: LevelError, Message: "something broke"},
		},
		{
			name: "lowercase level with colon",
			line: "2024-01-15T10:30:00Z error: lowercase works",
			want: Entry{Timestamp: base, Level: LevelError, Message: "lowercase works"},
		},
		{
			name: "warning alias",
			line: "WARNING deprecated flag used",
			want: Entry{Level: LevelWarn, Message: "deprecated flag used"},
		},
		{
			name: "err alias",
			line: "ERR short alias",
			want: Entry{Level: LevelError, Message: "short alias"},
		},
		{
			name: "debug level",
			line: "DEBUG cache miss",
			want: Entry{Level: LevelDebug, Message: "cache miss"},
		},
		{
			name: "tabs and surrounding whitespace",
			line: "  2024-01-15T10:30:00Z\tINFO\t  tabbed message  \r",
			want: Entry{Timestamp: base, Level: LevelInfo, Message: "tabbed message"},
		},
		{
			name: "message keeps inner spacing",
			line: "INFO a  b   c",
			want: Entry{Level: LevelInfo, Message: "a  b   c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.line)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tt.line, err)
			}
			if !got.Timestamp.Equal(tt.want.Timestamp) {
				t.Errorf("Timestamp = %v, want %v", got.Timestamp, tt.want.Timestamp)
			}
			if got.Level != tt.want.Level {
				t.Errorf("Level = %v, want %v", got.Level, tt.want.Level)
			}
			if got.Message != tt.want.Message {
				t.Errorf("Message = %q, want %q", got.Message, tt.want.Message)
			}
		})
	}
}

func TestParseMalformed(t *testing.T) {
	tests := []struct {
		name string
		line string
		want error
	}{
		{"empty", "", ErrEmptyLine},
		{"whitespace only", " \t \r", ErrEmptyLine},
		{"unknown level", "2024-01-15T10:30:00Z NOTICE hello", ErrUnknownLevel},
		{"free text", "this is not a log line", ErrUnknownLevel},
		{"timestamp only", "2024-01-15T10:30:00Z", ErrMissingLevel},
		{"level without message", "2024-01-15T10:30:00Z ERROR", ErrMissingMessage},
		{"level with blank message", "ERROR    ", ErrMissingMessage},
		{"invalid month", "2024-13-15T10:30:00Z ERROR x", ErrInvalidTimestamp},
		{"invalid date time", "2024-01-15 25:61:00 ERROR x", ErrInvalidTimestamp},
		{"date without time", "2024-01-15 ERROR x", ErrInvalidTimestamp},
		{"date only", "2024-01-15", ErrInvalidTimestamp},
		{"leading number", "404 page not found", ErrInvalidTimestamp},
		{"unclosed bracket", "[2024-01-15 10:30:00 ERROR x", ErrInvalidTimestamp},
		{"double space between date and time", "2024-01-15  10:30:00 ERROR x", ErrInvalidTimestamp},
		{"lone bracket digit", "[1", ErrInvalidTimestamp},
		{"binary garbage", "\x00\x01\xff\xfe", ErrUnknownLevel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.line)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Parse(%q) error = %v, want %v (entry %+v)", tt.line, err, tt.want, got)
			}
			if got != (Entry{}) {
				t.Errorf("Parse(%q) returned non-zero entry on error: %+v", tt.line, got)
			}
		})
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		token string
		want  Level
		ok    bool
	}{
		{"INFO", LevelInfo, true},
		{"info", LevelInfo, true},
		{"[Warn]", LevelWarn, true},
		{"WARNING:", LevelWarn, true},
		{"ERROR", LevelError, true},
		{"[ERR]:", LevelError, true},
		{"fatal", LevelFatal, true},
		{"DEBUG", LevelDebug, true},
		{"TRACE", LevelUnknown, false},
		{"[]", LevelUnknown, false},
		{"", LevelUnknown, false},
	}
	for _, tt := range tests {
		got, ok := ParseLevel(tt.token)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, %v", tt.token, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLevelString(t *testing.T) {
	for _, l := range Levels() {
		parsed, ok := ParseLevel(l.String())
		if !ok || parsed != l {
			t.Errorf("round trip of %v failed: got %v, %v", l, parsed, ok)
		}
	}
	if got := Level(200).String(); got != "Level(200)" {
		t.Errorf("out of range level String() = %q", got)
	}
	if !LevelError.IsError() || !LevelFatal.IsError() || LevelWarn.IsError() {
		t.Error("IsError classification is wrong")
	}
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		"2024-01-15T10:30:00Z ERROR x",
		"[2024-01-15 10:30:00] [WARN] y",
		"INFO z",
		"",
		"[",
		"2024-01-15 ",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		e, err := Parse(line)
		if err == nil && (e.Level == LevelUnknown || e.Message == "") {
			t.Fatalf("Parse(%q) succeeded with invalid entry %+v", line, e)
		}
	})
}

func BenchmarkParse(b *testing.B) {
	lines := []string{
		"2024-01-15T10:30:00.123Z INFO request completed in 12ms path=/api/users",
		"2024-01-15 10:30:00,456 ERROR database connection to 10.0.0.5:5432 failed",
		"[2024-01-15 10:30:00] [WARN] cache eviction rate high",
		"this line is malformed",
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		_, _ = Parse(lines[i%len(lines)])
		i++
	}
}
