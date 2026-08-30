package main

import (
	"crypto/rand"
	"encoding/hex"
)

// randomSecret generates the key used to sign JWTs.
//
// When SECRET is unset the backend generates a random one on every boot, which
// silently logs everyone out on restart. The launcher generates one once and
// stores it in the config instead.
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
