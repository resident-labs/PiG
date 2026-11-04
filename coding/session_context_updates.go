package coding

// dyna patch P5 (PATCHES.md): context updates.

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/extensions/sdk/json"
	"github.com/MichaelKinsy/PiG/internal/codingagent/prompts"
	"github.com/MichaelKinsy/PiG/internal/codingagent/tools"
)

// contextUpdateType is the custom type of a context update message.
const contextUpdateType = "context_update"

// SetHostPromptSections replaces the sections the host adds to the prompt, after the sections the Session builds. Each
// is wrapped in a tag of its name, as a before_agent_start section is, and one of a built section's name takes its
// place. The next request delivers the change. A section whose name is not a section name is refused.
func (s *Session) SetHostPromptSections(sections ai.OrderedSections) error {
	// Applying the sections to an empty prompt checks their names as a request would.
	if _, err := prompts.ApplyCustomSystemPromptSections(nil, sections); err != nil {
		return err
	}
	sections = cloneSystemSections(sections)
	s.hostSections.Store(&sections)
	return nil
}

// contextUpdate is the user message that tells the model how the desired sections differ from the ones it holds:
// a changed section as line diffs, a new one whole, and a removed one by name. Its details record the desired
// sections, which the model holds once it reads the message. Nil means nothing changed.
func contextUpdate(held map[string]string, desired ai.OrderedSections) *agent.AgentMessage {
	var changes []string
	sections := make(map[string]any, len(desired))
	for _, section := range desired {
		if section.Value == nil {
			continue
		}
		text := *section.Value
		sections[section.Name] = text
		previous, ok := held[section.Name]
		switch {
		case !ok:
			changes = append(changes, "New section "+section.Name+":\n"+text)
		case previous != text:
			// Each text gets a final newline, so the diff does not mark the lack of one.
			changes = append(changes, "Changed section "+section.Name+":\n"+strings.TrimSuffix(tools.UnifiedHunks(previous+"\n", text+"\n", 3), "\n"))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(held)) {
		if _, ok := sections[name]; !ok {
			changes = append(changes, "Removed section "+name+".")
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return &agent.AgentMessage{Custom: map[string]any{
		"role": agent.RoleCustom, "customType": contextUpdateType, "display": false, "timestamp": time.Now().UnixMilli(),
		"content": "[Context update]\n" + strings.Join(changes, "\n\n") + "\n[/Context update]",
		"details": map[string]any{"sections": sections},
	}}
}

// heldSections are the sections the model holds at the end of messages: those of the latest context update, or those
// of system, the system messages replayed, when a system message with sections follows every update.
func heldSections(messages []agent.AgentMessage, system ai.SystemMessage) map[string]string {
	for _, message := range slices.Backward(messages) {
		if message.System != nil && len(message.System.Sections) > 0 {
			break
		}
		if sections, ok := contextUpdateSections(message); ok {
			return sections
		}
	}
	held := make(map[string]string, len(system.Sections))
	for _, section := range system.Sections {
		if section.Value != nil {
			held[section.Name] = *section.Value
		}
	}
	return held
}

// contextUpdateSections reads the sections a context update records. Its details are a map when the Session made it and raw
// JSON when it was read from a session file. A session file may hold another extension's message of the type, so one whose
// details are not sections of text is not an update.
func contextUpdateSections(message agent.AgentMessage) (map[string]string, bool) {
	if message.Custom == nil || message.Custom["customType"] != contextUpdateType {
		return nil, false
	}
	encoded, err := json.Marshal(message.Custom["details"])
	if err != nil {
		return nil, false
	}
	var details struct {
		Sections map[string]string `json:"sections"`
	}
	if json.Unmarshal(encoded, &details) != nil || details.Sections == nil {
		return nil, false
	}
	return details.Sections, true
}

// refreshPromptResources rereads the context files and skills of the Session's own loader. When they changed, the base
// prompt is rebuilt and a running run's options take them, keeping its handlers' other edits, so the next request's
// sections hold them.
func (s *Session) refreshPromptResources() error {
	ref := s.resourceLoader.Load()
	if !s.contextUpdates || ref == nil || !ref.promptFromLoader || !s.defaultSystemPrompt {
		return nil
	}
	loader, ok := ref.loader.(*DefaultResourceLoader)
	if !ok {
		return nil
	}
	if err := loader.RefreshPromptResources(); err != nil {
		return err
	}
	s.toolRegistryMu.Lock()
	defer s.toolRegistryMu.Unlock()
	before := s.baseSystemPromptOptions.Load()
	s.rebuildSystemPrompt(s.ActiveToolNames())
	after := s.baseSystemPromptOptions.Load()
	if before != nil && slices.Equal(before.ContextFiles, after.ContextFiles) && slices.Equal(before.Skills, after.Skills) {
		return nil
	}
	if run := s.runPrompt.Load(); run != nil {
		next := *run
		next.Options.ContextFiles, next.Options.Skills = after.ContextFiles, after.Skills
		s.runPrompt.CompareAndSwap(run, &next)
	}
	return nil
}

// promptToolOrder is the order the prompt lists tools in. With ContextUpdates it is by name, so the same tools render
// the same whatever order they became active in, and a context update shows no change for them; otherwise it is the
// active order, as Pi's.
func (s *Session) promptToolOrder(names []string) []string {
	if s.contextUpdates {
		return slices.Sorted(slices.Values(names))
	}
	return names
}
