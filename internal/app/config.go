package app

import (
	"encoding/base64"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/deyna256/clan/internal/management"
)

type config struct {
	listenAddr    string
	dbPath        string
	adminToken    string
	encryptionKey []byte
	callbackAddr  string
}

func loadConfig(getenv func(string) string) (config, error) {
	value := func(name, fallback string) string {
		if found := getenv(name); found != "" {
			return found
		}
		return fallback
	}
	c := config{
		listenAddr:   value("CLAN_LISTEN_ADDR", "127.0.0.1:8080"),
		dbPath:       value("CLAN_DB_PATH", "./clan.db"),
		adminToken:   getenv("CLAN_ADMIN_TOKEN"),
		callbackAddr: value("CLAN_OAUTH_CALLBACK_ADDR", "127.0.0.1:1455"),
	}
	encoded := getenv("CLAN_ENCRYPTION_KEY")
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
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
	if management.ValidateConfig(management.Config{AdminToken: c.adminToken}) != nil {
		return errors.New("app: CLAN_ADMIN_TOKEN must contain a separate valid admin token")
	}
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
		number, parseErr := strconv.Atoi(port)
		if err != nil || parseErr != nil || number < 0 || number > 65535 {
			return errors.New("app: " + address.name + " must be a TCP host:port address")
		}
	}
	return nil
}
