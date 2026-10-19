package main

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"SEERR_URL": "http://seerr:5055/", "SEERR_API_KEY": "s",
		"RADARR_URL": "http://radarr:7878", "RADARR_API_KEY": "r",
		"SONARR_URL": "http://sonarr:8989", "SONARR_API_KEY": "so",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != ":8080" || c.userHeader != "X-Auth-Request-Email" || c.permissions != 160 || c.seerrURL != "http://seerr:5055" {
		t.Fatalf("config = %+v", c)
	}
}

func TestLoadConfigNamesWhatIsMissing(t *testing.T) {
	_, err := loadConfig(env(map[string]string{"SEERR_URL": "http://seerr"}))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"SEERR_API_KEY", "RADARR_URL", "SONARR_API_KEY"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}
