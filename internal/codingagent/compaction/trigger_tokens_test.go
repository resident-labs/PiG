package compaction

import "testing"

// dyna patch P3: a trigger below the reserve's threshold starts compaction sooner, and one above it changes nothing.
func TestTriggerTokensStartCompactionSooner(t *testing.T) {
	settings := CompactionSettings{Enabled: true, ReserveTokens: 16384}
	for _, tc := range []struct {
		trigger, tokens int
		want            bool
	}{
		{0, 150000, false},
		{0, 190000, true},
		{100000, 100000, false},
		{100000, 100001, true},
		{500000, 190000, true},
		{500000, 150000, false},
	} {
		settings.TriggerTokens = tc.trigger
		if got := ShouldCompact(tc.tokens, 200000, settings); got != tc.want {
			t.Errorf("trigger %d at %d tokens: %v", tc.trigger, tc.tokens, got)
		}
	}
	if ShouldCompact(300000, 200000, CompactionSettings{TriggerTokens: 1000}) {
		t.Error("compaction that is off started")
	}
}
