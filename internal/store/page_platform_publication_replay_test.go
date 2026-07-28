package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPagePublicationMutationReceiptSurvivesStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	post := createPagePlatformTestPost(t, st, "receipt-restart", "Receipt restart")
	project := createPagePlatformTestProject(t, st, post.ID)
	revision, _, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: project.WorkingRevisionID,
		RevisionKind: PageRevisionComposition,
		PageMetaJSON: `{"lang":"zh","slug":"receipt-restart","title":"Receipt restart"}`,
		ManifestJSON: `{"mode":"composition","schema_version":1}`,
		Origin:       PageOriginAPI, ActorID: "automation-key:7",
		RequestID: "receipt-revision", Summary: "receipt revision",
		ValidationJSON: `{"valid":true}`,
	})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	project, err = st.GetPageProject(project.ID)
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	build, err := st.CreatePageBuild(CreatePageBuildInput{
		ProjectID: project.ID, RevisionID: revision.ID, Status: PageBuildReady,
		ArtifactRef:     "composition:ssr/restart",
		ArtifactHash:    strings.Repeat("b", 64),
		DiagnosticsJSON: `[]`, RuntimeVersion: "composition-v1",
	})
	if err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	requestHash := strings.Repeat("a", 64)
	publication, created, err := st.PublishPageProject(PublishPageProjectInput{
		ProjectID: project.ID, RevisionID: revision.ID, BuildID: build.ID,
		ExpectedWorkingRevisionID: project.WorkingRevisionID,
		Action:                    PagePublicationPublish, ApprovalID: "approval-restart",
		ActorID: "automation-key:7", Origin: PageOriginAPI,
		RequestID: "publish-restart", RequestHash: requestHash,
		DataSnapshotHash: strings.Repeat("c", 64),
		DeliveryStatus:   PageDeliveryQueued,
	})
	if err != nil || !created || publication == nil {
		_ = st.Close()
		t.Fatalf("publish receipt: publication=%#v created=%v err=%v",
			publication, created, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, found, err := reopened.ReplayPagePublication(
		project.ID, "publish-restart", requestHash,
		"automation-key:7", PageOriginAPI, PagePublicationPublish,
	)
	if err != nil || !found || replayed == nil ||
		replayed.ID != publication.ID {
		t.Fatalf("restart replay: publication=%#v found=%v err=%v",
			replayed, found, err)
	}
	if _, _, err := reopened.ReplayPagePublication(
		project.ID, "publish-restart", strings.Repeat("d", 64),
		"automation-key:7", PageOriginAPI, PagePublicationPublish,
	); !errors.Is(err, ErrPageIdempotencyConflict) {
		t.Fatalf("changed restart replay err=%v, want idempotency conflict", err)
	}
}
