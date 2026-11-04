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
