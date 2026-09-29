package importer

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sentence-api/internal/database"
)

const maxTextBytes = 65535

var (
	categoryRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	uuidRE     = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
)

type Category struct {
	Code, Name string
	SortOrder  int32
}
type Sentence struct {
	UUID, Category, Content, Source, Author string
	Length                                  uint16
}
type Dataset struct {
	Categories                    []Category
	Sentences                     []Sentence
	InputCount, DeduplicatedCount uint64
}
type Summary struct {
	DryRun               bool    `json:"dry_run"`
	InputCount           uint64  `json:"input_count"`
	DeduplicatedCount    uint64  `json:"deduplicated_count"`
	SkippedCount         uint64  `json:"skipped_count"`
	NewSentences         uint64  `json:"new_sentences"`
	NewCategories        uint64  `json:"new_categories"`
	Changed              bool    `json:"changed"`
	DatasetVersionBefore string  `json:"dataset_version_before"`
	DatasetVersionAfter  *string `json:"dataset_version_after"`
}

type Error struct {
	Category string
	Err      error
}

func (e *Error) Error() string { return e.Category + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }
func problem(category, format string, args ...any) error {
	return &Error{Category: category, Err: fmt.Errorf(format, args...)}
}

// Parse streams one JSON value, rejecting duplicate/unknown members and invalid strings.
func Parse(ctx context.Context, r io.Reader) (Dataset, error) {
	d := json.NewDecoder(newValidatingReader(ctx, r))
	d.UseNumber()
	var out Dataset
	t, err := d.Token()
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, problem("invalid-json", "malformed JSON")
	}
	if delim, ok := t.(json.Delim); !ok || delim != '{' {
		return out, problem("invalid-json", "top level must be an object")
	}
	seen := map[string]bool{}
	for d.More() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		key, err := stringToken(d)
		if err != nil {
			return out, err
		}
		if seen[key] {
			return out, problem("duplicate-field", "duplicate top-level field")
		}
		seen[key] = true
		switch key {
		case "categories":
			if out.Categories, err = parseCategories(ctx, d); err != nil {
				return out, err
			}
		case "sentences":
			if out.Sentences, out.InputCount, out.DeduplicatedCount, err = parseSentences(ctx, d); err != nil {
				return out, err
			}
		default:
			return out, problem("unknown-field", "unknown top-level field")
		}
	}
	if _, err := d.Token(); err != nil {
		return out, problem("invalid-json", "malformed JSON")
	}
	if !seen["sentences"] || len(out.Sentences) == 0 {
		return out, problem("validation", "sentences must contain at least one item")
	}
	if _, err := d.Token(); err != io.EOF {
		return out, problem("invalid-json", "trailing data or malformed JSON")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

type validatingReader struct {
	ctx                     context.Context
	b                       *bufio.Reader
	pending                 []byte
	first, inString, escape bool
	unicodeN                int
	unicodeV                uint16
	expectLow               int
	needLow, readingLow     bool
	failed                  error
	one                     [4]byte
}

func newValidatingReader(ctx context.Context, r io.Reader) *validatingReader {
	return &validatingReader{ctx: ctx, b: bufio.NewReader(r), first: true}
}
func (v *validatingReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(v.pending) > 0 {
			p[n] = v.pending[0]
			v.pending = v.pending[1:]
			n++
			continue
		}
		if v.failed != nil {
			if n > 0 {
				return n, nil
			}
			return 0, v.failed
		}
		bs, e := v.next()
		if e != nil {
			v.failed = e
			continue
		}
		v.pending = bs
	}
	return n, nil
}
func (v *validatingReader) next() ([]byte, error) {
	if e := v.ctx.Err(); e != nil {
		return nil, e
	}
	c, e := v.b.ReadByte()
	if e == io.EOF {
		if v.needLow || v.expectLow != 0 || v.unicodeN != 0 {
			return nil, problem("invalid-unicode", "incomplete Unicode surrogate")
		}
		return nil, io.EOF
	}
	if e != nil {
		return nil, problem("input", "input read failed")
	}
	if v.first {
		v.first = false
		if c == 0xef {
			peek, _ := v.b.Peek(2)
			if len(peek) == 2 && peek[0] == 0xbb && peek[1] == 0xbf {
				return nil, problem("invalid-json", "UTF-8 BOM is not allowed")
			}
		}
	}
	if c >= 0x80 {
		if v.needLow || v.expectLow != 0 || v.unicodeN != 0 {
			return nil, problem("invalid-unicode", "unpaired Unicode surrogate")
		}
		_ = v.b.UnreadByte()
		peek, _ := v.b.Peek(4)
		if len(peek) == 0 {
			return nil, problem("invalid-json", "invalid UTF-8")
		}
		ru, size := utf8.DecodeRune(peek)
		if ru == utf8.RuneError && size == 1 {
			return nil, problem("invalid-json", "invalid UTF-8")
		}
		out := v.one[:size]
		if _, e = io.ReadFull(v.b, out); e != nil {
			return nil, problem("invalid-json", "invalid UTF-8")
		}
		return out, nil
	}
	if e = v.acceptASCII(c); e != nil {
		return nil, e
	}
	v.one[0] = c
	return v.one[:1], nil
}
func (v *validatingReader) acceptASCII(c byte) error {
	if v.expectLow > 0 {
		want := byte('u')
		if v.expectLow == 2 {
			want = '\\'
		}
		if c != want {
			return problem("invalid-unicode", "unpaired Unicode surrogate")
		}
		v.expectLow--
		if v.expectLow == 0 {
			v.unicodeN = 4
			v.unicodeV = 0
			v.readingLow = true
		}
		return nil
	}
	if v.unicodeN > 0 {
		x, ok := hexValue(c)
		if !ok {
			return problem("invalid-json", "invalid Unicode escape")
		}
		v.unicodeV = v.unicodeV<<4 | x
		v.unicodeN--
		if v.unicodeN == 0 {
			u := v.unicodeV
			if v.readingLow {
				if u < 0xdc00 || u > 0xdfff {
					return problem("invalid-unicode", "unpaired Unicode surrogate")
				}
				v.readingLow = false
				v.needLow = false
			} else if u >= 0xdc00 && u <= 0xdfff {
				return problem("invalid-unicode", "unpaired Unicode surrogate")
			} else if u >= 0xd800 && u <= 0xdbff {
				v.expectLow = 2
				v.needLow = true
			}
		}
		return nil
	}
	if !v.inString {
		if c == '"' {
			v.inString = true
		}
		return nil
	}
	if v.escape {
		v.escape = false
		if c == 'u' {
			v.unicodeN = 4
			v.unicodeV = 0
		}
		return nil
	}
	if c == '\\' {
		v.escape = true
		return nil
	}
	if c == '"' {
		v.inString = false
		return nil
	}
	if c < 0x20 {
		return problem("invalid-json", "unescaped control character")
	}
	return nil
}
func hexValue(c byte) (uint16, bool) {
	switch {
	case c >= '0' && c <= '9':
		return uint16(c - '0'), true
	case c >= 'a' && c <= 'f':
		return uint16(c - 'a' + 10), true
	case c >= 'A' && c <= 'F':
		return uint16(c - 'A' + 10), true
	}
	return 0, false
}

