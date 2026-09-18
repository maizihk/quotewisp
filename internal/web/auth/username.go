package auth

import (
	"regexp"
	"strings"
)

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,31}$`)

// ValidateUsername lowercases s and checks the admin username pattern.
func ValidateUsername(s string) (string, error) {
	normalized := strings.ToLower(s)
	if !usernameRE.MatchString(normalized) {
		return "", ErrInvalidUsername
	}
	return normalized, nil
}
