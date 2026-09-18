package store

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	codeRE          = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	adminUsernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,31}$`)
	uuidRE          = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func RuneLength(content string) uint16 {
	n := utf8.RuneCountInString(content)
	if n > 65535 {
		return 65535
	}
	return uint16(n)
}

func ValidateCategoryCode(code string) error {
	if !codeRE.MatchString(code) {
		return &ValidationError{Field: "code", Reason: "invalid category code"}
	}
	return nil
}

func ValidateCategoryName(name string) error {
	if !validText(name, 64, 256, false) {
		return &ValidationError{Field: "name", Reason: "invalid category name"}
	}
	return nil
}

func ValidateSentenceFields(content, source, author string, maxContentRunes int) error {
	maxRunes := 65535
	if maxContentRunes > 0 {
		maxRunes = maxContentRunes
	}
	if !validText(content, maxRunes, 65535, true) {
		return &ValidationError{Field: "content", Reason: "invalid content"}
	}
	if !validOptional(source, 255, 1020) {
		return &ValidationError{Field: "source", Reason: "invalid source"}
	}
	if !validOptional(author, 128, 512) {
		return &ValidationError{Field: "author", Reason: "invalid author"}
	}
	return nil
}

func validateRejectReason(reason string) error {
	if !utf8.ValidString(reason) {
		return &ValidationError{Field: "reject_reason", Reason: "invalid reject reason"}
	}
	n := utf8.RuneCountInString(reason)
	if n < 1 || n > 255 {
		return &ValidationError{Field: "reject_reason", Reason: "reject reason length out of range"}
	}
	return nil
}

func validateAdminUsername(username string) error {
	if !adminUsernameRE.MatchString(username) {
		return &ValidationError{Field: "username", Reason: "invalid username"}
	}
	return nil
}

func validateQuery(q string) error {
	if !utf8.ValidString(q) {
		return &ValidationError{Field: "q", Reason: "invalid search query"}
	}
	n := utf8.RuneCountInString(q)
	if n < 1 || n > 64 {
		return &ValidationError{Field: "q", Reason: "search query length out of range"}
	}
	return nil
}

func validateUUIDFilter(uuid string) error {
	if len(uuid) != 36 || !uuidRE.MatchString(uuid) {
		return &ValidationError{Field: "uuid", Reason: "invalid uuid"}
	}
	return nil
}

func validText(s string, maxRunes, maxBytes int, nonempty bool) bool {
	return utf8.ValidString(s) && len(s) <= maxBytes && utf8.RuneCountInString(s) <= maxRunes && (!nonempty || s != "") && !strings.EqualFold(strings.TrimSpace(s), "")
}

func validOptional(s string, maxRunes, maxBytes int) bool {
	return utf8.ValidString(s) && len(s) <= maxBytes && utf8.RuneCountInString(s) <= maxRunes
}

func runeCount(s string) int {
	return utf8.RuneCountInString(s)
}
