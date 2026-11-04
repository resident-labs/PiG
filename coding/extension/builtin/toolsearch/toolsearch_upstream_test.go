package toolsearch_test

// pi: packages/coding-agent/src/extensions/tool-search/tool.ts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/builtin/toolsearch"
)

// Ports packages/coding-agent/test/tool-search.test.ts (v1.0.0): the tokenize, Bm25Ranker and tool_search description
// cases. The codemode description catalog cases of the same file are in ../codemode/description_upstream_test.go.

func info(name, description, properties string) extension.ToolInfo {
	if properties == "" {
		properties = "{}"
	}
	return extension.ToolInfo{Name: name, Description: description, Parameters: json.RawMessage(`{"type":"object","properties":` + properties + `}`)}
}

func names(matches []toolsearch.Match) []string {
	out := []string{}
	for _, m := range matches {
		out = append(out, m.Name)
	}
	return out
}

func TestTokenizeSplitsCamelCaseAndSnakeCaseDropsStopWordsAndFoldsPlurals(t *testing.T) {
	for input, want := range map[string][]string{
		"listIssues for the GitHub_repo": {"list", "issue", "git", "hub", "repo"},
		"searches queries HTTPServer":    {"search", "query", "http", "server"},
	} {
		if got := toolsearch.Tokenize(input); !reflect.DeepEqual(got, want) {
			t.Errorf("Tokenize(%q) = %q, want %q", input, got, want)
		}
	}
}

func rankerDocuments() []toolsearch.Document {
	tools := []extension.ToolInfo{
		info("mcp__github__list_issues", "List issues in a repository.", `{"state":{"type":"string","description":"open or closed"}}`),
		info("mcp__github__create_pull_request", "Open a pull request.", ""),
		info("mcp__linear__search_issues", "Search Linear issues by text.", ""),
		info("mcp__docs__search", "Search the documentation.", ""),
	}
	documents := make([]toolsearch.Document, len(tools))
	for i, tool := range tools {
		documents[i] = toolsearch.CreateDocument(tool, nil)
	}
	return documents
}

func TestBm25RankerRanksByTermRelevanceAndRespectsTheLimit(t *testing.T) {
	ranker, documents := toolsearch.NewBm25Ranker(), rankerDocuments()
	if got, want := names(ranker.Rank("issue", documents, 8)), []string{"mcp__linear__search_issues", "mcp__github__list_issues"}; !reflect.DeepEqual(got, want) {
		t.Errorf("issue: %q, want %q", got, want)
	}
	if got := ranker.Rank("pull requests", documents, 8); len(got) == 0 || got[0].Name != "mcp__github__create_pull_request" {
		t.Errorf("pull requests: %+v", got)
	}
	if got := ranker.Rank("search", documents, 1); len(got) != 1 {
		t.Errorf("search limit 1: %+v", got)
	}
	// Property names and descriptions are searchable.
	if got, want := names(ranker.Rank("closed", documents, 8)), []string{"mcp__github__list_issues"}; !reflect.DeepEqual(got, want) {
		t.Errorf("closed: %q, want %q", got, want)
	}
}

func TestBm25RankerReturnsNothingForUnknownOrEmptyQueries(t *testing.T) {
	ranker, documents := toolsearch.NewBm25Ranker(), rankerDocuments()
	for _, query := range []string{"kubernetes", "the", "tickets"} { // "tickets": known v1 limit, no synonyms.
		if got := ranker.Rank(query, documents, 8); len(got) != 0 {
			t.Errorf("Rank(%q) = %+v, want none", query, got)
		}
	}
}

func TestBm25RankerIncludesTheNamespaceInTheSearchText(t *testing.T) {
	document := toolsearch.CreateDocument(info("mcp__x__run", "Run it.", ""), &extension.ToolNamespace{Name: "mcp__x", Description: "Kubernetes cluster tools"})
	got := toolsearch.NewBm25Ranker().Rank("kubernetes", []toolsearch.Document{document}, 8)
	if len(got) != 1 || got[0].Name != "mcp__x__run" || got[0].Score <= 0 {
		t.Fatalf("Rank = %+v", got)
	}
}

