### Fixed
- Bound semantic idleness only on native OpenAI Responses requests with provider ID `openai` and no custom Fetch (D131). The independent request-start budget defaults to 180 seconds, covers connection establishment and non-SSE error bodies, and sends a typed failure through existing retry/backoff. Positive `StreamOptions.StallTimeoutMs` overrides the budget; zero or less disables it. EOF and Close release the new timer and cancellation callback. Existing legacy D131 behavior and diagnostics remain unchanged. <!-- fix: d6eafcdc9c6169cb2dc2fcd38a12e9d54f465b46,bd304691c29c13280a24f3a394229507d176f917,a982e300b7f7c9b55bb5b243d14b00043eed29c7,0b85b749d628dbf42d4d62ad512864daf864896c -->

### Changed
- Only the same native OpenAI Responses route defaults `reasoning.summary` to `"detailed"` (D192). Explicit summaries win. Marginal summary cost is not measured.

### Known issues
- Anthropic, Completions, Azure, Codex SSE/WebSocket, Google/Vertex, Mistral, pi-messages, Bedrock, other Responses providers, images/non-stream requests and all custom Fetch retain fork-base behavior. They receive neither the new watchdog nor the detailed summary default.
