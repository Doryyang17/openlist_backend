package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

var ErrBusy = errors.New("preview queue is full")

// Runner owns exactly one process, and returns only after it has been reaped.
type Runner func(context.Context, string, int, func(Record) error) error

type Manager struct {
	mu       sync.Mutex
	jobs     map[string]*Job
	slot     chan struct{}
	runner   Runner
	bytes    int
	maxBytes int
	ttl      time.Duration
}

type Job struct {
	manager  *Manager
	key      string
	path     string
	records  []Record
	seen     map[float64]bool
	bytes    int
	progress int
	refs     int
	active   bool
	done     bool
	failed   bool
	cancel   context.CancelFunc
	notify   chan struct{}
	touched  time.Time
	used     time.Duration
}

func NewManager(runner Runner) *Manager {
	return &Manager{jobs: make(map[string]*Job), slot: make(chan struct{}, 1),
		runner: runner, maxBytes: MaxCacheBytes, ttl: 10 * time.Minute}
}

var Default = NewManager(RunNative)

// Acquire must be called only after checking access and resolving a local file.
// The key includes storage, canonical path, file size and nanosecond mtime.
func (m *Manager) Acquire(key, path string) (*Job, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	busy := 0
	for _, job := range m.jobs {
		if job.active || job.refs > 0 {
			busy++
		}
	}
	if job := m.jobs[key]; job != nil {
		if job.refs >= 8 || (job.refs == 0 && !job.active && busy >= 4) {
			return nil, nil, ErrBusy
		}
		job.refs++
		job.touched = time.Now()
		m.startLocked(job)
		return job, m.releaseOnce(job), nil
	}
	if busy >= 4 {
		return nil, nil, ErrBusy
	}
	if len(m.jobs) >= 128 && !m.evictLocked(nil) {
		return nil, nil, ErrBusy
	}
	job := &Job{manager: m, key: key, path: path, refs: 1, seen: make(map[float64]bool),
		notify: make(chan struct{}), touched: time.Now()}
	m.jobs[key] = job
	m.startLocked(job)
	return job, m.releaseOnce(job), nil
}

func (m *Manager) releaseOnce(job *Job) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			job.refs--
			job.touched = time.Now()
			if job.refs == 0 && job.cancel != nil {
				job.cancel()
			}
			// Failure is not retried in the background. A later explicit reopen
			// gets a fresh attempt after every current subscriber has left.
			if job.refs == 0 && job.failed && !job.active {
				m.bytes -= job.bytes
				delete(m.jobs, job.key)
			}
			// Expiration is scheduled even if there are no future requests.
			time.AfterFunc(m.ttl, func() { m.mu.Lock(); defer m.mu.Unlock(); m.pruneLocked() })
		})
	}
}

func (m *Manager) wakeLocked(job *Job) {
	close(job.notify)
	job.notify = make(chan struct{})
}

func (m *Manager) startLocked(job *Job) {
	if job.active || job.done || job.failed || job.refs == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.active, job.cancel = true, cancel
	go m.run(ctx, job)
}

func (m *Manager) run(ctx context.Context, job *Job) {
	select {
	case m.slot <- struct{}{}:
	case <-ctx.Done():
		m.finish(job, ctx.Err(), 0)
		return
	}
	m.mu.Lock()
	startAt, remaining := job.progress, 30*time.Second-job.used
	m.mu.Unlock()
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, remaining)
	err := m.runner(runCtx, job.path, startAt, func(record Record) error {
		if err := runCtx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if record.Kind == "meta" {
			if len(job.records) > 0 {
				if job.records[0].Event != record.Event {
					return errors.New("preview source changed")
				}
				return nil
			}
		} else if len(job.records) == 0 {
			return errors.New("missing preview metadata")
		}
		if record.Kind == "frame" || record.Kind == "progress" {
			if record.Progress != job.progress+1 {
				return errors.New("invalid preview sequence")
			}
			if record.Time > job.records[0].Duration+1 {
				return errors.New("invalid frame time")
			}
			if record.Kind == "frame" && job.seen[record.Time] {
				record.Kind, record.Image = "progress", nil
			}
			if len(record.Image) > 0 {
				job.seen[record.Time] = true
			}
		} else if record.Kind == "done" {
			if job.progress != TotalSamples || len(job.seen) == 0 {
				return errors.New("incomplete preview")
			}
			// Publish done only after the process exits successfully.
			return nil
		} else if record.Kind != "meta" {
			return errors.New("unexpected preview event")
		}
		added := len(record.Image)
		if job.bytes+added > MaxVideoBytes {
			return errors.New("preview video budget exceeded")
		}
		for m.bytes+added > m.maxBytes {
			if !m.evictLocked(job) {
				return errors.New("preview cache budget exceeded")
			}
		}
		job.bytes += added
		m.bytes += added
		job.records = append(job.records, record)
		if record.Progress > 0 {
			job.progress = record.Progress
		}
		m.wakeLocked(job)
		return nil
	})
	if err == nil && runCtx.Err() != nil {
		err = runCtx.Err()
	}
	cancel()
	<-m.slot
	m.finish(job, err, time.Since(started))
}

func (m *Manager) finish(job *Job, err error, elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job.used += elapsed
	job.active, job.cancel = false, nil
	if err == nil && job.progress == TotalSamples {
		job.done = true
	} else if !errors.Is(err, context.Canceled) {
		job.failed = true
	}
	m.wakeLocked(job)
	// A new lease can arrive while the previous canceled process is exiting.
	// Restart only after that process has been reaped and relinquished its slot.
	m.startLocked(job)
}

func (j *Job) Snapshot(cursor int) ([]Record, bool, bool, <-chan struct{}) {
	m := j.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if cursor < 0 || cursor > len(j.records) {
		cursor = len(j.records)
	}
	return append([]Record(nil), j.records[cursor:]...), j.done, j.failed, j.notify
}

func (m *Manager) pruneLocked() {
	for key, job := range m.jobs {
		if job.refs == 0 && !job.active && time.Since(job.touched) >= m.ttl {
			m.bytes -= job.bytes
			delete(m.jobs, key)
		}
	}
}

func (m *Manager) evictLocked(exclude *Job) bool {
	var oldest *Job
	for _, job := range m.jobs {
		if job == exclude || job.refs > 0 || job.active {
			continue
		}
		if oldest == nil || job.touched.Before(oldest.touched) {
			oldest = job
		}
	}
	if oldest == nil {
		return false
	}
	m.bytes -= oldest.bytes
	delete(m.jobs, oldest.key)
	return true
}

func HelperPath() string {
	if path := os.Getenv("OPENLIST_PREVIEW_HELPER"); path != "" {
		return path
	}
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "openlist-preview")
}

func RunNative(ctx context.Context, path string, start int, emit func(Record) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if start == TotalSamples {
		return nil
	}
	cmd := exec.CommandContext(ctx, HelperPath(), path, strconv.Itoa(start))
	cmd.WaitDelay = time.Second
	// Decoder diagnostics may include private paths. They are not sent to clients
	// or written to service logs, and cannot grow an unbounded stderr buffer.
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	complete := false
	for {
		record, readErr := ReadRecord(stdout)
		if readErr != nil {
			if readErr != io.EOF {
				err = readErr
			}
			break
		}
		if err = emit(record); err != nil {
			break
		}
		if record.Kind == "done" {
			complete = true
			break
		}
	}
	if err != nil || !complete {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("preview worker ended without completion")
	}
	return waitErr
}
