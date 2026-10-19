// Command zelirr serves the family page in front of Seerr, Radarr and Sonarr.
// It is configured by the environment; see the README.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mikluko/zelirr/internal/catalog"
	"github.com/mikluko/zelirr/internal/radarr"
	"github.com/mikluko/zelirr/internal/seerr"
	"github.com/mikluko/zelirr/internal/sonarr"
	"github.com/mikluko/zelirr/internal/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("zelirr", "err", err)
		os.Exit(1)
	}
}

type config struct {
	addr, userHeader     string
	seerrURL, seerrKey   string
	radarrURL, radarrKey string
	sonarrURL, sonarrKey string
	permissions          int
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		addr:       or(getenv("ZELIRR_ADDR"), ":8080"),
		userHeader: or(getenv("ZELIRR_USER_HEADER"), "X-Auth-Request-Email"),
		seerrURL:   strings.TrimRight(getenv("SEERR_URL"), "/"),
		seerrKey:   getenv("SEERR_API_KEY"),
		radarrURL:  strings.TrimRight(getenv("RADARR_URL"), "/"),
		radarrKey:  getenv("RADARR_API_KEY"),
		sonarrURL:  strings.TrimRight(getenv("SONARR_URL"), "/"),
		sonarrKey:  getenv("SONARR_API_KEY"),
	}
	var missing []string
	for name, v := range map[string]string{
		"SEERR_URL": c.seerrURL, "SEERR_API_KEY": c.seerrKey,
		"RADARR_URL": c.radarrURL, "RADARR_API_KEY": c.radarrKey,
		"SONARR_URL": c.sonarrURL, "SONARR_API_KEY": c.sonarrKey,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}
	perms, err := strconv.Atoi(or(getenv("ZELIRR_PERMISSIONS"), "160"))
	if err != nil {
		return c, fmt.Errorf("ZELIRR_PERMISSIONS: %w", err)
	}
	c.permissions = perms
	return c, nil
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	httpc := &http.Client{Timeout: 30 * time.Second}

	sc, err := seerr.NewClientWithResponses(cfg.seerrURL+"/api/v1", seerr.WithHTTPClient(httpc), seerr.WithRequestEditorFn(apiKey[seerr.RequestEditorFn](cfg.seerrKey)))
	if err != nil {
		return err
	}
	rc, err := radarr.NewClientWithResponses(cfg.radarrURL, radarr.WithHTTPClient(httpc), radarr.WithRequestEditorFn(apiKey[radarr.RequestEditorFn](cfg.radarrKey)))
	if err != nil {
		return err
	}
	soc, err := sonarr.NewClientWithResponses(cfg.sonarrURL, sonarr.WithHTTPClient(httpc), sonarr.WithRequestEditorFn(apiKey[sonarr.RequestEditorFn](cfg.sonarrKey)))
	if err != nil {
		return err
	}

	srv, err := web.New(catalog.New(sc, rc, soc, cfg.permissions), cfg.userHeader, log)
	if err != nil {
		return err
	}
	hs := &http.Server{Addr: cfg.addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	log.Info("listening", "addr", cfg.addr)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// apiKey sets the X-Api-Key header all three services take.
func apiKey[F ~func(context.Context, *http.Request) error](key string) F {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("X-Api-Key", key)
		return nil
	}
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
