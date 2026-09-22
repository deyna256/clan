package app

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	env := map[string]string{
		"CLAN_ADMIN_TOKEN":    "separate-admin-token",
		"CLAN_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	getenv := func(name string) string { return env[name] }
	c, err := loadConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if c.usageRetention != 90*24*time.Hour {
		t.Fatalf("retention = %s", c.usageRetention)
	}
	if c.listenAddr != "127.0.0.1:8080" || c.callbackAddr != "127.0.0.1:1455" || c.dbPath != "./clan.db" {
		t.Fatalf("incorrect defaults: listen=%s callback=%s database=%s", c.listenAddr, c.callbackAddr, c.dbPath)
	}
	env["CLAN_LISTEN_ADDR"] = "[::1]:9000"
	env["CLAN_OAUTH_CALLBACK_ADDR"] = ":1455"
	env["CLAN_DB_PATH"] = "/tmp/config-test.db"
	env["CLAN_USAGE_RETENTION"] = "48h"

	c, err = loadConfig(getenv)

	if err != nil || c.listenAddr != "[::1]:9000" || c.callbackAddr != ":1455" || c.dbPath != "/tmp/config-test.db" {
		t.Fatalf("overrides were not loaded: %v", err)
	}
	if c.usageRetention != 48*time.Hour {
		t.Fatalf("retention override = %s", c.usageRetention)
	}
}

func TestLoadConfigRejectsInvalidValuesWithoutDisclosingThem(t *testing.T) {
	for _, tt := range []struct{ name, variable, value string }{
		{name: "invalid retention", variable: "CLAN_USAGE_RETENTION", value: "secret-marker"},
		{name: "zero retention", variable: "CLAN_USAGE_RETENTION", value: "0"},
		{name: "negative retention", variable: "CLAN_USAGE_RETENTION", value: "-1h"},
		{name: "missing admin", variable: "CLAN_ADMIN_TOKEN"},
		{name: "admin whitespace", variable: "CLAN_ADMIN_TOKEN", value: "secret-marker token"},
		{name: "admin client key", variable: "CLAN_ADMIN_TOKEN", value: "clan_secret-marker"},
		{name: "missing key", variable: "CLAN_ENCRYPTION_KEY"},
		{name: "malformed key", variable: "CLAN_ENCRYPTION_KEY", value: "secret-marker"},
		{name: "short key", variable: "CLAN_ENCRYPTION_KEY", value: base64.StdEncoding.EncodeToString(make([]byte, 31))},
		{name: "long key", variable: "CLAN_ENCRYPTION_KEY", value: base64.StdEncoding.EncodeToString(make([]byte, 33))},
		{name: "key newline", variable: "CLAN_ENCRYPTION_KEY", value: base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"},
		{name: "blank database", variable: "CLAN_DB_PATH", value: " "},
		{name: "missing port", variable: "CLAN_LISTEN_ADDR", value: "secret-marker"},
		{name: "invalid port", variable: "CLAN_LISTEN_ADDR", value: "localhost:65536"},
		{name: "invalid callback", variable: "CLAN_OAUTH_CALLBACK_ADDR", value: "localhost:-1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{
				"CLAN_ADMIN_TOKEN":    "admin-token",
				"CLAN_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
			}
			env[tt.variable] = tt.value

			_, err := loadConfig(func(name string) string { return env[name] })

			if err == nil || !strings.Contains(err.Error(), tt.variable) || strings.Contains(err.Error(), "secret-marker") {
				t.Fatalf("invalid configuration error = %v", err)
			}
		})
	}
}

func TestNewApplicationRejectsInvalidDirectConfig(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*config)
		want   string
	}{
		{
			name: "admin token",
			modify: func(c *config) {
				c.adminToken = "clan_secret-marker"
			},
			want: "CLAN_ADMIN_TOKEN",
		},
		{
			name: "encryption key length",
			modify: func(c *config) {
				c.encryptionKey = c.encryptionKey[:31]
			},
			want: "CLAN_ENCRYPTION_KEY",
		},
		{
			name: "blank database path",
			modify: func(c *config) {
				c.dbPath = " "
			},
			want: "CLAN_DB_PATH",
		},
		{
			name: "invalid listen port",
			modify: func(c *config) {
				c.listenAddr = "localhost:65536"
			},
			want: "CLAN_LISTEN_ADDR",
		},
		{
			name: "invalid callback port",
			modify: func(c *config) {
				c.callbackAddr = "localhost:-1"
			},
			want: "CLAN_OAUTH_CALLBACK_ADDR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConfig(t)
			tt.modify(&c)

			// A bad config must fail before dependency setup.
			_, err := newApplication(t.Context(), c, nil, nil, "", "")

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("direct config validation = %v, want %s", err, tt.want)
			}
			if strings.Contains(err.Error(), "secret-marker") {
				t.Fatalf("validation disclosed the supplied secret: %v", err)
			}
		})
	}
}
