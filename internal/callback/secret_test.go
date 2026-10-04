package callback

import (
	"crypto/rand"
	"encoding/base64"
)

// NewTestSecret returns a random signing secret for tests.
func NewTestSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "whsec_" + base64.StdEncoding.EncodeToString(b)
}
