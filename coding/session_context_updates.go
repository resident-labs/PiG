package coding

// dyna patch P5 (PATCHES.md): context updates.

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/extensions/sdk/json"
	icodingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
	"github.com/MichaelKinsy/PiG/internal/codingagent/prompts"
	"github.com/MichaelKinsy/PiG/internal/codingagent/tools"
)

// contextUpdateType is the custom type of a context update message.
const contextUpdateType = "context_update"

// SetHostPromptSections replaces the sections the host adds to the prompt, after the sections the Session builds. Each
// is wrapped in a tag of its name, as a before_agent_start section is, and one of a built section's name takes its
// place. The next request delivers the change. A section whose name is not a section name, or is deferred_tools, is
// refused.
func (s *Session) SetHostPromptSections(sections ai.OrderedSections) error {
	if slices.ContainsFunc(sections, func(section ai.PromptSection) bool { return section.Name == deferredToolsSection }) {
		return fmt.Errorf("Reserved prompt section: %s", deferredToolsSection)
	}
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

// deferredToolsSection names the tools tool_search or a codemode script can reach that are not declared, in a head
// (dyna patch P7, PATCHES.md).
const deferredToolsSection = "deferred_tools"

// maxDeferredNames is how many tools a namespace lists, and actions a tool; the rest are counted.
const maxDeferredNames = 80

// deferredTool is a tool the deferred_tools section names: its name, its namespace's, and the action enum of its schema.
type deferredTool struct {
	name, namespace string
	actions         []string
}

// renderDeferredTools is the deferred_tools section: the tools by name only, grouped by the prefix their namespace gives
// them. A namespace's line names its tools that take no action, and each tool with actions gets its own line below it.
// It is absent when nothing is listed or neither tool_search nor codemode is active.
func renderDeferredTools(tools []deferredTool, toolSearch, codemode bool) (string, bool) {
	if len(tools) == 0 || (!toolSearch && !codemode) {
		return "", false
	}
	const script = "a codemode script calls one as tools.<prefix><name>."
	how := "tool_search loads one."
	switch {
	case codemode && toolSearch:
		how = "tool_search loads one, and " + script
	case codemode:
		how = "A" + script[1:]
	}
	capped := func(names []string) []string {
		if len(names) <= maxDeferredNames {
			return names
		}
		return append(slices.Clone(names[:maxDeferredNames]), fmt.Sprintf("+%d more; tool_search finds them", len(names)-maxDeferredNames))
	}
	groups := map[string][]deferredTool{}
	for _, tool := range tools {
		prefix := ""
		if tool.namespace != "" && strings.HasPrefix(tool.name, tool.namespace+"__") {
			prefix = tool.namespace + "__"
		}
		groups[prefix] = append(groups[prefix], deferredTool{name: strings.TrimPrefix(tool.name, prefix), actions: tool.actions})
	}
	lines := []string{"Tools not declared to you yet, by name prefix, with a tool's actions after it. " + how}
	for _, prefix := range slices.Sorted(maps.Keys(groups)) {
		sorted := groups[prefix]
		slices.SortFunc(sorted, func(a, b deferredTool) int { return strings.Compare(a.name, b.name) })
		kept := sorted[:min(len(sorted), maxDeferredNames)]
		var plain []string
		for _, tool := range kept {
			if len(tool.actions) == 0 {
				plain = append(plain, tool.name)
			}
		}
		if len(sorted) > maxDeferredNames {
			plain = append(plain, fmt.Sprintf("+%d more; tool_search finds them", len(sorted)-maxDeferredNames))
		}
		switch {
		case prefix != "" && len(plain) > 0:
			lines = append(lines, prefix+": "+strings.Join(plain, ", "))
		case prefix != "":
			lines = append(lines, prefix+":")
		case len(plain) > 0:
			lines = append(lines, strings.Join(plain, ", "))
		}
		for _, tool := range kept {
			if len(tool.actions) > 0 {
				lines = append(lines, "- "+tool.name+": "+strings.Join(capped(tool.actions), ", "))
			}
		}
	}
	return strings.Join(lines, "\n"), true
}

// withDeferredTools is sections with the deferred_tools section of the tools as they are now, the sections of a new
// head: only a head names them, so a tool_search load adds only its toolsAdded system message. The caller holds
// toolRegistryMu.
func (s *Session) withDeferredTools(sections ai.OrderedSections) (ai.OrderedSections, error) {
	active := map[string]bool{}
	for _, name := range s.ActiveToolNames() {
		active[name] = true
	}
	var tools []deferredTool
	for _, entry := range s.toolRegistry.entries {
		definition := entry.registration.Definition
		exposure := toolExposureOf(definition)
		if (exposure != extension.ToolExposureDeferred && exposure != extension.ToolExposureCodemode) || active[definition.Name] {
			continue
		}
		tool := deferredTool{name: definition.Name}
		if definition.Namespace != nil {
			tool.namespace = definition.Namespace.Name
		}
		// Schemas come from extensions and MCP servers; only a string enum counts as actions.
		var schema struct {
			Properties struct {
				Action struct {
					Enum []any `json:"enum"`
				} `json:"action"`
			} `json:"properties"`
		}
		if json.Unmarshal(definition.Parameters, &schema) == nil {
			for _, action := range schema.Properties.Action.Enum {
				if name, ok := action.(string); ok {
					tool.actions = append(tool.actions, name)
				}
			}
		}
		tools = append(tools, tool)
	}
	text, ok := renderDeferredTools(tools, active["tool_search"], active["codemode"])
	if !ok {
		return sections, nil
	}
	return prompts.ApplyCustomSystemPromptSections(sections, ai.OrderedSections{{Name: deferredToolsSection, Value: &text}})
}

// compactionHead is the sections a compaction's head holds: the current ones and the deferred tools as they are now.
// Its tools are the schemas of the session's executable tools (dyna patch P11), so the conversation after it declares what it can run
// and a tool a hook unloaded is neither declared again nor removed afterwards. Both are nil without ContextUpdates, which keeps the
// conversation's current system message.
func (s *Session) compactionHead() (ai.OrderedSections, []ai.ToolSchema, error) {
	if !s.contextUpdates {
		return nil, nil, nil
	}
	s.toolRegistryMu.RLock()
	defer s.toolRegistryMu.RUnlock()
	sections, err := s.effectiveSystemSections()
	if err != nil {
		return nil, nil, err
	}
	sections, err = s.withDeferredTools(sections)
	if err != nil {
		return nil, nil, err
	}
	executable := s.agent.Tools()
	tools := make([]ai.ToolSchema, len(executable))
	for i, tool := range executable {
		tools[i] = tool.Schema()
	}
	return sections, tools, nil
}

// appendCompaction records a compaction in manager with the head compactionHead gives.
func (s *Session) appendCompaction(manager *icodingagent.Session, summary, firstKeptEntryID string, tokensBefore int, details any, fromHook bool, usage *ai.Usage) (string, error) {
	head, tools, err := s.compactionHead()
	if err != nil {
		return "", err
	}
	return manager.AppendCompactionWithSections(summary, firstKeptEntryID, tokensBefore, details, fromHook, usage, head, tools)
}
