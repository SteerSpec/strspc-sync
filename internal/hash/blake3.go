package hash

import (
	"encoding/hex"

	"lukechampine.com/blake3"
)

// HashBytes returns the BLAKE3 hash of data as "b3_" + hex(first 32 bytes).
func HashBytes(data []byte) string {
	h := blake3.Sum256(data)
	return "b3_" + hex.EncodeToString(h[:])
}

// HashString returns the BLAKE3 hash of s as "b3_" + hex(first 32 bytes).
func HashString(s string) string {
	return HashBytes([]byte(s))
}

// Fingerprint produces a deterministic 32-hex-char identifier from an ordered
// list of string parts. Each part is separated by a null byte to prevent
// ambiguous concatenation. The result is the first 16 bytes of a BLAKE3 hash
// encoded as hexadecimal.
func Fingerprint(parts ...string) string {
	h := blake3.New(32, nil)
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
