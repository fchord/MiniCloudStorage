package shortcode

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func New(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("code length must be positive")
	}
	buf := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range buf {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		buf[i] = alphabet[v.Int64()]
	}
	return string(buf), nil
}

func FourDigitPIN() (string, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", v.Int64()), nil
}
