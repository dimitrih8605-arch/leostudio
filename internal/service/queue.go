package service

import (
	"fmt"
	"sort"
	"sync"

	"github.com/hirotomasato/leostudio/internal/store"
)

// JobType / JobStatus enumerate the queue domain.
type JobType string
type JobStatus string

const (
	JobImage JobType = "image"
	JobVideo JobType = "video"

	StatusPending   JobStatus = "pending"
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
	StatusCanceled  JobStatus = "canceled"
)

// JobSpec is the input used to enqueue a job. Reference images are pre-uploaded
// init image ids (image references / video start frame), so every job carries
// its own references and they never get mixed up between jobs.
type JobSpec struct {
	Type        JobType
	Prompt      string
	ModelID     string // image modelId / video slug
	AspectRatio string
	Resolution  string // video only
	Duration    int    // video only
	Audio       bool   // video only
	Quantity    int    // image only
	RefImageIDs []string
}

// Job is the full in-memory state of a queued generation.
type Job struct {
	ID int64
	JobSpec
	Status       JobStatus
	ResultURLs   []string
	ThumbURLs    []string
	UsedCookieID int64
	GenerationID string
	Error        string
	CreatedAt    int64
	UpdatedAt    int64
}

// generator is the minimal surface the worker needs. *LeonardoPool satisfies it
// in production; tests pass a stub so they never hit Leonardo.
type generator interface {
	runImage(spec JobSpec) (urls, thumbs []string, cookieID int64, genID string, err error)
	runVideo(spec JobSpec) (urls, thumbs []string, cookieID int64, genID string, err error)
}

// QueueManager orchestrates background generation jobs with a bounded worker
// pool, backed by SQLite for persistence/resume.
type QueueManager struct {
	gen         generator
	store       *store.Store
	concurrency int

	mu       sync.Mutex
	jobs     map[int64]*Job
	pending  chan int64
	started  bool
	onChange func()
}

// DefaultQueueConcurrency is intentionally conservative to avoid Leonardo
// rate-limits and SQLite write contention.
const DefaultQueueConcurrency = 2

// NewQueueManager builds a manager. Pass concurrency <= 0 to use the default.
func NewQueueManager(pool *LeonardoPool, st *store.Store, concurrency int) *QueueManager {
	if concurrency <= 0 {
		concurrency = DefaultQueueConcurrency
	}
	return &QueueManager{
		gen:         &poolGenerator{pool: pool},
		store:       st,
		concurrency: concurrency,
		jobs:        make(map[int64]*Job),
		pending:     make(chan int64, 1024),
	}
}

// SetOnChange registers a callback fired whenever any job changes state. Used
// by the desktop layer to emit a Wails event.
func (q *QueueManager) SetOnChange(fn func()) {
	q.mu.Lock()
	q.onChange = fn
	q.mu.Unlock()
}

// Start launches the worker pool. Safe to call once.
func (q *QueueManager) Start() {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return
	}
	q.started = true
	q.mu.Unlock()

	for i := 0; i < q.concurrency; i++ {
		go q.worker()
	}
}

// Enqueue creates pending jobs from specs, persists them, and dispatches them.
// Returns the new job ids.
func (q *QueueManager) Enqueue(specs []JobSpec) ([]int64, error) {
	ids := make([]int64, 0, len(specs))
	for _, spec := range specs {
		row := specToRow(spec, StatusPending)
		id, err := q.store.AddQueueJob(row)
		if err != nil {
			return ids, err
		}
		job := &Job{ID: id, JobSpec: spec, Status: StatusPending}
		q.mu.Lock()
		q.jobs[id] = job
		q.mu.Unlock()
		ids = append(ids, id)
		q.dispatch(id)
	}
	q.notify()
	return ids, nil
}

// List returns a snapshot of all jobs, oldest first.
func (q *QueueManager) List() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, 0, len(q.jobs))
	for _, j := range q.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Cancel marks a pending job canceled. Running jobs cannot be canceled mid-
