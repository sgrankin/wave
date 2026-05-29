// Package cc is server-side concurrency control: it validates an incoming
// client delta against the wavelet's history, transforms it to the current head
// version (Jupiter "transform to head"), and exposes the committed delta log.
// It rides on the operational transform (package waveop/op) and applies via the
// wavelet data model (package wavelet).
//
// This package currently implements the server side (the critical path for the
// edit loop). Client-side CC (in-flight tracking, reconnection) is a separate
// increment tied to the transport.
//
// Spec: docs/specs/03-concurrency-control.md.
package cc

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// TransformedWaveletDelta is a delta after server transformation and
// application: the operations as applied, the resulting (post-application)
// hashed version, and the application timestamp. (Ports TransformedWaveletDelta.)
type TransformedWaveletDelta struct {
	Author           id.ParticipantID
	ResultingVersion version.HashedVersion
	Timestamp        int64
	Ops              []waveop.Operation
}

// AppliedAtVersion is the version the delta was applied at: the resulting
// version minus the operation count. It is computed, not stored.
func (d TransformedWaveletDelta) AppliedAtVersion() uint64 {
	return d.ResultingVersion.Version() - uint64(len(d.Ops))
}

// ResponseCode is the OT error taxonomy (spec §ResponseCode). It is the
// vocabulary the concurrency layer reasons in; how it reaches the wire (free
// text vs structured) is a transport decision (spec 04).
type ResponseCode int

const (
	// OK indicates success.
	OK ResponseCode = iota
	// BadRequest indicates a malformed delta.
	BadRequest
	// InternalError indicates a server-side failure.
	InternalError
	// NotAuthorized indicates the author is not a participant or access is denied.
	NotAuthorized
	// VersionError indicates the target version/hash does not match history.
	VersionError
	// InvalidOperation indicates an op invalid before, during, or after transform.
	InvalidOperation
	// SchemaViolation indicates an op violates the document schema.
	SchemaViolation
	// SizeLimitExceeded indicates the delta or resulting document is too large.
	SizeLimitExceeded
	// PolicyViolation indicates rejection by namespace policy.
	PolicyViolation
	// Quarantined indicates the object is quarantined.
	Quarantined
	// TooOld indicates the target version is too far behind; reconnect and retry.
	TooOld
)

// Error is a concurrency-control failure carrying a ResponseCode.
type Error struct {
	Code ResponseCode
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	s := "cc: " + e.Msg
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// DeltaHistory is read access to a wavelet's committed delta log.
type DeltaHistory interface {
	// CurrentVersion returns the wavelet's current version number.
	CurrentVersion() uint64
	// CurrentHashedVersion returns the current hashed version.
	CurrentHashedVersion() version.HashedVersion
	// DeltaStartingAt returns the committed delta applied at the given version,
	// and whether one exists.
	DeltaStartingAt(version uint64) (TransformedWaveletDelta, bool)
	// HasSignature reports whether hv is a version the history has reached
	// (matching both version number and hash).
	HasSignature(hv version.HashedVersion) bool
}

// MemoryHistory is an in-memory DeltaHistory: a linear log of committed deltas
// keyed by the version each was applied at, plus the known signatures.
type MemoryHistory struct {
	deltas  map[uint64]TransformedWaveletDelta // appliedAtVersion -> delta
	sigs    map[uint64]version.HashedVersion   // version -> signature
	current version.HashedVersion
}

// NewMemoryHistory creates an empty history starting at the given version-zero
// (genesis) signature.
func NewMemoryHistory(zero version.HashedVersion) *MemoryHistory {
	h := &MemoryHistory{
		deltas:  map[uint64]TransformedWaveletDelta{},
		sigs:    map[uint64]version.HashedVersion{},
		current: zero,
	}
	h.sigs[zero.Version()] = zero
	return h
}

// Append records a committed delta. Its AppliedAtVersion must equal the current
// version; it advances the current version to the delta's resulting version.
// Empty deltas (no ops) are not recorded and do not advance the version.
//
// A non-contiguous append (applied-at version != current) is a programming
// error — it would silently break the history chain — so it panics rather than
// corrupt state. This also catches an underflowed AppliedAtVersion (a delta with
// more ops than its resulting version), which can never equal current.
func (h *MemoryHistory) Append(d TransformedWaveletDelta) {
	if len(d.Ops) == 0 {
		return
	}
	if d.AppliedAtVersion() != h.current.Version() {
		panic(fmt.Sprintf("cc: non-contiguous history append: delta applied at %d, current %d",
			d.AppliedAtVersion(), h.current.Version()))
	}
	h.deltas[d.AppliedAtVersion()] = d
	h.sigs[d.ResultingVersion.Version()] = d.ResultingVersion
	h.current = d.ResultingVersion
}

// CurrentVersion returns the current version number.
func (h *MemoryHistory) CurrentVersion() uint64 { return h.current.Version() }

// CurrentHashedVersion returns the current hashed version.
func (h *MemoryHistory) CurrentHashedVersion() version.HashedVersion { return h.current }

// DeltaStartingAt returns the delta applied at the given version.
func (h *MemoryHistory) DeltaStartingAt(v uint64) (TransformedWaveletDelta, bool) {
	d, ok := h.deltas[v]
	return d, ok
}

// HasSignature reports whether hv matches a known version in the history.
func (h *MemoryHistory) HasSignature(hv version.HashedVersion) bool {
	known, ok := h.sigs[hv.Version()]
	return ok && known.Compare(hv) == 0
}
