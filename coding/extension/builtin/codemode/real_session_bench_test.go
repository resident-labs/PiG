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
	// Candidate A prototype: prepare a sidecar, then reopen the session before timing its first use.
	var snapshotPath string
	if os.Getenv("CODEMODE_REAL_SNAPSHOT") == "1" {
		cache := &branchStoreCache{}
		cache.read(m)
		data, err := json.Marshal(struct {
			ID, Leaf string
			Values   map[string]json.RawMessage
		}{cache.sessionID, cache.leaf, cache.values})
		if err != nil {
			t.Fatal(err)
		}
		snapshotPath = filepath.Join(dir, "store.json")
		if err := os.WriteFile(snapshotPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		m.s, err = codingagent.NewSessionManagerWithDir(dir, dir).Open(copyPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if snapshotPath != "" {
		data, err := os.ReadFile(snapshotPath)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot struct {
			ID, Leaf string
			Values   map[string]json.RawMessage
		}
		if err := json.Unmarshal(data, &snapshot); err != nil {
			t.Fatal(err)
		}
		found := false
		for next := m.GetLeafID(); next != nil; {
			if *next == snapshot.Leaf {
				found = true
				break
			}
			entry, ok := m.GetEntryHandle(*next)
			if !ok {
				break
			}
			next = entry.Base().ParentID
		}
		if !found || snapshot.ID != m.GetSessionId() {
			t.Fatal("invalid snapshot")
		}
		options.storeCache = &branchStoreCache{sessionID: snapshot.ID, leaf: snapshot.Leaf, values: snapshot.Values}
	}
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
