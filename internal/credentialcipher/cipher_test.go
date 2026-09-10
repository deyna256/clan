package credentialcipher_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/upstream"
)

func TestNewRejectsWrongKeyLengths(t *testing.T) {
	for _, size := range []int{0, 16, 24, 31, 33} {
		key := bytes.Repeat([]byte{0x73}, size)

		cipher, err := credentialcipher.New(key)

		if err == nil || cipher != nil {
			t.Errorf("New() with %d key bytes = (%v, %v), want nil cipher and error", size, cipher, err)
		}
	}
}

func TestDecryptIndependentRecord(t *testing.T) {
	cipher := newCipher(t)
	record := fixtureRecord(t)
	wantRecord := bytes.Clone(record)
	want := []byte(`{"key":"test-secret"}`)

	plaintext, err := cipher.Decrypt("account", "upstream", record)

	if err != nil || !bytes.Equal(plaintext, want) {
		t.Fatalf("Decrypt() = (%q, %v), want independent fixture plaintext", plaintext, err)
	}
	if !bytes.Equal(record, wantRecord) {
		t.Error("Decrypt() changed its input record")
	}
	plaintext[0] ^= 1
	if !bytes.Equal(record, wantRecord) {
		t.Error("returned plaintext shares storage with the record")
	}
}

func TestEncryptCompleteCredentialPayloads(t *testing.T) {
	cipher := newCipher(t)
	tests := []struct {
		name      string
		plaintext []byte
	}{
		{name: "API key", plaintext: []byte(`{"key":"upstream-api-key"}`)},
		{name: "OAuth", plaintext: []byte(`{"access_token":"access","refresh_token":"refresh","expires_at":"2026-10-01T00:00:00Z","provider_data":{"signature":"opaque"}}`)},
		{name: "binary", plaintext: []byte{0, 0xff, 0xfe, '\n', 0x80}},
		{name: "nil"},
		{name: "empty", plaintext: []byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := bytes.Clone(tt.plaintext)

			record, err := cipher.Encrypt("account", "upstream", tt.plaintext)

			if err != nil {
				t.Fatal(err)
			}
			if len(record) != len(want)+29 {
				t.Fatalf("record length = %d, want %d", len(record), len(want)+29)
			}
			if record[0] != 1 {
				t.Fatalf("record version = %d, want 1", record[0])
			}
			if !bytes.Equal(tt.plaintext, want) {
				t.Fatal("Encrypt() changed its input plaintext")
			}
			clear(tt.plaintext)
			got, err := cipher.Decrypt("account", "upstream", record)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("Decrypt() after changing the original input = (%q, %v), want original content", got, err)
			}
		})
	}
}

func TestCipherDoesNotRetainMutableKey(t *testing.T) {
	key := fixtureKey()
	wantKey := bytes.Clone(key)
	cipher, err := credentialcipher.New(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, wantKey) {
		t.Fatal("New() changed its key input")
	}
	record := fixtureRecord(t)

	clear(key)
	plaintext, err := cipher.Decrypt("account", "upstream", record)

	if err != nil || string(plaintext) != `{"key":"test-secret"}` {
		t.Errorf("Decrypt() after changing the supplied key = (%q, %v), want unchanged cipher", plaintext, err)
	}
}

func TestDecryptionRejectsWrongKeyAndTamperedRecords(t *testing.T) {
	cipher := newCipher(t)
	wrongKey := fixtureKey()
	wrongKey[0] ^= 1
	wrongCipher, err := credentialcipher.New(wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	original := fixtureRecord(t)
	wrongVersion := bytes.Clone(original)
	wrongVersion[0] = 2
	wrongNonce := bytes.Clone(original)
	wrongNonce[1] ^= 1
	wrongCiphertext := bytes.Clone(original)
	wrongCiphertext[13] ^= 1
	wrongTag := bytes.Clone(original)
	wrongTag[len(wrongTag)-1] ^= 1
	tests := []struct {
		name   string
		cipher *credentialcipher.Cipher
		record []byte
	}{
		{name: "wrong key", cipher: wrongCipher, record: original},
		{name: "empty", cipher: cipher},
		{name: "version only", cipher: cipher, record: []byte{1}},
		{name: "incomplete nonce and tag", cipher: cipher, record: original[:28]},
		{name: "truncated tag", cipher: cipher, record: original[:len(original)-1]},
		{name: "unsupported version", cipher: cipher, record: wrongVersion},
		{name: "changed nonce", cipher: cipher, record: wrongNonce},
		{name: "changed ciphertext", cipher: cipher, record: wrongCiphertext},
		{name: "changed tag", cipher: cipher, record: wrongTag},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantRecord := bytes.Clone(tt.record)

			plaintext, err := tt.cipher.Decrypt("account", "upstream", tt.record)

			if err == nil || plaintext != nil {
				t.Fatalf("Decrypt() = (%q, %v), want nil plaintext and error", plaintext, err)
			}
			if !bytes.Equal(tt.record, wantRecord) {
				t.Error("failed Decrypt() changed its input record")
			}
			for _, secret := range []string{"test-secret", string(fixtureKey()), string(tt.record)} {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Error("decryption error exposes key, plaintext or record contents")
				}
			}
		})
	}
}

