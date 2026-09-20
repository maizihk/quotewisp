package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var errWebSecret = errors.New("web secret unavailable")

// ResolveWebSecret loads the service web secret, or creates it on first use.
// supplied is used only to migrate an explicitly configured legacy secret.
func ResolveWebSecret(dataDir, supplied string) (string, error) {
	if dataDir == "" {
		return "", errWebSecret
	}
	path := filepath.Join(dataDir, "web-secret.key")
	if s, ok := readWebSecret(path); ok {
		if supplied != "" && s != supplied {
			return "", errWebSecret
		}
		return s, nil
	}
	if _, err := os.Lstat(path); err == nil {
		if s, ok := readWebSecret(path); ok {
			if supplied != "" && s != supplied {
				return "", errWebSecret
			}
			return s, nil
		}
		return "", errWebSecret
	} else if !os.IsNotExist(err) {
		return "", errWebSecret
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", errWebSecret
	}
	value := supplied
	if value == "" {
		var raw [32]byte
		if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
			return "", errWebSecret
		}
		value = base64.RawURLEncoding.EncodeToString(raw[:])
	}
	if !validToken(value) {
		return "", errWebSecret
	}
	// Link gives readers an all-or-nothing winner when several processes start.
	tmp, err := os.CreateTemp(dataDir, ".web-secret.*")
	if err != nil {
		return "", errWebSecret
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.WriteString(value + "\n")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", errWebSecret
	}
	if err = os.Link(tmpName, path); err != nil && !os.IsExist(err) {
		return "", errWebSecret
	}
	dir, err := os.Open(dataDir)
	if err != nil {
		return "", errWebSecret
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", errWebSecret
	}
	final, ok := readWebSecret(path)
	if !ok {
		return "", errWebSecret
	}
	if supplied != "" && final != supplied {
		return "", errWebSecret
	}
	return final, nil
}

func readWebSecret(path string) (string, bool) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return "", false
	}
	if st.Size() > 257 {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || b[len(b)-1] != '\n' {
		return "", false
	}
	s := string(b[:len(b)-1])
	return s, validToken(s)
}
