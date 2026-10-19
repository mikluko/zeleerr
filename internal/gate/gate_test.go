package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/mikluko/zeleerr/internal/seerr"
)

// fakeSeerr signs in local users by password and serves its pages only to a
// valid session.
type fakeSeerr struct {
	mu        sync.Mutex
	passwords map[string]string // email -> password
	emails    map[int]string    // user id -> email
	sessions  map[string]string // sid -> email
	logins    int
	bodies    []string
}

func (f *fakeSeerr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	who := func() string {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			return ""
		}
		return f.sessions[c.Value]
	}
	switch {
	case r.URL.Path == "/api/v1/auth/local":
		var in struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if p, ok := f.passwords[in.Email]; !ok || p != in.Password {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		f.logins++
		sid := fmt.Sprintf("s%%3Asession-%d", f.logins)
		f.sessions[sid] = in.Email
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true})
		_, _ = io.WriteString(w, `{"id":1}`)
	case r.URL.Path == "/api/v1/auth/me":
		if who() == "" {
			http.Error(w, "{}", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"id":1}`)
	case strings.HasPrefix(r.URL.Path, "/api/v1/user/") && strings.HasSuffix(r.URL.Path, "/settings/password"):
		if r.Header.Get("X-Api-Key") != "key" {
			http.Error(w, "{}", http.StatusForbidden)
			return
		}
		var id int
		fmt.Sscanf(r.URL.Path, "/api/v1/user/%d/settings/password", &id)
		var in struct{ NewPassword string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.passwords[f.emails[id]] = in.NewPassword
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/api/v1/auth/logout":
		c, _ := r.Cookie(sessionCookie)
		delete(f.sessions, c.Value)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	default:
		email := who()
		if email == "" {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "{}", http.StatusForbidden)
			} else {
				http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.bodies = append(f.bodies, string(body))
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "rotated", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "locale", Value: "ru", Path: "/"})
		fmt.Fprintf(w, "hello %s", email)
	}
}

func (f *fakeSeerr) revokeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = map[string]string{}
}

type fakeUsers map[string]int

func (u fakeUsers) UserID(_ context.Context, email string) (int, error) {
	return u[email], nil
}

func newGate(t *testing.T) (*fakeSeerr, http.Handler) {
	t.Helper()
	f := &fakeSeerr{
		passwords: map[string]string{"jane@example.org": "something else"},
		emails:    map[int]string{7: "jane@example.org"},
		sessions:  map[string]string{},
	}
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	upstream, _ := url.Parse(ts.URL)
	admin, err := seerr.NewClientWithResponses(ts.URL+"/api/v1", seerr.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("X-Api-Key", "key")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	anon, err := seerr.NewClientWithResponses(ts.URL + "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	g := New(upstream, admin, anon, fakeUsers{"jane@example.org": 7}, []byte("gate key"), "X-Forwarded-Email", slog.New(slog.DiscardHandler))
	return f, g
}

func do(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Forwarded-Email", "Jane@Example.org")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNoUserNoEntry(t *testing.T) {
	_, g := newGate(t)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestFirstVisitSetsPasswordAndSignsIn(t *testing.T) {
	f, g := newGate(t)
	rec := do(g, "GET", "/discover", "", &http.Cookie{Name: sessionCookie, Value: "forged"})
	if rec.Code != http.StatusOK || rec.Body.String() != "hello jane@example.org" {
		t.Fatalf("code = %d, body = %q", rec.Code, rec.Body)
	}
	if f.passwords["jane@example.org"] == "something else" {
		t.Error("password was not set")
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Errorf("the browser got Seerr's session cookie %q", c.Value)
		}
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "locale=ru") {
		t.Errorf("other cookies are kept: %q", rec.Header().Values("Set-Cookie"))
	}

	do(g, "GET", "/discover", "")
	if f.logins != 1 {
		t.Errorf("signed in %d times, want once", f.logins)
	}
}

func TestLapsedSessionIsRenewedAndTheRequestReplayed(t *testing.T) {
	f, g := newGate(t)
	do(g, "GET", "/", "")
	f.revokeAll()

	rec := do(g, "POST", "/api/v1/request", `{"mediaId":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %q", rec.Code, rec.Body)
	}
	if f.logins != 2 {
		t.Errorf("signed in %d times, want twice", f.logins)
	}
	if last := f.bodies[len(f.bodies)-1]; last != `{"mediaId":1}` {
		t.Errorf("replayed body = %q", last)
	}

	f.revokeAll()
	if rec := do(g, "GET", "/discover", ""); rec.Code != http.StatusOK {
		t.Fatalf("a page after the session lapsed: code = %d", rec.Code)
	}
}

func TestLogoutForgetsTheSession(t *testing.T) {
	f, g := newGate(t)
	do(g, "GET", "/", "")
	do(g, "POST", "/api/v1/auth/logout", "")
	if rec := do(g, "GET", "/", ""); rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if f.logins != 2 {
		t.Errorf("signed in %d times, want twice", f.logins)
	}
}

func TestPasswordDependsOnKeyAndEmail(t *testing.T) {
	a := (&Gate{key: []byte("one")}).password("jane@example.org")
	if a == (&Gate{key: []byte("two")}).password("jane@example.org") || a == (&Gate{key: []byte("one")}).password("joe@example.org") {
		t.Fatal("password does not depend on both key and email")
	}
	if len(a) < 8 {
		t.Fatalf("password %q is shorter than Seerr's minimum of 8", a)
	}
}