// agent-session-mcp.test.ts (0.99.2) `expect(description("tool_search")).toBe(TOOL_SEARCH_DESCRIPTION)`: the description
// lists neither the searchable tools nor their namespaces, so it stays the same while MCP servers connect. The upstream
// "tool_search description lists the sources" case is gone.
func TestToolSearchDescriptionDoesNotListTheSearchableSources(t *testing.T) {
	want := "# Tool discovery\n\nSearches over deferred tool metadata with BM25 and exposes matching tools for the next model call.\n\nSome of the tools, such as tools of MCP servers, may not have been provided to you upfront, and you should use this tool (`tool_search`) to search for the required tools. For MCP tool discovery, always use `tool_search`."
	if toolsearch.ToolSearchDescription != want {
		t.Errorf("ToolSearchDescription:\n%q\nwant\n%q", toolsearch.ToolSearchDescription, want)
	}
	definition := toolsearch.Definition()
	if definition.Description != toolsearch.ToolSearchDescription {
		t.Errorf("definition description = %q", definition.Description)
	}
	if definition.PrepareLoadout != nil {
		t.Error("the tool still rewrites its description from the loadout")
	}
}

// createToolSearchDocument (tool.ts, 0.99.2) searches the namespace with its description and instructions.
func TestBm25RankerIncludesTheNamespaceInstructionsInTheSearchText(t *testing.T) {
	document := toolsearch.CreateDocument(info("mcp__x__run", "Run it.", ""), &extension.ToolNamespace{Name: "mcp__x", Instructions: "Kubernetes cluster tools"})
	got := toolsearch.NewBm25Ranker().Rank("kubernetes", []toolsearch.Document{document}, 8)
	if len(got) != 1 || got[0].Name != "mcp__x__run" || got[0].Score <= 0 {
		t.Fatalf("Rank = %+v", got)
	}
}

// packages/coding-agent/src/extensions/tool-search/tool.ts:65-69: the plural folds have length floors: "ies" and the
// "ches|shes|sses|xes|zes" endings fold above four characters, a bare trailing "s" above three and never after "ss".
func TestTokenizeFoldsPluralsOnlyAboveTheirLengthFloors(t *testing.T) {
	for term, want := range map[string]string{
		"bus": "bus", "abs": "abs", "tabs": "tab", "axes": "axe", "ties": "tie",
		"cries": "cry", "boxes": "box", "class": "class", "searches": "search", "issues": "issue",
	} {
		if got := toolsearch.Tokenize(term); len(got) != 1 || got[0] != want {
			t.Errorf("Tokenize(%q) = %q, want [%q]", term, got, want)
		}
	}
}

// dyna patch P6 (PATCHES.md): the cases of earendil-works/pi#11625 and #11632 in packages/coding-agent/test/tool-search.test.ts
// (issue #11331): the name and the namespace are fields of their own, and a query that names or covers a tool ranks it first.

func documentsOf(tools []extension.ToolInfo, namespace *extension.ToolNamespace, prefix string) []toolsearch.Document {
	documents := make([]toolsearch.Document, len(tools))
	for i, tool := range tools {
		ns := (*extension.ToolNamespace)(nil)
		if prefix != "" && strings.HasPrefix(tool.Name, prefix) {
			ns = namespace
		}
		documents[i] = toolsearch.CreateDocument(tool, ns)
	}
	return documents
}

func first(ranker *toolsearch.Bm25Ranker, query string, documents []toolsearch.Document) []string {
	return names(ranker.Rank(query, documents, 1))
}

func TestBm25RankerRanksAToolFirstWhenTheQueryIsItsName(t *testing.T) {
	documents := documentsOf([]extension.ToolInfo{
		info("mcp__app__queue_read", "Read a queued message of this thread.", ""),
		info("mcp__dev-app__thread_read", "Read durable state and a paginated timeline from any thread. The default view returns user messages, assistant messages and plans. Continue with afterPosition until hasMore is false.", ""),
	}, nil, "")
	ranker := toolsearch.NewBm25Ranker()
	for _, query := range []string{"thread_read", "dev_app__thread_read", "mcp__dev-app__thread_read", "Thread_Read", "thread read"} {
		if got := first(ranker, query, documents); !reflect.DeepEqual(got, []string{"mcp__dev-app__thread_read"}) {
			t.Errorf("%q: %q", query, got)
		}
	}
	// Partial names still rank by BM25.
	if got := first(ranker, "read", documents); !reflect.DeepEqual(got, []string{"mcp__app__queue_read"}) {
		t.Errorf("read: %q", got)
	}
}

