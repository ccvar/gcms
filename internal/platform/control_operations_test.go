package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func controlOperationTestStore(t *testing.T) (*Store, int64) {
	t.Helper()
	ps, err := Open(filepath.Join(t.TempDir(), "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	token := "gcmsp_receipt_test_token"
	keyID, err := ps.CreatePlatformKey("receipt-test", token, token[:13], KeyMembershipAll, "sites:create", nil, time.Time{})
	if err != nil {
		t.Fatalf("create platform key: %v", err)
	}
	return ps, keyID
}

func testControlHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestControlOperationReceiptLifecycle(t *testing.T) {
	ps, keyID := controlOperationTestStore(t)
	requestHash := testControlHash(`{"slug":"news"}`)
	receipt, reservation, err := ps.ReserveControlOperation(keyID, "sites.create", "create-news-1", requestHash)
	if err != nil || reservation != ControlOperationReserved {
		t.Fatalf("reserve = %#v %q %v", receipt, reservation, err)
	}
	if receipt.RequestHash != requestHash || receipt.State != ControlOperationRunning {
		t.Fatalf("initial receipt = %#v", receipt)
	}

	response := []byte(`{"id":42,"slug":"news"}`)
	if err := ps.CompleteControlOperation(keyID, "sites.create", "create-news-1", requestHash, 201, response); err != nil {
		t.Fatalf("complete: %v", err)
	}
	replayed, reservation, err := ps.ReserveControlOperation(keyID, "sites.create", "create-news-1", requestHash)
	if err != nil || reservation != ControlOperationReplay {
		t.Fatalf("replay = %#v %q %v", replayed, reservation, err)
	}
	if replayed.HTTPStatus != 201 || string(replayed.ResponseJSON) != string(response) || replayed.CompletedAt.IsZero() {
		t.Fatalf("completed receipt = %#v", replayed)
	}

	conflict, reservation, err := ps.ReserveControlOperation(keyID, "sites.create", "create-news-1", testControlHash(`{"slug":"other"}`))
	if err != nil || reservation != ControlOperationConflict || conflict.RequestHash != requestHash {
		t.Fatalf("conflict = %#v %q %v", conflict, reservation, err)
	}

	var storedHash string
	if err := ps.db.QueryRow(`SELECT request_hash FROM platform_control_operation_receipts
		WHERE key_id=? AND operation=? AND idempotency_key=?`, keyID, "sites.create", "create-news-1").Scan(&storedHash); err != nil {
		t.Fatalf("read stored hash: %v", err)
	}
	if storedHash != requestHash || storedHash == `{"slug":"news"}` {
		t.Fatalf("stored request value = %q, want only SHA-256", storedHash)
	}
}

func TestControlOperationReservationReleaseAllowsRetry(t *testing.T) {
	ps, keyID := controlOperationTestStore(t)
	hash := testControlHash("retryable-request")
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "retry-1", hash); err != nil || got != ControlOperationReserved {
		t.Fatalf("first reserve = %q %v", got, err)
	}
	if err := ps.ReleaseControlOperationReservation(keyID, "sites.create", "retry-1", hash); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "retry-1", hash); err != nil || got != ControlOperationReserved {
		t.Fatalf("reserve after release = %q %v", got, err)
	}
}

func TestControlOperationReservationIsAtomic(t *testing.T) {
	ps, keyID := controlOperationTestStore(t)
	hash := testControlHash("concurrent-request")
	const workers = 16
	start := make(chan struct{})
	results := make(chan ControlOperationReservation, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, result, err := ps.ReserveControlOperation(keyID, "sites.create", "concurrent-1", hash)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent reserve: %v", err)
	}
	reserved := 0
	inProgress := 0
	for result := range results {
		switch result {
		case ControlOperationReserved:
			reserved++
		case ControlOperationInProgress:
			inProgress++
		default:
			t.Errorf("unexpected reservation result %q", result)
		}
	}
	if reserved != 1 || inProgress != workers-1 {
		t.Fatalf("reserved=%d in_progress=%d, want 1/%d", reserved, inProgress, workers-1)
	}
}

func TestControlOperationStaleLeaseTakeover(t *testing.T) {
	ps, keyID := controlOperationTestStore(t)
	oldHash := testControlHash("abandoned-request")
	newHash := testControlHash("replacement-request")
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "stale-lease-1", oldHash); err != nil || got != ControlOperationReserved {
		t.Fatalf("initial reserve = %q %v", got, err)
	}

	// 未过租约：同请求仍在执行，不同请求仍冲突。
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "stale-lease-1", oldHash); err != nil || got != ControlOperationInProgress {
		t.Fatalf("fresh same request = %q %v", got, err)
	}
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "stale-lease-1", newHash); err != nil || got != ControlOperationConflict {
		t.Fatalf("fresh different request = %q %v", got, err)
	}

	staleAt := time.Now().Add(-ControlOperationLeaseTTL - time.Minute)
	if _, err := ps.db.Exec(`UPDATE platform_control_operation_receipts
		SET updated_at=?,http_status=202,response_json='{"partial":true}'
		WHERE key_id=? AND operation=? AND idempotency_key=?`,
		fmtTime(staleAt), keyID, "sites.create", "stale-lease-1"); err != nil {
		t.Fatalf("age running receipt: %v", err)
	}
	// 过期后不同请求仍然冲突；只有原请求可以续租接管。
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "stale-lease-1", newHash); err != nil || got != ControlOperationConflict {
		t.Fatalf("stale different request = %q %v", got, err)
	}
	taken, got, err := ps.ReserveControlOperation(keyID, "sites.create", "stale-lease-1", oldHash)
	if err != nil || got != ControlOperationReserved {
		t.Fatalf("stale takeover = %#v %q %v", taken, got, err)
	}
	if taken.RequestHash != oldHash || taken.State != ControlOperationRunning || taken.HTTPStatus != 0 || len(taken.ResponseJSON) != 0 || !taken.CompletedAt.IsZero() {
		t.Fatalf("takeover did not reset receipt = %#v", taken)
	}
	if !taken.UpdatedAt.After(staleAt) {
		t.Fatalf("takeover did not refresh lease: updated=%v stale=%v", taken.UpdatedAt, staleAt)
	}
	// 接管会刷新租约；同请求继续看到处理中。
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "stale-lease-1", oldHash); err != nil || got != ControlOperationInProgress {
		t.Fatalf("same request after takeover = %q %v", got, err)
	}
}

