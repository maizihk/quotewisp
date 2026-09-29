package importjobs

import (
	"errors"
	"sentence-api/internal/importer"
	"time"
)

var (
	ErrUpload   = errors.New("invalid upload stream")
	ErrBusy     = errors.New("another import is active")
	ErrNotFound = errors.New("import not found")
	ErrState    = errors.New("import state does not allow this action")
	ErrExpired  = errors.New("import preview expired")
	ErrTooLarge = errors.New("upload exceeds size limit")
	ErrDigest   = errors.New("upload digest mismatch")
)

type CategoryChange struct {
	Code   string
	Name   string
	Create bool
}

type Job struct {
	ID         string
	AdminID    uint64
	Digest     string
	Format     string
	Status     string
	Result     importer.Summary
	HasResult  bool
	Error      string
	Categories []CategoryChange
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UpdatedAt  time.Time
}
