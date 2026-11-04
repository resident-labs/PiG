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
