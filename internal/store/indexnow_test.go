package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIndexNowQueueCoalescesAndRetries(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	url := "https://example.com/en/posts/one/"
	if err := st.EnqueueIndexNow(url, "publish", now); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueIndexNow(url, "update", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if count, _ := st.IndexNowQueueCount(); count != 1 {
		t.Fatalf("queue count=%d want 1", count)
	}
	if due, err := st.DueIndexNow(now, 10); err != nil || len(due) != 0 {
		t.Fatalf("early due=%d err=%v", len(due), err)
	}
	due, err := st.DueIndexNow(now.Add(2*time.Minute), 10)
	if err != nil || len(due) != 1 || due[0].Reason != "update" {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	if err := st.RetryIndexNow([]string{url}, now.Add(3*time.Minute), "429"); err != nil {
		t.Fatal(err)
	}
	due, err = st.DueIndexNow(now.Add(4*time.Minute), 10)
	if err != nil || len(due) != 1 || due[0].Attempts != 1 || due[0].LastError != "429" {
		t.Fatalf("retried=%+v err=%v", due, err)
	}
	if err := st.DeleteIndexNow([]string{url}); err != nil {
		t.Fatal(err)
	}
	if count, _ := st.IndexNowQueueCount(); count != 0 {
		t.Fatalf("queue count after delete=%d", count)
	}
	if err := st.RecordIndexNowSubmissions(due, 200, true, ""); err != nil {
		t.Fatal(err)
	}
	history, err := st.ListIndexNowSubmissions(10)
	if err != nil || len(history) != 1 || history[0].URL != url || !history[0].Success {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}
