package totp

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B SHA1 vector. Test-only; not a production credential.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestRFC6238SHA1(t *testing.T) {
	got, err := Code(rfcSecret, time.Unix(59, 0).UTC())
	if err != nil {
		t.Fatalf("code: %v", err)
	}
	if got != "287082" {
		t.Fatalf("RFC vector mismatch")
	}
}

func TestValidateSkew(t *testing.T) {
	at := time.Unix(59, 0).UTC()
	if !Validate(rfcSecret, "287082", at) {
		t.Fatal("expected current step to match")
	}
	if Validate(rfcSecret, "000000", at) {
		t.Fatal("expected wrong code to fail")
	}
	if Validate(rfcSecret, "28708", at) {
		t.Fatal("expected short code to fail")
	}
}

func TestOTPAuthURLShape(t *testing.T) {
	u := OTPAuthURL(rfcSecret)
	if !strings.HasPrefix(u, "otpauth://totp/") {
		t.Fatalf("scheme")
	}
	if !strings.Contains(u, "MiniCloudStorage") || !strings.Contains(u, "Administrator") {
		t.Fatalf("label")
	}
	if !strings.Contains(u, "algorithm=SHA1") && !strings.Contains(u, "algorithm=sha1") {
		t.Fatalf("algorithm")
	}
	if !strings.Contains(u, "digits=6") || !strings.Contains(u, "period=30") {
		t.Fatalf("digits/period")
	}
	if !strings.Contains(u, "secret=") {
		t.Fatalf("secret param")
	}
}

func TestGenerateSecretUnique(t *testing.T) {
	a, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == "" || a == b {
		t.Fatal("expected distinct secrets")
	}
	if _, err := Code(a, time.Now()); err != nil {
		t.Fatalf("generated secret not usable")
	}
}
