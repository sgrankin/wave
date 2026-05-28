package waveop

import (
	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/version"
)

// WaveletDelta is an immutable, ordered group of operations from a single
// author targeting a particular wavelet version (ports WaveletDelta). Each
// operation advances the wavelet by one version.
type WaveletDelta struct {
	author        id.ParticipantID
	targetVersion version.HashedVersion
	ops           []Operation
}

// NewWaveletDelta builds a delta, copying ops so the result is immutable.
func NewWaveletDelta(author id.ParticipantID, targetVersion version.HashedVersion, ops []Operation) WaveletDelta {
	return WaveletDelta{
		author:        author,
		targetVersion: targetVersion,
		ops:           append([]Operation(nil), ops...),
	}
}

// Author returns the author of the delta's operations.
func (d WaveletDelta) Author() id.ParticipantID { return d.author }

// TargetVersion returns the wavelet version the delta applies to.
func (d WaveletDelta) TargetVersion() version.HashedVersion { return d.targetVersion }

// Len returns the number of operations.
func (d WaveletDelta) Len() int { return len(d.ops) }

// Op returns the i-th operation.
func (d WaveletDelta) Op(i int) Operation { return d.ops[i] }

// Ops returns the operations in order (a copy).
func (d WaveletDelta) Ops() []Operation { return append([]Operation(nil), d.ops...) }

// ResultingVersion is the wavelet version after applying this delta: the target
// version plus one increment per operation.
func (d WaveletDelta) ResultingVersion() uint64 {
	return d.targetVersion.Version() + uint64(len(d.ops))
}
