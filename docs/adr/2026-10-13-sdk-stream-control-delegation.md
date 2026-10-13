# Preserve stream controls while observing async iteration

## Context
wrapClaudeAgentQuery keeps the original query function's static return type, but substitutes a bare async generator for its result. A stream with interrupt/control methods therefore loses them. This is distinct from finalizing an early-ended span: callers cannot invoke the real stream's controls at all.

## Decision
Use a proxy over the traced iterator. Route only asyncIterator/next/return/throw to the traced iterator, with stable bound methods and a self-returning asyncIterator. Delegate other reads/getters to the original stream, binding and caching functions to its original receiver. Constructor identity is not rebound. Proxy target is the wrapper, so readonly/non-configurable properties on the original stream do not violate proxy get invariants when control methods are bound.

## Alternatives
Returning the original object loses tracing. Copying enumerable fields misses prototype controls and private-field getters. A proxy over the original object cannot override its frozen iterator/control properties safely. Hard-coding known control names misses compatible stream implementations and future control methods.

## Consequences
Controls operate on the real source, including private fields, while iterator operations continue through tracing. Repeated control reads have stable callable identity. This remains an iterator adapter: it does not promise original object identity, prototype reflection, own-property enumeration or forwarding arbitrary property writes. Those were already absent in the prior bare-generator wrapper. Early-return span finalization is handled separately; no duplicate fix is included here.

## Validation
Same final regressions on frozen abc85c6884e0ad6bcf9daf58093d31c6e5018ba9 fail for lost controls. Candidate complete SDK suite36pass0skip and TypeScriptnoEmit pass using Node24.19.0/Vitest4.1.11. Controlled streams exercise private-field getters/control receivers, stable control identity, readonly control properties, real-source interruption, direct next/throw, cleanup and error-span status. Tests execute the public wrapper with in-memory exporters and owned localHTTP stubs, not a mock replacement of the wrapper. No provider/model call or paid sandbox; actual ClaudeSDK/model session remains unverified. Logs remain local.
