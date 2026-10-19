// Package gate lets people an authenticating proxy has signed in use Seerr's
// own pages without signing in to Seerr: it holds a Seerr session for each of
// them and puts it on their requests.
package gate

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/mikluko/zeleerr/internal/seerr"
)

// sessionCookie is the cookie Seerr keeps its session in.
const sessionCookie = "connect.sid"

// maxBody bounds the request body kept to replay a request on a new session.
const maxBody = 32 << 20

// Users finds the Seerr user with an email, creating it if there is none.
type Users interface {
	UserID(ctx context.Context, email string) (int, error)
}

// Gate is the reverse proxy in front of Seerr.
type Gate struct {
	admin  seerr.ClientWithResponsesInterface
	anon   seerr.ClientWithResponsesInterface
	users  Users
	key    []byte
	header string
	proxy  *httputil.ReverseProxy
	log    *slog.Logger

	mu       sync.Mutex
	sessions map[string]string
	logins   map[string]*sync.Mutex
}

// New returns a Gate to the Seerr at upstream. admin carries an API key with
// the MANAGE_USERS permission, anon carries none. The email of the person
// comes in the request header userHeader.
//
// Each person signs in to Seerr as a local user with the password
// HMAC-SHA256(key, email); the Gate sets that password when the sign-in
// fails. Seerr asks for the current password to change the API key owner's
// own, so for that user a new key needs the password reset by hand.
func New(upstream *url.URL, admin, anon seerr.ClientWithResponsesInterface, users Users, key []byte, userHeader string, log *slog.Logger) *Gate {
	g := &Gate{
		admin:    admin,
		anon:     anon,
		users:    users,
		key:      key,
		header:   userHeader,
		log:      log,
		sessions: map[string]string{},
		logins:   map[string]*sync.Mutex{},
	}
	g.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		Transport:      &retry{gate: g, next: http.DefaultTransport},
		ModifyResponse: dropSessionCookie,
		ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	return g
}

type emailKey struct{}

func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.Header.Get(g.header)))
	if email == "" {
		http.Error(w, "no signed-in user", http.StatusUnauthorized)
		return
	}
	if r.Body != nil && r.Body != http.NoBody {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			http.Error(w, "reading the request", http.StatusBadRequest)
			return
		}
		if len(body) > maxBody {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	if r.URL.Path == "/api/v1/auth/logout" {
		defer g.forget(email)
	}
	g.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), emailKey{}, email)))
}

// retry puts the person's session on each request to Seerr, and when Seerr
// turns it away for a session that has lapsed, signs in again and sends the
// request once more.
type retry struct {
	gate *Gate
	next http.RoundTripper
}

func (t *retry) RoundTrip(req *http.Request) (*http.Response, error) {
	email := req.Context().Value(emailKey{}).(string)
	sid, err := t.gate.session(req.Context(), email, "")
	if err != nil {
		return nil, err
	}
	res, err := t.next.RoundTrip(withSession(req, sid))
	if err != nil || !turnedAway(res) || t.gate.valid(req.Context(), sid) {
		return res, err
	}
	fresh, err := t.gate.session(req.Context(), email, sid)
	if err != nil {
		return nil, err
	}
	again := req.Clone(req.Context())
	if req.GetBody != nil {
		if again.Body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	_ = res.Body.Close()
	return t.next.RoundTrip(withSession(again, fresh))
}

// turnedAway reports a response Seerr gives a request without a valid
// session, among others: a refusal, or its pages sending to /login.
func turnedAway(res *http.Response) bool {
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return true
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		loc, err := url.Parse(res.Header.Get("Location"))
		return err == nil && loc.Path == "/login"
	}
	return false
}

// withSession is req with the session as its only Seerr session cookie.
func withSession(req *http.Request, sid string) *http.Request {
	out := req.Clone(req.Context())
	if req.GetBody != nil {
		out.Body, _ = req.GetBody()
	}
	cookies := req.Cookies()
	out.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != sessionCookie {
			out.AddCookie(c)
		}
	}
	out.AddCookie(&http.Cookie{Name: sessionCookie, Value: sid})
	return out
}

// dropSessionCookie keeps Seerr's session cookie from the browser, which
// never holds one.
func dropSessionCookie(res *http.Response) error {
	kept := res.Header.Values("Set-Cookie")[:0:0]
	for _, line := range res.Header.Values("Set-Cookie") {
		if c, err := http.ParseSetCookie(line); err == nil && c.Name == sessionCookie {
			continue
		}
		kept = append(kept, line)
	}
	res.Header.Del("Set-Cookie")
	for _, line := range kept {
		res.Header.Add("Set-Cookie", line)
	}
	return nil
}

// session returns the person's Seerr session, signing in when there is none
// or the one there is stale.
func (g *Gate) session(ctx context.Context, email, stale string) (string, error) {
	g.mu.Lock()
	lock, ok := g.logins[email]
	if !ok {
		lock = &sync.Mutex{}
		g.logins[email] = lock
	}
	g.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()
	g.mu.Lock()
	sid := g.sessions[email]
	g.mu.Unlock()
	if sid != "" && sid != stale {
		return sid, nil
	}
	sid, err := g.login(ctx, email)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	g.sessions[email] = sid
	g.mu.Unlock()
	return sid, nil
}

func (g *Gate) forget(email string) {
	g.mu.Lock()
	delete(g.sessions, email)
	g.mu.Unlock()
}

func (g *Gate) valid(ctx context.Context, sid string) bool {
	res, err := g.anon.GetAuthMeWithResponse(ctx, func(_ context.Context, req *http.Request) error {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sid})
		return nil
	})
	return err == nil && res.StatusCode() == http.StatusOK
}

func (g *Gate) login(ctx context.Context, email string) (string, error) {
	password := g.password(email)
	if sid, err := g.signIn(ctx, email, password); err == nil {
		return sid, nil
	}
	id, err := g.users.UserID(ctx, email)
	if err != nil {
		return "", fmt.Errorf("gate: %w", err)
	}
	set, err := g.admin.PostUserUserIdSettingsPasswordWithResponse(ctx, float32(id), seerr.PostUserUserIdSettingsPasswordJSONRequestBody{NewPassword: password})
	if err != nil {
		return "", fmt.Errorf("gate: set password of user %d: %w", id, err)
	}
	if set.StatusCode() != http.StatusNoContent {
		return "", fmt.Errorf("gate: set password of user %d: %s", id, set.Status())
	}
	g.log.Info("password set", "user", id)
	return g.signIn(ctx, email, password)
}

func (g *Gate) signIn(ctx context.Context, email, password string) (string, error) {
	res, err := g.anon.PostAuthLocalWithResponse(ctx, seerr.PostAuthLocalJSONRequestBody{Email: email, Password: password})
	if err != nil {
		return "", fmt.Errorf("gate: sign in: %w", err)
	}
	if res.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("gate: sign in: %s", res.Status())
	}
	for _, c := range res.HTTPResponse.Cookies() {
		if c.Name == sessionCookie {
			return c.Value, nil
		}
	}
	return "", errors.New("gate: sign in: no session cookie")
}

func (g *Gate) password(email string) string {
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(email))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
