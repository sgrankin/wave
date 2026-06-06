package waveop_test

// Conformance suites from the Java OT operation tests that are not yet ported as
// conformance tests here. Each is recorded as an explicit skip so the gap is
// visible in the test run and traceable to its Java source.
//
// Scope note: this package (waveop) models wavelet-operation STRUCTURE and the
// Jupiter TRANSFORM only. FORWARD application of these ops to wavelet/blip state
// already exists in internal/wavelet (see apply.go, ad-hoc covered by
// apply_test.go) — so the forward-apply halves of the suites below ARE portable
// today and should be ported as conformance tests into internal/wavelet (tracked
// separately). What is genuinely absent from internal/ is op INVERSION
// (applyAndReturnReverse / getInverse / createReverseContext) and features dropped
// from the port (VersionUpdateOp, SubmitBlip, the separate "core" op layer); the
// cases depending on those are real gaps.

import "testing"

func TestConformanceAddParticipantSuite(t *testing.T) {
	t.Skip("PORTED ELSEWHERE: AddParticipantTest's forward-apply (add/remove with " +
		"duplicate/absent errors) is ported in " +
		"internal/wavelet/conformance_addparticipant_test.go. Only the " +
		"applyAndReturnReverse (op inversion) half is a genuine gap (absent from " +
		"internal/).")
}

func TestConformanceBlipOperationApplySuite(t *testing.T) {
	t.Skip("PORTED ELSEWHERE: BlipOperationTest's apply/contributor/timestamp forward " +
		"path is ported in internal/wavelet/conformance_blipop_test.go; only the " +
		"reverse (applyAndReturnReverse) half is a genuine gap. Portable structure " +
		"parts (getContext, sample-op worthiness, type dispatch) are in this " +
		"package's conformance_blipop_test.go.")
}

func TestConformanceBlipContentOperationApplySuite(t *testing.T) {
	t.Skip("PORTED ELSEWHERE: BlipContentOperationTest's forward-apply (compose into " +
		"blip content, contributor update) is ported in " +
		"internal/wavelet/conformance_blipcontentop_test.go; only the reverse half " +
		"is a genuine gap (no op inversion in internal/).")
}

func TestConformanceVersionUpdateOpSuite(t *testing.T) {
	t.Skip("CONFORMANCE GAP: VersionUpdateOpTest depends on createVersionUpdateOp, " +
		"SubmitBlip, and applying ops to a WaveletData (version/signature/blip " +
		"version metadata). VersionUpdateOp and SubmitBlip are genuinely absent " +
		"(dropped from the port).")
}

func TestConformanceWaveletOperationSuite(t *testing.T) {
	t.Skip("PORTED ELSEWHERE: WaveletOperationTest's apply() metadata side-effects " +
		"(timestamp/version) on wavelet state are ported in " +
		"internal/wavelet/conformance_waveletop_test.go (the signature/hashed-version " +
		"cases diverge — recorded there as CONFORMANCE DIVERGENCE skips); " +
		"createReverseContext / inversion is a genuine gap.")
}

func TestConformanceCoreWaveletOperationInverseSuite(t *testing.T) {
	t.Skip("CONFORMANCE GAP: CoreWaveletOperationTest exercises getInverse() round-trip " +
		"inversion. We have a single op set (no separate core layer) and no wavelet-op " +
		"inversion (genuine gap). The equality matrix from CoreWaveletOperationEqualsTest " +
		"IS ported in conformance_equality_test.go.")
}
