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
