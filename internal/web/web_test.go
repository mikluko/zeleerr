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

	"github.com/mikluko/zelirr/internal/catalog"
)

type fakeCatalog struct {
	titles    []catalog.Title
	downloads []catalog.Download
	err       error

	email     string
	requested []string
}

func (f *fakeCatalog) UserID(_ context.Context, email string) (int, error) {
	f.email = email
	return 7, f.err
}

func (f *fakeCatalog) Search(context.Context, string) ([]catalog.Title, error) {
	return f.titles, f.err
}

func (f *fakeCatalog) Request(_ context.Context, uid int, mediaType string, tmdbID int) error {
	f.requested = append(f.requested, fmt.Sprintf("%s/%d@%d", mediaType, tmdbID, uid))
	return f.err
}

func (f *fakeCatalog) Queue(context.Context) ([]catalog.Download, error) {
	return f.downloads, f.err
}

func serve(t *testing.T, cat Catalog, req *http.Request) (int, string) {
	t.Helper()
	s, err := New(cat, "X-Auth-Request-Email", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func signedIn(req *http.Request) *http.Request {
	req.Header.Set("X-Auth-Request-Email", "jane@example.org")
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
	for _, want := range []string{"Унесённые призраками", "(2001)", `name="tmdbId" value="129"`, "Уже есть"} {
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
		{Name: "Lanterns", Detail: "сезон 1, эпизодов: 2", Progress: 0.25, Left: 26 * time.Hour},
		{Name: "Ponyo", Progress: 1, Waiting: true},
	}}
	_, body := serve(t, cat, signedIn(httptest.NewRequest("GET", "/queue", nil)))
	for _, want := range []string{"Lanterns", `value="25"`, "1 д 2 ч", "скачано, раскладываем"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestBackendFailureShowsAnError(t *testing.T) {
	code, body := serve(t, &fakeCatalog{err: errors.New("boom")}, signedIn(httptest.NewRequest("GET", "/queue", nil)))
	if code != http.StatusBadGateway || !strings.Contains(body, "Что-то сломалось") {
		t.Fatalf("code = %d, body = %s", code, body)
	}
}

func TestFormatLeft(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                            "",
		30 * time.Second:             "меньше минуты",
		5 * time.Minute:              "5 мин",
		2*time.Hour + 5*time.Minute:  "2 ч 5 мин",
		26*time.Hour + 9*time.Minute: "1 д 2 ч",
	} {
		if got := formatLeft(d); got != want {
			t.Errorf("formatLeft(%v) = %q, want %q", d, got, want)
		}
	}
}
