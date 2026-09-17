package accesskey_test

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/accesskey"
)

const fixtureRaw = "clan_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

func TestGenerateReturnsCanonicalKeyAndVerificationHash(t *testing.T) {
	raw, hash := accesskey.Generate()

	if len(raw) != 48 || !strings.HasPrefix(raw, "clan_") {
		t.Fatal("Generate() returned an invalid key prefix or length")
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(raw[5:])
	if err != nil || len(payload) != 32 {
		t.Fatal("Generate() did not encode exactly 32 bytes as canonical base64url")
	}
	lookup, ok := accesskey.Hash(raw)
	if !ok || lookup != hash {
		t.Error("lookup hash does not match the generated verification record")
	}
}

func TestHashMatchesIndependentSHA256Vector(t *testing.T) {
	want := fixtureHash()

	got, ok := accesskey.Hash(fixtureRaw)
	if !ok || got != want {
		t.Errorf("Hash() = (%x, %t), want (%x, true)", got, ok, want)
	}
}

func TestHashDoesNotMatchDifferentKeyAndRecords(t *testing.T) {
	original := fixtureHash()
	changedFirst := original
	changedFirst[0] ^= 1
	changedLast := original
	changedLast[31] ^= 1
	otherRaw := "clan_B" + fixtureRaw[6:]
	tests := []struct {
		name string
		raw  string
		hash accesskey.VerificationHash
	}{
		{name: "different canonical key", raw: otherRaw, hash: original},
		{name: "changed first digest byte", raw: fixtureRaw, hash: changedFirst},
		{name: "changed last digest byte", raw: fixtureRaw, hash: changedLast},
		{name: "zero record", raw: fixtureRaw},
		{name: "digest presented as key", raw: hex.EncodeToString(original[:]), hash: original},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate, valid := accesskey.Hash(tt.raw)

			if valid && candidate == tt.hash {
				t.Error("Hash() matched a different key or verification record")
			}
		})
	}
	if _, ok := accesskey.Hash(otherRaw); !ok {
		t.Error("different-key test must present a canonical key")
	}
}

func TestMalformedKeysReturnNoVerificationHash(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty"},
		{name: "missing prefix", raw: fixtureRaw[5:]},
		{name: "prefix case", raw: "CLAN_" + fixtureRaw[5:]},
		{name: "wrong prefix", raw: "other" + fixtureRaw[5:]},
		{name: "short payload", raw: fixtureRaw[:47]},
		{name: "long payload", raw: fixtureRaw + "A"},
		{name: "padding", raw: fixtureRaw + "="},
		{name: "leading space", raw: " " + fixtureRaw},
		{name: "trailing space", raw: fixtureRaw + " "},
		{name: "standard base64 plus", raw: fixtureRaw[:47] + "+"},
		{name: "standard base64 slash", raw: fixtureRaw[:47] + "/"},
		{name: "unicode", raw: fixtureRaw[:46] + "é"},
		{name: "noncanonical trailing bits", raw: fixtureRaw[:47] + "9"},
		{name: "newline with unchanged length", raw: "clan_" + strings.Repeat("A", 42) + "\n"},
		{name: "carriage return with unchanged length", raw: "clan_" + strings.Repeat("A", 42) + "\r"},
		{name: "large input", raw: "clan_" + strings.Repeat("A", 1<<20)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, ok := accesskey.Hash(tt.raw)
			if ok || hash != (accesskey.VerificationHash{}) {
				t.Errorf("Hash() = (%x, %t), want zero hash and false", hash, ok)
			}
		})
	}
}

func fixtureHash() accesskey.VerificationHash {
	// Independently computed with Python hashlib.sha256 over the complete fixtureRaw.
	return accesskey.VerificationHash{
		0x67, 0x2e, 0x07, 0x04, 0xc4, 0x37, 0x59, 0x9f,
		0xdf, 0xbf, 0x39, 0x4f, 0x9f, 0x83, 0x71, 0xbc,
		0xee, 0x7f, 0x86, 0x83, 0x5a, 0xa1, 0x3a, 0x24,
		0x72, 0x7f, 0xac, 0xbc, 0xe6, 0x6c, 0x01, 0xce,
	}
}
