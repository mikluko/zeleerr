// Package catalog is what the family sees of Seerr, Radarr and Sonarr:
// titles to find and request, and what is downloading.
package catalog

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mikluko/zelirr/internal/radarr"
	"github.com/mikluko/zelirr/internal/seerr"
	"github.com/mikluko/zelirr/internal/sonarr"
)

// Status is Seerr's availability of a title; StatusNone means nobody has
// asked for it yet.
type Status int

const (
	StatusNone Status = iota
	StatusUnknown
	StatusPending
	StatusProcessing
	StatusPartial
	StatusAvailable
	StatusDeleted
)

// Requestable reports whether a request for the title makes sense.
func (s Status) Requestable() bool {
	return s == StatusNone || s == StatusUnknown || s == StatusDeleted
}

// Title is one movie or series found by a search.
type Title struct {
	MediaType string // "movie" or "tv"
	TMDBID    int
	Name      string
	Year      string
	Overview  string
	Poster    string // full URL, or "" when TMDB has none
	Status    Status
}

// Download is one movie, or one season of a series, in the download queue.
type Download struct {
	Name     string
	Detail   string
	Progress float64 // 0..1
	Left     time.Duration
	Waiting  bool // done downloading, waiting to be imported
}

// Catalog talks to the services on behalf of signed-in family members.
type Catalog struct {
	seerr       seerr.ClientWithResponsesInterface
	radarr      radarr.ClientWithResponsesInterface
	sonarr      sonarr.ClientWithResponsesInterface
	permissions int
	posterBase  string

	mu    sync.Mutex
	users map[string]int
}

// New returns a Catalog. permissions are the Seerr permission bits given to a
// user it creates for an email Seerr does not know yet.
func New(s seerr.ClientWithResponsesInterface, r radarr.ClientWithResponsesInterface, so sonarr.ClientWithResponsesInterface, permissions int) *Catalog {
	return &Catalog{
		seerr:       s,
		radarr:      r,
		sonarr:      so,
		permissions: permissions,
		posterBase:  "https://image.tmdb.org/t/p/w185",
		users:       map[string]int{},
	}
}

// UserID returns the Seerr user with the email, creating it if there is none.
// A created user gets a random password, so Seerr does not need to mail one.
func (c *Catalog) UserID(ctx context.Context, email string) (int, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return 0, errors.New("catalog: empty email")
	}
	c.mu.Lock()
	id, ok := c.users[email]
	c.mu.Unlock()
	if ok {
		return id, nil
	}

	take := float32(100)
	found, err := c.seerr.GetUserWithResponse(ctx, &seerr.GetUserParams{Q: &email, Take: &take})
	if err != nil {
		return 0, fmt.Errorf("catalog: list users: %w", err)
	}
	if found.JSON200 == nil {
		return 0, fmt.Errorf("catalog: list users: %s", found.Status())
	}
	if found.JSON200.Results != nil {
		for _, u := range *found.JSON200.Results {
			if u.Email != nil && u.Id != nil && strings.EqualFold(*u.Email, email) {
				id = *u.Id
				break
			}
		}
	}

	if id == 0 {
		name, _, _ := strings.Cut(email, "@")
		perms := float32(c.permissions)
		// The password is never used: the user acts only through zelirr.
		password := rand.Text()
		created, err := c.seerr.PostUserWithResponse(ctx, seerr.PostUserJSONRequestBody{Email: &email, Username: &name, Password: &password, Permissions: &perms})
		if err != nil {
			return 0, fmt.Errorf("catalog: create user: %w", err)
		}
		if created.JSON201 == nil || created.JSON201.Id == nil {
			return 0, fmt.Errorf("catalog: create user: %s", created.Status())
		}
		id = *created.JSON201.Id
	}

	c.mu.Lock()
	c.users[email] = id
	c.mu.Unlock()
	return id, nil
}

// Search finds movies and series by name; people are left out.
func (c *Catalog) Search(ctx context.Context, query string) ([]Title, error) {
	lang := "ru"
	res, err := c.seerr.GetSearchWithResponse(ctx, &seerr.GetSearchParams{Query: query, Language: &lang})
	if err != nil {
		return nil, fmt.Errorf("catalog: search: %w", err)
	}
	if res.JSON200 == nil {
		return nil, fmt.Errorf("catalog: search: %s", res.Status())
	}
	var titles []Title
	if res.JSON200.Results == nil {
		return titles, nil
	}
	for _, item := range *res.JSON200.Results {
		if m, err := item.AsMovieResult(); err == nil && m.MediaType == "movie" {
			titles = append(titles, Title{
				MediaType: "movie",
				TMDBID:    int(m.Id),
				Name:      m.Title,
				Year:      year(m.ReleaseDate),
				Overview:  deref(m.Overview),
				Poster:    c.poster(m.PosterPath),
				Status:    status(m.MediaInfo),
			})
			continue
		}
		if t, err := item.AsTvResult(); err == nil && t.MediaType != nil && *t.MediaType == "tv" && t.Id != nil {
			titles = append(titles, Title{
				MediaType: "tv",
				TMDBID:    int(*t.Id),
				Name:      deref(t.Name),
				Year:      year(t.FirstAirDate),
				Overview:  deref(t.Overview),
				Poster:    c.poster(t.PosterPath),
				Status:    status(t.MediaInfo),
			})
		}
	}
	return titles, nil
}

