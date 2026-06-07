package storage

// SettingsStore persists small singleton server settings as opaque key→bytes —
// configuration that must survive a restart but is not part of the hash-chained
// wave data. The motivating use is the session signing key (auth), which must be
// stable across restarts (regenerating it logs everyone out). Values are opaque
// blobs; the store does not interpret them and does not use the frozen delta codec.
//
// It is global (not per-wavelet), like AccountStore.
type SettingsStore interface {
	// GetSetting returns the value for key, and whether it exists.
	GetSetting(key string) (value []byte, ok bool, err error)
	// PutSetting creates or replaces the value for key.
	PutSetting(key string, value []byte) error
}
