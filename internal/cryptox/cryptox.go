// Package cryptox provides AES-256-GCM secret encryption and scrypt PIN hashing.
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/scrypt"

	"investhub/internal/store"
)

var masterKey []byte

// Init loads (or creates) the master key file under the data dir.
func Init() error {
	p := filepath.Join(store.DataDir, "master.key")
	if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
		masterKey = b
		return nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return err
	}
	if err := os.WriteFile(p, k, 0o600); err != nil {
		return err
	}
	masterKey = k
	return nil
}

// Encrypt returns "enc:<base64(iv|tag|ciphertext)>".
func Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("生成 IV 失败: %w", err) // F-16: 失败即上报，不再静默返回空
	}
	// Go's Seal appends the tag; store as iv|ct|tag which we split on read.
	ct := gcm.Seal(nil, iv, []byte(plain), nil)
	return "enc:" + base64.StdEncoding.EncodeToString(append(iv, ct...)), nil
}

// Decrypt reverses Encrypt; plaintext values pass through untouched.
func Decrypt(stored string) string {
	if stored == "" {
		return ""
	}
	if !strings.HasPrefix(stored, "enc:") {
		return stored // legacy plaintext
	}
	raw, err := base64.StdEncoding.DecodeString(stored[4:])
	if err != nil {
		return ""
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return ""
	}
	iv, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	out, err := gcm.Open(nil, iv, ct, nil)
	if err != nil {
		return ""
	}
	return string(out)
}

// HashPassword produces "scrypt$<saltHex>$<hashHex>".
func HashPassword(pin string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成 salt 失败: %w", err) // F-16: 不再用零 salt 生产哈希
	}
	h, err := scrypt.Key([]byte(pin), salt, 16384, 8, 1, 64)
	if err != nil {
		return "", err
	}
	return "scrypt$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(h), nil
}

// VerifyPassword does a constant-time comparison against a stored hash.
func VerifyPassword(pin, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "scrypt" {
		return false
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got, err := scrypt.Key([]byte(pin), salt, 16384, 8, 1, 64)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// RandomToken returns a hex string of n random bytes.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机令牌失败: %w", err) // F-16: 失败即上报
	}
	return hex.EncodeToString(b), nil
}

// UUID returns a random v4 UUID.
func UUID() string { return uuid.NewString() }

var ErrNoKey = errors.New("master key not initialised")
