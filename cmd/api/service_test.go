package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"sentence-api/internal/database"
	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
)

type fakeVersions struct {
	version uint64
	err     error
	calls   atomic.Int64
}

func (f *fakeVersions) Version(context.Context) (uint64, error) {
	f.calls.Add(1)
	return f.version, f.err
}

type fakeRefresh struct {
	mu       sync.Mutex
	current  *snapshot.Snapshot
	accept   bool
	calls    int
	callback func(bool, error)
	fast     bool
}

func (f *fakeRefresh) Current() *snapshot.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}
func (f *fakeRefresh) IsReloading() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.callback != nil }
func (f *fakeRefresh) TryStartRefresh(_ context.Context, _ bool, cb func(bool, error)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if !f.accept {
		return false
	}
	if f.fast {
		go cb(true, nil)
	} else {
		f.callback = cb
	}
	return true
}
func (f *fakeRefresh) complete(s *snapshot.Snapshot, err error) {
	f.mu.Lock()
	f.current = s
	cb := f.callback
	f.callback = nil
	f.mu.Unlock()
	cb(err == nil, err)
}
func (f *fakeRefresh) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func newController(ctx context.Context, v *fakeVersions, m *fakeRefresh, metrics *observability.Metrics, logs io.Writer) *refreshController {
	var stopping atomic.Bool
	return &refreshController{life: ctx, loader: v, manager: m, metrics: metrics, logger: slog.New(slog.NewJSONHandler(logs, nil)), loadTimeout: time.Second, stopping: &stopping, bg: &sync.WaitGroup{}}
}
func metricText(m *observability.Metrics) string {
	var b bytes.Buffer
	m.WritePrometheus(&b)
	return b.String()
}

func TestPollUnchangedDoesNotRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	metrics := observability.NewMetrics()
	m := &fakeRefresh{current: &snapshot.Snapshot{Version: 7}, accept: true}
	v := &fakeVersions{version: 7}
	c := newController(ctx, v, m, metrics, io.Discard)
	c.pollOnce()
	if m.callCount() != 0 {
		t.Fatal("unchanged version started load")
	}
	if !strings.Contains(metricText(metrics), `sentence_api_snapshot_refresh_total{result="success"} 0`) {
		t.Fatal("unchanged check counted refresh")
	}
}

func TestPollSkipsDatabaseWhileRefreshBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &fakeRefresh{current: &snapshot.Snapshot{Version: 1}, accept: true}
	m.callback = func(bool, error) {}
	v := &fakeVersions{version: 2}
	c := newController(ctx, v, m, observability.NewMetrics(), io.Discard)
	c.pollOnce()
	if v.calls.Load() != 0 {
		t.Fatal("busy poll queried database")
	}
	if m.callCount() != 0 {
		t.Fatal("busy poll started another refresh")
	}
}

func TestPollLowerVersionWarnsAndKeepsOldUntilComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	metrics := observability.NewMetrics()
	old := &snapshot.Snapshot{Version: 9, SentenceCount: 1}
	next := &snapshot.Snapshot{Version: 3, SentenceCount: 2}
	m := &fakeRefresh{current: old, accept: true}
	v := &fakeVersions{version: 3}
	var logs bytes.Buffer
	c := newController(ctx, v, m, metrics, &logs)
	c.pollOnce()
	if m.callCount() != 1 || m.Current() != old {
		t.Fatal("lower version was not started or old snapshot replaced early")
	}
	if !strings.Contains(logs.String(), "snapshot_version_decreased") {
		t.Fatal("lower-version warning missing")
	}
	m.complete(next, nil)
	c.bg.Wait()
	if m.Current() != next {
		t.Fatal("new snapshot not published")
	}
}

func TestBusyDoesNotClearInProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	metrics := observability.NewMetrics()
	metrics.SetRefreshInProgress(true)
	m := &fakeRefresh{current: &snapshot.Snapshot{Version: 1}, accept: false}
	c := newController(ctx, &fakeVersions{}, m, metrics, io.Discard)
	if c.start(true, nil) {
		t.Fatal("busy refresh accepted")
	}
	if !strings.Contains(metricText(metrics), "sentence_api_snapshot_refresh_in_progress 1") {
		t.Fatal("busy request cleared active gauge")
	}
}

func TestFastCompletionDoesNotLeaveGaugeSet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	metrics := observability.NewMetrics()
	m := &fakeRefresh{current: &snapshot.Snapshot{Version: 2}, accept: true, fast: true}
	c := newController(ctx, &fakeVersions{}, m, metrics, io.Discard)
	if !c.start(true, nil) {
		t.Fatal("not accepted")
	}
	c.bg.Wait()
	if !strings.Contains(metricText(metrics), "sentence_api_snapshot_refresh_in_progress 0") {
		t.Fatal("fast completion left gauge set")
	}
}

func TestShutdownCancellationIsCanceledNotFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	metrics := observability.NewMetrics()
	m := &fakeRefresh{current: &snapshot.Snapshot{Version: 1}, accept: true}
	c := newController(ctx, &fakeVersions{}, m, metrics, io.Discard)
	if !c.start(true, nil) {
		t.Fatal("not accepted")
	}
	cancel()
	m.complete(m.Current(), context.Canceled)
	c.bg.Wait()
	s := metricText(metrics)
	if !strings.Contains(s, `result="canceled"} 1`) || !strings.Contains(s, `result="failure"} 0`) {
		t.Fatalf("wrong result: %s", s)
	}
}

func TestOperationTimeoutCountsAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	metrics := observability.NewMetrics()
	m := &fakeRefresh{current: &snapshot.Snapshot{Version: 1}, accept: true}
	c := newController(ctx, &fakeVersions{}, m, metrics, io.Discard)
	if !c.start(true, nil) {
		t.Fatal("not accepted")
	}
	m.complete(m.Current(), context.DeadlineExceeded)
	c.bg.Wait()
	s := metricText(metrics)
	if !strings.Contains(s, `result="failure"} 1`) || !strings.Contains(s, `result="canceled"} 0`) {
		t.Fatalf("timeout result wrong: %s", s)
	}
}

func TestDatabaseObservationValidationAndServiceCancel(t *testing.T) {
	metrics := observability.NewMetrics()
	life, cancel := context.WithCancel(context.Background())
	recordDatabaseOperation(metrics, life, &database.ValidationError{Reason: "bad"})
	if !strings.Contains(metricText(metrics), "sentence_api_database_last_operation_success 1") {
		t.Fatal("validation error marked DB failure")
	}
	cancel()
	recordDatabaseOperation(metrics, life, context.Canceled)
	if !strings.Contains(metricText(metrics), "sentence_api_database_last_operation_success 1") {
		t.Fatal("service cancellation changed DB observation")
	}
	life2 := context.Background()
	recordDatabaseOperation(metrics, life2, errors.New("network"))
	if !strings.Contains(metricText(metrics), "sentence_api_database_last_operation_success 0") {
		t.Fatal("DB error marked success")
	}
}

type fakeHTTPShutdown struct {
	release chan struct{}
	closed  atomic.Bool
}

func (f *fakeHTTPShutdown) Shutdown(ctx context.Context) error {
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeHTTPShutdown) Close() error { f.closed.Store(true); return nil }

