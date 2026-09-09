package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// newIDLength is the byte length of a generated identifier (24 hex chars).
const newIDLength = 12

// New returns a random hex identifier, 24 characters long.
func New() string {
	b := make([]byte, newIDLength)

	_, err := rand.Read(b)
	if err != nil {
		// crypto/rand should never realistically fail; if it does, it
		// indicates a serious system-level problem. Panic rather than
		// silently returning zeros, which could cause ID collisions.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}

	return hex.EncodeToString(b)
}
