package importjobs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"sentence-api/internal/database"
	"sentence-api/internal/importer"
)

type Options struct {
	DB       *sql.DB
	Dir      string
	MaxBytes int64
	TTL      time.Duration
	Timeout  time.Duration
	Refresh  func(context.Context) error
}

type work struct{ id, action string }
type Manager struct {
	opts   Options
	life   context.Context
	mu     sync.Mutex
	active string
	queue  chan work
}

func New(ctx context.Context, opts Options, wg *sync.WaitGroup) (*Manager, error) {
	if opts.DB == nil || opts.Dir == "" || opts.MaxBytes <= 0 || opts.TTL <= 0 || opts.Timeout <= 0 || opts.Refresh == nil || wg == nil {
		return nil, errors.New("invalid import options")
	}
	if err := os.MkdirAll(opts.Dir, 0700); err != nil {
		return nil, errors.New("create upload directory")
	}
	info, err := os.Lstat(opts.Dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid upload directory")
	}
	if err = os.Chmod(opts.Dir, 0700); err != nil {
		return nil, errors.New("protect upload directory")
	}
	m := &Manager{opts: opts, life: ctx, queue: make(chan work, 1)}
	if err = m.recover(ctx); err != nil {
		return nil, err
	}
	wg.Add(1)
	go func() { defer wg.Done(); m.loop() }()
	return m, nil
}

