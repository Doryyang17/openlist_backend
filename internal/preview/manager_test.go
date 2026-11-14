package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func meta() Record {
	return Record{Event: Event{Kind: "meta", Duration: 100, Width: 240, Height: 138, Total: 101}}
}
func frame(n int) Record {
	return Record{Event: Event{Kind: "frame", Progress: n, Time: float64(n - 1)}, Image: []byte{1, 2, 3}}
}
func completed(ctx context.Context, _ string, start int, emit func(Record) error) error {
	if err := emit(meta()); err != nil {
		return err
	}
	for i := start + 1; i <= TotalSamples; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(frame(i)); err != nil {
			return err
		}
	}
	return emit(Record{Event: Event{Kind: "done"}})
}
func waitJob(t *testing.T, job *Job) ([]Record, bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		records, done, failed, changed := job.Snapshot(0)
		if done || failed {
			return records, failed
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatal("job did not finish")
		}
	}
}

func TestProtocolFragmentationAndLimits(t *testing.T) {
	var wire bytes.Buffer
	want := frame(1)
	if err := WriteRecord(&wire, want); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), wire.Bytes()...)
	got, err := ReadRecord(&oneByteReader{Reader: bytes.NewReader(raw)})
	if err != nil || got.Event != want.Event || !bytes.Equal(got.Image, want.Image) {
		t.Fatalf("roundtrip: %#v %v", got, err)
	}
	for _, data := range [][]byte{raw[:7], raw[:len(raw)-1]} {
		if _, err := ReadRecord(bytes.NewReader(data)); err == nil {
			t.Fatal("accepted truncated record")
		}
	}
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header, 1025)
	if _, err := ReadRecord(bytes.NewReader(header)); err == nil {
		t.Fatal("accepted excessive header")
	}
	binary.BigEndian.PutUint32(header, 1)
	binary.BigEndian.PutUint32(header[4:], MaxImageBytes+1)
	if _, err := ReadRecord(bytes.NewReader(header)); err == nil {
		t.Fatal("accepted excessive image")
	}
}

type oneByteReader struct{ io.Reader }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestCacheReuseAndFileVersionSeparation(t *testing.T) {
	var calls atomic.Int32
	m := NewManager(func(ctx context.Context, p string, n int, emit func(Record) error) error {
		calls.Add(1)
		return completed(ctx, p, n, emit)
	})
	j, release, err := m.Acquire("version-1", "source")
	if err != nil {
		t.Fatal(err)
	}
	records, failed := waitJob(t, j)
	if failed || len(records) != 102 {
		t.Fatalf("unexpected result %d %v", len(records), failed)
	}
	release()
	j2, r2, err := m.Acquire("version-1", "source")
	if err != nil {
		t.Fatal(err)
	}
	defer r2()
	waitJob(t, j2)
	if j2 != j || calls.Load() != 1 {
		t.Fatal("cache did not reuse completed job")
	}
	j3, r3, err := m.Acquire("version-2", "source")
	if err != nil {
		t.Fatal(err)
	}
	defer r3()
	waitJob(t, j3)
	if j3 == j || calls.Load() != 2 {
		t.Fatal("changed source reused stale images")
	}
}

func TestLeaseCancellationAndResumeSingleProcess(t *testing.T) {
	started := make(chan int, 4)
	var active, peak atomic.Int32
	m := NewManager(func(ctx context.Context, p string, start int, emit func(Record) error) error {
		a := active.Add(1)
		defer active.Add(-1)
		if a > peak.Load() {
			peak.Store(a)
		}
		if start == 0 {
			if err := emit(meta()); err != nil {
				return err
			}
			if err := emit(frame(1)); err != nil {
				return err
			}
			started <- start
			<-ctx.Done()
			return ctx.Err()
		}
		started <- start
		return completed(ctx, p, start, emit)
	})
	j, r1, _ := m.Acquire("one", "source")
	_, r2, _ := m.Acquire("one", "source")
	<-started
	r1()
	m.mu.Lock()
	refs := j.refs
	m.mu.Unlock()
	if refs != 1 {
		t.Fatal("lost shared lease")
	}
	r2()
	j2, r3, err := m.Acquire("one", "source")
	if err != nil {
		t.Fatal(err)
	}
	defer r3()
	_, failed := waitJob(t, j2)
	if failed || peak.Load() != 1 || (<-started) != 1 {
		t.Fatal("resume overlapped or restarted completed samples")
	}
}

func TestGlobalQueueAndCancellation(t *testing.T) {
	started := make(chan string, 8)
	m := NewManager(func(ctx context.Context, p string, _ int, _ func(Record) error) error {
		started <- p
		<-ctx.Done()
		return ctx.Err()
	})
	var releases []func()
	for _, key := range []string{"a", "b", "c", "d"} {
		_, r, e := m.Acquire(key, key)
		if e != nil {
			t.Fatal(e)
		}
		releases = append(releases, r)
	}
	<-started
	select {
	case <-started:
		t.Fatal("more than one process started")
	default:
	}
	if _, _, err := m.Acquire("e", "e"); !errors.Is(err, ErrBusy) {
		t.Fatal("unbounded queue")
	}
	for _, r := range releases {
		r()
	}
}

func TestBudgetEvictionAndIdleExpiration(t *testing.T) {
	m := NewManager(completed)
	m.maxBytes = 400
	m.ttl = 20 * time.Millisecond
	j, r, _ := m.Acquire("first", "source")
	_, failed := waitJob(t, j)
	if failed {
		t.Fatal("unexpected failure")
	}
	r()
	j2, r2, _ := m.Acquire("next", "source")
	_, failed = waitJob(t, j2)
	if failed {
		t.Fatal("did not evict idle cache")
	}
	r2()
	time.Sleep(40 * time.Millisecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) != 0 || m.bytes != 0 {
		t.Fatalf("idle cache not released: %d %d", len(m.jobs), m.bytes)
	}
}

func TestFailurePreservesCoarseFramesAndDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	m := NewManager(func(_ context.Context, _ string, _ int, emit func(Record) error) error {
		calls.Add(1)
		_ = emit(meta())
		for i := 1; i <= 21; i++ {
			_ = emit(frame(i))
		}
		return errors.New("read failed")
	})
	j, r, _ := m.Acquire("one", "source")
	records, failed := waitJob(t, j)
	defer r()
	if !failed || len(records) != 22 {
		t.Fatal("lost coarse preview")
	}
	j2, r2, _ := m.Acquire("one", "source")
	defer r2()
	waitJob(t, j2)
	if calls.Load() != 1 {
		t.Fatal("automatic failure retry")
	}
}
