// Package years puts a series' disambiguation year into release titles that
// lack it, so that Sonarr matches them.
//
// TheTVDB, and Sonarr with it, titles a series "Dark Matter (2024)" when
// another series has the same name. RuTracker titles a release
// "Темная материя / Dark Matter / S2E1-7 of 10 [2026, США, ...]": no year by
// the name, and the year in the tags is the season's. Sonarr matches neither
// and rejects the release as an unknown series.
package years

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Series is a series as Sonarr titles it.
type Series struct {
	Title string
}

// Namer rewrites release titles for a set of series.
type Namer struct {
	// byName holds, for each name without a year, the years of the series
	// titled with it, and the full titles.
	byName map[string][]titled
}

type titled struct {
	year  int
	title string
}

var yearSuffix = regexp.MustCompile(`^(.*\S)\s+\((\d{4})\)$`)

// NewNamer returns a Namer for the series; those titled without a year are
// left alone.
func NewNamer(series []Series) *Namer {
	n := &Namer{byName: map[string][]titled{}}
	for _, s := range series {
		m := yearSuffix.FindStringSubmatch(s.Title)
		if m == nil {
			continue
		}
		year, _ := strconv.Atoi(m[2])
		key := clean(m[1])
		n.byName[key] = append(n.byName[key], titled{year, s.Title})
	}
	return n
}

// tagYear is the year opening the first tag of a RuTracker title, "[2026, ...".
var tagYear = regexp.MustCompile(`\[\s*((?:19|20)\d{2})\b`)

// Rename returns the release title with the name of a series titled with a
// year replaced by that title, "Dark Matter" by "Dark Matter (2024)". Of the
// series of that name it takes the one with the latest year not after the
// year in the release's tags, so a season released later still goes to its
// series, and an older series of the same name keeps its releases. A title
// without a year in its tags, or naming no such series, comes back as it is.
func (n *Namer) Rename(title string) string {
	m := tagYear.FindStringSubmatch(title)
	if m == nil {
		return title
	}
	year, _ := strconv.Atoi(m[1])
	parts := strings.Split(title, " / ")
	for i, part := range parts {
		var best *titled
		for _, t := range n.byName[clean(part)] {
			if t.year <= year && (best == nil || t.year > best.year) {
				best = &t
			}
		}
		if best != nil {
			parts[i] = best.title
			return strings.Join(parts, " / ")
		}
	}
	return title
}

// clean folds a name to letters and digits in lower case.
func clean(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