const columns = "id,admin_id,digest,format,status,result_json,error_text,categories_json,created_at,expires_at,updated_at"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Job, error) {
	var j Job
	var result, message, categories sql.NullString
	err := row.Scan(&j.ID, &j.AdminID, &j.Digest, &j.Format, &j.Status, &result, &message, &categories, &j.CreatedAt, &j.ExpiresAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, errors.New("read import job")
	}
	j.Error = message.String
	if result.Valid {
		if err = json.Unmarshal([]byte(result.String), &j.Result); err != nil {
			return j, errors.New("invalid import result")
		}
		j.HasResult = true
	}
	if categories.Valid {
		if err = json.Unmarshal([]byte(categories.String), &j.Categories); err != nil {
			return j, errors.New("invalid category mappings")
		}
	}
	return j, nil
}
func validID(id string) bool { u, e := uuid.Parse(id); return e == nil && u.String() == id }
func (m *Manager) Get(ctx context.Context, owner uint64, id string) (Job, error) {
	if !validID(id) {
		return Job{}, ErrNotFound
	}
	return scan(m.opts.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM import_jobs WHERE id=? AND admin_id=?", id, owner))
}
func (m *Manager) get(ctx context.Context, id string) (Job, error) {
	return scan(m.opts.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM import_jobs WHERE id=?", id))
}
func (m *Manager) List(ctx context.Context, owner uint64) ([]Job, error) {
	rows, err := m.opts.DB.QueryContext(ctx, "SELECT "+columns+" FROM import_jobs WHERE admin_id=? ORDER BY created_at DESC,id DESC LIMIT 50", owner)
	if err != nil {
		return nil, errors.New("list import jobs")
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (m *Manager) path(id string) string { return filepath.Join(m.opts.Dir, id+".json") }

func (m *Manager) Upload(ctx context.Context, owner uint64, format string, r io.Reader) (Job, error) {
	if owner == 0 || (format != importer.FormatNative && format != importer.FormatHitokoto) {
		return Job{}, ErrState
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.life.Err() != nil {
		return Job{}, ErrState
	}
	if err := m.expireLocked(ctx); err != nil {
		return Job{}, err
	}
	if m.active != "" {
		return Job{}, ErrBusy
	}
	id := uuid.NewString()
	path := m.path(id)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Job{}, errors.New("create private upload")
	}
	retained := false
	defer func() {
		file.Close()
		if !retained {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(r, m.opts.MaxBytes+1))
	if n > m.opts.MaxBytes {
		return Job{}, ErrTooLarge
	}
	if err != nil {
		return Job{}, errors.Join(ErrUpload, err)
	}
	if err = ctx.Err(); err != nil {
		return Job{}, err
	}
	if err = file.Sync(); err != nil {
		return Job{}, errors.New("save upload")
	}
	if err = file.Close(); err != nil {
		return Job{}, errors.New("close upload")
	}
	now := time.Now().UTC()
	j := Job{ID: id, AdminID: owner, Digest: hex.EncodeToString(hash.Sum(nil)), Format: format, Status: "queued_preview", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(m.opts.TTL)}
	_, err = m.opts.DB.ExecContext(ctx, "INSERT INTO import_jobs (id,admin_id,digest,format,status,created_at,expires_at,updated_at) VALUES (?,?,?,?,?,?,?,?)", id, owner, j.Digest, format, j.Status, now, j.ExpiresAt, now)
	if err != nil {
		return Job{}, errors.New("create import job")
	}
	retained = true
	m.active = id
	m.queue <- work{id, "preview"}
	return j, nil
}

func (m *Manager) Confirm(ctx context.Context, owner uint64, id, digest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.life.Err() != nil {
		return ErrState
	}
	j, err := m.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	if j.Digest != digest {
		return ErrDigest
	}
	if j.Status != "preview" {
		return ErrState
	}
	if !time.Now().Before(j.ExpiresAt) {
		if err = m.setStatus(ctx, id, "expired", "预览已过期，请重新上传。"); err != nil {
			return err
		}
		m.clearLocked(id)
		return ErrExpired
	}
	if m.active != "" && m.active != id {
		return ErrBusy
	}
	if err = m.transition(ctx, id, "preview", "queued_import"); err != nil {
		return err
	}
	m.active = id
	m.queue <- work{id, "import"}
	return nil
}
func (m *Manager) Cancel(ctx context.Context, owner uint64, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, err := m.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	if j.Status != "preview" {
		return ErrState
	}
	if err = m.transition(ctx, id, "preview", "canceled"); err != nil {
		return err
	}
	m.clearLocked(id)
	return nil
}
func (m *Manager) RetryRefresh(ctx context.Context, owner uint64, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.life.Err() != nil {
		return ErrState
	}
	j, err := m.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	if j.Status != "refresh_failed" {
		return ErrState
	}
	if m.active != "" {
		return ErrBusy
	}
	if err = m.transition(ctx, id, "refresh_failed", "queued_refresh"); err != nil {
		return err
	}
	m.active = id
	m.queue <- work{id, "refresh"}
	return nil
}
func (m *Manager) transition(ctx context.Context, id, from, to string) error {
	result, err := m.opts.DB.ExecContext(ctx, "UPDATE import_jobs SET status=?,updated_at=?,error_text=NULL WHERE id=? AND status=?", to, time.Now().UTC(), id, from)
	if err != nil {
		return errors.New("update import status")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrState
	}
	return nil
}
func (m *Manager) setStatus(ctx context.Context, id, status, message string) error {
	_, err := m.opts.DB.ExecContext(ctx, "UPDATE import_jobs SET status=?,error_text=?,updated_at=? WHERE id=?", status, message, time.Now().UTC(), id)
	if err != nil {
		return errors.New("save import status")
	}
	return nil
}
func (m *Manager) clearLocked(id string) {
	if m.active == id {
		m.active = ""
	}
	_ = os.Remove(m.path(id))
}
func (m *Manager) release(id string) { m.mu.Lock(); defer m.mu.Unlock(); m.clearLocked(id) }
func (m *Manager) expireLocked(ctx context.Context) error {
	if m.active == "" {
		return nil
	}
	j, err := m.get(ctx, m.active)
	if err != nil {
		return err
	}
	if j.Status == "preview" && !time.Now().Before(j.ExpiresAt) {
		if err = m.setStatus(ctx, j.ID, "expired", "预览已过期，请重新上传。"); err != nil {
			return err
		}
		m.clearLocked(j.ID)
	}
	return nil
}
func (m *Manager) loop() {
	interval := time.Minute
	if m.opts.TTL/2 < interval {
		interval = m.opts.TTL / 2
	}
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-m.life.Done():
			return
		case task := <-m.queue:
			m.execute(task)
		case <-timer.C:
			ctx, cancel := context.WithTimeout(m.life, 5*time.Second)
			m.mu.Lock()
			_ = m.expireLocked(ctx)
			m.mu.Unlock()
			cancel()
		}
	}
}

func (m *Manager) readData(ctx context.Context, j Job) (importer.Dataset, []CategoryChange, error) {
	file, err := os.Open(m.path(j.ID))
	if err != nil {
		return importer.Dataset{}, nil, errors.New("uploaded file unavailable")
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, m.opts.MaxBytes+1))
	if err != nil || n > m.opts.MaxBytes {
		return importer.Dataset{}, nil, ErrDigest
	}
	if hex.EncodeToString(hash.Sum(nil)) != j.Digest {
		return importer.Dataset{}, nil, ErrDigest
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return importer.Dataset{}, nil, err
	}
	data, err := importer.ParseFormat(ctx, file, j.Format)
	if err != nil {
		return data, nil, err
	}
	mappings := make([]CategoryChange, 0, len(data.Categories))
	for i := range data.Categories {
		c := &data.Categories[i]
		var name string
		var order int32
		err = m.opts.DB.QueryRowContext(ctx, "SELECT name,sort_order FROM categories WHERE code=?", c.Code).Scan(&name, &order)
		create := errors.Is(err, sql.ErrNoRows)
		if err != nil && !create {
			return data, nil, errors.New("read category mapping")
		}
		// Hitokoto supplies codes rather than local display names. Existing local
		// names are authoritative; native JSON still keeps its strict conflict rule.
		if !create && j.Format == importer.FormatHitokoto {
			c.Name = name
			c.SortOrder = order
		}
		mappings = append(mappings, CategoryChange{Code: c.Code, Name: c.Name, Create: create})
	}
	return data, mappings, nil
}
func safeError(err error) string {
	if errors.Is(err, ErrDigest) {
		return "文件摘要不一致，请重新上传。"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "处理超时，请检查文件规模后重试。"
	}
	var e *importer.Error
	if errors.As(err, &e) {
		switch e.Category {
		case "database":
			// Importer database errors contain fixed operation labels, never driver text.
			return "数据库处理失败：" + e.Error()
		case "commit-outcome-unknown":
			return "数据库提交结果待核实，请查看任务结果后再操作。"
		default:
			s := e.Error()
			if len(s) > 2048 {
				s = string([]rune(s)[:min(len([]rune(s)), 512)])
			}
			return s
		}
	}
	return "处理失败，请重新上传或稍后重试。"
}
func (m *Manager) fail(id string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// On shutdown preserve an explicit interrupted state; startup recovery will
	// also handle any status write interrupted by an unavailable database.
	status := "failed"
	if m.life.Err() != nil {
		status = "interrupted"
	}
	_ = m.setStatus(ctx, id, status, safeError(err))
	m.release(id)
}
func (m *Manager) execute(task work) {
	ctx, cancel := context.WithTimeout(m.life, m.opts.Timeout)
	defer cancel()
	j, err := m.get(ctx, task.id)
	if err != nil {
		if task.action == "refresh" {
			m.refreshFailed(task.id)
			return
		}
		m.fail(task.id, err)
		return
	}
	if task.action == "refresh" {
		if err = m.transition(ctx, j.ID, "queued_refresh", "refreshing"); err != nil {
			m.refreshFailed(j.ID)
			return
		}
		m.refresh(j.ID)
		return
	}
	from, to := "queued_preview", "previewing"
	if task.action == "import" {
		from, to = "queued_import", "importing"
	}
	if err = m.transition(ctx, j.ID, from, to); err != nil {
		m.fail(j.ID, err)
		return
	}
	data, mappings, err := m.readData(ctx, j)
	if err != nil {
		m.fail(j.ID, err)
		return
	}
	if task.action == "preview" {
		summary, err := importer.Run(ctx, m.opts.DB, data, true)
		if err != nil {
			m.fail(j.ID, err)
			return
		}
		if !time.Now().Before(j.ExpiresAt) {
			_ = m.setStatus(ctx, j.ID, "expired", "预览已过期，请重新上传。")
			m.release(j.ID)
			return
		}
		result, _ := json.Marshal(summary)
		cats, _ := json.Marshal(mappings)
		_, err = m.opts.DB.ExecContext(ctx, "UPDATE import_jobs SET status='preview',result_json=?,categories_json=?,updated_at=? WHERE id=? AND status='previewing'", string(result), string(cats), time.Now().UTC(), j.ID)
		if err != nil {
			m.fail(j.ID, err)
		}
		return
	}
	_, err = importer.RunWithReceipt(ctx, m.opts.DB, data, func(tx *sql.Tx, result importer.Summary) error {
		raw, e := json.Marshal(result)
		if e != nil {
			return e
		}
		res, e := tx.ExecContext(ctx, "UPDATE import_jobs SET status='committed',result_json=?,error_text=NULL,updated_at=? WHERE id=? AND status='importing'", string(raw), time.Now().UTC(), j.ID)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrState
		}
		return nil
	})
	if err != nil {
		var failure *importer.Error
		if errors.As(err, &failure) && failure.Category == "commit-outcome-unknown" {
			probe, stop := context.WithTimeout(context.Background(), 5*time.Second)
			committed, checkErr := m.committed(probe, j.ID)
			stop()
			if checkErr != nil {
				m.release(j.ID)
				return
			} // retain importing/committed for locked restart reconciliation
			if committed {
				m.refresh(j.ID)
				return
			}
		}
		m.fail(j.ID, err)
		return
	}
	m.refresh(j.ID)
}
func (m *Manager) committed(ctx context.Context, id string) (bool, error) {
	tx, err := m.opts.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, "SELECT status FROM import_jobs WHERE id=?"+database.ForUpdate(m.opts.DB), id).Scan(&status); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return status == "committed" || status == "complete" || status == "refresh_failed", nil
}
func (m *Manager) refresh(id string) {
	defer m.release(id)
	ctx, cancel := context.WithTimeout(m.life, m.opts.Timeout)
	err := m.opts.Refresh(ctx)
	cancel()
	save, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err != nil {
		_ = m.setStatus(save, id, "refresh_failed", "数据已写入，快照刷新未完成。可单独重试刷新，无需再次导入。")
		return
	}
	_ = m.setStatus(save, id, "complete", "")
}
func (m *Manager) recover(ctx context.Context) error {
	// These conditional updates lock the receipt row. If an old transaction is
	// still committing, recovery waits for it; if it has not reached the receipt,
	// changing its state makes its guarded receipt write fail and rolls it back.
	tx, err := m.opts.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, "UPDATE import_jobs SET status='refresh_failed',error_text=?,updated_at=? WHERE status IN ('committed','queued_refresh','refreshing')", "数据已写入；服务重启后请重试刷新。", now); err != nil {
		return errors.New("recover committed imports")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE import_jobs SET status='interrupted',error_text=?,updated_at=? WHERE status IN ('queued_preview','previewing','preview','queued_import','importing')", "服务已重启，此任务未提交。请重新上传。", now); err != nil {
		return errors.New("recover interrupted imports")
	}
	if err = tx.Commit(); err != nil {
		return errors.New("commit import recovery")
	}
	entries, err := os.ReadDir(m.opts.Dir)
	if err != nil {
		return errors.New("list private uploads")
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && validID(strings.TrimSuffix(entry.Name(), ".json")) {
			if err = os.Remove(filepath.Join(m.opts.Dir, entry.Name())); err != nil {
				return fmt.Errorf("clean expired upload")
			}
		}
	}
	return nil
}

func (m *Manager) refreshFailed(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = m.setStatus(ctx, id, "refresh_failed", "数据已写入，快照刷新未完成。可单独重试刷新。")
	m.release(id)
}
