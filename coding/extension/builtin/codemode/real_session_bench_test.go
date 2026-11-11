//go:build linux

package codemode

import (
	"context"
	"encoding/json"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/internal/codingagent"
	"os"
	"path/filepath"
	"runtime/pprof"
	"syscall"
	"testing"
	"time"
)

// TestExecuteRealSessionProfile is opt-in because its input is a private session.
// It copies the file before opening it; no test writes to the source transcript.
func TestExecuteRealSessionProfile(t *testing.T) {
	path := os.Getenv("CODEMODE_REAL_SESSION")
	if path == "" {
		t.Skip("CODEMODE_REAL_SESSION is unset")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "session.jsonl")
	if err = os.WriteFile(copyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := codingagent.NewSessionManagerWithDir(dir, dir).Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	m := &measuredBranch{s: s}
	base := extension.NewContext(dir, nil, func() error { return nil }, extension.ContextActions{SessionManager: m})
	ctx := extension.WithToolContext(context.Background(), extension.NewToolContext(base, "bench", context.Background(), extension.ToolActions{}))
	options := Options{CacheDir: t.TempDir()}
	if os.Getenv("CODEMODE_REAL_CACHED") == "1" {
		options.storeCache = &branchStoreCache{}
	}
	run := func() {
		if _, err := Execute(ctx, "bench", json.RawMessage(`{"code":"return;"}`), nil, options); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	run()
	t.Logf("first Execute: %s", time.Since(start))
	if out := os.Getenv("CODEMODE_REAL_PROFILE"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		if err = pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	start = time.Now()
	run()
	t.Logf("warm Execute: %s", time.Since(start))
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	t.Logf("peak RSS: %d KiB", usage.Maxrss)
	if out := os.Getenv("CODEMODE_REAL_HEAP"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err = pprof.Lookup("allocs").WriteTo(f, 0); err != nil {
			t.Fatal(err)
		}
	}
}