func TestIdentityBindingUsesExactUnambiguousBytes(t *testing.T) {
	cipher := newCipher(t)
	tests := []struct {
		name          string
		account       account.ID
		upstream      upstream.ID
		wrongAccount  account.ID
		wrongUpstream upstream.ID
	}{
		{name: "account", account: "account", upstream: "upstream", wrongAccount: "other", wrongUpstream: "upstream"},
		{name: "upstream", account: "account", upstream: "upstream", wrongAccount: "account", wrongUpstream: "other"},
		{name: "swapped identities", account: "account", upstream: "upstream", wrongAccount: "upstream", wrongUpstream: "account"},
		{name: "ambiguous concatenation", account: "a", upstream: "bc", wrongAccount: "ab", wrongUpstream: "c"},
		{name: "embedded separators", account: "a\x00b", upstream: "c", wrongAccount: "a", wrongUpstream: "b\x00c"},
		{name: "non-UTF-8 account", account: "\xff", upstream: "upstream", wrongAccount: "\xfe", wrongUpstream: "upstream"},
		{name: "non-UTF-8 upstream", account: "account", upstream: "\xff", wrongAccount: "account", wrongUpstream: "\xfe"},
		{name: "surrounding whitespace", account: " account ", upstream: " upstream ", wrongAccount: "account", wrongUpstream: "upstream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte("credential")
			record, err := cipher.Encrypt(tt.account, tt.upstream, payload)
			if err != nil {
				t.Fatal(err)
			}

			matching, matchingErr := cipher.Decrypt(tt.account, tt.upstream, record)
			wrong, wrongErr := cipher.Decrypt(tt.wrongAccount, tt.wrongUpstream, record)

			if matchingErr != nil || !bytes.Equal(matching, payload) {
				t.Errorf("matching identities: Decrypt() = (%q, %v), want original payload", matching, matchingErr)
			}
			if wrongErr == nil || wrong != nil {
				t.Errorf("wrong identities: Decrypt() = (%q, %v), want nil plaintext and error", wrong, wrongErr)
			}
		})
	}
}

func TestBlankIdentitiesAreRejected(t *testing.T) {
	cipher := newCipher(t)
	tests := []struct {
		account  account.ID
		upstream upstream.ID
	}{
		{account: "", upstream: "upstream"},
		{account: " \t\n", upstream: "upstream"},
		{account: "account", upstream: ""},
		{account: "account", upstream: " \t\n"},
	}
	for i, tt := range tests {
		payload := []byte("credential")
		record := fixtureRecord(t)

		encrypted, encryptErr := cipher.Encrypt(tt.account, tt.upstream, payload)
		decrypted, decryptErr := cipher.Decrypt(tt.account, tt.upstream, record)

		if encryptErr == nil || encrypted != nil || decryptErr == nil || decrypted != nil {
			t.Errorf("case %d: Encrypt() error %v, Decrypt() error %v; want nil outputs and errors", i, encryptErr, decryptErr)
		}
	}
}

func newCipher(t *testing.T) *credentialcipher.Cipher {
	t.Helper()
	cipher, err := credentialcipher.New(fixtureKey())
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func fixtureKey() []byte {
	return []byte{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
		16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
	}
}

func fixtureRecord(t *testing.T) []byte {
	t.Helper()
	// Node 26/OpenSSL 3.6.4 AES-256-GCM: key 00..1f, nonce 00..0b,
	// AAD 01|uint64BE(7)|account|upstream, plaintext {"key":"test-secret"}.
	record, err := hex.DecodeString("01000102030405060708090a0b3c20bd7ebcc7f839f924e4ff9c9a1d0ef1b3f3168dce0e5db0ed6ce3b903ff379491b61a57")
	if err != nil {
		t.Fatal(err)
	}
	return record
}
