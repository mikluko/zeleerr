package main

import (
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	c, err := loadConfig(func(k string) string {
		return map[string]string{"SEERR_URL": "http://127.0.0.1:5055/", "SEERR_API_KEY": "k", "GATE_KEY": "g"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != ":8081" || c.userHeader != "X-Forwarded-Email" || c.seerrURL != "http://127.0.0.1:5055" || c.permissions != 160 {
		t.Fatalf("config = %+v", c)
	}
	if _, err := loadConfig(func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "GATE_KEY") {
		t.Fatalf("err = %v, want it to name GATE_KEY", err)
	}
}