func TestControlOperationStaleTakeoverIsAtomic(t *testing.T) {
	ps, keyID := controlOperationTestStore(t)
	oldHash := testControlHash("crashed-owner")
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "stale-race-1", oldHash); err != nil || got != ControlOperationReserved {
		t.Fatalf("initial reserve = %q %v", got, err)
	}
	if _, err := ps.db.Exec(`UPDATE platform_control_operation_receipts SET updated_at=?
		WHERE key_id=? AND operation=? AND idempotency_key=?`,
		fmtTime(time.Now().Add(-ControlOperationLeaseTTL-time.Minute)), keyID, "sites.create", "stale-race-1"); err != nil {
		t.Fatalf("age receipt: %v", err)
	}

	const workers = 12
	start := make(chan struct{})
	results := make(chan ControlOperationReservation, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, result, err := ps.ReserveControlOperation(keyID, "sites.create", "stale-race-1", oldHash)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("stale takeover: %v", err)
	}
	reserved := 0
	inProgress := 0
	for result := range results {
		switch result {
		case ControlOperationReserved:
			reserved++
		case ControlOperationInProgress:
			inProgress++
		default:
			t.Errorf("unexpected takeover result %q", result)
		}
	}
	if reserved != 1 || inProgress != workers-1 {
		t.Fatalf("reserved=%d in_progress=%d, want 1/%d", reserved, inProgress, workers-1)
	}
}

func TestCompletedControlOperationCannotBeTakenOver(t *testing.T) {
	ps, keyID := controlOperationTestStore(t)
	hash := testControlHash("completed-owner")
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "completed-stale-1", hash); err != nil || got != ControlOperationReserved {
		t.Fatalf("reserve = %q %v", got, err)
	}
	if err := ps.CompleteControlOperation(keyID, "sites.create", "completed-stale-1", hash, 201, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := ps.db.Exec(`UPDATE platform_control_operation_receipts SET updated_at=?
		WHERE key_id=? AND operation=? AND idempotency_key=?`,
		fmtTime(time.Now().Add(-ControlOperationLeaseTTL-time.Hour)), keyID, "sites.create", "completed-stale-1"); err != nil {
		t.Fatalf("age completed receipt: %v", err)
	}
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "completed-stale-1", testControlHash("different")); err != nil || got != ControlOperationConflict {
		t.Fatalf("completed takeover = %q %v", got, err)
	}
}

func TestOpenRecreatesControlOperationReceiptTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-system.db")
	ps, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	token := "gcmsp_legacy_receipt"
	keyID, err := ps.CreatePlatformKey("legacy", token, token[:13], KeyMembershipAll, "sites:create", nil, time.Time{})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if _, err := ps.db.Exec(`DROP TABLE platform_control_operation_receipts`); err != nil {
		t.Fatalf("simulate legacy schema: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close legacy store: %v", err)
	}

	ps, err = Open(path)
	if err != nil {
		t.Fatalf("reopen legacy store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "legacy-1", testControlHash("legacy")); err != nil || got != ControlOperationReserved {
		t.Fatalf("reserve after migration = %q %v", got, err)
	}
}

func TestControlOperationReceiptPersistsAcrossOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent-system.db")
	ps, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	token := "gcmsp_persistent_receipt"
	keyID, err := ps.CreatePlatformKey("persistent", token, token[:13], KeyMembershipAll, "sites:create", nil, time.Time{})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	hash := testControlHash("persistent-request")
	if _, got, err := ps.ReserveControlOperation(keyID, "sites.create", "persistent-1", hash); err != nil || got != ControlOperationReserved {
		t.Fatalf("reserve = %q %v", got, err)
	}
	wantBody := []byte(`{"site_id":73}`)
	if err := ps.CompleteControlOperation(keyID, "sites.create", "persistent-1", hash, 201, wantBody); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ps, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	receipt, got, err := ps.ReserveControlOperation(keyID, "sites.create", "persistent-1", hash)
	if err != nil || got != ControlOperationReplay {
		t.Fatalf("replay after reopen = %#v %q %v", receipt, got, err)
	}
	if receipt.HTTPStatus != 201 || string(receipt.ResponseJSON) != string(wantBody) {
		t.Fatalf("persisted response = %#v", receipt)
	}
}
