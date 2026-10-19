package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	c, err := loadConfig(func(k string) string {
		return map[string]string{"PROWLARR_URL": "http://prowlarr:9696/", "SONARR_URL": "http://sonarr:8989", "SONARR_API_KEY": "k"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != ":9696" || c.prowlarrURL != "http://prowlarr:9696" || c.refresh != 10*time.Minute {
		t.Fatalf("config = %+v", c)
	}
	if _, err := loadConfig(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "SONARR_API_KEY") {
		t.Fatalf("err = %v", err)
	}
}