func TestShutdownAllDrainsHTTPAndUsesCommonDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := &fakeHTTPShutdown{release: release}
	var bg sync.WaitGroup
	closed := atomic.Bool{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan shutdownResult, 1)
	go func() { done <- shutdownAll(ctx, srv, &bg, func() error { closed.Store(true); return nil }) }()
	select {
	case <-done:
		t.Fatal("shutdown did not drain request")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	r := <-done
	if r.http != nil || !closed.Load() {
		t.Fatalf("shutdown=%v closed=%v", r.http, closed.Load())
	}
	var stuck sync.WaitGroup
	stuck.Add(1)
	srv2 := &fakeHTTPShutdown{release: make(chan struct{})}
	dbClosed := atomic.Bool{}
	short, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	r = shutdownAll(short, srv2, &stuck, func() error { dbClosed.Store(true); return nil })
	for limit := time.Now().Add(time.Second); !dbClosed.Load() && time.Now().Before(limit); {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(r.http, context.DeadlineExceeded) || !dbClosed.Load() || !srv2.closed.Load() {
		t.Fatalf("deadline=%v dbclosed=%v httpclosed=%v", r.http, dbClosed.Load(), srv2.closed.Load())
	}
	stuck.Done()
}

func TestShutdownAllDeadlineDoesNotWaitForBlockedDatabaseClose(t *testing.T) {
	srv := &fakeHTTPShutdown{release: make(chan struct{})}
	var bg sync.WaitGroup
	block := make(chan struct{})
	called := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	r := shutdownAll(ctx, srv, &bg, func() error {
		select {
		case <-called:
		default:
			close(called)
		}
		<-block
		return nil
	})
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("deadline return took %v", elapsed)
	}
	if !errors.Is(r.http, context.DeadlineExceeded) {
		t.Fatalf("result=%v", r.http)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("database close was not attempted")
	}
	close(block)
}

func unixHTTPServer(t *testing.T, h http.Handler) (*http.Server, *http.Client, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "http.sock")
	ln, e := net.Listen("unix", path)
	if e != nil {
		if errors.Is(e, os.ErrPermission) || errors.Is(e, syscall.EPERM) {
			t.Skipf("sandbox disallows sockets: %v", e)
		}
		t.Fatal(e)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	client := &http.Client{Transport: transport}
	t.Cleanup(func() { transport.CloseIdleConnections(); _ = srv.Close() })
	return srv, client, "http://unix/test"
}

func TestShutdownAllDrainsRealHTTPRequestWithoutCancelingIt(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	requestCanceled := make(chan struct{}, 1)
	life, cancelLife := context.WithCancel(context.Background())
	srv, client, url := unixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			requestCanceled <- struct{}{}
			return
		case <-release:
			w.WriteHeader(204)
		}
	}))
	response := make(chan *http.Response, 1)
	requestErr := make(chan error, 1)
	go func() {
		r, e := client.Get(url)
		if e != nil {
			requestErr <- e
			return
		}
		response <- r
	}()
	<-entered
	cancelLife()
	if life.Err() == nil {
		t.Fatal("test lifecycle was not canceled")
	}
	var bg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan shutdownResult, 1)
	go func() { done <- shutdownAll(ctx, srv, &bg, func() error { return nil }) }()
	select {
	case <-requestCanceled:
		t.Fatal("background lifecycle cancellation reached active request")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-done:
		t.Fatal("shutdown returned before request drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case r := <-response:
		defer r.Body.Close()
		if r.StatusCode != 204 {
			t.Fatalf("status=%d", r.StatusCode)
		}
	case e := <-requestErr:
		t.Fatal(e)
	case <-time.After(time.Second):
		t.Fatal("request did not complete")
	}
	if r := <-done; r.http != nil {
		t.Fatal(r.http)
	}
}

func TestShutdownAllClosesRealConnectionAtDeadline(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	srv, client, url := unixHTTPServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-canceled }))
	requestDone := make(chan error, 1)
	go func() { _, e := client.Get(url); requestDone <- e }()
	<-entered
	var bg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r := shutdownAll(ctx, srv, &bg, func() error { return nil })
	if !errors.Is(r.http, context.DeadlineExceeded) {
		t.Fatalf("result=%v", r.http)
	}
	close(canceled)
	select {
	case e := <-requestDone:
		if e == nil {
			t.Fatal("request unexpectedly completed after forced close")
		}
	case <-time.After(time.Second):
		t.Fatal("forced close did not release client")
	}
}