func stringToken(d *json.Decoder) (string, error) {
	t, e := d.Token()
	if e != nil {
		return "", problem("invalid-json", "%v", e)
	}
	s, ok := t.(string)
	if !ok {
		return "", problem("invalid-json", "object member name must be a string")
	}
	return s, nil
}
func arrayStart(d *json.Decoder, field string) error {
	t, e := d.Token()
	if e != nil {
		return problem("invalid-json", "%s: %v", field, e)
	}
	x, ok := t.(json.Delim)
	if !ok || x != '[' {
		return problem("validation", "%s must be an array", field)
	}
	return nil
}
func objectStart(d *json.Decoder, field string) error {
	t, e := d.Token()
	if e != nil {
		return problem("invalid-json", "%s: %v", field, e)
	}
	x, ok := t.(json.Delim)
	if !ok || x != '{' {
		return problem("validation", "%s item must be an object", field)
	}
	return nil
}

func parseCategories(ctx context.Context, d *json.Decoder) ([]Category, error) {
	if err := arrayStart(d, "categories"); err != nil {
		return nil, err
	}
	var out []Category
	codes := map[string]bool{}
	for i := 0; d.More(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := objectStart(d, "categories"); err != nil {
			return nil, err
		}
		var c Category
		seen := map[string]bool{}
		for d.More() {
			k, e := stringToken(d)
			if e != nil {
				return nil, e
			}
			if seen[k] {
				return nil, problem("duplicate-field", "categories[%d] has a duplicate field", i)
			}
			seen[k] = true
			switch k {
			case "code":
				c.Code, e = requiredString(d, k)
			case "name":
				c.Name, e = requiredString(d, k)
			case "sort_order":
				var n json.Number
				n, e = numberToken(d, k)
				if e == nil {
					var v int64
					v, e = parseInt(n, -1<<31, 1<<31-1)
					c.SortOrder = int32(v)
				}
			default:
				return nil, problem("unknown-field", "categories[%d] has an unknown field", i)
			}
			if e != nil {
				return nil, e
			}
		}
		if _, e := d.Token(); e != nil {
			return nil, problem("invalid-json", "%v", e)
		}
		if !seen["code"] || !seen["name"] {
			return nil, problem("validation", "categories[%d] requires code and name", i)
		}
		if !categoryRE.MatchString(c.Code) {
			return nil, problem("validation", "categories[%d].code is invalid", i)
		}
		if err := validText(c.Name, 64, 256, false); err != nil {
			return nil, problem("validation", "categories[%d].name: %v", i, err)
		}
		if codes[c.Code] {
			return nil, problem("conflict", "duplicate category code %q", c.Code)
		}
		codes[c.Code] = true
		out = append(out, c)
	}
	_, e := d.Token()
	return out, e
}

