package auth

import (
	"crypto/rand"
	"fmt"

	"github.com/sgrankin/wave/internal/storage"
)

// signingKeyName is the SettingsStore key under which the session signing key is
// persisted.
const signingKeyName = "auth.session.signing-key"

// SigningKey returns the persisted session signing key, generating and storing a
// fresh random one on first use. The key must be stable across restarts —
// regenerating it invalidates every outstanding session cookie — so it lives in
// the SettingsStore rather than being minted per boot. With an ephemeral store
// (an in-memory database) the key is necessarily regenerated each run, which is
// acceptable: nothing persists across such a restart anyway.
func SigningKey(store storage.SettingsStore) ([]byte, error) {
	key, ok, err := store.GetSetting(signingKeyName)
	if err != nil {
		return nil, fmt.Errorf("auth: load signing key: %w", err)
	}
	if ok && len(key) >= minKeyLen {
		return key, nil
	}
	// Generate exactly minKeyLen bytes so a freshly minted key always passes the
	// len >= minKeyLen check above on the next load; using a separate constant
	// risked silently regenerating (and invalidating all sessions) every restart.
	key = make([]byte, minKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("auth: generate signing key: %w", err)
	}
	if err := store.PutSetting(signingKeyName, key); err != nil {
		return nil, fmt.Errorf("auth: persist signing key: %w", err)
	}
	return key, nil
}
