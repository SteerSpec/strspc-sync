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
