package snapshot

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrNotFound          = errors.New("no matching sentence")
	ErrUnknownCategory   = errors.New("unknown category")
	ErrRefreshInProgress = errors.New("snapshot refresh in progress")
	codeRE               = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

type Sentence struct {
	ID                                      uint64
	UUID, Content, Category, Source, Author string
	Length                                  uint16
}
type Category struct {
	Code, Name string
	SortOrder  int32
	Count      uint64
}
type Snapshot struct {
	Version                  uint64
	LoadedAt                 time.Time
	ByUUID                   map[string]*Sentence
	ByCategory               map[string][]*Sentence
	Categories               []Category
	TextBytes, SentenceCount uint64
}
type CategoryRow struct {
	Code, Name string
	SortOrder  int32
}
type SentenceRow = Sentence

type Builder struct {
	s   *Snapshot
	cat map[string]int
}

func NewBuilder(version uint64, loadedAt time.Time) *Builder {
	return &Builder{s: &Snapshot{Version: version, LoadedAt: loadedAt, ByUUID: make(map[string]*Sentence), ByCategory: make(map[string][]*Sentence)}, cat: make(map[string]int)}
}
func (b *Builder) AddCategory(r CategoryRow) error {
	if !codeRE.MatchString(r.Code) {
		return fmt.Errorf("category code is invalid")
	}
	if !validText(r.Name, 64, 256, false) {
		return fmt.Errorf("category %q name is invalid", r.Code)
	}
	if _, ok := b.cat[r.Code]; ok {
		return fmt.Errorf("duplicate category %q", r.Code)
	}
	b.cat[r.Code] = len(b.s.Categories)
	b.s.Categories = append(b.s.Categories, Category{Code: r.Code, Name: r.Name, SortOrder: r.SortOrder})
	b.s.ByCategory[r.Code] = nil
	b.s.TextBytes += uint64(len(r.Code) + len(r.Name))
	return nil
}
func (b *Builder) AddSentence(r SentenceRow) error {
	if _, ok := b.cat[r.Category]; !ok {
		return fmt.Errorf("sentence references unknown category")
	}
	u, e := uuid.Parse(r.UUID)
	if e != nil || len(r.UUID) != 36 || u.String() != r.UUID {
		return fmt.Errorf("sentence uuid is invalid or non-canonical")
	}
	if _, ok := b.s.ByUUID[r.UUID]; ok {
		return fmt.Errorf("duplicate sentence uuid")
	}
	if r.ID == 0 {
		return fmt.Errorf("sentence id must be positive")
	}
	if !validText(r.Content, 65535, 65535, true) {
		return fmt.Errorf("sentence content is invalid")
	}
	n := utf8.RuneCountInString(r.Content)
	if n > 65535 || uint16(n) != r.Length {
		return fmt.Errorf("sentence length mismatch")
	}
	if !validOptional(r.Source, 255, 1020) || !validOptional(r.Author, 128, 512) {
		return fmt.Errorf("sentence metadata is invalid")
	}
	x := r
	b.s.ByUUID[x.UUID] = &x
	b.s.ByCategory[x.Category] = append(b.s.ByCategory[x.Category], &x)
	b.s.SentenceCount++
	b.s.TextBytes += uint64(len(x.UUID) + len(x.Content) + len(x.Category) + len(x.Source) + len(x.Author))
	return nil
}
func (b *Builder) Build(ctx context.Context) (*Snapshot, error) {
	if b.s.Version == 0 {
		return nil, fmt.Errorf("dataset version must be positive")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	sort.Slice(b.s.Categories, func(i, j int) bool {
		a, c := b.s.Categories[i], b.s.Categories[j]
		if a.SortOrder != c.SortOrder {
			return a.SortOrder < c.SortOrder
		}
		return a.Code < c.Code
	})
	for i := range b.s.Categories {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		c := &b.s.Categories[i]
		rows := b.s.ByCategory[c.Code]
		if e := sortRows(ctx, rows); e != nil {
			return nil, e
		}
		c.Count = uint64(len(rows))
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	b.s.LoadedAt = time.Now().UTC()
	return b.s, nil
}

type sortCanceled struct{ err error }

func sortRows(ctx context.Context, rows []*Sentence) (err error) {
	defer func() {
		if p := recover(); p != nil {
			if c, ok := p.(sortCanceled); ok {
				err = c.err
				return
			}
			panic(p)
		}
	}()
	var comparisons uint32
	sort.Slice(rows, func(i, j int) bool {
		comparisons++
		if comparisons&1023 == 0 {
			if e := ctx.Err(); e != nil {
				panic(sortCanceled{e})
			}
		}
		if rows[i].Length != rows[j].Length {
			return rows[i].Length < rows[j].Length
		}
		return rows[i].ID < rows[j].ID
	})
	return ctx.Err()
}
func validText(s string, maxRunes, maxBytes int, nonempty bool) bool {
	return utf8.ValidString(s) && len(s) <= maxBytes && utf8.RuneCountInString(s) <= maxRunes && (!nonempty || s != "") && !strings.EqualFold(strings.TrimSpace(s), "")
}
func validOptional(s string, r, b int) bool {
	return utf8.ValidString(s) && len(s) <= b && utf8.RuneCountInString(s) <= r
}

func (s *Snapshot) LookupUUID(id string) (*Sentence, bool) { v, ok := s.ByUUID[id]; return v, ok }
func (s *Snapshot) Select(categories []string, min, max uint16, offset func(uint64) uint64) (*Sentence, error) {
	codes := categories
	if codes == nil {
		codes = make([]string, len(s.Categories))
		for i := range s.Categories {
			codes[i] = s.Categories[i].Code
		}
	}
	seen := map[string]bool{}
	type span struct {
		rows   []*Sentence
		lo, hi int
	}
	sp := make([]span, 0, len(codes))
	var total uint64
	for _, c := range codes {
		if seen[c] {
			continue
		}
		seen[c] = true
		rows, ok := s.ByCategory[c]
		if !ok {
			return nil, ErrUnknownCategory
		}
		lo := sort.Search(len(rows), func(i int) bool { return rows[i].Length >= min })
		hi := sort.Search(len(rows), func(i int) bool { return rows[i].Length > max })
		sp = append(sp, span{rows, lo, hi})
		total += uint64(hi - lo)
	}
	if total == 0 {
		return nil, ErrNotFound
	}
	var n uint64
	if offset == nil {
		n = rand.Uint64N(total)
	} else {
		n = offset(total)
		if n >= total {
			return nil, fmt.Errorf("random offset out of range")
		}
	}
	for _, p := range sp {
		z := uint64(p.hi - p.lo)
		if n < z {
			return p.rows[p.lo+int(n)], nil
		}
		n -= z
	}
	panic("unreachable")
}

type Loader interface {
	Version(context.Context) (uint64, error)
	Load(context.Context) (*Snapshot, error)
}
type Manager struct {
	loader  Loader
	timeout time.Duration
	current atomic.Pointer[Snapshot]
	busy    atomic.Bool
}

func NewManager(l Loader, t time.Duration) *Manager      { return &Manager{loader: l, timeout: t} }
func (m *Manager) Current() *Snapshot                    { return m.current.Load() }
func (m *Manager) Ready() bool                           { return m.Current() != nil }
func (m *Manager) LoadInitial(ctx context.Context) error { _, e := m.Refresh(ctx, true); return e }
func (m *Manager) IsReloading() bool                     { return m.busy.Load() }
func (m *Manager) Refresh(ctx context.Context, force bool) (bool, error) {
	if !m.busy.CompareAndSwap(false, true) {
		return false, ErrRefreshInProgress
	}
	defer m.busy.Store(false)
	return m.refreshHeld(ctx, force)
}
func (m *Manager) TryStartRefresh(ctx context.Context, force bool, done func(bool, error)) bool {
	if !m.busy.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer m.busy.Store(false)
		changed, err := m.refreshHeld(ctx, force)
		if done != nil {
			done(changed, err)
		}
	}()
	return true
}
func (m *Manager) refreshHeld(ctx context.Context, force bool) (bool, error) {
	c := m.Current()
	if !force && c != nil {
		q, e := withTimeout(ctx, m.timeout)
		if e != nil {
			return false, e
		}
		v, e := m.loader.Version(q)
		q.cancel()
		if e != nil {
			return false, e
		}
		if v == c.Version {
			return false, nil
		}
	}
	q, e := withTimeout(ctx, m.timeout)
	if e != nil {
		return false, e
	}
	next, e := m.loader.Load(q)
	if e == nil {
		e = q.Err()
	}
	q.cancel()
	if e != nil {
		return false, e
	}
	if next == nil {
		return false, errors.New("loader returned nil snapshot")
	}
	if next.Version == 0 {
		return false, errors.New("loader returned invalid snapshot")
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	m.current.Store(next)
	return true, nil
}

type timeoutContext struct {
	context.Context
	cancel context.CancelFunc
}

func withTimeout(p context.Context, d time.Duration) (timeoutContext, error) {
	if e := p.Err(); e != nil {
		return timeoutContext{}, e
	}
	c, x := context.WithTimeout(p, d)
	return timeoutContext{c, x}, nil
}
