# loganalyzer

A concurrent log analyzer written in Go. It streams one or more log files
through a pool of worker goroutines and reports log-level counts, error
rates and frequencies over time, recurring error messages, and normalized
failure patterns. Malformed lines are counted and sampled instead of
aborting the run.

```
$ loganalyzer top-errors testdata/app.log testdata/worker.log
Total errors (ERROR+FATAL): 12, distinct patterns: 5

#  COUNT  SHARE   FATAL  FIRST SEEN            LAST SEEN             PATTERN
1  5      41.67%  0      2024-01-15T10:00:03Z  2024-01-15T10:01:01Z  database connection to <ip>:<n> failed: connection refused
2  3      25.00%  0      2024-01-15T10:00:05Z  2024-01-15T10:01:06Z  timeout after <n>ms calling inventory service
3  2      16.67%  0      2024-01-15T10:00:30Z  2024-01-15T10:01:15Z  payment <n> declined for user <n>
...
```

## Architecture

```
            ┌──────────────┐   chan reader.Batch    ┌──────────────┐
 files ───► │ reader pool  │ ─────────────────────► │ worker pool  │ ──► partial aggregates ──► Merge ──► Report
 (or "-")   │ (--readers)  │  (bounded, batched)    │ (--workers)  │     (one per worker,
            └──────────────┘                        └──────────────┘      no shared state)
```

| Package               | Responsibility                                                                                     |
| --------------------- | -------------------------------------------------------------------------------------------------- |
| `cmd/loganalyzer`     | Entry point; wires SIGINT/SIGTERM to a context and maps errors to exit codes.                      |
| `internal/cli`        | Cobra commands (`analyze`, `stats`, `top-errors`), flag validation, text and JSON rendering.      |
| `internal/processor`  | The concurrent pipeline: reader pool, worker pool, cancellation, error propagation, merging.      |
| `internal/reader`     | Streams an `io.Reader` line by line in batches, with a hard cap on line length.                   |
| `internal/parser`     | Parses one line into `Entry{Timestamp, Level, Message}` or a sentinel error.                      |
| `internal/analysis`   | Mergeable `Aggregator` (counts, error groups, timeline, malformed samples) and message normalization. |
| `internal/loggen`     | Deterministic synthetic log generator used by tests and benchmarks.                               |

The packages form a strict dependency chain (`cli → processor → reader, parser, analysis`).
`parser`, `reader`, and `analysis` don't depend on each other's internals and can be tested in isolation.

### Data flow

1. **Reading.** `processor.FileSources` checks every path up front (it must exist, must not be a directory, and must not be listed twice), so a typo is reported before any work starts. Up to `--readers` files are then read concurrently. Each reader uses a `bufio.Reader` sized to `--max-line-bytes` and sends `Batch{Source, Lines}` values of `--batch-size` lines into a bounded channel. Memory per reader is one buffer plus one batch, regardless of file size.
2. **Processing.** `--workers` goroutines `range` over the channel. Each one parses its lines and updates its **own** `analysis.Aggregator`. Because nothing is shared, the hot path takes no locks.
3. **Merging.** When all readers finish, the channel is closed. The workers drain it and exit, and their aggregates are merged into one.
4. **Reporting.** `Aggregator.Report(topN)` produces an immutable, JSON-serializable `Report`. Each subcommand renders the parts it needs.

### Graceful shutdown and errors

- Ctrl-C or SIGTERM cancels the context. Readers stop at their next batch send, no new files are started, the channel is closed, and workers finish the batches already queued. `processor.Run` returns only after **every** goroutine it started has exited (verified by `TestRunCancellation`). The CLI then exits with code **130** and writes nothing to stdout, so a partial report is never mistaken for a complete one.
- A failure to open or read any file cancels the whole run and is reported with the file name (exit code **1**).
- Malformed lines are **not** errors. They are counted by reason, and up to 20 samples are kept (the earliest in input order).

## Supported log format

```
[timestamp] LEVEL message
```

| Part      | Accepted forms                                                                                                                  |
| --------- | ------------------------------------------------------------------------------------------------------------------------------- |
| timestamp | optional; `2024-01-15T10:30:00Z`, `2024-01-15T10:30:00.123+05:30`, `2024-01-15T10:30:00` (UTC), `2024-01-15 10:30:00`, `2024-01-15 10:30:00,123`, any of these in `[...]` |
| level     | `DEBUG`, `INFO`, `WARN`/`WARNING`, `ERROR`/`ERR`, `FATAL`; case-insensitive, optionally `[LEVEL]` or `LEVEL:`                   |
| message   | the rest of the line (required)                                                                                                 |

Lines that are empty count as **blank**. Lines with an invalid timestamp, missing or unknown level, missing message, or longer than `--max-line-bytes` count as **malformed**, grouped by reason. All timestamps are reported in UTC.

### Error patterns

ERROR and FATAL messages are grouped two ways:

- **messages**: exact message text.
- **patterns**: the message with variable parts replaced by placeholders, so that recurring failures group together even when IDs differ.

