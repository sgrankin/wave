package codec

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/version"
	"github.com/sgrankin/wave/internal/waveop"
)

// This file adds the client-facing payload encodings used by the live transport
// (a submitted client delta, and a bare hashed version). They share the frozen
// CoreDeterministic encoder and the op/context wire helpers in codec.go, so
// operation bytes are identical to those that feed the hash chain. The transport
// envelope that carries these payloads lives in package transport and is NOT
// frozen — only these payloads are.

// ClientDelta is an unapplied delta a client submits: the author, the wavelet
// version it targets, and the operations. Unlike StoredDelta it carries a
// target version (not a resulting one) and no timestamp — the server stamps the
// application time on apply.
type ClientDelta struct {
	Author        id.ParticipantID
	TargetVersion version.HashedVersion
	Ops           []waveop.Operation
}

// EncodeClientDelta returns the canonical CBOR encoding of a client delta:
// [author, targetVersion, ops].
func EncodeClientDelta(d ClientDelta) []byte {
	return marshal([]any{
		d.Author.Address(),
		wireHV{Version: d.TargetVersion.Version(), Hash: d.TargetVersion.HistoryHash()},
		opsValue(d.Ops),
	})
}

// DecodeClientDelta parses a client delta encoding.
func DecodeClientDelta(data []byte) (ClientDelta, error) {
	var raw []cbor.RawMessage
	if err := cbor.Unmarshal(data, &raw); err != nil {
		return ClientDelta{}, err
	}
	if len(raw) != 3 {
		return ClientDelta{}, fmt.Errorf("codec: client delta has %d fields, want 3", len(raw))
	}
	var addr string
	if err := cbor.Unmarshal(raw[0], &addr); err != nil {
		return ClientDelta{}, err
	}
	author, err := id.NewParticipantID(addr)
	if err != nil {
		return ClientDelta{}, err
	}
	var hv wireHV
	if err := cbor.Unmarshal(raw[1], &hv); err != nil {
		return ClientDelta{}, err
	}
	var opsRaw []cbor.RawMessage
	if err := cbor.Unmarshal(raw[2], &opsRaw); err != nil {
		return ClientDelta{}, err
	}
	ops, err := opsFrom(opsRaw)
	if err != nil {
		return ClientDelta{}, err
	}
	return ClientDelta{
		Author:        author,
		TargetVersion: version.NewHashedVersion(hv.Version, hv.Hash),
		Ops:           ops,
	}, nil
}

// EncodeHashedVersion returns the canonical CBOR encoding of a hashed version
// ([version, hash]). Used on the wire to report a submit's resulting version.
func EncodeHashedVersion(hv version.HashedVersion) []byte {
	return marshal(wireHV{Version: hv.Version(), Hash: hv.HistoryHash()})
}

// DecodeHashedVersion parses a hashed version encoding.
func DecodeHashedVersion(data []byte) (version.HashedVersion, error) {
	var hv wireHV
	if err := cbor.Unmarshal(data, &hv); err != nil {
		return version.HashedVersion{}, err
	}
	return version.NewHashedVersion(hv.Version, hv.Hash), nil
}
