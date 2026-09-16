package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"valheim-panel/internal/store"
)

const (
	iterations = 210000
	keyLength  = 32
)

func HashPassword(password string) (store.PasswordRecord, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return store.PasswordRecord{}, err
	}
	sum := pbkdf2([]byte(password), salt, iterations, keyLength)
	return store.PasswordRecord{
		Salt:       hex.EncodeToString(salt),
		Hash:       hex.EncodeToString(sum),
		Iterations: iterations,
	}, nil
}

func VerifyPassword(password string, record store.PasswordRecord) bool {
	salt, err := hex.DecodeString(record.Salt)
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(record.Hash)
	if err != nil {
		return false
	}
	actual := pbkdf2([]byte(password), salt, record.Iterations, len(expected))
	return hmac.Equal(actual, expected)
}

func CreateToken(secret string, user store.User, ttl time.Duration) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(map[string]any{
		"sub":      user.ID,
		"username": user.Username,
		"role":     user.Role,
		"iat":      time.Now().Unix(),
		"exp":      time.Now().Add(ttl).Unix(),
	})
	headerPart := base64.RawURLEncoding.EncodeToString(header)
	payloadPart := base64.RawURLEncoding.EncodeToString(payload)
	unsigned := headerPart + "." + payloadPart
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

type Claims struct {
	Subject  string `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

func VerifyToken(secret, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("invalid token")
	}
	unsigned := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(unsigned))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return Claims{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, err
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, err
	}
	if claims.Subject == "" || claims.Expires < time.Now().Unix() {
		return Claims{}, errors.New("token expired")
	}
	return claims, nil
}

func TokenFromRequest(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	cookie, err := r.Cookie("valheim_panel_token")
	if err == nil {
		return cookie.Value
	}
	return ""
}

func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	blocks := (keyLen + hLen - 1) / hLen
	result := make([]byte, 0, blocks*hLen)
	for block := 1; block <= blocks; block++ {
		counter := make([]byte, 4)
		binary.BigEndian.PutUint32(counter, uint32(block))
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write(counter)
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		result = append(result, t...)
	}
	return result[:keyLen]
}

func ID(prefix string) string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf)
}
