// Package web serves the family page: a search that requests movies and
// series, and the download queue, on htmx fragments.
package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/mikluko/zeleerr/internal/catalog"
	"github.com/mikluko/zeleerr/internal/profile"
)

//go:embed templates static
var assets embed.FS

// Catalog is what the page needs of catalog.Catalog.
type Catalog interface {
	UserID(ctx context.Context, email string) (int, error)
	Search(ctx context.Context, lang, query string) ([]catalog.Title, error)
	Request(ctx context.Context, userID int, mediaType string, tmdbID int) error
	Queue(ctx context.Context, lang string) ([]catalog.Download, error)
}

// Profiles is what the page needs of profile.Source.
type Profiles interface {
	Lookup(ctx context.Context, token string) (profile.Profile, error)
}

// Options configure a Server.
type Options struct {
	// UserHeader carries the signed-in person's email, set by the
	// authenticating proxy in front of the server.
	UserHeader string
	// TokenHeader carries their access token, for Profiles; with Profiles nil
	// the page names people by email.
	TokenHeader string
	Profiles    Profiles
	// SeerrURL is where movies and series link to their pages.
	SeerrURL string
}

// Server is the HTTP side of zeleerr.
type Server struct {
	cat  Catalog
	opts Options
	log  *slog.Logger
	tmpl *template.Template
}

// New returns a Server.
func New(cat Catalog, opts Options, log *slog.Logger) (*Server, error) {
	opts.SeerrURL = strings.TrimRight(opts.SeerrURL, "/")
	s := &Server{cat: cat, opts: opts, log: log}
	tmpl, err := template.New("").Funcs(s.funcs("en")).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s.tmpl = tmpl
	return s, nil
}

// funcs are the template functions, those that speak bound to l.
func (s *Server) funcs(l locale) template.FuncMap {
	return template.FuncMap{
		"percent": func(p float64) int { return int(p * 100) },
		"seerr":   s.seerrLink,
		"lang":    func() string { return string(l) },
		"t":       l.t,
		"left":    l.left,
		"seasons": l.seasons,
	}
}

func (s *Server) seerrLink(mediaType string, tmdbID int) string {
	return fmt.Sprintf("%s/%s/%d", s.opts.SeerrURL, mediaType, tmdbID)
}

// Handler routes the page, its fragments, the static files and /healthz.
// Everything but the last two answers 401 to a request without the header.
func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	app := http.NewServeMux()
	app.HandleFunc("GET /{$}", s.page)
	app.HandleFunc("GET /search", s.search)
	app.HandleFunc("POST /request", s.request)
	app.HandleFunc("GET /queue", s.queue)
	app.HandleFunc("GET /lang", s.language)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.Handle("/", s.identify(app))
	return mux
}

// viewer is the signed-in person.
type viewer struct {
	Email string
	profile.Profile
}

type viewerKey struct{}

func viewerOf(r *http.Request) viewer {
	v, _ := r.Context().Value(viewerKey{}).(viewer)
	return v
}

func (s *Server) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := viewer{Email: strings.TrimSpace(r.Header.Get(s.opts.UserHeader))}
		if v.Email == "" {
			http.Error(w, s.locale(r).t("who"), http.StatusUnauthorized)
			return
		}
		if s.opts.Profiles != nil {
			p, err := s.opts.Profiles.Lookup(r.Context(), r.Header.Get(s.opts.TokenHeader))
			if err != nil {
				s.log.Warn("profile", "email", v.Email, "err", err)
			}
			v.Profile = p
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewerKey{}, v)))
	})
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "page", struct {
		Viewer viewer
		Lang   locale
		Chosen bool
	}{viewerOf(r), s.locale(r), chosen(r) != ""})
}

// language keeps the language the person picked, or forgets it for "auto".
func (s *Server) language(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{Name: langCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if l := r.URL.Query().Get("to"); supported(l) {
		c.Value, c.MaxAge = l, 400*24*60*60
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return
	}
	titles, err := s.cat.Search(r.Context(), string(s.locale(r)), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "results", titles)
}

func (s *Server) request(w http.ResponseWriter, r *http.Request) {
	mediaType := r.FormValue("mediaType")
	tmdbID, err := strconv.Atoi(r.FormValue("tmdbId"))
	if err != nil || (mediaType != "movie" && mediaType != "tv") {
		http.Error(w, s.locale(r).t("bad request"), http.StatusBadRequest)
		return
	}
	uid, err := s.cat.UserID(r.Context(), viewerOf(r).Email)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.cat.Request(r.Context(), uid, mediaType, tmdbID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "action", catalog.Title{MediaType: mediaType, TMDBID: tmdbID, Status: catalog.StatusPending})
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	downloads, err := s.cat.Queue(r.Context(), string(s.locale(r)))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "queue", downloads)
}

// render executes the template in the language of the request.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl, err := s.tmpl.Clone()
	if err == nil {
		err = tmpl.Funcs(s.funcs(s.locale(r))).ExecuteTemplate(w, name, data)
	}
	if err != nil {
		s.log.Error("render", "template", name, "err", err)
	}
}

// fail logs err and answers with a fragment htmx shows in place of the
// result, so the page says what went wrong instead of silently not updating.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request", "path", r.URL.Path, "err", err)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	s.render(w, r, "error", nil)
}
