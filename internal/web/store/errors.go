package store

import "errors"

var (
	ErrNotFound         = errors.New("not found")
	ErrConflict         = errors.New("conflict")
	ErrDuplicate        = errors.New("duplicate")
	ErrUnchanged        = errors.New("unchanged")
	ErrCategoryDisabled = errors.New("category disabled")
	ErrLastAdmin        = errors.New("last admin")
	ErrQueueFull        = errors.New("queue full")
	ErrStaleAuth        = errors.New("stale auth")
)

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return e.Field + ": " + e.Reason
	}
	return e.Reason
}
