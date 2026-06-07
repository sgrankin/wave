// Package profileapi serves participant profiles — the human-readable display
// names shown in place of raw addresses — as authenticated JSON HTTP endpoints
// over the account store. The address is always the identity; a profile is
// presentation metadata layered on top, so a missing or empty profile is benign
// (the client falls back to the address).
//
// Mount Routes() behind auth.Service.Middleware so a session is required and the
// authenticated participant is bound to the request; setProfile writes only the
// caller's own account (read from the injected identify func).
package profileapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sgrankin/wave/internal/id"
	"github.com/sgrankin/wave/internal/storage"
)

const (
	// maxProfilesPerRequest bounds the per-request account fan-out (each ?addr=
	// is one store lookup). The roster/inbox views request well under this.
	maxProfilesPerRequest = 256
	// maxDisplayNameRunes caps a display name (presentation only — a generous
	// bound that still stops a runaway value from being stored or rendered).
	maxDisplayNameRunes = 128
)

// Accounts is the subset of storage.AccountStore this API needs (satisfied by
// *sqlite.Store): read any account for a profile, write the caller's own.
type Accounts interface {
	GetAccount(pid id.ParticipantID) (*storage.Account, bool, error)
	PutAccount(a *storage.Account) error
}

// Profile is one participant's public presentation metadata.
type Profile struct {
	Address     string `json:"address"`
	DisplayName string `json:"displayName"`
}

// Handler serves GET /api/profiles and POST /api/profile.
type Handler struct {
	accounts Accounts
	identify func(*http.Request) (id.ParticipantID, bool)
	logger   *slog.Logger
}

// New builds a Handler over the account store. identify resolves the
// authenticated participant from the request (e.g. auth.ParticipantFrom when
// mounted behind auth.Service.Middleware). A nil logger uses slog.Default().
func New(accounts Accounts, identify func(*http.Request) (id.ParticipantID, bool), logger *slog.Logger) *Handler {
	return &Handler{accounts: accounts, identify: identify, logger: logger}
}

func (h *Handler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}

// Routes returns the API mux (GET /api/profiles, POST /api/profile). Mount it
// behind the authentication middleware.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/profiles", h.getProfiles)
	mux.HandleFunc("POST /api/profile", h.setProfile)
	return mux
}

// getProfiles batch-resolves profiles for the addresses in repeated ?addr=
// params: GET /api/profiles?addr=alice@example.com&addr=bob@example.com. It
// returns one entry per *valid* requested address (display name possibly empty
// when the account is unknown or unnamed), so the client can cache the result
// and avoid refetching; malformed addresses are skipped. A store read error for
// one address is logged and that address is skipped, never failing the batch.
func (h *Handler) getProfiles(w http.ResponseWriter, r *http.Request) {
	addrs := r.URL.Query()["addr"]
	if len(addrs) > maxProfilesPerRequest {
		addrs = addrs[:maxProfilesPerRequest]
	}
	profiles := make([]Profile, 0, len(addrs))
	// Dedupe on the canonical address so a request padded with duplicates does not
	// issue a store read (and emit a response entry) per copy.
	seen := make(map[string]struct{}, len(addrs))
	for _, addr := range addrs {
		pid, err := id.NewParticipantID(addr)
		if err != nil {
			continue // malformed address: benign skip
		}
		if _, dup := seen[pid.Address()]; dup {
			continue
		}
		seen[pid.Address()] = struct{}{}
		name := ""
		acct, ok, err := h.accounts.GetAccount(pid)
		if err != nil {
			h.log().Warn("profileapi: skipping address that failed to load", "address", addr, "err", err)
			continue
		}
		if ok && acct.Human != nil {
			name = acct.Human.DisplayName
		}
		profiles = append(profiles, Profile{Address: pid.Address(), DisplayName: name})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"profiles": profiles})
}

// setProfile updates the authenticated participant's own display name from a
// JSON body {"displayName": "..."}. The name is trimmed and length-capped; an
// empty value clears it. Auto-provisions a minimal human account if none exists
// (mirroring login provisioning), and refuses to write a human profile onto a
// robot account. Returns 204 on success.
//
// The read-modify-write of the account is last-writer-wins (no transaction): a
// concurrent writer of the SAME account between GetAccount and PutAccount would
// be clobbered. Acceptable here — only DisplayName is mutated, the window is tiny,
// and at single-machine scale a self-edit racing another self-edit is benign. The
// !ok auto-provision branch is defensive: behind the normal auth middleware the
// account already exists (Provisioner.Ensure ran at login), but the handler does
// not assume that.
func (h *Handler) setProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identify(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Cap the body before decoding so an over-long name is rejected at read time
	// rather than after fully buffering it (the rune cap below runs too late to
	// bound memory). A few KB easily holds a 128-rune name plus JSON framing.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if len([]rune(name)) > maxDisplayNameRunes {
		http.Error(w, "displayName too long", http.StatusBadRequest)
		return
	}
	acct, ok, err := h.accounts.GetAccount(p)
	if err != nil {
		// Log the detail; return a generic message so storage internals do not leak
		// to the client (mirroring getProfiles' log-and-skip discipline).
		h.log().Error("profileapi: get account", "participant", p, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch {
	case !ok:
		acct = &storage.Account{ID: p, Kind: storage.AccountHuman, Human: &storage.HumanAccount{}}
	case acct.Kind != storage.AccountHuman || acct.Human == nil:
		http.Error(w, "not a human account", http.StatusBadRequest)
		return
	}
	acct.Human.DisplayName = name
	if err := h.accounts.PutAccount(acct); err != nil {
		h.log().Error("profileapi: put account", "participant", p, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
