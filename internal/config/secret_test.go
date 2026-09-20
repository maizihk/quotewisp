package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestResolveWebSecretPersistsAndMigrates(t *testing.T) {
	d := t.TempDir()
	s, err := ResolveWebSecret(d, "")
	if err != nil || !validToken(s) {
		t.Fatalf("generate: %v", err)
	}
	if got, err := ResolveWebSecret(d, ""); err != nil || got != s {
		t.Fatalf("reuse: %q %v", got, err)
	}
	d2 := t.TempDir()
	legacy := "legacy-secret-012345678901234567890123"
	if got, err := ResolveWebSecret(d2, legacy); err != nil || got != legacy {
		t.Fatalf("migration: %q %v", got, err)
	}
}

func TestResolveWebSecretRejectsConflictAndInvalid(t *testing.T) {
	d := t.TempDir()
	if _, err := ResolveWebSecret(d, "short"); err == nil {
		t.Fatal("accepted short secret")
	}
	s, err := ResolveWebSecret(d, "good-secret-012345678901234567890123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveWebSecret(d, s+"x"); err == nil {
		t.Fatal("accepted conflicting secret")
	}
	if err = os.WriteFile(filepath.Join(d, "web-secret.key"), []byte("bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveWebSecret(d, ""); err == nil {
		t.Fatal("accepted corrupt secret")
	}
}

func TestResolveWebSecretRejectsFileForms(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "web-secret.key")
	if err := os.WriteFile(p, []byte("valid-secret-012345678901234567890123\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWebSecret(d, ""); err == nil || strings.Contains(err.Error(), p) {
		t.Fatal("accepted broad permissions or leaked path")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", p); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWebSecret(d, ""); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 258), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWebSecret(d, ""); err == nil {
		t.Fatal("accepted oversized file")
	}
}

func TestResolveWebSecretReadOnlyExisting(t *testing.T) {
	d := t.TempDir()
	s := "readonly-secret-0123456789012345678901"
	if err := os.WriteFile(filepath.Join(d, "web-secret.key"), []byte(s+"\n"), 0400); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWebSecret(d, "")
	if err != nil || got != s {
		t.Fatalf("readonly read: %q %v", got, err)
	}
}

func TestResolveWebSecretUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are ineffective as root")
	}
	d := t.TempDir()
	if err := os.Chmod(d, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(d, 0700)
	if _, err := ResolveWebSecret(d, ""); err == nil {
		t.Fatal("created secret in unwritable directory")
	}
	s := "directory-read-secret-012345678901234567"
	p := filepath.Join(d, "web-secret.key")
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0500); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveWebSecret(d, ""); err != nil || got != s {
		t.Fatalf("read-only directory: %q %v", got, err)
	}
}

func TestResolveWebSecretRejectsDirectoryAtKeyPath(t *testing.T) {
	d := t.TempDir()
	if err := os.Mkdir(filepath.Join(d, "web-secret.key"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWebSecret(d, ""); err == nil {
		t.Fatal("accepted directory as key")
	}
}

func TestResolveWebSecretConcurrent(t *testing.T) {
	d := t.TempDir()
	results := make(chan string, 16)
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := ResolveWebSecret(d, "")
			if err != nil {
				errs <- err
				return
			}
			results <- s
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent resolve: %v", err)
	}
	var first string
	for s := range results {
		if first == "" {
			first = s
		}
		if s != first {
			t.Fatalf("keys differ")
		}
	}
	if !validToken(first) {
		t.Fatal("no valid result")
	}
}
