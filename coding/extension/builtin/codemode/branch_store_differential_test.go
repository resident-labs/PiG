package codemode

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/internal/codingagent"
)

func TestReviewCacheConcurrent(t *testing.T) {
	m := &measuredBranch{s: codingagent.NewSession("review", t.TempDir())}
	_, err := m.s.AppendCustomEntry(StoreEntryType, StoreEntryData{Set: map[string]json.RawMessage{"x": json.RawMessage(`1`)}})
	if err != nil {
		t.Fatal(err)
	}
	c := &branchStoreCache{}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 100 {
				v := c.read(m)
				if string(v["x"]) != "1" {
					t.Error(v)
				}
				v["x"] = json.RawMessage(`2`)
			}
		})
	}
	wg.Wait()
}

func TestReviewCacheDifferential(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		r := rand.New(rand.NewSource(seed))
		m := &measuredBranch{s: codingagent.NewSession("review", t.TempDir())}
		c := &branchStoreCache{}
		var ids []string
		for step := 0; step < 500; step++ {
			var err error
			switch r.Intn(8) {
			case 0, 1, 2:
				key := string(rune('a' + r.Intn(8)))
				d := StoreEntryData{}
				if r.Intn(2) == 0 {
					d.Set = map[string]json.RawMessage{key: json.RawMessage(`123`)}
				} else {
					d.Delete = []string{key}
				}
				_, err = m.s.AppendCustomEntry(StoreEntryType, d)
			case 3:
				if len(ids) > 0 {
					id := ids[r.Intn(len(ids))]
					err = m.s.SetLeafID(&id)
				}
			case 4:
				if len(ids) > 0 {
					_, err = m.s.AppendCompaction("summary", ids[r.Intn(len(ids))], 100, nil, false, nil)
				}
			case 5:
				err = m.s.SetLeafID(nil)
			case 7:
				// Reconstruct a fresh process's session index and discard its store cache.
				leaf := m.GetLeafID()
				dir := t.TempDir()
				path := filepath.Join(dir, "session.jsonl")
				f, e := os.Create(path)
				if e != nil {
					t.Fatal(e)
				}
				enc := json.NewEncoder(f)
				if e := enc.Encode(m.s.GetHeader()); e != nil {
					t.Fatal(e)
				}
				for _, entry := range m.s.GetEntries() {
					if e := enc.Encode(entry); e != nil {
						t.Fatal(e)
					}
				}
				if e := f.Close(); e != nil {
					t.Fatal(e)
				}
				m.s, err = codingagent.NewSessionManagerWithDir(dir, dir).Open(path)
				if err != nil {
					t.Fatal(err)
				}
				err = m.s.SetLeafID(leaf)
				c = &branchStoreCache{}
			case 6:
				if r.Intn(10) == 0 {
					m.s = codingagent.NewSession("replacement", t.TempDir())
					ids = nil
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if leaf := m.GetLeafID(); leaf != nil {
				ids = append(ids, *leaf)
			}
			got := c.read(m)
			want := storeOf(m.GetBranch())
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d step %d: got %s want %s", seed, step, got, want)
			}
			got["mutated"] = json.RawMessage(`0`)
		}
	}
}
