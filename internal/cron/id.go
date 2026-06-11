package cron

import (
	"crypto/rand"
	"encoding/hex"
)

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "cron_" + hex.EncodeToString(b[:])
}
