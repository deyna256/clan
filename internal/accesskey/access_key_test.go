package accesskey_test

import (
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/accesskey"
)

func TestNewRejectsInvalidIdentity(t *testing.T) {
	tests := []struct {
		name      string
		identity  accesskey.Identity
		wantField string
	}{
		{name: "missing id", identity: accesskey.Identity{Name: "Agent"}, wantField: "id"},
		{name: "blank id", identity: accesskey.Identity{ID: " \t\n", Name: "Agent"}, wantField: "id"},
		{name: "missing name", identity: accesskey.Identity{ID: "key"}, wantField: "name"},
		{name: "blank name", identity: accesskey.Identity{ID: "key", Name: " \t\n"}, wantField: "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := accesskey.New(tt.identity, true)

			if err == nil {
				t.Fatal("New() accepted an invalid identity")
			}
			if !strings.Contains(" "+err.Error()+" ", " "+tt.wantField+" ") {
				t.Errorf("error %q does not identify field %q", err, tt.wantField)
			}
			if key.Identity() != (accesskey.Identity{}) {
				t.Error("New() returned a partial identity on failure")
			}
			if key.Enabled() {
				t.Error("key returned on failure allows access")
			}
		})
	}
}

func TestNewPreservesIdentityAndStatus(t *testing.T) {
	for _, tt := range []struct {
		name    string
		enabled bool
	}{
		{name: "enabled", enabled: true},
		{name: "disabled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			identity := accesskey.Identity{ID: " key ", Name: " Agent "}

			key, err := accesskey.New(identity, tt.enabled)

			if err != nil {
				t.Fatal(err)
			}
			if key.Identity() != identity || key.Enabled() != tt.enabled {
				t.Errorf("key = (%+v, %t), want (%+v, %t)", key.Identity(), key.Enabled(), identity, tt.enabled)
			}
		})
	}
}
