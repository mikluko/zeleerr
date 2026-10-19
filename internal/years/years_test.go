package years

import "testing"

func TestRename(t *testing.T) {
	n := NewNamer([]Series{
		{"Avatar: The Last Airbender (2024)"},
		{"Avatar: The Last Airbender"},
		{"Dark Matter (2015)"},
		{"Dark Matter (2024)"},
		{"The Lowdown (2025)"},
		{"Silo"},
	})
	for _, c := range []struct{ in, want string }{
		{
			"Подноготная / The Lowdown / S1E1-8 of 8 [2025, США, драма, WEB-DL 2160p]",
			"Подноготная / The Lowdown (2025) / S1E1-8 of 8 [2025, США, драма, WEB-DL 2160p]",
		},
		{ // a later season goes to the series it belongs to
			"Темная материя / Dark Matter / S2E1-7 of 10 [2026, США, WEB-DL 2160p]",
			"Темная материя / Dark Matter (2024) / S2E1-7 of 10 [2026, США, WEB-DL 2160p]",
		},
		{ // the older series of the same name keeps its releases
			"Тёмная материя / Dark Matter / S1E1-13 of 13 [2015, Канада, WEB-DL 1080p]",
			"Тёмная материя / Dark Matter (2015) / S1E1-13 of 13 [2015, Канада, WEB-DL 1080p]",
		},
		{
			"Аватар: Легенда об Аанге / Avatar: The Last Airbender / S1E1-8 of 8 [2024, США, WEB-DL 1080p]",
			"Аватар: Легенда об Аанге / Avatar: The Last Airbender (2024) / S1E1-8 of 8 [2024, США, WEB-DL 1080p]",
		},
		{ // before any series titled with a year: left for the one without
			"Аватар: Легенда об Аанге / Avatar: The Last Airbender / S1E1-20 of 20 [2005, США, мультфильм, BDRip]",
			"Аватар: Легенда об Аанге / Avatar: The Last Airbender / S1E1-20 of 20 [2005, США, мультфильм, BDRip]",
		},
		{ // a series titled without a year is not touched
			"Укрытие / Silo / S4E1-10 of 10 [2027, США, WEB-DL 1080p]",
			"Укрытие / Silo / S4E1-10 of 10 [2027, США, WEB-DL 1080p]",
		},
		{ // no year in the tags
			"Dark Matter S02 WEB-DL 1080p",
			"Dark Matter S02 WEB-DL 1080p",
		},
	} {
		if got := n.Rename(c.in); got != c.want {
			t.Errorf("Rename(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
