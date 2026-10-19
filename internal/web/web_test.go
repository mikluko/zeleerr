package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mikluko/zeleerr/internal/catalog"
	"github.com/mikluko/zeleerr/internal/profile"
)

type fakeCatalog struct {
	titles    []catalog.Title
	downloads []catalog.Download
	err       error

	email     string
	lang      string
	requested []string
}

func (f *fakeCatalog) UserID(_ context.Context, email string) (int, error) {
	f.email = email
	return 7, f.err
}

func (f *fakeCatalog) Search(_ context.Context, lang, _ string) ([]catalog.Title, error) {
	f.lang = lang
	return f.titles, f.err
}

func (f *fakeCatalog) Request(_ context.Context, uid int, mediaType string, tmdbID int) error {
	f.requested = append(f.requested, fmt.Sprintf("%s/%d@%d", mediaType, tmdbID, uid))
	return f.err
}

func (f *fakeCatalog) Queue(_ context.Context, lang string) ([]catalog.Download, error) {
	f.lang = lang
	return f.downloads, f.err
}

// fakeProfiles knows the profile of the token "jane".
type fakeProfiles struct{}

func (fakeProfiles) Lookup(_ context.Context, token string) (profile.Profile, error) {
	if token != "jane" {
		return profile.Profile{}, errors.New("unknown token")
	}
	return profile.Profile{Name: "Jane Doe", Picture: "https://id.example.org/jane.png", Locale: "ru"}, nil
}

