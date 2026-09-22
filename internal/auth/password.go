package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	DefaultMemory      = 64 * 1024
	DefaultIterations  = 3
	DefaultParallelism = 2
	DefaultKeyLength   = 32
	DefaultSaltLength  = 16
)

type PasswordHash struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	Salt        []byte
	Hash        []byte
}

func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must be between 12 and 1024 bytes")
	}
	salt := make([]byte, DefaultSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, DefaultIterations, DefaultMemory, DefaultParallelism, DefaultKeyLength)
	encode := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", DefaultMemory, DefaultIterations, DefaultParallelism, encode.EncodeToString(salt), encode.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parsed, err := Parse(encoded)
	if err != nil || len(password) == 0 {
		return false
	}
	actual := argon2.IDKey([]byte(password), parsed.Salt, parsed.Iterations, parsed.Memory, parsed.Parallelism, uint32(len(parsed.Hash)))
	return subtle.ConstantTimeCompare(actual, parsed.Hash) == 1
}

func Parse(encoded string) (PasswordHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return PasswordHash{}, errors.New("invalid argon2id password hash")
	}
	params := map[string]string{}
	for _, item := range strings.Split(parts[3], ",") {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return PasswordHash{}, errors.New("invalid argon2id parameters")
		}
		params[key] = value
	}
	memory, err1 := strconv.ParseUint(params["m"], 10, 32)
	iterations, err2 := strconv.ParseUint(params["t"], 10, 32)
	parallelism, err3 := strconv.ParseUint(params["p"], 10, 8)
	salt, err4 := base64.RawStdEncoding.DecodeString(parts[4])
	hash, err5 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || memory < 8192 || iterations < 1 || parallelism < 1 || len(salt) < 8 || len(hash) < 16 {
		return PasswordHash{}, errors.New("invalid argon2id password hash")
	}
	return PasswordHash{Memory: uint32(memory), Iterations: uint32(iterations), Parallelism: uint8(parallelism), Salt: salt, Hash: hash}, nil
}