func parseSentences(ctx context.Context, d *json.Decoder) ([]Sentence, uint64, uint64, error) {
	if err := arrayStart(d, "sentences"); err != nil {
		return nil, 0, 0, err
	}
	var out []Sentence
	by := map[string]int{}
	var input, dedup uint64
	for i := 0; d.More(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		input++
		if err := objectStart(d, "sentences"); err != nil {
			return nil, 0, 0, err
		}
		var s Sentence
		seen := map[string]bool{}
		for d.More() {
			k, e := stringToken(d)
			if e != nil {
				return nil, 0, 0, e
			}
			if seen[k] {
				return nil, 0, 0, problem("duplicate-field", "sentences[%d] has a duplicate field", i)
			}
			seen[k] = true
			switch k {
			case "uuid":
				s.UUID, e = requiredString(d, k)
			case "category":
				s.Category, e = requiredString(d, k)
			case "content":
				s.Content, e = requiredString(d, k)
			case "source":
				s.Source, e = nullableString(d, k)
			case "author":
				s.Author, e = nullableString(d, k)
			default:
				return nil, 0, 0, problem("unknown-field", "sentences[%d] has an unknown field", i)
			}
			if e != nil {
				return nil, 0, 0, e
			}
		}
		if _, e := d.Token(); e != nil {
			return nil, 0, 0, problem("invalid-json", "%v", e)
		}
		for _, k := range []string{"uuid", "category", "content"} {
			if !seen[k] {
				return nil, 0, 0, problem("validation", "sentences[%d] requires %s", i, k)
			}
		}
		if !uuidRE.MatchString(s.UUID) {
			return nil, 0, 0, problem("validation", "sentences[%d].uuid is invalid", i)
		}
		s.UUID = strings.ToLower(s.UUID)
		if !categoryRE.MatchString(s.Category) {
			return nil, 0, 0, problem("validation", "sentences[%d].category is invalid", i)
		}
		if e := validText(s.Content, 65535, maxTextBytes, false); e != nil {
			return nil, 0, 0, problem("validation", "sentences[%d].content: %v", i, e)
		}
		if e := validText(s.Source, 255, 1020, true); e != nil {
			return nil, 0, 0, problem("validation", "sentences[%d].source: %v", i, e)
		}
		if e := validText(s.Author, 128, 512, true); e != nil {
			return nil, 0, 0, problem("validation", "sentences[%d].author: %v", i, e)
		}
		n := utf8.RuneCountInString(s.Content)
		if n > 65535 {
			return nil, 0, 0, problem("validation", "sentences[%d].content length exceeds uint16", i)
		}
		s.Length = uint16(n)
		if p, ok := by[s.UUID]; ok {
			if out[p] != s {
				return nil, 0, 0, problem("conflict", "sentences[%d] conflicts with duplicate UUID", i)
			}
			dedup++
			continue
		}
		by[s.UUID] = len(out)
		out = append(out, s)
	}
	_, e := d.Token()
	return out, input, dedup, e
}

