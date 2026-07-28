package platform

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openPilotTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func bindPilotTest(t *testing.T, st *Store) *PilotBinding {
	t.Helper()
	binding, err := st.BindPilot("device-1", "secret-1", "My Mac", "0.2.39", PilotProtocolVersion, "macos", "binding-1", "platform", 9, 0, "Production", "gcmsp_test…", 12)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestPilotDeviceSecretStoredAsHashAndRevokedImmediately(t *testing.T) {
	st := openPilotTestStore(t)
	binding := bindPilotTest(t, st)
	var stored string
	if err := st.db.QueryRow(`SELECT secret_hash FROM pilot_devices WHERE id='device-1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "secret-1" || stored != pilotHash("secret-1") {
		t.Fatalf("device secret storage = %q", stored)
	}
	if _, ok, err := st.AuthenticatePilot("device-1", "secret-1"); err != nil || !ok {
		t.Fatalf("authenticate before revoke: ok=%v err=%v", ok, err)
	}
	if err := st.RevokePilotBinding(binding.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.AuthenticatePilot("device-1", "secret-1"); err != nil || ok {
		t.Fatalf("old device credential survived revoke: ok=%v err=%v", ok, err)
	}
	if _, err := st.BindPilot("device-1", "secret-2", "My Mac", "0.2.40", PilotProtocolVersion, "macos", "binding-2", "platform", 9, 0, "Production", "gcmsp_test…", 12); err != nil {
		t.Fatalf("rebind revoked device with a new secret: %v", err)
	}
	if _, ok, err := st.AuthenticatePilot("device-1", "secret-2"); err != nil || !ok {
		t.Fatalf("new device credential after rebind: ok=%v err=%v", ok, err)
	}
}

func TestPilotTaskIdempotencyLeaseEventsAndCompletion(t *testing.T) {
	st := openPilotTestStore(t)
	binding := bindPilotTest(t, st)
	task, created, err := st.CreatePilotTask("task-1", binding.ID, "request-1", "conversation-1", "conversation.create", "write", "Audit SEO", []int64{12, 13}, []string{"a", "b"}, []string{"A", "B"}, "codex", "", "high", "")
	if err != nil || !created {
		t.Fatalf("create task: created=%v err=%v", created, err)
	}
	if belongs, err := st.PilotConversationBelongs(binding.ID, task.ConversationID); err != nil || !belongs {
		t.Fatalf("conversation ownership: belongs=%v err=%v", belongs, err)
	}
	if belongs, err := st.PilotConversationBelongs(binding.ID, "missing-conversation"); err != nil || belongs {
		t.Fatalf("missing conversation ownership: belongs=%v err=%v", belongs, err)
	}
	replay, created, err := st.CreatePilotTask("task-other", binding.ID, "request-1", "conversation-other", "conversation.create", "write", "Audit SEO", []int64{12, 13}, []string{"a", "b"}, []string{"A", "B"}, "codex", "", "high", "")
	if err != nil || created || replay.ID != task.ID {
		t.Fatalf("idempotent replay: %#v created=%v err=%v", replay, created, err)
	}
	_, _, err = st.CreatePilotTask("task-conflict", binding.ID, "request-1", "conversation-x", "conversation.create", "write", "Publish everything", []int64{12}, []string{"a"}, []string{"A"}, "codex", "", "high", "")
	if !errors.Is(err, ErrPilotRequestConflict) {
		t.Fatalf("conflicting request id error = %v", err)
	}
	claimed, err := st.ClaimPilotTask("device-1", "lease-1", time.Minute)
	if err != nil || claimed == nil || claimed.ID != task.ID {
		t.Fatalf("claim = %#v err=%v", claimed, err)
	}
	if second, err := st.ClaimPilotTask("device-1", "lease-2", time.Minute); err != nil || second != nil {
		t.Fatalf("second claim = %#v err=%v", second, err)
	}
	payload, _ := json.Marshal(map[string]string{"text": "working"})
	event, err := st.AppendPilotTaskEvent("device-1", task.ID, "lease-1", "progress", string(payload))
	if err != nil || event.Seq != 1 {
		t.Fatalf("append event = %#v err=%v", event, err)
	}
	if err := st.UpdatePilotTask("device-1", task.ID, "wrong", "running", "", "", "", "", time.Minute); !errors.Is(err, ErrPilotLeaseLost) {
		t.Fatalf("wrong lease error = %v", err)
	}
	waiting, err := st.SetPilotTaskConfirmation("device-1", task.ID, "lease-1", "permit-1", `{"tool":"Bash","desc":"publish"}`, time.Minute)
	if err != nil || waiting.Status != "waiting_confirmation" || waiting.ConfirmationID != "permit-1" {
		t.Fatalf("set confirmation = %#v err=%v", waiting, err)
	}
	decided, err := st.DecidePilotTaskConfirmation(task.ID, true)
	if err != nil || decided.ConfirmationDecision != "allow" {
		t.Fatalf("decide confirmation = %#v err=%v", decided, err)
	}
	if err := st.UpdatePilotTask("device-1", task.ID, "lease-1", "completed", "done", "result", "", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetPilotTask(task.ID)
	if got.Status != "completed" || got.FinalOutput != "result" || got.CompletedAt.IsZero() {
		t.Fatalf("completed task = %#v", got)
	}
}

func TestPilotUnlockIsSingleUseAndFullyBound(t *testing.T) {
	st := openPilotTestStore(t)
	binding := bindPilotTest(t, st)
	if err := st.CreatePilotUnlock("unlock-1", "token-1", "admin", "device-1", binding.ID, 12, "sites.delete", "request-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConsumePilotUnlock("token-1", "device-1", binding.ID, 13, "sites.delete", "request-1"); err == nil {
		t.Fatal("unlock accepted wrong site")
	}
	id, err := st.ConsumePilotUnlock("token-1", "device-1", binding.ID, 12, "sites.delete", "request-1")
	if err != nil || id != "unlock-1" {
		t.Fatalf("consume = %q err=%v", id, err)
	}
	if _, err := st.ConsumePilotUnlock("token-1", "device-1", binding.ID, 12, "sites.delete", "request-1"); err == nil {
		t.Fatal("unlock token was reusable")
	}
}

func TestPilotMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, table := range []string{"pilot_devices", "pilot_bindings", "pilot_tasks", "pilot_task_events", "pilot_conversations", "pilot_unlocks", "pilot_audit_logs", "pilot_binding_snapshots"} {
		var name string
		err := second.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		if name != table {
			t.Fatalf("table %s missing", table)
		}
	}
}

func TestPilotBindingSnapshotIsOwnedByDevice(t *testing.T) {
	st := openPilotTestStore(t)
	binding := bindPilotTest(t, st)
	if err := st.SyncPilotBindingSnapshot("other-device", binding.ID, `[]`, `[]`); !errors.Is(err, ErrPilotNotFound) {
		t.Fatalf("wrong device snapshot error = %v", err)
	}
	if err := st.SyncPilotBindingSnapshot("device-1", binding.ID, `{`, `[]`); err == nil {
		t.Fatal("invalid snapshot JSON was accepted")
	}
	if err := st.SyncPilotBindingSnapshot("device-1", binding.ID, `[{"id":"daily"}]`, `[{"id":"managed"}]`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.GetPilotBindingSnapshot(binding.ID)
	if err != nil || snapshot.DeviceID != "device-1" || snapshot.ScheduledTasksJSON != `[{"id":"daily"}]` || snapshot.ManagedSitesJSON != `[{"id":"managed"}]` {
		t.Fatalf("snapshot = %#v err=%v", snapshot, err)
	}
}
