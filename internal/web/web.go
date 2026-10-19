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
	"time"

	"github.com/mikluko/zeleerr/internal/catalog"
)

//go:embed templates static
var assets embed.FS

// Catalog is what the page needs of catalog.Catalog.
type Catalog interface {
	UserID(ctx context.Context, email string) (int, error)
	Search(ctx context.Context, query string) ([]catalog.Title, error)
	Request(ctx context.Context, userID int, mediaType string, tmdbID int) error
	Queue(ctx context.Context) ([]catalog.Download, error)
}

// Server is the HTTP side of zeleerr.
type Server struct {
	cat    Catalog
	header string
	log    *slog.Logger
	tmpl   *template.Template
}

// New returns a Server that takes the signed-in user's email from the request
// header userHeader, set by the authenticating proxy in front of it.
func New(cat Catalog, userHeader string, log *slog.Logger) (*Server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"percent": func(p float64) int { return int(p * 100) },
		"left":    formatLeft,
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{cat: cat, header: userHeader, log: log, tmpl: tmpl}, nil
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.Handle("/", s.identify(app))
	return mux
}

type userKey struct{}

func (s *Server) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		email := strings.TrimSpace(r.Header.Get(s.header))
		if email == "" {
			http.Error(w, "не удалось узнать, кто вы", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, email)))
	})
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	s.render(w, "page", r.Context().Value(userKey{}))
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		s.render(w, "results", []catalog.Title(nil))
		return
	}
	titles, err := s.cat.Search(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "results", titles)
}

func (s *Server) request(w http.ResponseWriter, r *http.Request) {
	mediaType := r.FormValue("mediaType")
	tmdbID, err := strconv.Atoi(r.FormValue("tmdbId"))
	if err != nil || (mediaType != "movie" && mediaType != "tv") {
		http.Error(w, "непонятная заявка", http.StatusBadRequest)
		return
	}
	uid, err := s.cat.UserID(r.Context(), r.Context().Value(userKey{}).(string))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.cat.Request(r.Context(), uid, mediaType, tmdbID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "action", catalog.Title{MediaType: mediaType, TMDBID: tmdbID, Status: catalog.StatusPending})
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	downloads, err := s.cat.Queue(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, "queue", downloads)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render", "template", name, "err", err)
	}
}

// fail logs err and answers with a fragment htmx shows in place of the
// result, so the page says what went wrong instead of silently not updating.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request", "path", r.URL.Path, "err", err)
	w.WriteHeader(http.StatusBadGateway)
	s.render(w, "error", nil)
}

// formatLeft renders a remaining download time the way a person says it.
func formatLeft(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return "меньше минуты"
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч %d мин", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d д %d ч", int(d.Hours())/24, int(d.Hours())%24)
	}
}
