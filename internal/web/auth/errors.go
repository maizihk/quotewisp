package auth

import "errors"

var (
	ErrWeakPassword    = errors.New("weak password")
	ErrInvalidUsername = errors.New("invalid username")
	ErrCSRF            = errors.New("csrf check failed")
	ErrBadHash         = errors.New("malformed password hash")
)
