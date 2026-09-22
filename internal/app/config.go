package app

import (
	"cmp"
	"encoding/base64"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/deyna256/clan/internal/management"
)

type config struct {
	listenAddr     string
	dbPath         string
	adminToken     string
	encryptionKey  []byte
	callbackAddr   string
	usageRetention time.Duration
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		listenAddr:   cmp.Or(getenv("CLAN_LISTEN_ADDR"), "127.0.0.1:8080"),
		dbPath:       cmp.Or(getenv("CLAN_DB_PATH"), "./clan.db"),
		adminToken:   getenv("CLAN_ADMIN_TOKEN"),
		callbackAddr: cmp.Or(getenv("CLAN_OAUTH_CALLBACK_ADDR"), "127.0.0.1:1455"),
	}
	retention, err := time.ParseDuration(value("CLAN_USAGE_RETENTION", "2160h"))
	if err != nil || retention <= 0 {
		return config{}, errors.New("app: CLAN_USAGE_RETENTION must be a positive Go duration")
	}
	c.usageRetention = retention
	encoded := getenv("CLAN_ENCRYPTION_KEY")
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	// Report malformed encoding and invalid lengths with the same safe env error.
	if err != nil || len(key) != 32 || strings.ContainsAny(encoded, "\r\n") {
		return config{}, errors.New("app: CLAN_ENCRYPTION_KEY must be standard base64 encoding of 32 bytes")
	}
	c.encryptionKey = key
	if err := c.validate(); err != nil {
		clear(key)
		return config{}, err
	}
	return c, nil
}

func (c config) validate() error {
	if c.usageRetention <= 0 {
		return errors.New("app: CLAN_USAGE_RETENTION must be a positive Go duration")
	}
	if management.ValidateConfig(management.Config{AdminToken: c.adminToken}) != nil {
		return errors.New("app: CLAN_ADMIN_TOKEN must contain a separate valid admin token")
	}
	// Keep the key-length invariant for configs constructed directly in tests;
	// production loadConfig already checks the decoded key length.
	if len(c.encryptionKey) != 32 {
		return errors.New("app: CLAN_ENCRYPTION_KEY must decode to 32 bytes")
	}
	if strings.TrimSpace(c.dbPath) == "" {
		return errors.New("app: CLAN_DB_PATH must not be blank")
	}
	for _, address := range []struct{ name, value string }{
		{name: "CLAN_LISTEN_ADDR", value: c.listenAddr},
		{name: "CLAN_OAUTH_CALLBACK_ADDR", value: c.callbackAddr},
	} {
		_, port, err := net.SplitHostPort(address.value)
		_, parseErr := strconv.ParseUint(port, 10, 16)
		if err != nil || parseErr != nil {
			return errors.New("app: " + address.name + " must be a TCP host:port address")
		}
	}
	return nil
}
