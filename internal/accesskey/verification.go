package accesskey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
)

const keyPrefix = "clan_"

// VerificationHash is a SHA-256 digest for storage and indexed key lookup.
// It contains no raw key. Storage must validate the byte length before conversion.
type VerificationHash [32]byte

// Generate returns a 256-bit random access key and its verification hash.
// The raw key is clan_ followed by 43 canonical unpadded base64url characters.
// Callers own one-time delivery and must not log or retain the raw key for later
// retrieval. The raw string has no automatic redaction.
func Generate() (raw string, hash VerificationHash) {
	var secret [32]byte
	rand.Read(secret[:])
	raw = keyPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	return raw, sha256.Sum256([]byte(raw))
}

// Hash derives the verification hash of an exact canonical access key.
// Invalid input returns a zero hash and false. It does not normalize input or
// prove randomness; issue new keys with [Generate].
func Hash(raw string) (VerificationHash, bool) {
	if len(raw) != 48 || !strings.HasPrefix(raw, keyPrefix) {
		return VerificationHash{}, false
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(raw[len(keyPrefix):])
	// Strict decoding ignores CR/LF; exact encoded and decoded lengths exclude them.
	if err != nil || len(secret) != 32 {
		return VerificationHash{}, false
	}
	return sha256.Sum256([]byte(raw)), true
}

// Verify reports whether the canonical raw key matches this hash.
// It compares all digest bytes in constant time and rejects malformed input.
// It does not check identity, permissions or enabled status.
func (hash VerificationHash) Verify(raw string) bool {
	candidate, ok := Hash(raw)
	return ok && subtle.ConstantTimeCompare(hash[:], candidate[:]) == 1
}
