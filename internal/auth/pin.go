package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const MaxSkewSeconds = 120

// GeneratePIN generates a 6-digit numeric pairing PIN.
func GeneratePIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// ComputeHMAC returns hex-encoded HMAC-SHA256 of payload using pin as key.
func ComputeHMAC(pin, payload string) string {
	mac := hmac.New(sha256.New, []byte(pin))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignHeader creates "<unix_ts>:<signature>" for HTTP request authentication.
func SignHeader(pin, method, path string, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	return ts + ":" + ComputeHMAC(pin, ts+"|"+method+"|"+path)
}

// VerifyHeader validates "<unix_ts>:<signature>" within MaxSkewSeconds.
func VerifyHeader(pin, method, path, header string, now time.Time) bool {
	parts := strings.SplitN(header, ":", 2)
	if len(parts) != 2 {
		return false
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	diff := now.Unix() - ts
	if diff < -MaxSkewSeconds || diff > MaxSkewSeconds {
		return false
	}
	expected := ComputeHMAC(pin, fmt.Sprintf("%d|%s|%s", ts, method, path))
	return hmac.Equal([]byte(parts[1]), []byte(expected))
}