func requiredString(d *json.Decoder, field string) (string, error) {
	t, e := d.Token()
	if e != nil {
		return "", problem("invalid-json", "%s: %v", field, e)
	}
	s, ok := t.(string)
	if !ok {
		return "", problem("validation", "%s must be a string", field)
	}
	if !utf8.ValidString(s) {
		return "", problem("invalid-unicode", "%s contains invalid Unicode", field)
	}
	return s, nil
}
func nullableString(d *json.Decoder, field string) (string, error) {
	t, e := d.Token()
	if e != nil {
		return "", problem("invalid-json", "%s: %v", field, e)
	}
	if t == nil {
		return "", nil
	}
	s, ok := t.(string)
	if !ok {
		return "", problem("validation", "%s must be a string or null", field)
	}
	if !utf8.ValidString(s) {
		return "", problem("invalid-unicode", "%s contains invalid Unicode", field)
	}
	return s, nil
}
func numberToken(d *json.Decoder, field string) (json.Number, error) {
	t, e := d.Token()
	if e != nil {
		return "", problem("invalid-json", "%s: %v", field, e)
	}
	n, ok := t.(json.Number)
	if !ok {
		return "", problem("validation", "%s must be an integer", field)
	}
	return n, nil
}
func parseInt(n json.Number, min, max int64) (int64, error) {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return 0, problem("validation", "integer required")
	}
	var v int64
	_, e := fmt.Sscan(s, &v)
	if e != nil || v < min || v > max {
		return 0, problem("validation", "integer out of range")
	}
	return v, nil
}
func validText(s string, maxRunes, maxBytes int, emptyOK bool) error {
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	if len(s) > maxBytes {
		return fmt.Errorf("exceeds %d bytes", maxBytes)
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return fmt.Errorf("exceeds %d Unicode code points", maxRunes)
	}
	if !emptyOK {
		nonspace := false
		for _, r := range s {
			if !unicode.IsSpace(r) {
				nonspace = true
				break
			}
		}
		if !nonspace {
			return errors.New("must not be empty or whitespace-only")
		}
	}
	return nil
}

// Import parses, validates and applies one input using a single transaction.
func Import(ctx context.Context, db *sql.DB, r io.Reader, dryRun bool) (Summary, error) {
	data, e := Parse(ctx, r)
	if e != nil {
		return Summary{}, e
	}
	return Run(ctx, db, data, dryRun)
}

