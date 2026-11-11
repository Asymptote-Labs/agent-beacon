// Package threatrules implements the Beacon "Threat Rules" detection-rule standard: a
// YAML rule format whose match conditions are CEL expressions over the Beacon endpoint
// event, plus a reference evaluator and an embedded-fixture conformance harness.
//
// This package is the open reference implementation of the spec under
// spec/threat-rules. A separate (possibly closed) engine conforms if it produces the
// same Verdict each rule's fixtures declare.
//
// # cel-go coupling
//
// The evaluation fast path relies on details of cel-go (v0.30) beyond its public contract. Each
// is guarded by a test that fails if an upgrade changes it:
//
//   - fastobj.go reproduces the field semantics of ext.NativeTypes for objects read by field
//     index: getFieldValue (zero times, nil pointers), the CEL naming of fields, and the equality
//     of the extension's object values. Guarded by TestFastEnvIsInstalled and
//     TestFastPathChangesNothing.
//   - regexcall.go's matchesCall implements interpreter.InterpretableV2 and InterpretableCall and
//     evaluates its arguments on the ExecutionFrame as cel-go's own call does, so the cost
//     tracker sees the same argument stack and counts the same cost. Guarded by
//     TestFastPathChangesNothing (cost equality) and TestInvalidRegexErrorsAtEval.
//   - matcher.go's untracked evaluation relies on the checker's cost estimator (EstimateCost
//     with a SizeEstimator) bounding the tracked cost, and on the overload IDs listed in
//     untrackedOverloads. Guarded by TestUntrackedCostIsBounded, TestUntrackedShape and
//     TestScanAgreesWithReferenceAtLowCostLimits.
package threatrules
