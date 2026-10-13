# Keep plugin events containing an invalid Date

## Context
Pi and Cline flatten live SDK event objects before serializing. safeClone calls Date.toISOString even for an invalid Date, which raises RangeError. The host-safety backstop then drops the entire telemetry event, including useful tool output and session context. Native JSON serialization represents an invalid Date as null.

## Decision
Represent an invalid Date as null in both plugin serializers and their embedded CLI asset mirrors. Preserve the existing ISO spelling for valid dates. Keep cycle/depth/error/bigint handling and fail-open host behavior unchanged.

## Alternatives
Dropping the entire event loses otherwise valid activity. Inventing a timestamp makes unavailable data look real. Removing dates entirely also drops valid timestamps. Fixing one source copy leaves installed plugin behavior divergent.

## Consequences
An unavailable timestamp is visibly null while other fields survive. Healthy timestamps and plugin handler return values retain their behavior. This does not claim recovery from arbitrary throwing getters or custom Date subclasses.

## Validation
Same owned public-plugin fixtures on frozen abc85c6884e0ad6bcf9daf58093d31c6e5018ba9:2valid-date controls pass/2invalid-date regressions fail with zero sent events. Candidate Pi+Cline suites27pass0fail/61assertions on isolatedBun1.4.3. Bun no-bundle build and exact source/embedded mirror comparisons pass. Tests use injected local senders, no installed client, model/provider calls or real hook process. Go embedded-asset test outcome is recorded separately if available; no hosted or actual client validation claimed. Raw logs remain local.

The first Go asset check did not collect because a fresh clone lacks ignored internal/embedded/hooks.bin. The repository's documented Makefile ensure-placeholder target creates a build-only placeholder, which was then used for asset tests; no real hook binary/client execution is implied. The setup failure and final outcome remain separately logged.

Final Go Pi/Cline asset tests pass (Go1.27.2,0.851s) after the documented placeholder prerequisite. Initial Bun multi-entry invocation required an outdir; rerunning the documented single-entry no-bundle build separately for each plugin passes. Both command errors are retained, and no generated binary/source is published.

## Producer admission evidence
Pi pinned3daa20280f6a7c7cbe0935680c9a38fdc3863005 defines custom ToolResultEvent.details as unknown and emits result.details directly into extension callbacks. The fixture now supplies the actual required call id, input, content and error fields for a custom clock tool. Sources: https://github.com/earendil-works/pi/blob/3daa20280f6a7c7cbe0935680c9a38fdc3863005/packages/coding-agent/src/core/extensions/types.ts and https://github.com/earendil-works/pi/blob/3daa20280f6a7c7cbe0935680c9a38fdc3863005/packages/coding-agent/src/core/agent-session.ts.

Cline pinneded469c753a0d7a0d41f906cf3fc75a983cd766ac defines AgentToolResult output as generic/unknown. AgentRuntime assigns the custom tool's returned object directly to result.output and passes it to in-process afterTool callbacks, with native Date startedAt/endedAt fields. The Cline fixture now uses that result.output shape. Sources: https://github.com/cline/cline/blob/ed469c753a0d7a0d41f906cf3fc75a983cd766ac/sdk/packages/shared/src/agent.ts and https://github.com/cline/cline/blob/ed469c753a0d7a0d41f906cf3fc75a983cd766ac/sdk/packages/agents/src/agent-runtime.ts.

Thus a custom tool returning an unavailable Date in metadata is admissible on the documented in-process paths; this is not a claim that built-in tools produce invalid dates or that every sandboxed/JSON-mediated client retains Date objects. No SDK/model runtime was launched; primary source inspection plus the actual public Beacon callbacks establish this bounded admission case. Previous frozenfa165044d326123133129f1bb53fb7b3bc070398 and generic-shape evidence remain historical; final compatible-shape baseline/candidate outcomes are separately retained.

Final compatible-shape replay: frozen upstream2controls pass/2invalid-date regressions fail; complete candidate Pi/Cline27tests pass/61assertions. Implementation and embedded mirrors are unchanged from the original frozen candidate; producer grounding and fixtures are the reviewed correction.
