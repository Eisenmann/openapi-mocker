package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New returns a random hex identifier, 24 characters long.
func New() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never realistically fail; if it does, it
		// indicates a serious system-level problem. Panic rather than
		// silently returning zeros, which could cause ID collisions.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)
}
