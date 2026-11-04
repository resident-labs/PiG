package coding

import (
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
)

// dyna patch P3: the session's trigger reaches the settings every compaction check reads, and zero clears it.
func TestSessionCompactionTriggerTokens(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("done", ai.StopReasonStop, 0))
	for _, tokens := range []int{120000, 0} {
		h.session.SetCompactionTriggerTokens(tokens)
		settings, err := h.session.compactionSettings()
		if err != nil || settings.TriggerTokens != tokens {
			t.Fatalf("settings %+v, %v", settings, err)
		}
	}
}
