package service

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hirotomasato/leostudio/internal/store"
)

// stubGenerator is a controllable generator for tests. It records peak
// concurrency and can fail jobs whose prompt is listed in failPrompts.
type stubGenerator struct {
	mu          sync.Mutex
	active      int
	peak        int
	calls       int32
	delay       time.Duration
	failPrompts map[string]bool
	gate        chan struct{} // if non-nil, each call waits for a token
}

func (s *stubGenerator) run(spec JobSpec) (urls, thumbs []string, cookieID int64, genID string, err error) {
	atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	s.active++
	if s.active > s.peak {
		s.peak = s.active
	}
	s.mu.Unlock()

	if s.gate != nil {
		<-s.gate
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}

	s.mu.Lock()
	s.active--
	s.mu.Unlock()

	if s.failPrompts[spec.Prompt] {
		return nil, nil, 0, "", fmt.Errorf("stub forced failure for %q", spec.Prompt)
	}
	return []string{"http://x/" + spec.Prompt + ".jpg"}, nil, 1, "gen-" + spec.Prompt, nil
}

func (s *stubGenerator) runImage(spec JobSpec) ([]string, []string, int64, string, error) {
	return s.run(spec)
}
func (s *stubGenerator) runVideo(spec JobSpec) ([]string, []string, int64, string, error) {
	return s.run(spec)
}

func newTestQueue(t *testing.T, gen generator, concurrency int) *QueueManager {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "queue.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if concurrency <= 0 {
		concurrency = DefaultQueueConcurrency
	}
	return &QueueManager{
		gen:         gen,
		store:       st,
		concurrency: concurrency,
		jobs:        make(map[int64]*Job),
		pending:     make(chan int64, 1024),
	}
}

// waitFor polls until cond() is true or the deadline elapses.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func countStatus(q *QueueManager, status JobStatus) int {
	n := 0
	for _, j := range q.List() {
		if j.Status == status {
			n++
		}
	}
	return n
}

func TestEnqueueProcessesAllJobs(t *testing.T) {
	gen := &stubGenerator{failPrompts: map[string]bool{}}
	q := newTestQueue(t, gen, 2)
	q.Start()

	specs := []JobSpec{
		{Type: JobImage, Prompt: "a", Quantity: 1},
		{Type: JobImage, Prompt: "b", Quantity: 1},
		{Type: JobVideo, Prompt: "c"},
	}
	if _, err := q.Enqueue(specs); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	waitFor(t, 3*time.Second, func() bool { return countStatus(q, StatusCompleted) == 3 })
	if got := atomic.LoadInt32(&gen.calls); got != 3 {
		t.Fatalf("expected 3 generator calls, got %d", got)
	}
}

// Property 1: running count never exceeds concurrency.
func TestConcurrencyLimitRespected(t *testing.T) {
	gate := make(chan struct{})
	gen := &stubGenerator{failPrompts: map[string]bool{}, gate: gate}
	q := newTestQueue(t, gen, 2)
	q.Start()

	specs := make([]JobSpec, 6)
	for i := range specs {
		specs[i] = JobSpec{Type: JobImage, Prompt: fmt.Sprintf("p%d", i), Quantity: 1}
	}
	if _, err := q.Enqueue(specs); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Let all jobs flow through, releasing one gate token at a time.
	released := 0
	for released < len(specs) {
		select {
		case gate <- struct{}{}:
			released++
		case <-time.After(2 * time.Second):
			t.Fatalf("worker did not pick up job (released %d)", released)
		}
	}

	waitFor(t, 3*time.Second, func() bool { return countStatus(q, StatusCompleted) == len(specs) })

	gen.mu.Lock()
	peak := gen.peak
	gen.mu.Unlock()
	if peak > 2 {
		t.Fatalf("peak concurrency %d exceeded limit 2", peak)
	}
}