// flight (credits may already be spent); returns an error in that case.
func (q *QueueManager) Cancel(id int64) error {
	q.mu.Lock()
	job, ok := q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("queue: job %d not found", id)
	}
	if job.Status != StatusPending {
		st := job.Status
		q.mu.Unlock()
		return fmt.Errorf("queue: job %d cannot be canceled (status %s)", id, st)
	}
	job.Status = StatusCanceled
	q.persistLocked(job)
	q.mu.Unlock()
	q.notify()
	return nil
}

// Retry re-queues a failed or canceled job with its original spec.
func (q *QueueManager) Retry(id int64) error {
	q.mu.Lock()
	job, ok := q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("queue: job %d not found", id)
	}
	if job.Status != StatusFailed && job.Status != StatusCanceled {
		st := job.Status
		q.mu.Unlock()
		return fmt.Errorf("queue: job %d cannot be retried (status %s)", id, st)
	}
	job.Status = StatusPending
	job.Error = ""
	job.ResultURLs = nil
	job.ThumbURLs = nil
	q.persistLocked(job)
	q.mu.Unlock()
	q.dispatch(id)
	q.notify()
	return nil
}

// ClearFinished removes completed/failed/canceled jobs from memory and store.
func (q *QueueManager) ClearFinished() error {
	if _, err := q.store.DeleteFinishedQueueJobs(); err != nil {
		return err
	}
	q.mu.Lock()
	for id, j := range q.jobs {
		switch j.Status {
		case StatusCompleted, StatusFailed, StatusCanceled:
			delete(q.jobs, id)
		}
	}
	q.mu.Unlock()
	q.notify()
	return nil
}

// ResumeFromStore loads persisted jobs, flips any leftover "running" back to
// "pending" (unclean shutdown), and dispatches pending jobs.
func (q *QueueManager) ResumeFromStore() error {
	if _, err := q.store.RequeueRunningJobs(); err != nil {
		return err
	}
	rows, err := q.store.ListQueueJobs()
	if err != nil {
		return err
	}
	var toDispatch []int64
	q.mu.Lock()
	for _, r := range rows {
		job := rowToJob(r)
		q.jobs[job.ID] = job
		if job.Status == StatusPending {
			toDispatch = append(toDispatch, job.ID)
		}
	}
	q.mu.Unlock()
	for _, id := range toDispatch {
		q.dispatch(id)
	}
	q.notify()
	return nil
}

// ---- internals ----

// dispatch hands a job id to the worker pool without blocking the caller.
func (q *QueueManager) dispatch(id int64) {
	select {
	case q.pending <- id:
	default:
		// Buffer full (very large backlog): offload the blocking send so the
		// caller (a Wails binding) never stalls.
		go func() { q.pending <- id }()
	}
}

func (q *QueueManager) worker() {
	for id := range q.pending {
		q.process(id)
	}
}

func (q *QueueManager) process(id int64) {
	// Claim the job: only proceed if still pending (it may have been canceled).
	q.mu.Lock()
	job, ok := q.jobs[id]
	if !ok || job.Status != StatusPending {
		q.mu.Unlock()
		return
	}
	job.Status = StatusRunning
	q.persistLocked(job)
	spec := job.JobSpec
	q.mu.Unlock()
	q.notify()

	urls, thumbs, cookieID, genID, err := q.runSpec(spec)

	q.mu.Lock()
	// Job may have been cleared meanwhile; re-fetch.
	job, ok = q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return
	}
	if err != nil {
		job.Status = StatusFailed
		job.Error = err.Error()
	} else {
		job.Status = StatusCompleted
		job.ResultURLs = urls
		job.ThumbURLs = thumbs
		job.UsedCookieID = cookieID
		job.GenerationID = genID
	}
	q.persistLocked(job)
	q.mu.Unlock()
	q.notify()
}

// runSpec dispatches to the right generator and recovers from panics so a
// single bad job never kills a worker.
func (q *QueueManager) runSpec(spec JobSpec) (urls, thumbs []string, cookieID int64, genID string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("queue: job panicked: %v", r)
		}
	}()
	if spec.Type == JobVideo {
		return q.gen.runVideo(spec)
	}
	return q.gen.runImage(spec)
}

