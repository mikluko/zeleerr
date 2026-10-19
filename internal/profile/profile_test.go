package profile

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLookup(t *testing.T) {
	calls := 0
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_, _ = io.WriteString(w, `{"userinfo_endpoint":"`+ts.URL+`/api/oidc/userinfo"}`)
		case "/api/oidc/userinfo":
			calls++
			if r.Header.Get("Authorization") != "Bearer good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"sub":"1","preferred_username":"jane","picture":"https://id/p.png","locale":"ru-RU"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	s := New(ts.URL+"/", ts.Client(), time.Minute)
	for range 2 {
		p, err := s.Lookup(context.Background(), "good")
		if err != nil {
			t.Fatal(err)
		}
		if p != (Profile{Name: "jane", Picture: "https://id/p.png", Locale: "ru-RU"}) {
			t.Fatalf("profile = %+v", p)
		}
	}
	if calls != 1 {
		t.Errorf("userinfo asked %d times, want once", calls)
	}
	if _, err := s.Lookup(context.Background(), "bad"); err == nil {
		t.Error("want an error for a token the provider refuses")
	}
	if _, err := s.Lookup(context.Background(), ""); err == nil {
		t.Error("want an error for no token")
	}
}
