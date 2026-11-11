# Real-session Execute measurement

Measured on Linux amd64 with Go 1.27.3, GOMAXPROCS=1 and GOMEMLIMIT=2GiB. The private input is a 111 MB Dyna session, opened by SessionManager.Open from a temporary copy. No transcript contents are committed. TestExecuteRealSessionProfile runs the same empty-work script (`return;`) twice. The CPU profile covers the second execution; peak RSS covers the whole process, including file copy, open and cold initialization.

| Path | First Execute | Second Execute | Peak RSS |
| --- | --- | --- | --- |
| Stateless baseline | 10.425 s | 10.091 s | 973,076 KiB |
| Retained store checkpoint | 10.734 s | 0.137 s | 1,112,752 KiB |

The baseline profile attributes 9.05 s of 10.04 s sampled CPU (90.1%) to GetBranch → entriesAtLocked → readSlotsLocked; DecodeSessionEntry accounts for 9.00 s. The large cost is decoding cold message records, not merely marshaling unrelated custom entries. The allocation profile across initialization and both calls totals 7.59 GB; internal/jsonstringify.object allocates 1.02 GB directly and 3.38 GB cumulatively. The fixed second execution has no GetBranch or message-decoding samples (40 ms sampled CPU; profile overhead dominates this short interval).

The checkpoint eliminates that repeated decode. Cold initialization still decodes history once; it is not a cold-start improvement and does not reduce peak process RSS in this measurement. External caller work before Execute and after its return is not measured: the baseline Execute itself reproduces the reported 9.5 s latency.

Reproduce with a compiled package test binary:

```sh
go test -c -o /tmp/codemode.test ./coding/extension/builtin/codemode
GOMAXPROCS=1 GOMEMLIMIT=2GiB CODEMODE_REAL_SESSION=/path/to/private-copy.jsonl CODEMODE_REAL_CACHED=0 CODEMODE_REAL_PROFILE=/tmp/before.cpu CODEMODE_REAL_HEAP=/tmp/before.allocs /tmp/codemode.test -test.run=TestExecuteRealSessionProfile -test.v
GOMAXPROCS=1 GOMEMLIMIT=2GiB CODEMODE_REAL_SESSION=/path/to/private-copy.jsonl CODEMODE_REAL_CACHED=1 CODEMODE_REAL_PROFILE=/tmp/after.cpu CODEMODE_REAL_HEAP=/tmp/after.allocs /tmp/codemode.test -test.run=TestExecuteRealSessionProfile -test.v
go tool pprof -top -cum /tmp/codemode.test /tmp/before.cpu
```
