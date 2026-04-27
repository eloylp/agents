package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestVerifySignature exercises the HMAC-SHA256 signature check that gates
// every incoming GitHub webhook delivery.
func TestVerifySignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"hello":"world"}`)
	secret := "secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifySignature(body, secret, sig) {
		t.Fatalf("expected signature to verify")
	}
	if verifySignature(body, secret, "sha256=deadbeef") {
		t.Fatalf("bad signature should not verify")
	}
	if verifySignature(body, "", sig) {
		t.Fatalf("empty secret must not verify")
	}
}