| Replaced                              | Placeholder |
| ------------------------------------- | ----------- |
| `"double quoted"` / `'single'` strings | `<str>`     |
| UUIDs                                 | `<uuid>`    |
| IPv4 addresses                        | `<ip>`      |
| `0x...` literals, mixed hex IDs ≥ 8 chars | `<hex>` |
| any other digit run (`42`, `1.5`)     | `<n>`       |

For example, `connection to 10.0.0.1:5432 timed out after 30s` normalizes to
`connection to <ip>:<n> timed out after <n>s`.

## Installation

Requires Go 1.25 or newer.

```sh
go install ./cmd/loganalyzer          # installs into $(go env GOPATH)/bin
# or
go build -o bin/loganalyzer ./cmd/loganalyzer
```

Set a version at build time with
`-ldflags "-X loganalyzer/internal/cli.Version=v1.0.0"`.

## Usage

```
loganalyzer [command] [file...] [flags]

Commands:
  analyze     Full report: summary, levels, top error patterns and messages, malformed lines
  stats       Log-level counts, error rate, per-file statistics, and error frequency over time
  top-errors  Most frequent ERROR/FATAL patterns (or exact messages)

Global flags:
  -f, --file stringArray     input log file, repeatable ("-" for stdin)
  -w, --workers int          number of parsing worker goroutines (default: number of CPUs)
      --readers int          maximum number of files read concurrently (default 4)
      --batch-size int       lines per batch sent to workers (default 512)
      --max-line-bytes int   lines longer than this are truncated and counted as malformed (default 1 MiB)
      --json                 write results as JSON
      --log-level string     diagnostic log level: debug, info, warn, error (default "warn")
      --log-format string    diagnostic log format: text or json (default "text")
```

Command-specific flags:

| Command      | Flag            | Default   | Meaning                                         |
| ------------ | --------------- | --------- | ----------------------------------------------- |
| `analyze`    | `-n, --top`     | 10        | entries per error list (0 = all)                |
| `analyze`    | `--bucket`      | `1m`      | error timeline bucket width (0 disables)        |
| `stats`      | `--bucket`      | `1m`      | error timeline bucket width (0 disables)        |
| `top-errors` | `-n, --top`     | 10        | entries to show (0 = all)                       |
| `top-errors` | `--by`          | `pattern` | group by `pattern` or exact `message`           |

### Examples

```sh
# Full report over two files with 8 workers
loganalyzer analyze testdata/app.log testdata/worker.log --workers 8

# Same, using --file flags and JSON output
loganalyzer analyze -f testdata/app.log -f testdata/worker.log --json

# Level counts, per-file breakdown, errors per 5 minutes
loganalyzer stats /var/log/app/*.log --bucket 5m

# 20 most frequent exact error messages, as JSON
loganalyzer top-errors app.log --by message --top 20 --json | jq '.errors[].key'

# Read from stdin
kubectl logs deploy/api | loganalyzer top-errors -

# Diagnostic logs (structured, to stderr) never mix with results on stdout
loganalyzer stats app.log --json --log-level debug --log-format json 2>debug.jsonl >stats.json
```

### Output and exit codes

Results go to **stdout**. Structured diagnostics (`log/slog`) go to **stderr**. By default only warnings are shown, such as the count of skipped malformed lines.

| Code | Meaning                                              |
| ---- | ---------------------------------------------------- |
| 0    | success                                              |
| 1    | invalid flags/input, or a file could not be read     |
| 130  | interrupted (SIGINT/SIGTERM); no results were written |

The JSON shapes are defined by `analysis.Report` (for `analyze`), `statsOutput`, and `topErrorsOutput` (in `internal/cli/commands.go`). Ratios (`error_rate`, `share`) are fractions in `[0, 1]`. Every level is always present in `levels`, even if its count is zero.

## Design decisions

- **Per-worker aggregation instead of a shared, mutex-protected map.** Every line updates several counters and maps, so a shared lock would serialize the workers. Giving each worker a private `Aggregator` and merging once at the end (a map-reduce pattern) keeps the hot path lock-free. It also makes the aggregator the unit you would ship between machines if this were extended to multiple nodes.
- **Deterministic output.** Lists are sorted by count with ties broken by key. The "example" for each group is the earliest occurrence by `(file, line)`, not whichever worker finished first. Timestamps are normalized to UTC. As a result, `--workers 1` and `--workers 16` produce byte-identical JSON, and the tests check exactly that.
- **Batched channel sends.** Sending one line per channel operation spends a noticeable share of time on synchronization. Batches of 512 lines amortize that cost, and the bounded channel (2 × workers batches) gives backpressure so fast readers cannot pile up memory.
- **`bufio.Reader.ReadSlice` instead of `bufio.Scanner`.** `Scanner` stops the whole stream with `ErrTooLong` when one line exceeds its buffer. The reader here truncates the overlong line, flags it as malformed, skips the rest of it, and keeps going.
- **Hand-written parser, regex normalizer.** `Parse` runs on every line, so it uses a small hand-written tokenizer and returns sentinel errors without allocating (about 68 ns/line, 0 allocs). Normalization only runs on ERROR/FATAL lines, where readable regular expressions are a reasonable trade-off.
- **Bounded memory on high-cardinality input.** Distinct messages and patterns are capped at 10,000 per aggregator. Errors beyond the cap are still counted in totals and reported as `untracked_*_errors`. Malformed samples are capped at 20, with text truncated to 256 bytes.
- **Validate inputs before processing.** All paths are checked before any goroutine starts, so a missing tenth file fails in milliseconds rather than after nine files have been processed.
- **Minimal dependencies.** Only Cobra (plus its `pflag` dependency) is used. Logging uses the standard library's `log/slog`, and tests use only the standard library.

