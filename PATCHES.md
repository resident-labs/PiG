# Dyna patches

This is resident-labs/PiG, Dyna's fork of PiG. The branch `dyna` is the pinned PiG release plus the patches below; Dyna's
`next/go.mod` replaces `github.com/MichaelKinsy/PiG` with an exact commit of it. Each patch is small, additive and leaves
every existing behaviour as it was, so it can be offered upstream as it stands. A patch's code names it as
`dyna patch P<n>`.

Base: PiG v0.5.3 (526c86df8).

## P1 Members on a user message

What: `agent.NewUserMessage(content, timestamp, members)` builds a user message that carries members besides role,
content and timestamp. `PromptOptions.Members` and `QueueInputOptions.Members` put them on a prompt's, steer's or
follow-up's message. They are written with the message to the session file, as members of a decoded message PiG does not
model already are, and no provider request carries them, since a request is built from the message's fields. A member
named role, content or timestamp, or one that is not JSON, is refused (`agent.ValidateUserMembers`) before input handlers
see the input, so nothing is queued or started.

Why: a host that runs PiG in process needs to know which of its inputs a session file already holds, for example after
its own process restarted mid-turn. Dyna labels each input with `deliveryId`, as its Pi fork did, so the session file
format is unchanged across the move to PiG.

Locked by: `TestUserMessageMembersAreRecordedAndNotSent`, `TestUserMessageMembersCannotReplaceTheMessage`,
`TestAUserMessageWithoutMembersIsPlain` (agent), `TestPromptAndSteerMembersAreRecorded`,
`TestAnInputWithAMemberOfTheMessageIsRefused` (coding).

