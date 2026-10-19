package years

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// NewProxy returns a reverse proxy to the Prowlarr at upstream that renames
// release titles in its Torznab search answers with the Namer namer returns,
// and passes everything else through. With a nil Namer it renames nothing.
func NewProxy(upstream *url.URL, namer func() *Namer, log *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Host = upstream.Host
			if isSearch(r.In) {
				// The answer is rewritten, so it has to come uncompressed.
				r.Out.Header.Del("Accept-Encoding")
			}
		},
		ModifyResponse: func(res *http.Response) error {
			n := namer()
			if n == nil || !isSearch(res.Request) || res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "xml") {
				return nil
			}
			body, err := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if err != nil {
				return err
			}
			renamed, err := RewriteTitles(body, n.Rename)
			if err != nil {
				log.Warn("feed left as it is", "path", res.Request.URL.Path, "err", err)
				renamed = body
			}
			res.Body = io.NopCloser(bytes.NewReader(renamed))
			res.ContentLength = int64(len(renamed))
			res.Header.Set("Content-Length", strconv.Itoa(len(renamed)))
			return nil
		},
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
}

// isSearch reports a Torznab search of an indexer, /<id>/api?t=search or
// t=tvsearch, the requests whose answers carry release titles.
func isSearch(r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 || parts[1] != "api" {
		return false
	}
	if _, err := strconv.Atoi(parts[0]); err != nil {
		return false
	}
	t := r.URL.Query().Get("t")
	return t == "search" || t == "tvsearch"
}
