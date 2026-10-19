// Command torznab-years stands between Sonarr and Prowlarr and puts the
// disambiguation year of Sonarr's series into release titles that lack it;
// see package years. It is configured by the environment; see the README.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mikluko/zeleerr/internal/years"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("torznab-years", "err", err)
		os.Exit(1)
	}
}

type config struct {
	addr                 string
	prowlarrURL          string
	sonarrURL, sonarrKey string
	refresh              time.Duration
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		addr:        or(getenv("YEARS_ADDR"), ":9696"),
		prowlarrURL: strings.TrimRight(getenv("PROWLARR_URL"), "/"),
		sonarrURL:   strings.TrimRight(getenv("SONARR_URL"), "/"),
		sonarrKey:   getenv("SONARR_API_KEY"),
	}
	var missing []string
	for _, v := range []struct{ name, value string }{
		{"PROWLARR_URL", c.prowlarrURL}, {"SONARR_URL", c.sonarrURL}, {"SONARR_API_KEY", c.sonarrKey},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}
	refresh, err := time.ParseDuration(or(getenv("YEARS_REFRESH"), "10m"))
	if err != nil {
		return c, fmt.Errorf("YEARS_REFRESH: %w", err)
	}
	c.refresh = refresh
	return c, nil
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	upstream, err := url.Parse(cfg.prowlarrURL)
	if err != nil {
		return fmt.Errorf("PROWLARR_URL: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var namer atomic.Pointer[years.Namer]
	httpc := &http.Client{Timeout: 30 * time.Second}
	go func() {
		for {
			series, err := fetchSeries(ctx, httpc, cfg.sonarrURL, cfg.sonarrKey)
			if err != nil {
				log.Warn("series", "err", err)
			} else {
				namer.Store(years.NewNamer(series))
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(cfg.refresh):
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /_years/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.Handle("/", years.NewProxy(upstream, namer.Load, log))
	hs := &http.Server{Addr: cfg.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	log.Info("listening", "addr", cfg.addr, "upstream", cfg.prowlarrURL)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// fetchSeries lists the series Sonarr has, by the titles it knows them by.
func fetchSeries(ctx context.Context, c *http.Client, base, key string) ([]years.Series, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v3/series", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", key)
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New(res.Status)
	}
	var series []years.Series
	return series, json.NewDecoder(res.Body).Decode(&series)
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
