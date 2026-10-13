# End SDK spans when async stream consumption stops

## Context
The SDK wraps Claude Agent SDK async iterables and promises to keep their spans open while consumed. An early consumer break runs the underlying iterator cleanup but bypasses the wrapper's success/error branches, so its span never ends or exports.

## Decision
Finalize non-error streams in the async generator's finally block. Preserve existing exception recording and error finalization exactly once; retain success status only on complete consumption. Early stop ends the span without claiming that the source completed successfully.

## Alternatives
Ending on the first yielded message loses stream lifetime. Marking every early stop an error changes caller cancellation semantics. Reimplementing AsyncIterator manually would widen the wrapper contract unnecessarily.

## Consequences
Consumer break or return releases the observed span as the underlying iterator closes. Completed and failing streams retain existing status behavior. A stream never consumed has no generator-finalization event and is outside this correction.

## Validation
Frozen upstream abc85c6884e0ad6bcf9daf58093d31c6e5018ba9 with the same regression:1failure33filtered cases; underlying cleanup ran but no finished span exported. Candidate SDK suite34pass0skip and TypeScript noEmit pass using Node24.19.0/Vitest4.1.11. Fixtures use in-memory exporter and owned local HTTP stub; no model calls or provider spending. Logs remain local. No real Claude sandbox/client path tested; sandbox scenarios require separate money authorization and Linux, as AGENTS describes.
