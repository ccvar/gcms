package store

import (
	"database/sql"
	"time"
)

// IndexNowQueueItem is one durable URL notification waiting to be delivered.
// Reason is diagnostic only: the IndexNow protocol uses the same payload for
// additions, updates, redirects and removals.
type IndexNowQueueItem struct {
	URL         string
	Reason      string
	Attempts    int
	AvailableAt time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type IndexNowSubmission struct {
	ID          int64
	URL         string
	Reason      string
	StatusCode  int
	Success     bool
	Error       string
	SubmittedAt time.Time
}

// EnqueueIndexNow coalesces repeated changes to the same URL. A fresh content
// change resets the retry counter and moves delivery to the requested time.
func (s *Store) EnqueueIndexNow(url, reason string, availableAt time.Time) error {
	if availableAt.IsZero() {
		availableAt = time.Now()
	}
	now := time.Now()
	_, err := s.db.Exec(`
		INSERT INTO indexnow_queue(url,reason,attempts,available_at,last_error,created_at,updated_at)
		VALUES(?,?,0,?,'',?,?)
		ON CONFLICT(url) DO UPDATE SET
			reason=excluded.reason,
			attempts=0,
			available_at=excluded.available_at,
			last_error='',
			updated_at=excluded.updated_at`,
		url, reason, fmtTime(availableAt), fmtTime(now), fmtTime(now))
	return err
}

// DueIndexNow returns a bounded due batch without removing it. Delivery is
// serialized by the web layer; rows remain durable across restarts.
func (s *Store) DueIndexNow(now time.Time, limit int) ([]*IndexNowQueueItem, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := s.db.Query(`
		SELECT url,reason,attempts,available_at,last_error,created_at,updated_at
		FROM indexnow_queue
		WHERE available_at<=?
		ORDER BY available_at,updated_at
		LIMIT ?`, fmtTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IndexNowQueueItem
	for rows.Next() {
		var item IndexNowQueueItem
		var available, created, updated sql.NullString
		if err := rows.Scan(&item.URL, &item.Reason, &item.Attempts, &available, &item.LastError, &created, &updated); err != nil {
			return nil, err
		}
		item.AvailableAt = parseTime(available)
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		out = append(out, &item)
	}
	return out, rows.Err()
}

func (s *Store) DeleteIndexNow(urls []string) error {
	if len(urls) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`DELETE FROM indexnow_queue WHERE url=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, url := range urls {
		if _, err := stmt.Exec(url); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RetryIndexNow(urls []string, availableAt time.Time, message string) error {
	if len(urls) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		UPDATE indexnow_queue
		SET attempts=attempts+1,available_at=?,last_error=?,updated_at=?
		WHERE url=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now()
	for _, url := range urls {
		if _, err := stmt.Exec(fmtTime(availableAt), message, fmtTime(now), url); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) IndexNowQueueCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM indexnow_queue`).Scan(&count)
	return count, err
}

func (s *Store) RecordIndexNowSubmissions(items []*IndexNowQueueItem, statusCode int, success bool, message string) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO indexnow_submissions(url,reason,status_code,success,error,submitted_at)
		VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := fmtTime(time.Now())
	for _, item := range items {
		if item == nil {
			continue
		}
		if _, err := stmt.Exec(item.URL, item.Reason, statusCode, success, message, now); err != nil {
			return err
		}
	}
	// Submission history is operational evidence, not an unbounded audit log.
	if _, err := tx.Exec(`DELETE FROM indexnow_submissions WHERE id NOT IN (
		SELECT id FROM indexnow_submissions ORDER BY id DESC LIMIT 1000
	)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListIndexNowSubmissions(limit int) ([]*IndexNowSubmission, error) {
	if limit < 1 || limit > 1000 {
		limit = 100

	}
	rows, err := s.db.Query(`
		SELECT id,url,reason,status_code,success,error,submitted_at
		FROM indexnow_submissions
		ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IndexNowSubmission
	for rows.Next() {
		var item IndexNowSubmission
		var success int
		var submitted sql.NullString
		if err := rows.Scan(&item.ID, &item.URL, &item.Reason, &item.StatusCode, &success, &item.Error, &submitted); err != nil {
			return nil, err
		}
		item.Success = success != 0
		item.SubmittedAt = parseTime(submitted)
		out = append(out, &item)
	}
	return out, rows.Err()
}
