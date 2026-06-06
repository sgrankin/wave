package cc

// Ported from Java ResponseCodeTest:
//   wave/src/test/java/org/waveprotocol/wave/concurrencycontrol/common/ResponseCodeTest.java
//
// The Java test exercises the client-CC ResponseCode enum, whose machinery
// (UNKNOWN(-1) sentinel + of(int)/getValue() reflection) is Java-enum-specific
// and deliberately absent from our port: cc.ResponseCode is a plain typed int
// constant set whose values ARE the wire integers. The load-bearing semantics —
// the integer value each code maps to — is the proto ResponseStatus.ResponseCode
// (clientserver.proto) and the spec table (docs/specs/03-concurrency-control.md
// §ResponseCode), which start at OK=0 with NO UNKNOWN(-1). We assert that mapping
// directly. The of(int)/IndexOutOfBounds behavior is recorded as skipped (no such
// API in our port).

import "testing"

// TestResponseCodeWireValues asserts each ResponseCode constant maps to the wire
// integer value prescribed by the proto enum and the spec table. This is the
// intent of ResponseCodeTest.testOf (Java's getValue() round-trip), adapted to
// our plain-int-constant representation.
func TestResponseCodeWireValues(t *testing.T) {
	cases := []struct {
		code ResponseCode
		want int
		name string
	}{
		{OK, 0, "OK"},
		{BadRequest, 1, "BAD_REQUEST"},
		{InternalError, 2, "INTERNAL_ERROR"},
		{NotAuthorized, 3, "NOT_AUTHORIZED"},
		{VersionError, 4, "VERSION_ERROR"},
		{InvalidOperation, 5, "INVALID_OPERATION"},
		{SchemaViolation, 6, "SCHEMA_VIOLATION"},
		{SizeLimitExceeded, 7, "SIZE_LIMIT_EXCEEDED"},
		{PolicyViolation, 8, "POLICY_VIOLATION"},
		{Quarantined, 9, "QUARANTINED"},
		{TooOld, 10, "TOO_OLD"},
	}
	for _, c := range cases {
		if int(c.code) != c.want {
			t.Errorf("%s = %d, want %d", c.name, int(c.code), c.want)
		}
	}

	// The constants must be a contiguous block 0..10 (matching the proto/spec) so
	// that the wire mapping has no gaps — this is the property the Java of(int)
	// round-trip implicitly relied on.
	if TooOld-OK != 10 {
		t.Errorf("code range OK..TooOld spans %d, want 10 contiguous values", TooOld-OK)
	}
}

// TestResponseCodeOfDropped documents the dropped Java enum machinery.
func TestResponseCodeOfDropped(t *testing.T) {
	t.Skip("ADAPT: no ResponseCode.of(int)/getValue()/UNKNOWN(-1) in our port; " +
		"cc.ResponseCode is a plain int-constant set (no -1 sentinel, no reflective " +
		"of()/IndexOutOfBounds). Wire mapping is asserted by TestResponseCodeWireValues.")
}