// persistLocked writes the current job state to the store. Caller holds q.mu.
func (q *QueueManager) persistLocked(job *Job) {
	row := specToRow(job.JobSpec, job.Status)
	row.ID = job.ID
	row.ResultURLs = job.ResultURLs
	row.ThumbURLs = job.ThumbURLs
	row.UsedCookieID = job.UsedCookieID
	row.GenerationID = job.GenerationID
	row.ErrorMessage = job.Error
	_ = q.store.UpdateQueueJob(row)
}

func (q *QueueManager) notify() {
	q.mu.Lock()
	fn := q.onChange
	q.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func specToRow(spec JobSpec, status JobStatus) store.QueueJobRow {
	audio := 0
	if spec.Audio {
		audio = 1
	}
	return store.QueueJobRow{
		Type:        string(spec.Type),
		Status:      string(status),
		Prompt:      spec.Prompt,
		ModelID:     spec.ModelID,
		AspectRatio: spec.AspectRatio,
		Resolution:  spec.Resolution,
		Duration:    spec.Duration,
		Audio:       audio,
		Quantity:    spec.Quantity,
		RefImageIDs: spec.RefImageIDs,
	}
}

func rowToJob(r store.QueueJobRow) *Job {
	return &Job{
		ID: r.ID,
		JobSpec: JobSpec{
			Type:        JobType(r.Type),
			Prompt:      r.Prompt,
			ModelID:     r.ModelID,
			AspectRatio: r.AspectRatio,
			Resolution:  r.Resolution,
			Duration:    r.Duration,
			Audio:       r.Audio == 1,
			Quantity:    r.Quantity,
			RefImageIDs: r.RefImageIDs,
		},
		Status:       JobStatus(r.Status),
		ResultURLs:   r.ResultURLs,
		ThumbURLs:    r.ThumbURLs,
		UsedCookieID: r.UsedCookieID,
		GenerationID: r.GenerationID,
		Error:        r.ErrorMessage,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

// poolGenerator adapts *LeonardoPool to the generator interface, mapping job
// specs onto the existing Generate / GenerateVideo pipelines.
type poolGenerator struct {
	pool *LeonardoPool
}

func (g *poolGenerator) runImage(spec JobSpec) (urls, thumbs []string, cookieID int64, genID string, err error) {
	res, err := g.pool.Generate(GenerateRequest{
		Prompt:            spec.Prompt,
		N:                 spec.Quantity,
		ModelID:           spec.ModelID,
		AspectRatio:       spec.AspectRatio,
		ReferenceImageIDs: spec.RefImageIDs,
	})
	if err != nil {
		return nil, nil, 0, "", err
	}
	out := make([]string, 0, len(res.Data))
	for _, d := range res.Data {
		out = append(out, d.URL)
	}
	return out, nil, res.Provider.UsedCookieID, res.Provider.GenerationID, nil
}

func (g *poolGenerator) runVideo(spec JobSpec) (urls, thumbs []string, cookieID int64, genID string, err error) {
	var startFrameID string
	if len(spec.RefImageIDs) > 0 {
		startFrameID = spec.RefImageIDs[0]
	}
	res, err := g.pool.GenerateVideo(VideoRequest{
		Prompt:      spec.Prompt,
		ModelSlug:   spec.ModelID,
		AspectRatio: spec.AspectRatio,
		Resolution:  spec.Resolution,
		Duration:    spec.Duration,
		Audio:       spec.Audio,
		ImageID:     startFrameID,
	})
	if err != nil {
		return nil, nil, 0, "", err
	}
	mp4s := make([]string, 0, len(res.Data))
	thumbsOut := make([]string, 0, len(res.Data))
	for _, d := range res.Data {
		mp4s = append(mp4s, d.MP4URL)
		if d.ThumbnailURL != "" {
			thumbsOut = append(thumbsOut, d.ThumbnailURL)
		}
	}
	return mp4s, thumbsOut, res.Provider.UsedCookieID, res.Provider.GenerationID, nil
}
