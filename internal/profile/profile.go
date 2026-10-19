// Package profile tells who a signed-in person is from the OpenID Connect
// provider's userinfo, by the access token the authenticating proxy passes on.
package profile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Profile is what the page shows of a person. Any field may be empty.
type Profile struct {
	Name    string
	Picture string // URL
	Locale  string // the standard locale claim, a BCP 47 tag
}

// Source looks profiles up at an OpenID Connect provider.
type Source struct {
	issuer string
	client *http.Client
	ttl    time.Duration

	mu       sync.Mutex
	userinfo string
	cache    map[[sha256.Size]byte]entry
}

type entry struct {
	profile Profile
	expires time.Time
}

// New returns a Source for the provider at issuer. A profile is kept for ttl
// after it was fetched, under a hash of the token.
func New(issuer string, client *http.Client, ttl time.Duration) *Source {
	return &Source{issuer: strings.TrimRight(issuer, "/"), client: client, ttl: ttl, cache: map[[sha256.Size]byte]entry{}}
}

// Lookup returns the profile of the person the access token was issued to.
func (s *Source) Lookup(ctx context.Context, token string) (Profile, error) {
	if token == "" {
		return Profile{}, errors.New("profile: no access token")
	}
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	s.mu.Lock()
	e, ok := s.cache[key]
	s.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.profile, nil
	}

	endpoint, err := s.endpoint(ctx)
	if err != nil {
		return Profile{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var claims struct {
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
		Locale            string `json:"locale"`
	}
	if err := s.getJSON(req, &claims); err != nil {
		return Profile{}, fmt.Errorf("profile: userinfo: %w", err)
	}
	p := Profile{Name: claims.Name, Picture: claims.Picture, Locale: claims.Locale}
	if p.Name == "" {
		p.Name = claims.PreferredUsername
	}

	s.mu.Lock()
	for k, e := range s.cache {
		if now.After(e.expires) {
			delete(s.cache, k)
		}
	}
	s.cache[key] = entry{p, now.Add(s.ttl)}
	s.mu.Unlock()
	return p, nil
}

// endpoint is the provider's userinfo endpoint, from its discovery document.
func (s *Source) endpoint(ctx context.Context) (string, error) {
	s.mu.Lock()
	known := s.userinfo
	s.mu.Unlock()
	if known != "" {
		return known, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	var doc struct {
		Userinfo string `json:"userinfo_endpoint"`
	}
	if err := s.getJSON(req, &doc); err != nil {
		return "", fmt.Errorf("profile: discovery: %w", err)
	}
	if doc.Userinfo == "" {
		return "", errors.New("profile: discovery names no userinfo endpoint")
	}
	s.mu.Lock()
	s.userinfo = doc.Userinfo
	s.mu.Unlock()
	return doc.Userinfo, nil
}

func (s *Source) getJSON(req *http.Request, v any) error {
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New(res.Status)
	}
	return json.NewDecoder(res.Body).Decode(v)
}
