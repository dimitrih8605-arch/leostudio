package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestQueueJobRoundTrip(t *testing.T) {
	st := openTestStore(t)

	id, err := st.AddQueueJob(QueueJobRow{
		Type:        "image",
		Status:      "pending",
		Prompt:      "a cat",
		ModelID:     "model-1",
		AspectRatio: "1:1",
		Quantity:    2,
		RefImageIDs: []string{"ref-a", "ref-b"},
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	jobs, err := st.ListQueueJobs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	got := jobs[0]
	if got.Prompt != "a cat" || got.ModelID != "model-1" || got.Quantity != 2 {
		t.Fatalf("unexpected fields: %+v", got)
	}
	if len(got.RefImageIDs) != 2 || got.RefImageIDs[0] != "ref-a" || got.RefImageIDs[1] != "ref-b" {
		t.Fatalf("ref image ids not round-tripped: %+v", got.RefImageIDs)
	}

	// Update → completed with results.
	got.Status = "completed"
	got.ResultURLs = []string{"https://x/1.jpg"}
	got.UsedCookieID = 7
	got.GenerationID = "gen-123"
	if err := st.UpdateQueueJob(got); err != nil {
		t.Fatalf("update: %v", err)
	}

	jobs, _ = st.ListQueueJobs()
	got = jobs[0]
	if got.Status != "completed" {
		t.Fatalf("status not updated: %s", got.Status)
	}
	if len(got.ResultURLs) != 1 || got.ResultURLs[0] != "https://x/1.jpg" {
		t.Fatalf("result urls not updated: %+v", got.ResultURLs)
	}
	if got.UsedCookieID != 7 || got.GenerationID != "gen-123" {
		t.Fatalf("cookie/gen not updated: %+v", got)
	}

	// Delete.
	if err := st.DeleteQueueJob(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	jobs, _ = st.ListQueueJobs()
	if len(jobs) != 0 {
		t.Fatalf("want 0 after delete, got %d", len(jobs))
	}
}

func TestDeleteFinishedQueueJobs(t *testing.T) {
	st := openTestStore(t)

	statuses := []string{"pending", "running", "completed", "failed", "canceled"}
	for _, s := range statuses {
		if _, err := st.AddQueueJob(QueueJobRow{Type: "image", Status: s, Prompt: "p"}); err != nil {
			t.Fatalf("add %s: %v", s, err)
		}
	}

	deleted, err := st.DeleteFinishedQueueJobs()
	if err != nil {
		t.Fatalf("delete finished: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("want 3 deleted (completed/failed/canceled), got %d", deleted)
	}

	jobs, _ := st.ListQueueJobs()
	if len(jobs) != 2 {
		t.Fatalf("want 2 remaining, got %d", len(jobs))
	}
	for _, j := range jobs {
		if j.Status != "pending" && j.Status != "running" {
			t.Fatalf("unexpected surviving status: %s", j.Status)
		}
	}
}

func TestRequeueRunningJobs(t *testing.T) {
	st := openTestStore(t)

	_, _ = st.AddQueueJob(QueueJobRow{Type: "image", Status: "running", Prompt: "a"})
	_, _ = st.AddQueueJob(QueueJobRow{Type: "video", Status: "running", Prompt: "b"})
	_, _ = st.AddQueueJob(QueueJobRow{Type: "image", Status: "pending", Prompt: "c"})
	_, _ = st.AddQueueJob(QueueJobRow{Type: "image", Status: "completed", Prompt: "d"})

	affected, err := st.RequeueRunningJobs()
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if affected != 2 {
		t.Fatalf("want 2 requeued, got %d", affected)
	}

	jobs, _ := st.ListQueueJobs()
	running := 0
	pending := 0
	completed := 0
	for _, j := range jobs {
		switch j.Status {
		case "running":
			running++
		case "pending":
			pending++
		case "completed":
			completed++
		}
	}
	if running != 0 {
		t.Fatalf("expected no running jobs after requeue, got %d", running)
	}
	if pending != 3 {
		t.Fatalf("expected 3 pending (2 requeued + 1 original), got %d", pending)
	}
	if completed != 1 {
		t.Fatalf("expected completed untouched, got %d", completed)
	}
}
