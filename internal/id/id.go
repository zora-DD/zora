// Package id creates opaque identifiers for persisted domain objects.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a random, URL-safe identifier with a human-readable prefix.
func New(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}
