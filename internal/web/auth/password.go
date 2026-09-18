package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
	minPassword  = 8
	maxPassword  = 128
)

// HashPassword hashes password with argon2id after policy checks.
func HashPassword(password string) (string, error) {
	if err := checkPasswordPolicy(password); err != nil {
		return "", err
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword checks password against a PHC argon2id string.
func VerifyPassword(phc, password string) (bool, error) {
	params, salt, want, err := parsePHC(phc)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(want)))
	if len(got) != len(want) {
		return false, nil
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func checkPasswordPolicy(password string) error {
	if !utf8.ValidString(password) {
		return ErrWeakPassword
	}
	n := utf8.RuneCountInString(password)
	if n < minPassword || n > maxPassword {
		return ErrWeakPassword
	}
	return nil
}

type phcParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
}

func parsePHC(phc string) (phcParams, []byte, []byte, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return phcParams{}, nil, nil, ErrBadHash
	}
	if parts[2] != "v=19" {
		return phcParams{}, nil, nil, ErrBadHash
	}

	var p phcParams
	for _, field := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			return phcParams{}, nil, nil, ErrBadHash
		}
		v, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return phcParams{}, nil, nil, ErrBadHash
		}
		switch kv[0] {
		case "m":
			p.memory = uint32(v)
		case "t":
			p.time = uint32(v)
		case "p":
			if v == 0 || v > 255 {
				return phcParams{}, nil, nil, ErrBadHash
			}
			p.threads = uint8(v)
		default:
			return phcParams{}, nil, nil, ErrBadHash
		}
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return phcParams{}, nil, nil, ErrBadHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return phcParams{}, nil, nil, ErrBadHash
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(hash) == 0 {
		return phcParams{}, nil, nil, ErrBadHash
	}
	return p, salt, hash, nil
}