## Testing

```sh
go test ./...                 # unit + integration tests
go test -race ./...           # same, with the race detector
go test -run '^$' -fuzz FuzzParse -fuzztime 30s ./internal/parser
```

What is covered:

- **parser**: every supported timestamp/level form, every malformed category, level aliases, and a fuzz target asserting that `Parse` never panics and never returns an invalid entry.
- **reader**: CRLF, missing final newline, blank lines, batch boundaries, overlong lines (exactly at, one over, and far over the limit), one-byte reads, read errors, and cancellation while blocked on a full channel.
- **analysis**: counts, rates, ordering and tie-breaking, `topN`, timeline bucketing, UTC normalization, sample bounding, the group cap, and **merge equivalence** (splitting input across 2, 3, or 7 aggregators gives the same report as one).
- **processor**: identical reports for 1–16 workers, 1–3 readers, batch sizes 1–512, and queue size 1 (run under `-race`); mixed malformed input; cancellation with a goroutine-leak check; open and read failures; path validation.
- **cli**: end-to-end runs of all three commands in text and JSON against `testdata/`, `--file` plus positional inputs, stdin, logs kept on stderr, flag validation errors, interruption (exit 130), and `--version`.

## Benchmarks

```sh
go test -run '^$' -bench . -benchmem ./...
```

`BenchmarkRun` processes 4 generated files × 100,000 lines (≈ 32 MB, ~15% ERROR/FATAL, 0.1% malformed) end to end, with files served from the OS page cache. Results on an Apple M4 (4 performance + 6 efficiency cores), Go 1.26.5:

| Workers | Time/op | Throughput | Lines/s   | Speed-up |
| ------- | ------- | ---------- | --------- | -------- |
| 1       | 282 ms  | 113 MB/s   | 1.42 M    | 1.0×     |
| 2       | 149 ms  | 215 MB/s   | 2.69 M    | 1.9×     |
| 4       | 81 ms   | 397 MB/s   | 4.96 M    | 3.5×     |
| 8       | 72 ms   | 442 MB/s   | 5.54 M    | 3.9×     |
| 16      | 64 ms   | 496 MB/s   | 6.21 M    | 4.4×     |

Scaling is close to linear up to the 4 performance cores. After that the gains come from the slower efficiency cores and level off. Using more workers than CPUs gives little benefit.

Micro-benchmarks:

| Benchmark           | Time/op  | Allocs/op |
| ------------------- | -------- | --------- |
| `BenchmarkParse`     | 68 ns    | 0         |
| `BenchmarkNormalize` | 2.4 µs   | 13        |

Normalization is the most expensive per-line step. It only runs on error lines, but in error-heavy logs it accounts for most of the CPU time (see Limitations).

Memory stays flat as input grows. Peak RSS for `loganalyzer stats` with default settings (10 workers) was **72 MB** for a 79 MB file, **85 MB** for 395 MB (5 M lines, 0.6 s), and **90 MB** for 1.2 GB (15 M lines, 1.8 s). The remaining memory is mostly the capped per-worker error-group maps, not file data.

## Limitations and future work

- **Single process.** "Distributed" here means the work is spread across goroutines on one machine. The mergeable `Aggregator` is the natural unit for running across several machines (each analyzes its own shards, then partial results are merged), but no network transport is implemented.
- **One line format.** JSON logs, logfmt (`level=error msg=...`), and multi-line entries such as stack traces are not parsed. A continuation line is counted as malformed. Custom formats would need a pluggable parser interface.
- **No compressed input.** `.gz` files must be decompressed first, for example `zcat app.log.gz | loganalyzer stats -`.
- **Heuristic normalization.** Any digit run becomes `<n>`, so `http2` and `http3` group together. Words without digits, such as usernames, are not normalized. The regex normalizer (about 2.4 µs per error line) could be replaced by a single-pass tokenizer if error-heavy throughput matters.
- **Approximate above the group cap.** Once more than 10,000 distinct messages or patterns appear, which groups get tracked depends on how lines were distributed across workers. Totals stay exact.
- **Timestamps without an offset are assumed to be UTC.**
- **Sparse timeline.** The timeline lists only buckets that contain errors, so gaps are implied rather than shown as zero.
