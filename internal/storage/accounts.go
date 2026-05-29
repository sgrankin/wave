package storage

import "github.com/sgrankin/wave/internal/id"

// AccountKind discriminates the two account subtypes (spec §8 AccountData).
type AccountKind string

const (
	// AccountHuman is a human-owned account.
	AccountHuman AccountKind = "human"
	// AccountRobot is a robot-agent account.
	AccountRobot AccountKind = "robot"
)

// PasswordDigest is a salted password hash (spec §8: digest = hash(salt ‖
// password)). The store persists these bytes opaquely; the hashing scheme and
// verification belong to the auth layer.
type PasswordDigest struct {
	Salt   []byte
	Digest []byte
}

// HumanAccount is the data for a human-owned account (spec §8 HumanAccountData).
type HumanAccount struct {
	// Password is the salted digest, or nil when password authentication is
	// disabled for this account (e.g. external/passkey-only).
	Password *PasswordDigest
	// Locale is a BCP-47 tag (e.g. "en"); empty if unset.
	Locale string
}

// RobotAccount is the data for a robot-agent account (spec §8 RobotAccountData).
type RobotAccount struct {
	URL            string // callback URL
	ConsumerSecret string // OAuth consumer secret
	Capabilities   []byte // opaque capabilities blob (nil until fetched)
	Verified       bool   // true once ownership is verified
}

// Account is a persisted account record keyed by ParticipantID. Exactly one of
// Human/Robot is set, matching Kind.
type Account struct {
	ID    id.ParticipantID
	Kind  AccountKind
	Human *HumanAccount
	Robot *RobotAccount
}

// AccountStore persists accounts keyed by ParticipantID (spec §5.2, §5.7). It is
// global (not per-wavelet).
type AccountStore interface {
	// GetAccount returns the account for pid, and whether it exists.
	GetAccount(pid id.ParticipantID) (*Account, bool, error)
	// PutAccount creates or replaces an account.
	PutAccount(a *Account) error
	// RemoveAccount deletes an account (no-op if absent).
	RemoveAccount(pid id.ParticipantID) error
}