Upstream: not offered (owner's decision pending).

## P2 Continue a recovered session

What: `Session.ContinueRecovered(ctx, ContinueOptions{Interrupted, Started})` resumes a session whose process ended
during a run. A final attempt that failed is left out of the model's context, as a retry leaves it out. Each tool call of
the last assistant message that has no result gets an error result with the `Interrupted` text, written to the session
file, and is not run again. The run then continues from the last message; a session that ends with an answer returns
`ErrNothingToContinue`.

Why: Pi resumes a session only with a new prompt. A host that restarts mid-run must not send its inputs twice, so it
finishes the run they started. Dyna's Pi fork had the same as RPC `continue`.

Locked by: `TestContinueRecordsInterruptedToolCalls`, `TestContinueReadsAPendingInput`,
`TestContinueLeavesOutAFailedAttempt`, `TestContinueAfterAnAnswerIsRefused`, `TestContinueAnswersOnlyTheCallsWithoutResults` (coding).

Upstream: not offered (owner's decision pending).

## P3 Compaction trigger tokens

What: `CompactionSettings.TriggerTokens` starts automatic compaction once the context passes that many tokens, when that
comes before the reserve's threshold (`contextWindow - reserveTokens`); zero keeps the reserve's rule. It is serialized
only when set. `Session.SetCompactionTriggerTokens(n)` sets it for one session, unsaved, as a per-process flag would.

Why: Pi compacts only near the end of the window. A host that runs several profiles on one model compacts each
conversation at its own size without writing shared settings. Dyna's Pi fork had the same as
`--compaction-trigger-tokens`.

Locked by: `TestTriggerTokensStartCompactionSooner` (internal/codingagent/compaction),
`TestSessionCompactionTriggerTokens` (coding).

Upstream: not offered (owner's decision pending).

## P4 Model transfer

What: `Session.TransferModel(ctx, target, ModelTransferOptions{ThinkingLevel, CompactionTriggerTokens})` moves the
conversation to a model between runs. A conversation that fits the target and is small switches at once (`unchanged`
or `switched`). Otherwise it is compacted first, on the model the branch records, because that model can read all of
it and still has its prompt cache; that includes a conversation over 100,000 tokens that fits but moves to another
model. If compaction fails, times out after ten minutes or leaves too much, the session switches anyway: a conversation
that fits stays whole, and one that does not gets a compaction entry written without a model, whose summary names the
session file and tells the new model to read it (`boundary`), with the previous summary when it fits, and the tail the
cut point keeps within the limit. A cancelled context stops the compaction and leaves the model as it was (`aborted`).
The session file keeps every entry on all paths. `runAutoCompaction` gains an internal variant that runs on a given
model and settings and reports how it ended; its behaviour is unchanged.

Why: Pi checks the context against a new model only at its next request, when it may already be too large, and then
compacts on the new model with a cold cache. Dyna's Pi fork had the same as `AgentSession.transferModel` and RPC
`transfer_model`; upstream issue #6426 asked for the check.

Locked by: `TestTransferOfASmallConversation`, `TestTransferCompactsOnTheOldModelFirst`,
`TestTransferLeavesABoundaryWhenNoSummaryFits`, `TestAStoppedTransferKeepsTheModel`,
`TestTransferOfALargeConversationToAnotherModelCompacts`, `TestATransferOverTheTriggerOnlyKeepsTheConversation`,
`TestABoundaryKeepsATailOnlyWithinTheTrigger`, `TestABoundaryKeepsNoTailOverTheLimit`,
`TestATransferSummarizesOnTheModelTheBranchRecords`, `TestATransferWhoseSummaryTimesOutMovesAnyway` (coding).

Upstream: not offered (owner's decision pending).

## P5 Context updates

What: `SessionOptions.ContextUpdates` (and `CreateAgentSessionFromServicesOptions.ContextUpdates`) delivers a change to
the prompt's sections after the first system message as one user message instead of a mid-conversation system message:
a custom `context_update` message whose content is `[Context update]`, then each changed section as line diffs
(`tools.UnifiedHunks`, jsdiff's hunks with three lines of context, without the `\ No newline at end of file` line the Pi
fork's diff has, which only says a section has no final newline), each new section whole and each removed one by name,
then `[/Context update]`. Its details record the sections it delivers, so a resumed session, and the next request,
compare against the latest update, or against the system messages when one with sections follows it. The head is never
rewritten, so the cached prefix holds on every provider. `Session.SetHostPromptSections` sets sections the host adds
after the built ones, each wrapped in a tag of its name. With ContextUpdates the Session also rereads its own loader's
context files and skills before each request (`DefaultResourceLoader.RefreshPromptResources`; skills from the folders
its last reload found), and a running run's options take them when they changed, so an AGENTS.md or skill edit between
two tool calls reaches the next request. It also lists tools in the prompt by name, so the same tools render the same
whatever order they became active in. Without ContextUpdates nothing changes.

Why: Pi sends a changed section as a system message, which most providers fold into the head, so a change rewrites the
cached prefix. Dyna's Pi fork had this as `context-updates.ts` with `set_prompt_sections` and its P13 refresh; Dyna's
state sections and its agents' AGENTS.md files change through a session's life.

Locked by: `TestHostSectionsChangeThroughContextUpdates`, `TestAContextUpdateArrivesBeforeTheNextRequestOfARun`,
`TestAResumedSessionSendsOnlyTheChangeSinceItsLastUpdate`, `TestAHostSectionWithABadNameIsRefused`,
`TestTheLatestOfAnUpdateAndASystemMessageHoldsTheSections`, `TestAnAgentsFileEditReachesTheNextRequest`,
`TestAToolActiveAgainChangesNoSection`, `TestHostSectionsReachASessionWithAPlainPrompt`, `TestASkillEditReachesTheNextRequest`
(coding).

## P6 Tool search ranking

What: `toolsearch.Bm25Ranker` ranks with the name and the namespace as fields of their own. `Document` has `Name`,
`Text` (description, schema descriptions, property names) and `NamespaceText`. A query term in the name counts 3
times its IDF, one in the namespace 0.5 times, and BM25 length normalization applies to the description only. A
tool whose whole local name (the last `__` segment) is in the query gets a bonus of the sum of the query terms'
IDF. A query that is exactly a tool's name (normalized, or a suffix after `__`) ranks that tool first. Among the
rest, a tool that matches more query terms ranks above one that matches fewer, then the score decides.

Why: the upstream fixes (earendil-works/pi #11625 name ranking and #11632 query coverage ranking) came after PiG's
pin, and Dyna's Node fork carries them. Without them a model that searches for a tool by its name gets tools that
only mention the words. This patch changes the order and the scores `tool_search` and `searchTools()` return, so
`toolsearch_oracle_test.go` compares only the tokenizer and the document name with the pinned Pi.

Locked by: `TestBm25RankerRanksAToolFirstWhenTheQueryIsItsName`,
`TestBm25RankerRanksANameMatchAboveMentionsInDescriptions`,
`TestBm25RankerRanksAToolFirstWhenTheQueryContainsItsWholeName`,
`TestBm25RankerRanksAToolMatchingMoreQueryTermsFirstWhateverTheScores`,
`TestBm25RankerRanksTheExactNameAboveAToolThatCoversTheSameTermsWithAHigherScore`, `TestBm25ScoresMatchUpstream`
(toolsearch).

Upstream: not offered (owner's decision pending).

## P7 Deferred tools in the head

What: with `SessionOptions.ContextUpdates` (P5), a head (the first system message, and each compaction's) holds a
`deferred_tools` section that names, by name only, every registered tool with `deferred` or `codemode` exposure that
is not active, grouped by the prefix its namespace gives it: a namespace's line names its tools that take no action,
and a tool whose schema has an `action` string enum gets its own `- name: actions` line. A namespace lists at most 80
tools and a tool 80 actions; the rest are counted as `+N more; tool_search finds them`. The first line says how they
are reached: `tool_search`, a codemode script, or both, as active; with neither, or nothing to list, there is no
section. Between heads every request carries the section the head delivered, so a `tool_search` load changes no
section and adds only its `toolsAdded` system message: the system prompt and the request's tools stay byte-identical.
A compaction's head also holds the sections as they are then, not the conversation's first ones
(`Session.compactionHead`, `AppendCompactionWithSections`), so the conversation after it needs no update.
`deferred_tools` is a reserved host section name. `mcpext.Options.DeferredToolsSection` drops the MCP extension's
`mcp_servers` section and makes the first prompt wait for every enabled server, so the head names their tools;
`toolsearch.Options.DeferredToolsSection` gives `tool_search` a description that points at the section. Without them
nothing changes.

Why: a model that does not know what a search can reach searches blind, and listing servers alone names no tools.
Dyna's Pi fork had this as P15 (`renderDeferredToolsSection`, `_headSections`), built only with a head so the cached
prefix holds. Names sort byte-wise here, not with JavaScript's `localeCompare`.

Locked by: `TestTheFirstHeadNamesTheDeferredTools`, `TestALoadChangesNoSection`,
`TestACompactionHeadHoldsTheCurrentSections`, `TestTheDeferredToolsSectionCountsWhatItLeavesOut`,
`TestTheDeferredToolsSectionIsReserved`, `TestTheDeferredToolsSectionSortsByName` (coding), `TestAgentSessionMCPWithDeferredToolsSectionWaitsForEveryServerAndListsNone`
(mcpext), `TestToolSearchDescriptionNamesTheDeferredToolsSection` (toolsearch).

Upstream: not offered (owner's decision pending).

## P8 Active effort at the top level

What: `ai.StreamOptions.ActiveEffortAtTopLevel` makes an Anthropic request that carries mid-conversation effort markers
send the active effort as its top-level `output_config.effort`, where Pi sends `"high"`. A request without markers
already sends the active effort there, so the option changes nothing for it. `SessionOptions.ActiveEffortAtTopLevel`
(and `CreateAgentSessionFromServicesOptions.ActiveEffortAtTopLevel`) sets it on every request of a Session through
`buildRequestOptions`, compaction and branch summaries included. Without it nothing changes.

Why: Dyna's Pi fork has sent the active effort at the top level since its "v2 effort fix" (`third_party/pi/DYNA.md`,
pack level 11), so a session keeps the effort its profile chose on turns that carry markers, not "high". Upstream keeps `"high"` (Pi e3bc05e87).

Locked by: `TestActiveEffortAtTopLevelReplacesTheHighDefault` (ai), `TestActiveEffortAtTopLevelReachesEveryRequest`,
`TestCreateAgentSessionFromServicesPassesActiveEffortAtTopLevel`
(coding).

Upstream: not offered (owner's decision pending).

## P9 A Package install ends with its context

What: `packagemanager.WithContext(ctx)` is an `InstallOption` that runs the processes of a new install under `ctx`: the npm install of an npm Package, and the `git clone`, the `git checkout` of its ref and the dependency install of a git Package. When `ctx` ends they are killed and the install fails with `ctx`'s error. Only that install path takes a context: updating an existing git checkout (`EnsureGitRef`'s fetch, reset and clean), the metadata queries (`RunCmd`, `RunPackageCapture`, git.go's `GetLocalGitUpdateTarget`), `temporary.go`, and the update and removal operations (`package_manager.go`, `package_update_operations.go`) run as before. `RunPackageProcessContext` is `RunPackageProcess` with a context (which keeps its signature and runs under `context.Background()`), `InstallPackageArtifacts` takes install options, and `PackageManager.ResolveContext(ctx, onMissing)` passes its context to the installs of `installMissing` (`Resolve` is `ResolveContext` with `context.Background()`). `DefaultResourceLoader.discover` calls `ResolveContext`, so the context of `ReloadWith` ends the installs of a reload.

Why: a host that reloads a loader under a deadline, as Dyna's box does to check a Package before it keeps it, cannot otherwise stop the install: the reload's goroutine kept running npm or git, and wrote into a folder the host had already removed.

Locked by: `TestDefaultLoaderEndsAnInstallWithItsContext` (coding): a fake npm that hangs on `install` makes `ReloadWith` return the deadline's error within seconds, and the process is gone. `TestDefaultLoaderEndsAGitInstallWithItsContext` (coding) does the same for a git Package whose clone, checkout or dependency install hangs.

Upstream: not offered (owner's decision pending).

## P10 Inline extensions first

What: `DefaultResourceLoaderOptions.InlineExtensionsFirst` loads `ExtensionFactories` ahead of the Package and settings extensions instead of after them. Registrations are first-wins by tool name, so an inline extension's tool then wins a clash with a Package's.

Why: Pi loads `--extension` paths before Packages, so a host's own extension, which Dyna's `dyna.js` was in Pi, wins. Without the option a host had to reorder `ExtensionsOverride`'s result by the `<inline:` path convention.

Locked by: `TestDefaultResourceLoaderInlineExtensionsFirst` (coding): the order of the extension paths with and without the option.

Upstream: not offered (owner's decision pending).

## P11 A compaction head declares the executable tools; tool_search has a default limit

What: two parts.
(1) With `SessionOptions.ContextUpdates`, a compaction's head (`Session.appendCompaction` and the compaction draft of a session boundary, both through `Session.compactionHead`) declares the session's executable tools: its `toolsAdded` holds the schemas of `agent.state.tools`, not the tools the transcript declares. `AppendCompactionWithSections` takes the tools as a last argument; nil keeps the transcript's. Without `ContextUpdates` nothing changes.
(2) `toolsearch.Options.DefaultLimit` is the number of tools a `tool_search` call without a `limit` loads, and the number the model-visible schema states (`Defaults to N.`). Zero keeps `toolsearch.DefaultLimit` (8), so PiG's behaviour is unchanged without it. An explicit `limit` argument wins. `searchTools()` in codemode scripts only ranks, loads nothing, and keeps `toolsearch.DefaultLimit`.

Why: (1) a host's compaction hook (`session_before_compact` calling `ctx.SetActiveTools`) can unload a tool. The head named the transcript's tools, so the unloaded tool was declared again and then removed by a `toolsRemoved` note, and the saving arrived one compaction late. (2) Dyna wants a search to load 3 tools, and the schema text must not tell the model 8.

Files: `coding/session_context_updates.go`, `coding/session_boundaries.go`, `internal/codingagent/session.go`, `internal/codingagent/session_manager_projection.go`, `coding/extension/builtin/toolsearch/tool.go`.

Drop it: if upstream declares the executable tools in a compaction head, remove the `tools` argument and the `compactionHead` tools; if it makes the default limit configurable, use its option and delete `Options.DefaultLimit`.

Locked by: `TestACompactionHeadDeclaresTheToolsAHookLeftActive`, `TestACompactionHeadWithoutAHookDeclaresEveryTool`, `TestABoundaryCompactionHeadDeclaresTheExecutableTools` (coding), `TestToolSearchDefaultLimitStaysEightWithoutTheOption`, `TestToolSearchDefaultLimitOptionSetsTheLimitAndTheSchemaText`, `TestToolSearchExplicitLimitWinsOverTheDefaultLimitOption` (toolsearch), `TestSearchToolsKeepsTheDefaultLimitOfEight` (codemode).

Upstream: not offered (owner's decision, 2026-11-04).

## P12 Stall watchdog and detailed reasoning summaries

What: (1) `StreamOptions.StallTimeoutMs` bounds how long a model call may go without a semantic SSE event, connect and time to first byte included; the default is 180 s (a shorter configured `httpIdleTimeoutMs` wins), zero or less disables it. The watchdog is `sseIdleTransport`, the one transport every SSE provider client and every caller-supplied `Fetch` client goes through. A stall aborts the call with a typed cause that diagnostics record as `provider_transport_failure` with error name `StreamStallError`, and the call retries through the same path as a mid-stream `unexpected EOF`. Bedrock and the Codex WebSocket keep their own transports. (2) A Responses request to `openai` or `openai-codex` with a reasoning effort sends `reasoning.summary: "detailed"` when `ReasoningSummary` is empty; other providers keep `"auto"`.

Why: a sol call showed nothing for 18 minutes until the owner restarted, and sol thinking showed bold headers only.

Files: `ai/sse_event_idle.go`, `ai/http_transport.go`, `ai/transport_diagnostic.go`, `ai/types.go`, `ai/openai_responses.go`, `docs/parity/divergences/D131.md`, `D191.md`.

Locked by: `TestStreamStallAfterEventsAbortsAndRetriesOnce`, `TestStreamStallBeforeTheFirstByteAborts`, `TestStreamWithSteadyEventsIsNeverAborted`, `TestStreamStallTimerIsStoppedWhenTheStreamCompletes` (ai), `TestResponsesRequestAsksForADetailedReasoningSummaryByDefault` (ai).

Upstream: not offered (owner's decision, 2026-11-04).
