package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/text/language"
)

// The first is what a browser asking for none of them gets.
var matcher = language.NewMatcher([]language.Tag{language.English, language.Russian})

var messages = map[string]map[string]string{
	"en": {
		"title":         "What to watch",
		"auto":          "auto",
		"placeholder":   "Movie or series",
		"searching":     "searching…",
		"queue":         "Queue",
		"nothing found": "Nothing found.",
		"movie":         "movie",
		"tv":            "series",
		"request":       "Request",
		"requested":     "Requested",
		"processing":    "Searching and downloading",
		"partial":       "Partly here",
		"available":     "Here already",
		"queue empty":   "Nothing is downloading or being searched for.",
		"importing":     "downloaded, importing",
		"looking":       "looking for a release",
		"season":        "season %d, %d episodes",
		"seasons":       "seasons %s",
		"one season":    "season %s",
		"error":         "Something broke. Try again a little later.",
		"who":           "could not tell who you are",
		"bad request":   "a request that makes no sense",
		"under minute":  "under a minute",
		"minutes":       "%d min",
		"hours":         "%d h %d min",
		"days":          "%d d %d h",
	},
	"ru": {
		"title":         "Что посмотреть",
		"auto":          "авто",
		"placeholder":   "Фильм или сериал",
		"searching":     "ищем…",
		"queue":         "Очередь",
		"nothing found": "Ничего не нашлось.",
		"movie":         "фильм",
		"tv":            "сериал",
		"request":       "Заказать",
		"requested":     "Заказано",
		"processing":    "Ищем и качаем",
		"partial":       "Есть частично",
		"available":     "Уже есть",
		"queue empty":   "Сейчас ничего не качается и не ищется.",
		"importing":     "скачано, раскладываем",
		"looking":       "ищем раздачу",
		"season":        "сезон %d, эпизодов: %d",
		"seasons":       "сезоны: %s",
		"one season":    "сезон %s",
		"error":         "Что-то сломалось. Попробуйте ещё раз чуть позже.",
		"who":           "не удалось узнать, кто вы",
		"bad request":   "непонятная заявка",
		"under minute":  "меньше минуты",
		"minutes":       "%d мин",
		"hours":         "%d ч %d мин",
		"days":          "%d д %d ч",
	},
}

// locale is the language a request is answered in, as an ISO 639-1 code
// that is also what TMDB takes for names.
type locale string

// langCookie keeps the language a person picked on the page.
const langCookie = "lang"

func supported(l string) bool {
	_, ok := messages[l]
	return ok
}

// chosen is the language picked on the page, or "".
func chosen(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && supported(c.Value) {
		return c.Value
	}
	return ""
}

// locale is the language picked on the page, else the locale claim of the
// person's profile, else the browser's.
func (s *Server) locale(r *http.Request) locale {
	if l := chosen(r); l != "" {
		return locale(l)
	}
	header := r.Header.Get("Accept-Language")
	if claim := viewerOf(r).Locale; claim != "" {
		header = claim
	}
	tags, _, _ := language.ParseAcceptLanguage(header)
	_, i, _ := matcher.Match(tags...)
	return []locale{"en", "ru"}[i]
}

// t is the message key in the locale, formatted with args when it has verbs.
func (l locale) t(key string, args ...any) string {
	m, ok := messages[string(l)][key]
	if !ok {
		panic(fmt.Sprintf("web: no message %q in %s", key, l))
	}
	if len(args) == 0 {
		return m
	}
	return fmt.Sprintf(m, args...)
}

// left renders a remaining download time the way a person says it.
func (l locale) left(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return l.t("under minute")
	case d < time.Hour:
		return l.t("minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return l.t("hours", int(d.Hours()), int(d.Minutes())%60)
	default:
		return l.t("days", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// seasons names the seasons of a request still being searched for.
func (l locale) seasons(numbers []int) string {
	s := make([]string, len(numbers))
	for i, n := range numbers {
		s[i] = fmt.Sprint(n)
	}
	if len(s) == 1 {
		return l.t("one season", s[0])
	}
	return l.t("seasons", strings.Join(s, ", "))
}
