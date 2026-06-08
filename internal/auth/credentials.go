package auth

import (
	"fmt"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

// resolveCredential maps a verified (method, subject) — a GitHub numeric id, an
// OIDC issuer+sub, a passkey credential id — to the Wave account it is bound to.
// The proof is NOT an address (unlike the address-asserting providers in
// auth.go), so interactive login flows look the address up here rather than
// reading it off the request. ok is false when no binding exists yet (the caller
// then provisions one under its MintPolicy and records it with bindCredential).
//
// See docs/architecture/04-auth-model.md §3.
func resolveCredential(store storage.CredentialStore, method, subject string) (id.ParticipantID, bool, error) {
	c, ok, err := store.GetCredential(method, subject)
	if err != nil {
		return id.ParticipantID{}, false, fmt.Errorf("auth: resolve credential %s: %w", method, err)
	}
	if !ok {
		return id.ParticipantID{}, false, nil
	}
	return c.Account, true, nil
}

// bindCredential records that (method, subject) authenticates as account, with
// optional method-specific JSON data (issuer, public key, …). It is the write
// side of resolveCredential: a fresh login that just minted/derived an address
// persists the binding so the next login resolves directly to it. createdAt is
// the unix-seconds timestamp of first binding.
func bindCredential(store storage.CredentialStore, method, subject string, account id.ParticipantID, data string, createdAt int64) error {
	if err := store.PutCredential(storage.Credential{
		Method:    method,
		Subject:   subject,
		Account:   account,
		Data:      data,
		CreatedAt: createdAt,
	}); err != nil {
		return fmt.Errorf("auth: bind credential %s: %w", method, err)
	}
	return nil
}
