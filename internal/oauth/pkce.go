package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// verifyPKCE checks an RFC 7636 S256 code_verifier against the stored
// code_challenge: BASE64URL(SHA256(verifier)) must equal the challenge.
func verifyPKCE(codeChallenge, codeVerifier string) bool {
	if codeChallenge == "" || codeVerifier == "" {
		return false
	}
	sum := sha256.Sum256([]byte(codeVerifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(codeChallenge)) == 1
}
