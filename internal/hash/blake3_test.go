package hash

import (
	"strings"
	"testing"
)

func TestHashBytesDeterministic(t *testing.T) {
	data := []byte("hello world")
	h1 := HashBytes(data)
	h2 := HashBytes(data)
	if h1 != h2 {
		t.Errorf("same input produced different hashes: %s vs %s", h1, h2)
	}
}

func TestHashBytesDifferentInputs(t *testing.T) {
	h1 := HashBytes([]byte("hello"))
	h2 := HashBytes([]byte("world"))
	if h1 == h2 {
		t.Error("different inputs produced the same hash")
	}
}

func TestHashBytesPrefix(t *testing.T) {
	h := HashBytes([]byte("test"))
	if !strings.HasPrefix(h, "b3_") {
		t.Errorf("hash %q does not start with b3_", h)
	}
}

func TestHashStringMatchesHashBytes(t *testing.T) {
	s := "test string"
	if HashString(s) != HashBytes([]byte(s)) {
		t.Error("HashString and HashBytes differ for the same input")
	}
}

func TestHashBytesEmpty(t *testing.T) {
	h := HashBytes([]byte{})
	if !strings.HasPrefix(h, "b3_") {
		t.Errorf("empty input hash should have b3_ prefix, got %s", h)
	}
	if len(h) != 3+64 { // "b3_" + 64 hex chars
		t.Errorf("expected hash length 67, got %d", len(h))
	}
}

func TestHashBytesLarge(t *testing.T) {
	data := make([]byte, 1<<20) // 1MB
	for i := range data {
		data[i] = byte(i % 256)
	}
	h1 := HashBytes(data)
	h2 := HashBytes(data)
	if h1 != h2 {
		t.Error("large input produced non-deterministic hashes")
	}
	if !strings.HasPrefix(h1, "b3_") {
		t.Errorf("hash should have b3_ prefix, got %s", h1)
	}
}