// Request asks Seerr for a movie, or for every season of a series, on behalf
// of the user.
func (c *Catalog) Request(ctx context.Context, userID int, mediaType string, tmdbID int) error {
	body := seerr.PostRequestJSONRequestBody{
		MediaType: seerr.PostRequestJSONBodyMediaType(mediaType),
		MediaId:   float32(tmdbID),
	}
	switch mediaType {
	case "movie":
	case "tv":
		var seasons seerr.PostRequestJSONBody_Seasons
		if err := seasons.FromPostRequestJSONBodySeasons1("all"); err != nil {
			return fmt.Errorf("catalog: request: %w", err)
		}
		body.Seasons = &seasons
	default:
		return fmt.Errorf("catalog: request: unknown media type %q", mediaType)
	}
	res, err := c.seerr.PostRequestWithResponse(ctx, body, asUser(userID))
	if err != nil {
		return fmt.Errorf("catalog: request: %w", err)
	}
	if res.JSON201 == nil {
		return fmt.Errorf("catalog: request: %s", res.Status())
	}
	return nil
}

// Queue lists what Radarr and Sonarr are downloading, a series' episodes
// folded into one entry per season, the least done first.
func (c *Catalog) Queue(ctx context.Context) ([]Download, error) {
	var out []Download

	size, yes := int32(200), true
	movies, err := c.radarr.GetApiV3QueueWithResponse(ctx, &radarr.GetApiV3QueueParams{PageSize: &size, IncludeMovie: &yes})
	if err != nil {
		return nil, fmt.Errorf("catalog: radarr queue: %w", err)
	}
	if movies.JSON200 == nil {
		return nil, fmt.Errorf("catalog: radarr queue: %s", movies.Status())
	}
	if movies.JSON200.Records != nil {
		for _, r := range *movies.JSON200.Records {
			name := deref(r.Title)
			if r.Movie != nil && r.Movie.Title != nil {
				name = *r.Movie.Title
			}
			out = append(out, Download{
				Name:     name,
				Progress: progress(r.Size, r.Sizeleft),
				Left:     left(r.Timeleft),
				Waiting:  waitingRadarr(r.TrackedDownloadState),
			})
		}
	}

	shows, err := c.sonarr.GetApiV3QueueWithResponse(ctx, &sonarr.GetApiV3QueueParams{PageSize: &size, IncludeSeries: &yes})
	if err != nil {
		return nil, fmt.Errorf("catalog: sonarr queue: %w", err)
	}
	if shows.JSON200 == nil {
		return nil, fmt.Errorf("catalog: sonarr queue: %s", shows.Status())
	}
	type key struct {
		series string
		season int32
	}
	type agg struct {
		size, left float64
		episodes   int
		remaining  time.Duration
		waiting    bool
		downloads  map[string]bool
	}
	seasons := map[key]*agg{}
	var order []key
	if shows.JSON200.Records != nil {
		for _, r := range *shows.JSON200.Records {
			k := key{series: deref(r.Title)}
			if r.Series != nil && r.Series.Title != nil {
				k.series = *r.Series.Title
			}
			if r.SeasonNumber != nil {
				k.season = *r.SeasonNumber
			}
			a, ok := seasons[k]
			if !ok {
				a = &agg{waiting: true, downloads: map[string]bool{}}
				seasons[k] = a
				order = append(order, k)
			}
			a.episodes++
			// A season pack lists each of its episodes with the pack's own
			// download and size: count every download once.
			id := deref(r.DownloadId)
			if id == "" || !a.downloads[id] {
				a.downloads[id] = true
				a.size += value(r.Size)
				a.left += value(r.Sizeleft)
			}
			if l := left(r.Timeleft); l > a.remaining {
				a.remaining = l
			}
			a.waiting = a.waiting && waitingSonarr(r.TrackedDownloadState)
		}
	}
	for _, k := range order {
		a := seasons[k]
		detail := fmt.Sprintf("сезон %d, эпизодов: %d", k.season, a.episodes)
		out = append(out, Download{
			Name:     k.series,
			Detail:   detail,
			Progress: progress(&a.size, &a.left),
			Left:     a.remaining,
			Waiting:  a.waiting,
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Progress < out[j].Progress })
	return out, nil
}

func asUser(id int) seerr.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("X-API-User", strconv.Itoa(id))
		return nil
	}
}

func (c *Catalog) poster(path *string) string {
	if path == nil || *path == "" {
		return ""
	}
	return c.posterBase + *path
}

func status(m *seerr.MediaInfo) Status {
	if m == nil || m.Status == nil {
		return StatusNone
	}
	return Status(*m.Status)
}

func year(date *string) string {
	if date == nil || len(*date) < 4 {
		return ""
	}
	return (*date)[:4]
}

func progress(size, sizeleft *float64) float64 {
	s, l := value(size), value(sizeleft)
	if s <= 0 {
		return 0
	}
	return (s - l) / s
}

// left parses the *arr "timeleft", hh:mm:ss or d.hh:mm:ss.
func left(s *string) time.Duration {
	if s == nil || *s == "" {
		return 0
	}
	var days time.Duration
	rest := *s
	if d, r, ok := strings.Cut(rest, "."); ok && strings.Count(r, ":") == 2 {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0
		}
		days, rest = time.Duration(n)*24*time.Hour, r
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return 0
	}
	var d time.Duration
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return 0
		}
		d += time.Duration(n) * unit
	}
	return days + d
}

func waitingRadarr(s *radarr.TrackedDownloadState) bool {
	return s != nil && *s != radarr.TrackedDownloadStateDownloading
}

func waitingSonarr(s *sonarr.TrackedDownloadState) bool {
	return s != nil && *s != sonarr.TrackedDownloadStateDownloading
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func value(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}
