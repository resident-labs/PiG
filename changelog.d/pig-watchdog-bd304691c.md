### Fixed
- Abort HTTP SSE model calls after 180 seconds without semantic progress and send the typed failure through existing retries (D131). Positive `StreamOptions.StallTimeoutMs` values replace the default; zero or less disables only the new budget. Preserve legacy idle timers and non-stream request contexts, close stalled custom Fetch bodies, and stop timers on completion. Keep the request budget and cancellation active for non-SSE error bodies, and preserve typed body failures through provider error handling without classifying ordinary HTTP error text as a stall. <!-- fix: d6eafcdc9c6169cb2dc2fcd38a12e9d54f465b46,bd304691c29c13280a24f3a394229507d176f917,a982e300b7f7c9b55bb5b243d14b00043eed29c7 -->

### Changed
- OpenAI and Codex Responses reasoning requests default `reasoning.summary` to `"detailed"` (D192). Explicit summary modes and other Responses providers retain their previous behavior.

### Known issues
- The SSE stall watchdog does not cover Amazon Bedrock event streams or Codex WebSockets (D131).