// Property 2: one failing job does not stop the others.
func TestFailureIsolation(t *testing.T) {
	gen := &stubGenerator{failPrompts: map[string]bool{"bad": true}}
	q := newTestQueue(t, gen, 2)
	q.Start()

	_, err := q.Enqueue([]JobSpec{
		{Type: JobImage, Prompt: "ok1", Quantity: 1},
		{Type: JobImage, Prompt: "bad", Quantity: 1},
		{Type: JobImage, Prompt: "ok2", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	waitFor(t, 3*time.Second, func() bool {
		return countStatus(q, StatusCompleted)+countStatus(q, StatusFailed) == 3
	})
	if c := countStatus(q, StatusCompleted); c != 2 {
		t.Fatalf("want 2 completed, got %d", c)
	}
	if f := countStatus(q, StatusFailed); f != 1 {
		t.Fatalf("want 1 failed, got %d", f)
	}
}

// Property 5 + 7: cancel only pending; clear only finished.
func TestCancelAndClear(t *testing.T) {
	gate := make(chan struct{})
	gen := &stubGenerator{failPrompts: map[string]bool{}, gate: gate}
	q := newTestQueue(t, gen, 1) // single worker so others stay pending
	q.Start()

	ids, err := q.Enqueue([]JobSpec{
		{Type: JobImage, Prompt: "first", Quantity: 1},
		{Type: JobImage, Prompt: "second", Quantity: 1},
		{Type: JobImage, Prompt: "third", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait until the first job is running (occupying the single worker).
	waitFor(t, 2*time.Second, func() bool { return countStatus(q, StatusRunning) == 1 })

	// Cancel a still-pending job (the last one).
	if err := q.Cancel(ids[2]); err != nil {
		t.Fatalf("cancel pending: %v", err)
	}
	// Canceling the running job must fail.
	if err := q.Cancel(ids[0]); err == nil {
		t.Fatal("expected error canceling a running job")
	}

	// Release everything so the rest finishes.
	go func() {
		for i := 0; i < 3; i++ {
			gate <- struct{}{}
		}
	}()

	waitFor(t, 3*time.Second, func() bool {
		return countStatus(q, StatusPending)+countStatus(q, StatusRunning) == 0
	})

	// One canceled, two completed.
	if c := countStatus(q, StatusCanceled); c != 1 {
		t.Fatalf("want 1 canceled, got %d", c)
	}
	if c := countStatus(q, StatusCompleted); c != 2 {
		t.Fatalf("want 2 completed, got %d", c)
	}

	// Clear finished removes all three (2 completed + 1 canceled).
	if err := q.ClearFinished(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n := len(q.List()); n != 0 {
		t.Fatalf("want 0 after clear, got %d", n)
	}
}

// Property 4: retry moves failed -> pending -> completed.
func TestRetryFailedJob(t *testing.T) {
	gen := &stubGenerator{failPrompts: map[string]bool{"flaky": true}}
	q := newTestQueue(t, gen, 1)
	q.Start()

	ids, err := q.Enqueue([]JobSpec{{Type: JobImage, Prompt: "flaky", Quantity: 1}})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return countStatus(q, StatusFailed) == 1 })

	// Make the next attempt succeed, then retry.
	gen.mu.Lock()
	gen.failPrompts = map[string]bool{}
	gen.mu.Unlock()

	if err := q.Retry(ids[0]); err != nil {
		t.Fatalf("retry: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return countStatus(q, StatusCompleted) == 1 })

	// Retrying a completed job must fail.
	if err := q.Retry(ids[0]); err == nil {
		t.Fatal("expected error retrying a completed job")
	}
}

// Property 6: resume requeues running jobs and dispatches pending ones.
func TestResumeFromStore(t *testing.T) {
	gen := &stubGenerator{failPrompts: map[string]bool{}}
	q := newTestQueue(t, gen, 2)

	// Seed the store directly to simulate a previous session left mid-flight.
	_, _ = q.store.AddQueueJob(store.QueueJobRow{Type: "image", Status: "running", Prompt: "leftover"})
	_, _ = q.store.AddQueueJob(store.QueueJobRow{Type: "image", Status: "pending", Prompt: "waiting"})
	_, _ = q.store.AddQueueJob(store.QueueJobRow{Type: "image", Status: "completed", Prompt: "done"})

	q.Start()
	if err := q.ResumeFromStore(); err != nil {
		t.Fatalf("resume: %v", err)
	}

	// The leftover running + the pending one both get processed to completion;
	// the already-completed one stays completed.
	waitFor(t, 3*time.Second, func() bool { return countStatus(q, StatusCompleted) == 3 })
}
