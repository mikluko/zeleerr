package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mikluko/zeleerr/internal/radarr"
	"github.com/mikluko/zeleerr/internal/seerr"
	"github.com/mikluko/zeleerr/internal/sonarr"
)

// fake serves canned JSON per "METHOD /path" and records the requests.
type fake struct {
	t         *testing.T
	responses map[string]fakeResponse
	requests  []*http.Request
	bodies    []string
}

type fakeResponse struct {
	code int
	body string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, string(body))
	resp, ok := f.responses[r.Method+" "+r.URL.Path]
	if !ok {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.code)
	_, _ = io.WriteString(w, resp.body)
}

func newCatalog(t *testing.T, s, r, so *fake) *Catalog {
	t.Helper()
	srv := func(f *fake) string {
		if f == nil {
			f = &fake{t: t}
		}
		ts := httptest.NewServer(f)
		t.Cleanup(ts.Close)
		return ts.URL
	}
	sc, err := seerr.NewClientWithResponses(srv(s) + "/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := radarr.NewClientWithResponses(srv(r))
	if err != nil {
		t.Fatal(err)
	}
	soc, err := sonarr.NewClientWithResponses(srv(so))
	if err != nil {
		t.Fatal(err)
	}
	return New(sc, rc, soc, 160)
}

func TestUserIDFindsExistingUser(t *testing.T) {
	s := &fake{t: t, responses: map[string]fakeResponse{
		"GET /api/v1/user": {200, `{"results":[{"id":3,"email":"Other@x.org"},{"id":7,"email":"Jane@Example.org"}]}`},
	}}
	c := newCatalog(t, s, nil, nil)

	for range 2 {
		id, err := c.UserID(context.Background(), "jane@example.org")
		if err != nil {
			t.Fatal(err)
		}
		if id != 7 {
			t.Fatalf("id = %d, want 7", id)
		}
	}
	if len(s.requests) != 1 {
		t.Fatalf("requests = %d, want 1: the second lookup is cached", len(s.requests))
	}
}

func TestUserIDCreatesMissingUser(t *testing.T) {
	s := &fake{t: t, responses: map[string]fakeResponse{
		"GET /api/v1/user":                          {200, `{"results":[]}`},
		"POST /api/v1/user":                         {201, `{"id":12,"email":"jane@example.org"}`},
		"POST /api/v1/user/12/settings/permissions": {200, `{"permissions":160}`},
	}}
	c := newCatalog(t, s, nil, nil)

	id, err := c.UserID(context.Background(), "jane@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if id != 12 {
		t.Fatalf("id = %d, want 12", id)
	}
	var user struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(s.bodies[1]), &user); err != nil {
		t.Fatal(err)
	}
	if user.Email != "jane@example.org" || user.Username != "jane" || len(user.Password) < 20 {
		t.Fatalf("created user = %+v", user)
	}
	var perms struct {
		Permissions float64 `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(s.bodies[2]), &perms); err != nil {
		t.Fatal(err)
	}
	if perms.Permissions != 160 {
		t.Fatalf("permissions = %v, want 160", perms.Permissions)
	}
}

func TestSearchKeepsMoviesAndSeries(t *testing.T) {
	s := &fake{t: t, responses: map[string]fakeResponse{
		"GET /api/v1/search": {200, `{"page":1,"results":[
			{"id":129,"mediaType":"movie","title":"Унесённые призраками","releaseDate":"2001-07-20","posterPath":"/p.jpg","mediaInfo":{"status":5}},
			{"id":1399,"mediaType":"tv","name":"Игра престолов","firstAirDate":"2011-04-17"},
			{"id":1,"mediaType":"person","name":"Хаяо Миядзаки"}
		]}`},
	}}
	c := newCatalog(t, s, nil, nil)

	got, err := c.Search(context.Background(), "ru", "хаяо миядзаки")
	if err != nil {
		t.Fatal(err)
	}
	want := []Title{
		{MediaType: "movie", TMDBID: 129, Name: "Унесённые призраками", Year: "2001", Poster: "https://image.tmdb.org/t/p/w185/p.jpg", Status: StatusAvailable},
		{MediaType: "tv", TMDBID: 1399, Name: "Игра престолов", Year: "2011", Status: StatusNone},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d titles, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("title %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if q := s.requests[0].URL.Query(); q.Get("query") != "хаяо миядзаки" || q.Get("language") != "ru" {
		t.Errorf("query = %v", q)
	}
	if raw := s.requests[0].URL.RawQuery; strings.Contains(raw, "+") {
		t.Errorf("raw query %q encodes a space as +, which Seerr refuses", raw)
	}
}

func TestRequestActsAsUserAndAsksForEverySeason(t *testing.T) {
	s := &fake{t: t, responses: map[string]fakeResponse{
		"POST /api/v1/request": {201, `{"id":40}`},
	}}
	c := newCatalog(t, s, nil, nil)

	if err := c.Request(context.Background(), 7, "tv", 1399); err != nil {
		t.Fatal(err)
	}
	if got := s.requests[0].Header.Get("X-API-User"); got != "7" {
		t.Errorf("X-API-User = %q, want 7", got)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(s.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body["mediaType"] != "tv" || body["mediaId"] != float64(1399) || body["seasons"] != "all" {
		t.Errorf("body = %v", body)
	}
}

func TestRequestRejectsUnknownMediaType(t *testing.T) {
	c := newCatalog(t, nil, nil, nil)
	if err := c.Request(context.Background(), 7, "person", 1); err == nil {
		t.Fatal("want an error for a person")
	}
}

func TestQueueFoldsSeasonPackAndSortsByProgress(t *testing.T) {
	r := &fake{t: t, responses: map[string]fakeResponse{
		"GET /api/v3/queue": {200, `{"records":[
			{"title":"Ponyo.2008.1080p","movie":{"title":"Ponyo","tmdbId":12429},"size":100,"sizeleft":10,"timeleft":"00:05:00","trackedDownloadState":"downloading"}
		]}`},
	}}
	so := &fake{t: t, responses: map[string]fakeResponse{
		"GET /api/v3/queue": {200, `{"records":[
			{"title":"Lanterns.S01","series":{"title":"Lanterns","tmdbId":100},"seasonNumber":1,"downloadId":"A","size":200,"sizeleft":150,"timeleft":"1.02:00:00","trackedDownloadState":"downloading"},
			{"title":"Lanterns.S01","series":{"title":"Lanterns","tmdbId":100},"seasonNumber":1,"downloadId":"A","size":200,"sizeleft":150,"timeleft":"1.02:00:00","trackedDownloadState":"downloading"}
		]}`},
	}}
	s := &fake{t: t, responses: map[string]fakeResponse{
		"GET /api/v1/request": {200, `{"results":[
			{"type":"tv","media":{"tmdbId":15260,"status":3},"seasons":[{"seasonNumber":1},{"seasonNumber":2}]},
			{"type":"movie","media":{"tmdbId":12429,"status":3}},
			{"type":"movie","media":{"tmdbId":508883,"status":4}}
		]}`},
		"GET /api/v1/movie/12429": {200, `{"id":12429,"title":"Рыбка Поньо на утёсе","posterPath":"/ponyo.jpg","watchProviders":[{"iso_3166_1":"RU","flatrate":[]}]}`},
		"GET /api/v1/tv/100":      {500, `{}`},
		"GET /api/v1/tv/15260":    {200, `{"id":15260,"name":"Время приключений","posterPath":"/at.jpg"}`},
	}}
	c := newCatalog(t, s, r, so)

	got, err := c.Queue(context.Background(), "ru")
	if err != nil {
		t.Fatal(err)
	}
	want := []Download{
		{MediaType: "tv", TMDBID: 100, Name: "Lanterns", Season: 1, Episodes: 2, Progress: 0.25, Left: 26 * time.Hour},
		{MediaType: "movie", TMDBID: 12429, Name: "Рыбка Поньо на утёсе", Poster: "https://image.tmdb.org/t/p/w185/ponyo.jpg", Progress: 0.9, Left: 5 * time.Minute},
		{MediaType: "tv", TMDBID: 15260, Name: "Время приключений", Seasons: []int{1, 2}, Poster: "https://image.tmdb.org/t/p/w185/at.jpg", Searching: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("queue =\n%+v\nwant\n%+v", got, want)
	}
	if q := s.requests[1].URL.Query(); q.Get("language") != "ru" {
		t.Errorf("details asked in %q, want ru", q.Get("language"))
	}
	if q := r.requests[0].URL.Query(); q.Get("includeMovie") != "true" {
		t.Errorf("radarr query = %v", q)
	}

	if _, err := c.Queue(context.Background(), "ru"); err != nil {
		t.Fatal(err)
	}
	lookups := 0
	for _, req := range s.requests {
		if req.URL.Path == "/api/v1/movie/12429" {
			lookups++
		}
	}
	if lookups != 1 {
		t.Errorf("movie looked up %d times, want once", lookups)
	}
}

func TestStatusRequestable(t *testing.T) {
	for s, want := range map[Status]bool{
		StatusNone: true, StatusUnknown: true, StatusDeleted: true,
		StatusPending: false, StatusProcessing: false, StatusPartial: false, StatusAvailable: false,
	} {
		if s.Requestable() != want {
			t.Errorf("Status(%d).Requestable() = %v", s, !want)
		}
	}
}
