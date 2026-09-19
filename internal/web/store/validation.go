package store

import (
	"net/url"
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

func ValidateWebSentenceFields(content, source, author string, maxContentRunes int) error {
	if err := ValidateSentenceFields(content, source, author, maxContentRunes); err != nil {
		return err
	}
	if source == "" && author == "" {
		return &ValidationError{Field: "source", Reason: "出处与作者至少填写一项"}
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

func NormalizeSiteSettings(in SiteSettings) SiteSettings {
	in.Name = strings.TrimSpace(in.Name)
	in.EnglishName = strings.TrimSpace(in.EnglishName)
	in.Slogan = strings.TrimSpace(in.Slogan)
	in.Contact = strings.TrimSpace(in.Contact)
	in.PublicOrigin = strings.TrimRight(strings.TrimSpace(in.PublicOrigin), "/")
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	in.BeianText = strings.TrimSpace(in.BeianText)
	in.BeianURL = strings.TrimSpace(in.BeianURL)
	return in
}

func ValidateSiteSettings(in SiteSettings) error {
	if !validText(in.Name, 64, 256, true) {
		return &ValidationError{Field: "site_name", Reason: "网站名称无效"}
	}
	if !validBrandText(in.EnglishName, 64, 256, false) {
		return &ValidationError{Field: "english_name", Reason: "英文名称无效"}
	}
	if !validBrandText(in.Slogan, 128, 512, false) {
		return &ValidationError{Field: "slogan", Reason: "品牌标语无效"}
	}
	if !utf8.ValidString(in.Contact) || in.Contact == "" || len(in.Contact) > 256 {
		return &ValidationError{Field: "contact", Reason: "联系方式无效"}
	}
	if err := validateOptionalHTTPURL("public_origin", in.PublicOrigin, false); err != nil {
		return err
	}
	if err := validateOptionalHTTPURL("repo_url", in.RepoURL, false); err != nil {
		return err
	}
	if in.BeianText != "" && !validText(in.BeianText, 128, 512, true) {
		return &ValidationError{Field: "beian_text", Reason: "备案号无效"}
	}
	if err := validateOptionalHTTPURL("beian_url", in.BeianURL, true); err != nil {
		return err
	}
	if in.BeianURL != "" && in.BeianText == "" {
		return &ValidationError{Field: "beian_url", Reason: "填写备案链接时必须同时填写备案号"}
	}
	return nil
}

func validBrandText(s string, maxRunes, maxBytes int, nonempty bool) bool {
	if s == "" {
		return !nonempty
	}
	if !validText(s, maxRunes, maxBytes, nonempty) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return false
		}
	}
	return true
}

func validateOptionalHTTPURL(field, v string, allowQuery bool) error {
	if v == "" {
		return nil
	}
	if len(v) > 512 {
		return &ValidationError{Field: field, Reason: fieldReason(field)}
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.Contains(v, "#") || u.Fragment != "" || u.RawFragment != "" {
		return &ValidationError{Field: field, Reason: fieldReason(field)}
	}
	if !allowQuery && (u.RawQuery != "" || u.ForceQuery || strings.Contains(v, "?")) {
		return &ValidationError{Field: field, Reason: fieldReason(field)}
	}
	return nil
}

func fieldReason(field string) string {
	switch field {
	case "public_origin":
		return "站点公开地址无效"
	case "repo_url":
		return "源代码地址无效"
	case "beian_url":
		return "备案链接无效"
	default:
		return "参数无效"
	}
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
