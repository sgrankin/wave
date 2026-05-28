// Package waveop defines wavelet-level operations — the units that make up a
// wavelet delta — and the Jupiter-style transform over them. A wavelet
// operation either mutates a blip's document (a WaveletBlipOperation wrapping a
// BlipContentOperation, whose inner DocOp is transformed by package op),
// changes the participant set (AddParticipant / RemoveParticipant), or does
// nothing (NoOp).
//
// Applying operations to wavelet data depends on the wavelet data model (a
// later phase); this package models operation structure and transform, which
// together with package op form the OT core.
//
// Spec: docs/specs/02-operational-transform.md §Wavelet-level transform.
package waveop

import (
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/version"
)

// NoTimestamp marks a context that carries no timestamp (Constants.NO_TIMESTAMP).
const NoTimestamp int64 = -1

// Context is the metadata attached to a wavelet operation: who created it, when,
// how many versions it advances, and optionally the resulting hashed version.
// (Ports WaveletOperationContext.)
type Context struct {
	Creator          id.ParticipantID
	Timestamp        int64
	VersionIncrement int64
	HashedVersion    *version.HashedVersion // nil if absent
}

// HasTimestamp reports whether the context carries a meaningful timestamp.
func (c Context) HasTimestamp() bool { return c.Timestamp != NoTimestamp }

// HasHashedVersion reports whether the context carries a resulting hashed version.
func (c Context) HasHashedVersion() bool { return c.HashedVersion != nil }

// Operation is a wavelet-level operation. The concrete types are
// WaveletBlipOperation, AddParticipant, RemoveParticipant, and NoOp. The
// interface is sealed (only this package can implement it).
type Operation interface {
	// Context returns the operation's metadata.
	Context() Context
	isWaveletOperation()
}

// BlipOperation is an operation on a single blip within a wavelet. The
// transformed kind is BlipContentOperation (which wraps a DocOp); the interface
// is sealed.
type BlipOperation interface {
	Context() Context
	isBlipOperation()
}

// UpdateContributorMethod selects how a blip-content operation updates the
// blip's contributor list when applied (ports BlipOperation.UpdateContributorMethod).
type UpdateContributorMethod int

const (
	// ContributorAdd adds the author to the contributor list if absent.
	ContributorAdd UpdateContributorMethod = iota
	// ContributorRemove removes the author if present.
	ContributorRemove
	// ContributorNone leaves the contributor list unchanged.
	ContributorNone
)

// BlipContentOperation boxes a document operation as a blip operation; applying
// it feeds ContentOp to the blip's content (ports BlipContentOperation). Method
// is the contributor-list update method used on apply.
type BlipContentOperation struct {
	Ctx       Context
	ContentOp op.DocOp
	Method    UpdateContributorMethod
}

// Context returns the operation's metadata.
func (b BlipContentOperation) Context() Context { return b.Ctx }
func (b BlipContentOperation) isBlipOperation() {}

// WaveletBlipOperation applies a blip operation to an identified blip within the
// wavelet (ports WaveletBlipOperation). Its context is the contained blip
// operation's context.
type WaveletBlipOperation struct {
	BlipID string
	BlipOp BlipOperation
}

// Context returns the contained blip operation's context.
func (w WaveletBlipOperation) Context() Context    { return w.BlipOp.Context() }
func (w WaveletBlipOperation) isWaveletOperation() {}

// AddParticipant adds a participant to the wavelet (ports AddParticipant).
type AddParticipant struct {
	Ctx         Context
	Participant id.ParticipantID
}

// Context returns the operation's metadata.
func (a AddParticipant) Context() Context    { return a.Ctx }
func (a AddParticipant) isWaveletOperation() {}

// RemoveParticipant removes a participant from the wavelet (ports RemoveParticipant).
type RemoveParticipant struct {
	Ctx         Context
	Participant id.ParticipantID
}

// Context returns the operation's metadata.
func (r RemoveParticipant) Context() Context    { return r.Ctx }
func (r RemoveParticipant) isWaveletOperation() {}

// NoOp does nothing; it is produced when concurrent participant operations
// cancel (ports NoOp).
type NoOp struct {
	Ctx Context
}

// Context returns the operation's metadata.
func (n NoOp) Context() Context    { return n.Ctx }
func (n NoOp) isWaveletOperation() {}