func serve(t *testing.T, cat Catalog, req *http.Request) (int, string) {
	t.Helper()
	s, err := New(cat, Options{UserHeader: "X-Auth-Request-Email", TokenHeader: "X-Forwarded-Access-Token", Profiles: fakeProfiles{}, SeerrURL: "https://seerr.example.org/"}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

// signedIn adds what the proxy and a Russian browser send.
func signedIn(req *http.Request) *http.Request {
	req.Header.Set("X-Auth-Request-Email", "jane@example.org")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	return req
}

func TestPageNeedsTheUserHeader(t *testing.T) {
	code, _ := serve(t, &fakeCatalog{}, httptest.NewRequest("GET", "/", nil))
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", code)
	}
	code, body := serve(t, &fakeCatalog{}, signedIn(httptest.NewRequest("GET", "/", nil)))
	if code != http.StatusOK || !strings.Contains(body, "jane@example.org") {
		t.Fatalf("code = %d, body = %s", code, body)
	}
}

func TestHealthzAndStaticNeedNoUser(t *testing.T) {
	for _, path := range []string{"/healthz", "/static/htmx.min.js", "/static/style.css"} {
		if code, _ := serve(t, &fakeCatalog{}, httptest.NewRequest("GET", path, nil)); code != http.StatusOK {
			t.Errorf("%s: code = %d", path, code)
		}
	}
}

func TestSearchShowsRequestButtonOnlyWhenRequestable(t *testing.T) {
	cat := &fakeCatalog{titles: []catalog.Title{
		{MediaType: "movie", TMDBID: 129, Name: "Унесённые призраками", Year: "2001", Status: catalog.StatusNone},
		{MediaType: "tv", TMDBID: 1399, Name: "Игра престолов", Status: catalog.StatusAvailable},
	}}
	code, body := serve(t, cat, signedIn(httptest.NewRequest("GET", "/search?q=x", nil)))
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{"Унесённые призраками", "(2001)", `name="tmdbId" value="129"`, `data-tmdb-id="1399"`, `href="https://seerr.example.org/movie/129"`, "Уже есть"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "Заказать") != 1 {
		t.Errorf("want one request button:\n%s", body)
	}
}

func TestRequestActsAsTheSignedInUser(t *testing.T) {
	cat := &fakeCatalog{}
	form := url.Values{"mediaType": {"tv"}, "tmdbId": {"1399"}}
	req := signedIn(httptest.NewRequest("POST", "/request", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	code, body := serve(t, cat, req)
	if code != http.StatusOK || !strings.Contains(body, "Заказано") {
		t.Fatalf("code = %d, body = %s", code, body)
	}
	if cat.email != "jane@example.org" || len(cat.requested) != 1 || cat.requested[0] != "tv/1399@7" {
		t.Fatalf("email = %q, requested = %v", cat.email, cat.requested)
	}
}

func TestRequestRejectsNonsense(t *testing.T) {
	form := url.Values{"mediaType": {"person"}, "tmdbId": {"1"}}
	req := signedIn(httptest.NewRequest("POST", "/request", strings.NewReader(form.Encode())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if code, _ := serve(t, &fakeCatalog{}, req); code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

func TestQueue(t *testing.T) {
	cat := &fakeCatalog{downloads: []catalog.Download{
		{MediaType: "tv", TMDBID: 100, Name: "Lanterns", Season: 1, Episodes: 2, Progress: 0.25, Left: 26 * time.Hour, Poster: "https://img/l.jpg"},
		{MediaType: "movie", TMDBID: 12429, Name: "Ponyo", Progress: 1, Waiting: true},
		{MediaType: "tv", TMDBID: 15260, Name: "Время приключений", Seasons: []int{1, 2}, Searching: true},
	}}
	_, body := serve(t, cat, signedIn(httptest.NewRequest("GET", "/queue", nil)))
	for _, want := range []string{
		"Lanterns", "сезон 1, эпизодов: 2", `value="25"`, "1 д 2 ч", `src="https://img/l.jpg"`, "сезоны: 1, 2",
		"скачано, раскладываем",
		`href="https://seerr.example.org/tv/15260"`, "ищем раздачу",
		`id="q-tv-100-1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "<progress"); n != 2 {
		t.Errorf("%d progress bars, want 2: none for a search", n)
	}
}

func TestEmptySearchSaysNothing(t *testing.T) {
	code, body := serve(t, &fakeCatalog{}, signedIn(httptest.NewRequest("GET", "/search?q=", nil)))
	if code != http.StatusOK || strings.TrimSpace(body) != "" {
		t.Fatalf("code = %d, body = %q", code, body)
	}
}

func TestBackendFailureShowsAnError(t *testing.T) {
	code, body := serve(t, &fakeCatalog{err: errors.New("boom")}, signedIn(httptest.NewRequest("GET", "/queue", nil)))
	if code != http.StatusBadGateway || !strings.Contains(body, "Что-то сломалось") {
		t.Fatalf("code = %d, body = %s", code, body)
	}
}

func TestLanguageFollowsTheBrowser(t *testing.T) {
	for accept, want := range map[string]string{
		"ru-RU,ru;q=0.9,en;q=0.8": "Очередь",
		"en-GB,en;q=0.9":          "Queue",
		"de-DE":                   "Queue",
		"":                        "Queue",
	} {
		cat := &fakeCatalog{}
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Auth-Request-Email", "jane@example.org")
		req.Header.Set("Accept-Language", accept)
		_, body := serve(t, cat, req)
		if !strings.Contains(body, want) {
			t.Errorf("Accept-Language %q: page lacks %q", accept, want)
		}
	}

	cat := &fakeCatalog{}
	serve(t, cat, signedIn(httptest.NewRequest("GET", "/queue", nil)))
	if cat.lang != "ru" {
		t.Errorf("queue asked in %q, want ru", cat.lang)
	}
}

func TestEveryLanguageHasEveryMessage(t *testing.T) {
	for lang, m := range messages {
		for key := range messages["en"] {
			if _, ok := m[key]; !ok {
				t.Errorf("%s lacks %q", lang, key)
			}
		}
		if len(m) != len(messages["en"]) {
			t.Errorf("%s has %d messages, en %d", lang, len(m), len(messages["en"]))
		}
	}
}

func TestLeft(t *testing.T) {
	ru, en := locale("ru"), locale("en")
	for d, want := range map[time.Duration][2]string{
		0:                            {"", ""},
		30 * time.Second:             {"меньше минуты", "under a minute"},
		5 * time.Minute:              {"5 мин", "5 min"},
		2*time.Hour + 5*time.Minute:  {"2 ч 5 мин", "2 h 5 min"},
		26*time.Hour + 9*time.Minute: {"1 д 2 ч", "1 d 2 h"},
	} {
		if got := ru.left(d); got != want[0] {
			t.Errorf("ru.left(%v) = %q, want %q", d, got, want[0])
		}
		if got := en.left(d); got != want[1] {
			t.Errorf("en.left(%v) = %q, want %q", d, got, want[1])
		}
	}
}

func TestPageShowsTheProfile(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Auth-Request-Email", "jane@example.org")
	req.Header.Set("X-Forwarded-Access-Token", "jane")
	req.Header.Set("Accept-Language", "en")
	_, body := serve(t, &fakeCatalog{}, req)
	for _, want := range []string{"Jane Doe", `src="https://id.example.org/jane.png"`, "Очередь"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q: the profile's name, picture, and its locale over the browser's", want)
		}
	}
}

func TestPickedLanguageIsKept(t *testing.T) {
	rec := httptest.NewRecorder()
	s, err := New(&fakeCatalog{}, Options{UserHeader: "X-Auth-Request-Email", TokenHeader: "X-Forwarded-Access-Token", Profiles: fakeProfiles{}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	req := signedIn(httptest.NewRequest("GET", "/lang?to=en", nil))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want 303", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "lang" || cookies[0].Value != "en" {
		t.Fatalf("cookies = %v", cookies)
	}

	page := signedIn(httptest.NewRequest("GET", "/", nil))
	page.Header.Set("X-Forwarded-Access-Token", "jane")
	page.AddCookie(cookies[0])
	_, body := serve(t, &fakeCatalog{}, page)
	if !strings.Contains(body, "Queue") {
		t.Error("the picked language does not beat the profile's and the browser's")
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedIn(httptest.NewRequest("GET", "/lang?to=auto", nil)))
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Errorf("auto does not forget the pick: %v", c)
	}
}

func TestLocaleClaimC(t *testing.T) {
	s := &Server{}
	for claim, want := range map[string]locale{"C": "en", "POSIX": "en", "ru-RU": "ru", "en-GB": "en"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Language", "ru")
		req = req.WithContext(context.WithValue(req.Context(), viewerKey{}, viewer{Profile: profile.Profile{Locale: claim}}))
		if got := s.locale(req); got != want {
			t.Errorf("claim %q: locale %q, want %q", claim, got, want)
		}
	}
}
