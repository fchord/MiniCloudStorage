package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	Period     = 30
	Digits     = 6
	SkewSteps  = 1
	secretSize = 20
	issuer     = "MiniCloudStorage"
	account    = "Administrator"
)

// GenerateSecret returns a 160-bit secret as unpadded base32.
func GenerateSecret() (string, error) {
	raw := make([]byte, secretSize)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return encodeSecret(raw), nil
}

func encodeSecret(raw []byte) string {
	return strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "=")
}

func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	if pad := len(s) % 8; pad != 0 {
		s += strings.Repeat("=", 8-pad)
	}
	return base32.StdEncoding.DecodeString(s)
}

// Code returns the 6-digit SHA1 TOTP for secret at time t.
func Code(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	counter := uint64(t.Unix() / Period)
	return hotp(key, counter), nil
}

func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < Digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", Digits, bin%mod)
}

// Validate accepts the current step and ±SkewSteps.
func Validate(secret, code string, t time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != Digits {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	key, err := decodeSecret(secret)
	if err != nil || len(key) == 0 {
		return false
	}
	counter := int64(t.Unix() / Period)
	for d := -SkewSteps; d <= SkewSteps; d++ {
		c := counter + int64(d)
		if c < 0 {
			continue
		}
		if hmac.Equal([]byte(hotp(key, uint64(c))), []byte(code)) {
			return true
		}
	}
	return false
}

// OTPAuthURL is the Microsoft Authenticator-compatible provisioning URI.
// Callers must not log the return value.
func OTPAuthURL(secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(Digits))
	q.Set("period", strconv.Itoa(Period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
