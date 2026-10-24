package panelauth

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image/png"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const passwordIterations = 600000

func randomToken(size int) string {
	return hex.EncodeToString(randomBytes(size))
}

func randomBytes(size int) []byte {
	b := make([]byte, size)
	// crypto/rand.Read in this Go version terminates on an unrecoverable RNG error.
	_, _ = rand.Read(b)
	return b
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func hashPassword(password string) (string, error) {
	salt := randomBytes(16)
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return "pbkdf2-sha256$600000$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key), nil
}

func checkPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := hex.DecodeString(parts[3])
	if err != nil || len(want) != 32 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	return err == nil && subtle.ConstantTimeCompare(key, want) == 1
}

func (s *Service) encrypt(id int64, secret string) string {
	nonce := randomBytes(s.cipher.NonceSize())
	sealed := s.cipher.Seal(nonce, nonce, []byte(secret), []byte(strconv.FormatInt(id, 10)))
	return base64.RawStdEncoding.EncodeToString(sealed)
}

func (s *Service) decrypt(id int64, encrypted string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(encrypted)
	if err != nil || len(data) < s.cipher.NonceSize()+s.cipher.Overhead() {
		return "", fmt.Errorf("invalid encrypted MFA secret")
	}
	n := s.cipher.NonceSize()
	plain, err := s.cipher.Open(nil, data[:n], data[n:], []byte(strconv.FormatInt(id, 10)))
	if err != nil {
		return "", fmt.Errorf("MFA secret cannot be decrypted; check PANEL_AUTH_KEY")
	}
	return string(plain), nil
}

func newCipher(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Return the accepted time step so it can be consumed atomically and never reused.
func totpCounter(secret, code string, now time.Time, previous int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	step := now.Unix() / 30
	for counter := step - 1; counter <= step+1; counter++ {
		if counter <= previous {
			continue
		}
		want, err := totp.GenerateCodeCustom(secret, time.Unix(counter*30, 0), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && equal(want, code) {
			return counter, true
		}
	}
	return 0, false
}

type Enrollment struct {
	Secret string `json:"secret"`
	QR     string `json:"qr"`
}

func enrollment(secret string, id int64) (Enrollment, error) {
	params := url.Values{"secret": {secret}, "issuer": {"Family expenses"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	key, err := otp.NewKeyFromURL("otpauth://totp/" + url.PathEscape("Family expenses:"+strconv.FormatInt(id, 10)) + "?" + params.Encode())
	if err != nil {
		return Enrollment{}, err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return Enrollment{}, err
	}
	var b bytes.Buffer
	if err = png.Encode(&b, img); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{Secret: secret, QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())}, nil
}
