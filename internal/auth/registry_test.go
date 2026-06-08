package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sgrankin/wave/internal/auth"
)

// fakeInteractive is a minimal InteractiveMethod for the registry listing test.
type fakeInteractive struct {
	name, label, start string
}

func (m fakeInteractive) Name() string      { return m.name }
func (m fakeInteractive) Label() string     { return m.label }
func (m fakeInteractive) StartPath() string { return m.start }
func (m fakeInteractive) Mount(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"/start", func(w http.ResponseWriter, _ *http.Request) {})
}

// TestMethodsHandlerListsInteractive: GET /auth/methods returns only interactive
// methods (name+label+start), and stateless methods (DevMethod) are omitted.
func TestMethodsHandlerListsInteractive(t *testing.T) {
	reg := auth.NewRegistry(
		auth.DevMethod{Domain: "example.com"}, // stateless → omitted
		fakeInteractive{name: "github", label: "Sign in with GitHub", start: "/auth/github/start"},
		fakeInteractive{name: "oidc", label: "Sign in with SSO", start: "/auth/oidc/start"},
	)
	rec := httptest.NewRecorder()
	reg.MethodsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/auth/methods", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []struct{ Name, Label, Start string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("got %d methods, want 2 (dev omitted): %+v", len(got), got)
	}
	if got[0].Name != "github" || got[0].Start != "/auth/github/start" {
		t.Errorf("first method = %+v, want github", got[0])
	}
	if got[1].Label != "Sign in with SSO" {
		t.Errorf("second label = %q, want Sign in with SSO", got[1].Label)
	}
}

// TestMethodsHandlerEmpty: with no interactive methods, the endpoint returns [] not
// null (so the client can iterate unconditionally).
func TestMethodsHandlerEmpty(t *testing.T) {
	reg := auth.NewRegistry(auth.DevMethod{Domain: "example.com"})
	rec := httptest.NewRecorder()
	reg.MethodsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/auth/methods", nil))
	if body := rec.Body.String(); body != "[]\n" {
		t.Errorf("empty methods body = %q, want []", body)
	}
}

// TestRegistryRequiresLoopback: a registry with a loopback-only method reports
// true; one with only IdP/exclusive methods reports false.
func TestRegistryRequiresLoopback(t *testing.T) {
	if !auth.NewRegistry(auth.DevMethod{}).RequiresLoopback() {
		t.Error("dev method registry should require loopback")
	}
	if auth.NewRegistry(auth.ProxyMethod{ProxyExclusive: true}).RequiresLoopback() {
		t.Error("proxy-exclusive registry should not require loopback")
	}
	if !auth.NewRegistry(auth.ProxyMethod{}).RequiresLoopback() {
		t.Error("non-exclusive proxy registry should require loopback")
	}
	if auth.NewRegistry(fakeInteractive{name: "github"}).RequiresLoopback() {
		t.Error("an IdP method should not require loopback")
	}
}
