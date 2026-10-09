// Package secure reúne primitivas criptográficas do doomctl: criptografia de
// segredos em repouso (AES-256-GCM), hash de senha (PBKDF2-SHA256) e tokens.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------- Box: AES-256-GCM ----------

type Box struct{ aead cipher.AEAD }

// LoadKey decodifica DOOMCTL_SECRET_KEY (base64 ou hex de 32 bytes). Se vazio,
// lê/gera keyFile (0600) para que a instalação funcione sem passo manual.
func LoadKey(value, keyFile string) ([]byte, bool, error) {
	if value != "" {
		k, err := decodeKey(value)
		return k, false, err
	}
	if b, err := os.ReadFile(keyFile); err == nil {
		k, err := decodeKey(strings.TrimSpace(string(b)))
		return k, false, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

func decodeKey(v string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := hex.DecodeString(v); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, errors.New("DOOMCTL_SECRET_KEY deve ter 32 bytes em base64 (openssl rand -base64 32) ou hex")
}

func NewBox(key []byte) (*Box, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

func (b *Box) Open(ct []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(ct) < n {
		return nil, errors.New("ciphertext curto")
	}
	return b.aead.Open(nil, ct[:n], ct[n:], nil)
}

// ---------- Senhas: PBKDF2-SHA256 ----------

const pbkdf2Iter = 600_000

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(dk)), nil
}

func CheckPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	dk, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(dk, want) == 1
}

// DummyCheck gasta o mesmo tempo de uma verificação real (evita enumeração de usuários).
var dummyHash, _ = HashPassword("doomctl-dummy-password")

func DummyCheck(pw string) { CheckPassword(dummyHash, pw) }

// ValidatePasswordPolicy aplica a política mínima de senha.
func ValidatePasswordPolicy(pw string) error {
	if len([]rune(pw)) < 10 {
		return errors.New("a senha deve ter pelo menos 10 caracteres")
	}
	var lower, upper, digit, other bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			n++
		}
	}
	if n < 3 {
		return errors.New("use ao menos 3 destes: minúsculas, maiúsculas, números, símbolos")
	}
	return nil
}

// ---------- Tokens ----------

func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func SHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// RandomPassword gera uma senha legível que atende à política.
func RandomPassword() string {
	const alpha = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	rnd := make([]byte, 16)
	if _, err := rand.Read(rnd); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alpha[int(rnd[i])%len(alpha)]
	}
	return string(b[:5]) + "-" + string(b[5:10]) + "-" + string(b[10:]) + "#7a"
}