func Run(ctx context.Context, db *sql.DB, data Dataset, dry bool) (sum Summary, err error) {
	sum = Summary{DryRun: dry, InputCount: data.InputCount, DeduplicatedCount: data.DeduplicatedCount}
	opts := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: dry}
	tx, e := db.BeginTx(ctx, opts)
	if e != nil {
		return sum, problem("database", "begin transaction failed")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var version uint64
	q := "SELECT version FROM dataset_versions WHERE id=1"
	if !dry && !database.IsSQLite(db) {
		q += " FOR UPDATE"
	}
	if e = tx.QueryRowContext(ctx, q).Scan(&version); e != nil || version == 0 {
		return sum, problem("database", "dataset version is missing or invalid")
	}
	sum.DatasetVersionBefore = fmt.Sprint(version)
	catIDs := map[string]uint64{}
	newCats := map[string]Category{}
	for _, c := range data.Categories {
		newCats[c.Code] = c
	}
	needed := map[string]bool{}
	for _, s := range data.Sentences {
		needed[s.Category] = true
	}
	for code := range newCats {
		needed[code] = true
	}
	keys := sortedKeys(needed)
	for _, chunk := range chunks(keys, 500) {
		args := make([]any, len(chunk))
		marks := make([]string, len(chunk))
		for i, v := range chunk {
			args[i] = v
			marks[i] = "?"
		}
		rows, e := tx.QueryContext(ctx, "SELECT id,code,name,enabled,sort_order FROM categories WHERE code IN ("+strings.Join(marks, ",")+")", args...)
		if e != nil {
			return sum, problem("database", "category lookup failed")
		}
		for rows.Next() {
			var id uint64
			var code, name string
			var enabled bool
			var order int32
			if e = rows.Scan(&id, &code, &name, &enabled, &order); e != nil {
				rows.Close()
				return sum, problem("database", "category scan failed")
			}
			if !enabled {
				rows.Close()
				return sum, problem("conflict", "category %q is disabled", code)
			}
			if c, ok := newCats[code]; ok && (c.Name != name || c.SortOrder != order) {
				rows.Close()
				return sum, problem("conflict", "category %q differs from database", code)
			}
			catIDs[code] = id
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return sum, problem("database", "category lookup failed")
		}
		if e = rows.Close(); e != nil {
			return sum, problem("database", "category lookup failed")
		}
	}
	for code := range needed {
		if _, ok := catIDs[code]; !ok {
			if _, defined := newCats[code]; !defined {
				return sum, problem("conflict", "unknown category %q", code)
			}
		}
	}
	type existing struct {
		category, content, source, author string
		length                            uint16
		status                            uint8
		enabled                           bool
		published                         sql.NullTime
	}
	existingBy := map[string]existing{}
	uuids := make([]string, len(data.Sentences))
	for i, s := range data.Sentences {
		uuids[i] = strings.ReplaceAll(s.UUID, "-", "")
	}
	for _, chunk := range chunks(uuids, 500) {
		args := make([]any, len(chunk))
		marks := make([]string, len(chunk))
		for i, v := range chunk {
			args[i] = v
			marks[i] = "?"
		}
		rows, e := tx.QueryContext(ctx, "SELECT s.uuid,c.code,s.content,s.source,s.author,s.length,s.status,c.enabled,s.published_at FROM sentences s JOIN categories c ON c.id=s.category_id WHERE REPLACE(LOWER(s.uuid),'-','') IN ("+strings.Join(marks, ",")+")", args...)
		if e != nil {
			return sum, problem("database", "sentence lookup failed")
		}
		for rows.Next() {
			var raw string
			var x existing
			var src, auth sql.NullString
			if e = rows.Scan(&raw, &x.category, &x.content, &src, &auth, &x.length, &x.status, &x.enabled, &x.published); e != nil {
				rows.Close()
				return sum, problem("database", "sentence scan failed")
			}
			key := strings.ToLower(raw)
			lookupKey := key
			if len(key) == 32 {
				lookupKey = key[:8] + "-" + key[8:12] + "-" + key[12:16] + "-" + key[16:20] + "-" + key[20:]
			}
			if !uuidRE.MatchString(raw) || raw != key {
				rows.Close()
				return sum, problem("conflict", "database UUID for related record is not canonical")
			}
			x.source = src.String
			x.author = auth.String
			if _, dup := existingBy[lookupKey]; dup {
				rows.Close()
				return sum, problem("conflict", "database contains duplicate normalized UUID %q", key)
			}
			existingBy[lookupKey] = x
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return sum, problem("database", "sentence lookup failed")
		}
		if e = rows.Close(); e != nil {
			return sum, problem("database", "sentence lookup failed")
		}
	}
	var additions []Sentence
	for _, s := range data.Sentences {
		if x, ok := existingBy[s.UUID]; ok {
			if x.status != 1 || !x.enabled || !x.published.Valid || x.length != s.Length || x.category != s.Category || x.content != s.Content || x.source != s.Source || x.author != s.Author {
				return sum, problem("conflict", "UUID %q conflicts with database", s.UUID)
			}
			sum.SkippedCount++
			continue
		}
		additions = append(additions, s)
	}
	sum.NewSentences = uint64(len(additions))
	for code := range newCats {
		if _, ok := catIDs[code]; !ok {
			sum.NewCategories++
		}
	}
	if dry {
		if e = tx.Commit(); e != nil {
			return sum, problem("database", "finish dry-run transaction failed")
		}
		committed = true
		sum.Changed = false
		return sum, nil
	}
	for _, code := range sortedCategoryCodes(newCats) {
		if _, ok := catIDs[code]; ok {
			continue
		}
		c := newCats[code]
		res, e := tx.ExecContext(ctx, "INSERT INTO categories (code,name,enabled,sort_order) VALUES (?,?,TRUE,?)", c.Code, c.Name, c.SortOrder)
		if e != nil {
			return sum, problem("database", "category insert failed")
		}
		id, e := res.LastInsertId()
		if e != nil {
			return sum, problem("database", "category insert id unavailable")
		}
		catIDs[code] = uint64(id)
	}
	now := time.Now().UTC()
	for start := 0; start < len(additions); start += 250 {
		end := start + 250
		if end > len(additions) {
			end = len(additions)
		}
		vals := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*7)
		for _, s := range additions[start:end] {
			var src, auth any
			if s.Source != "" {
				src = s.Source
			}
			if s.Author != "" {
				auth = s.Author
			}
			vals = append(vals, "(?,?,?,?,?,?,1,?)")
			args = append(args, s.UUID, catIDs[s.Category], s.Content, src, auth, s.Length, now)
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO sentences (uuid,category_id,content,source,author,length,status,published_at) VALUES "+strings.Join(vals, ","), args...); e != nil {
			return sum, problem("database", "sentence insert failed")
		}
	}
	changed := sum.NewCategories+sum.NewSentences > 0
	after := version
	if changed {
		stamp := "CURRENT_TIMESTAMP(6)"
		maxVersion := "18446744073709551615"
		if database.IsSQLite(db) {
			stamp = "CURRENT_TIMESTAMP"
			maxVersion = "9223372036854775807"
		}
		res, e := tx.ExecContext(ctx, "UPDATE dataset_versions SET version=version+1,published_at="+stamp+" WHERE id=1 AND version < "+maxVersion)
		if e != nil {
			return sum, problem("database", "dataset version update failed")
		}
		n, e := res.RowsAffected()
		if e != nil || n != 1 {
			return sum, problem("database", "dataset version update affected no row")
		}
		after++
	}
	if e = commitWrite(tx); e != nil {
		return sum, e
	}
	committed = true
	sum.Changed = changed
	a := fmt.Sprint(after)
	sum.DatasetVersionAfter = &a
	return sum, nil
}

type committer interface{ Commit() error }

func commitWrite(tx committer) error {
	if e := tx.Commit(); e != nil {
		return problem("commit-outcome-unknown", "commit confirmation failed")
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func sortedCategoryCodes(m map[string]Category) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func chunks(v []string, n int) [][]string {
	var out [][]string
	for len(v) > 0 {
		x := n
		if len(v) < x {
			x = len(v)
		}
		out = append(out, v[:x])
		v = v[x:]
	}
	return out
}
