// Command seerr-gate serves Seerr to people an authenticating proxy has
// signed in, each as a Seerr user of the same email. It is configured by the
// environment; see the README.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mikluko/zeleerr/internal/catalog"
	"github.com/mikluko/zeleerr/internal/gate"
	"github.com/mikluko/zeleerr/internal/seerr"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("seerr-gate", "err", err)
		os.Exit(1)
	}
}

type config struct {
	addr, userHeader   string
	seerrURL, seerrKey string
	key                string
	permissions        int
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		addr:       or(getenv("GATE_ADDR"), ":8081"),
		userHeader: or(getenv("GATE_USER_HEADER"), "X-Forwarded-Email"),
		seerrURL:   strings.TrimRight(getenv("SEERR_URL"), "/"),
		seerrKey:   getenv("SEERR_API_KEY"),
		key:        getenv("GATE_KEY"),
	}
	var missing []string
	for _, v := range []struct{ name, value string }{
		{"SEERR_URL", c.seerrURL}, {"SEERR_API_KEY", c.seerrKey}, {"GATE_KEY", c.key},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}
	perms, err := strconv.Atoi(or(getenv("GATE_PERMISSIONS"), "160"))
	if err != nil {
		return c, fmt.Errorf("GATE_PERMISSIONS: %w", err)
	}
	c.permissions = perms
	return c, nil
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	upstream, err := url.Parse(cfg.seerrURL)
	if err != nil {
		return fmt.Errorf("SEERR_URL: %w", err)
	}
	httpc := &http.Client{Timeout: 30 * time.Second}
	admin, err := seerr.NewClientWithResponses(cfg.seerrURL+"/api/v1", seerr.WithHTTPClient(httpc),
		seerr.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("X-Api-Key", cfg.seerrKey)
			return nil
		}))
	if err != nil {
		return err
	}
	anon, err := seerr.NewClientWithResponses(cfg.seerrURL+"/api/v1", seerr.WithHTTPClient(httpc))
	if err != nil {
		return err
	}
	g := gate.New(upstream, admin, anon, catalog.New(admin, nil, nil, cfg.permissions), []byte(cfg.key), cfg.userHeader, log)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /_gate/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.Handle("/", g)
	hs := &http.Server{Addr: cfg.addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	log.Info("listening", "addr", cfg.addr, "upstream", cfg.seerrURL)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