func TestBm25RankerRanksANameMatchAboveMentionsInDescriptions(t *testing.T) {
	documents := documentsOf([]extension.ToolInfo{
		info("mcp__ci__status", "Deploy status: last deploy, deploy log, deploy errors.", ""),
		info("mcp__ci__deploy_release", "Start a release of the current branch to an environment, run the migrations, switch traffic over, and wait until the new version is live and healthy.", ""),
	}, nil, "")
	if got := first(toolsearch.NewBm25Ranker(), "deploy", documents); !reflect.DeepEqual(got, []string{"mcp__ci__deploy_release"}) {
		t.Errorf("deploy: %q", got)
	}
}

func TestBm25RankerRanksAToolFirstWhenTheQueryContainsItsWholeName(t *testing.T) {
	peekaboo := &extension.ToolNamespace{Name: "mcp__peekaboo", Instructions: "Peekaboo controls macOS apps by app name."}
	documents := documentsOf([]extension.ToolInfo{
		info("mcp__peekaboo__see", "Capture the app window of the peekaboo app and describe the app.", ""),
		info("mcp__peekaboo__menu", "Click a menu item of an app.", ""),
		info("mcp__peekaboo__app", "Launch, quit, relaunch, focus, hide, or switch applications. Targets an application by name, bundle identifier, or process ID, and can wait until it is ready.", ""),
		info("mcp__app__queue_read", "Read a queued message of this thread.", ""),
		info("mcp__dev-app__thread_read", "Read durable state and a paginated timeline from any thread. The default view returns user messages, assistant messages and plans.", ""),
	}, peekaboo, "mcp__peekaboo__")
	ranker := toolsearch.NewBm25Ranker()
	for query, want := range map[string]string{
		"peekaboo app":               "mcp__peekaboo__app",
		"open the peekaboo app tool": "mcp__peekaboo__app",
		"read thread_read state":     "mcp__dev-app__thread_read",
		// Without the whole name, mentions in descriptions decide.
		"describe window": "mcp__peekaboo__see",
	} {
		if got := first(ranker, query, documents); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("%q: %q, want %q", query, got, want)
		}
	}
}

func TestBm25RankerRanksAToolMatchingMoreQueryTermsFirstWhateverTheScores(t *testing.T) {
	documents := documentsOf([]extension.ToolInfo{
		info("mcp__peekaboo__app", "Launch, quit, focus, or switch applications by name or bundle identifier.", ""),
		info("mcp__peekaboo__see", "Capture the app window and describe the UI elements of the app.", ""),
		info("mcp__peekaboo__window", "Move, resize, or close a window of an app.", ""),
		info("mcp__peekaboo__image", "Take a screenshot of the screen, an app, or a window.", ""),
	}, nil, "")
	ranker := toolsearch.NewBm25Ranker()
	for query, want := range map[string]string{
		// `window` and `app` are whole names, but each covers one of the two terms.
		"screenshot window": "mcp__peekaboo__image",
		"describe app":      "mcp__peekaboo__see",
		// An exact name still ranks first.
		"window": "mcp__peekaboo__window",
	} {
		if got := first(ranker, query, documents); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("%q: %q, want %q", query, got, want)
		}
	}
}

// dyna patch P6 (PATCHES.md): the upstream cases never let the exact-name rule decide, because the exact tool also
// wins on coverage and score. Here `mcp__b__read_thread` covers the same terms, gets the same whole-name bonus and
// scores higher through its description, so only the exact-name sort puts `mcp__a__thread_read` first.
func TestBm25RankerRanksTheExactNameAboveAToolThatCoversTheSameTermsWithAHigherScore(t *testing.T) {
	documents := documentsOf([]extension.ToolInfo{
		info("mcp__b__read_thread", "Read thread. Read the thread, thread read, read thread.", ""),
		info("mcp__a__thread_read", "Fetch the durable state, the paginated timeline, the plans and the messages of a conversation, whatever its length.", ""),
		info("mcp__c__ping", "Check that the server answers.", ""),
	}, nil, "")
	ranker := toolsearch.NewBm25Ranker()
	for _, query := range []string{"thread read", "thread_read"} {
		if got := first(ranker, query, documents); !reflect.DeepEqual(got, []string{"mcp__a__thread_read"}) {
			t.Errorf("%q: %q", query, got)
		}
	}
}
