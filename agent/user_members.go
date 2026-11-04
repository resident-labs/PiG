package agent

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/MichaelKinsy/PiG/ai"
	json "github.com/MichaelKinsy/PiG/extensions/sdk/json"
)

// NewUserMessage builds a user message that carries members besides role, content and timestamp. They are written with the
// message wherever it is recorded, as members of a decoded message Pi does not model are, and no provider request sends
// them, since a request is built from the message's fields.
//
// dyna patch P1 (PATCHES.md): Pi has no way to label a user message for its host. A host that runs PiG in process, such as one
// that must find which of its inputs a session file already holds, names each input here.
func NewUserMessage(content ai.UserContent, timestamp int64, members map[string]json.RawMessage) (AgentMessage, error) {
	message := AgentMessage{User: &UserMessage{Role: RoleUser, Content: content, Timestamp: timestamp}}
	if len(members) == 0 {
		return message, nil
	}
	if err := ValidateUserMembers(members); err != nil {
		return AgentMessage{}, err
	}
	encoded, err := message.MarshalJSON()
	if err != nil {
		return AgentMessage{}, err
	}
	text := bytes.NewBuffer(encoded[:len(encoded)-1])
	for _, name := range slices.Sorted(maps.Keys(members)) {
		key, err := json.Marshal(name)
		if err != nil {
			return AgentMessage{}, err
		}
		text.WriteByte(',')
		text.Write(key)
		text.WriteByte(':')
		text.Write(members[name])
	}
	text.WriteByte('}')
	var labelled AgentMessage
	if err := labelled.UnmarshalJSON(text.Bytes()); err != nil {
		return AgentMessage{}, err
	}
	return labelled, nil
}

// ValidateUserMembers refuses a member that names the message's own role, content or timestamp, or that is not JSON. A
// session checks its input's members with it before input handlers see the input.
func ValidateUserMembers(members map[string]json.RawMessage) error {
	for _, name := range slices.Sorted(maps.Keys(members)) {
		if name == "role" || name == "content" || name == "timestamp" {
			return fmt.Errorf("user message member %q is the message's own", name)
		}
		if !json.Valid(members[name]) {
			return errors.New("user message member " + name + " is not JSON")
		}
	}
	return nil
}
