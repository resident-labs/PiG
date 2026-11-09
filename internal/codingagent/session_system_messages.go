package codingagent

import "github.com/MichaelKinsy/PiG/agent"

// GetSystemMessages returns the system messages on the current branch in ancestry
// order, including those before compaction. Slot metadata filters out other
// message bodies before decoding, so restoring a tool loadout does not read the
// source-code and tool-result payloads of the whole history.
func (s *Session) GetSystemMessages() []agent.AgentMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.leafID == nil {
		return nil
	}
	path := s.pathIndexesLocked(*s.leafID)
	var indexes []int32
	for _, idx := range path {
		slot := &s.slots[idx]
		if slot.typ() == typeMessage && slot.role() == "system" {
			indexes = append(indexes, idx)
		}
	}
	var systems []agent.AgentMessage
	for _, entry := range s.entriesAtLocked(indexes) {
		if message, ok := AsMessage(entry); ok && message.Message.System != nil {
			systems = append(systems, message.Message)
		}
	}
	return systems
}
