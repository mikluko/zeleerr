package years

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const feed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<title>RuTracker.org</title>
<item>
<title>Подноготная / The Lowdown / S1E1-8 of 8 [2025, США &amp; Канада, WEB-DL 2160p]</title>
<guid>https://rutracker.org/forum/viewtopic.php?t=1</guid>
<torznab:attr name="seeders" value="50"/>
</item>
<item>
<title>Укрытие / Silo / S4E1-10 of 10 [2027, США, WEB-DL 1080p]</title>
</item>
</channel>
</rss>
`

func TestRewriteTitlesKeepsTheRest(t *testing.T) {
	n := NewNamer([]Series{{"The Lowdown (2025)"}})
	got, err := RewriteTitles([]byte(feed), n.Rename)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(feed, "Подноготная / The Lowdown / S1E1-8", "Подноготная / The Lowdown (2025) / S1E1-8", 1)
	if string(got) != want {
		t.Fatalf("feed =\n%s\nwant\n%s", got, want)
	}
}

func TestProxyRenamesOnlySearchAnswers(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<caps><title>The Lowdown / x [2025]</title></caps>`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/download") {
			_, _ = io.WriteString(w, "torrent bytes")
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = io.WriteString(w, feed)
	}))
	defer prowlarr.Close()
	upstream, _ := url.Parse(prowlarr.URL)
	n := NewNamer([]Series{{"The Lowdown (2025)"}})
	proxy := httptest.NewServer(NewProxy(upstream, func() *Namer { return n }, slog.New(slog.DiscardHandler)))
	defer proxy.Close()

	get := func(path string) string {
		res, err := http.Get(proxy.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	if body := get("/1/api?t=tvsearch&q=The+Lowdown&apikey=k"); !strings.Contains(body, "The Lowdown (2025) / S1E1-8") {
		t.Errorf("search answer not renamed:\n%s", body)
	}
	if body := get("/1/api?t=caps"); strings.Contains(body, "(2025)") {
		t.Errorf("caps renamed: %s", body)
	}
	if body := get("/1/download?link=x"); body != "torrent bytes" {
		t.Errorf("download = %q", body)
	}
}

func TestProxyWithoutNamerPassesThrough(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, feed)
	}))
	defer prowlarr.Close()
	upstream, _ := url.Parse(prowlarr.URL)
	proxy := httptest.NewServer(NewProxy(upstream, func() *Namer { return nil }, slog.New(slog.DiscardHandler)))
	defer proxy.Close()
	res, err := http.Get(proxy.URL + "/1/api?t=tvsearch")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(b) != feed {
		t.Errorf("feed changed without a namer")
	}
}
