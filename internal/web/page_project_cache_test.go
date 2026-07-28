package web

import (
	"testing"

	"cms.ccvar.com/internal/store"
)

func TestPageProjectDraftInvalidationDoesNotTouchPublicOrCloudflareState(t *testing.T) {
	s := newTestPublicServer(t, "")
	configureManualCloudflareForInvalidationTest(t, s)
	s.content["published-content"] = contentCacheEntry{}
	s.endpoints["published-sitemap"] = endpointCacheEntry{}
	s.pages["published-page"] = pageCacheEntry{}

	s.invalidatePageProjectDraft()

	s.cacheMu.RLock()
	content, endpoints, pages := len(s.content), len(s.endpoints), len(s.pages)
	s.cacheMu.RUnlock()
	if content != 1 || endpoints != 1 || pages != 1 {
		t.Fatalf(
			"draft invalidation changed public caches: content=%d endpoints=%d pages=%d",
			content,
			endpoints,
			pages,
		)
	}
	if pending := s.store.Setting(cloudflareSyncPendingKey); pending != "" {
		t.Fatalf("draft invalidation marked Cloudflare pending: %q", pending)
	}
	if next := s.store.Setting(cloudflareSyncNextAtKey); next != "" {
		t.Fatalf("draft invalidation scheduled Cloudflare: %q", next)
	}
}

func TestPageProjectPublicationInvalidationClearsPublicAndMarksDelivery(t *testing.T) {
	s := newTestPublicServer(t, "")
	configureManualCloudflareForInvalidationTest(t, s)
	s.content["published-content"] = contentCacheEntry{}
	s.endpoints["published-sitemap"] = endpointCacheEntry{}
	s.pages["published-page"] = pageCacheEntry{}

	s.invalidatePageProjectPublication()

	s.cacheMu.RLock()
	content, endpoints, pages := len(s.content), len(s.endpoints), len(s.pages)
	s.cacheMu.RUnlock()
	if content != 0 || endpoints != 0 || pages != 0 {
		t.Fatalf(
			"publication did not clear public caches: content=%d endpoints=%d pages=%d",
			content,
			endpoints,
			pages,
		)
	}
	if pending := s.store.Setting(cloudflareSyncPendingKey); pending != "1" {
		t.Fatalf("publication Cloudflare pending = %q, want 1", pending)
	}
	if next := s.store.Setting(cloudflareSyncNextAtKey); next != "" {
		t.Fatalf("manual Cloudflare mode should not schedule a timer: %q", next)
	}
}

func TestPagePublicationDeliveryLifecycle(t *testing.T) {
	s := newTestPublicServer(t, "")
	if got := s.initialPagePublicationDeliveryStatus(); got != store.PageDeliveryLive {
		t.Fatalf("dynamic-only delivery status = %q, want live", got)
	}
	configureManualCloudflareForInvalidationTest(t, s)
	if got := s.initialPagePublicationDeliveryStatus(); got != store.PageDeliveryQueued {
		t.Fatalf("Cloudflare delivery status = %q, want queued", got)
	}

	postID, err := s.store.CreatePost(&store.Post{
		Type: "page", Slug: "delivery-lifecycle", Title: "Delivery lifecycle",
		Content: "legacy", Status: "draft", EditorMode: "markdown",
		Lang: "zh", Author: "tester",
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	post, err := s.store.GetPostByID(postID)
	if err != nil || post == nil {
		t.Fatalf("read page: post=%+v err=%v", post, err)
	}
	project, err := s.store.CreatePageProject(store.CreatePageProjectInput{
		PostID: post.ID, Mode: store.PageModeComposition, SchemaVersion: 1,
		ShellMode: store.PageShellSite, CreatedBy: store.PageOriginAdmin,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	metaJSON, err := store.PageRevisionMetaFromPost(post).CanonicalJSON()
	if err != nil {
		t.Fatalf("page metadata: %v", err)
	}
	revision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: store.PageRevisionComposition,
		PageMetaJSON: metaJSON,
		ManifestJSON: `{"schema_version":1,"mode":"composition","shell":{"mode":"site"},"theme":{"inherit":true},"layout":{"content_max_width":"wide","section_gap":"comfortable"},"sections":[{"id":"hero","type":"hero.centered","props":{"title":"Delivery"}}]}`,
		Origin:       store.PageOriginAdmin, ActorID: "admin",
		RequestID: "delivery-revision", ValidationJSON: `{"valid":true}`,
	})
	if err != nil {
		t.Fatalf("create revision: %v", err)
	}
	build, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID: project.ID, RevisionID: revision.ID,
		Status: store.PageBuildReady, RuntimeVersion: compositionRuntimeVersion,
	})
	if err != nil {
		t.Fatalf("create build: %v", err)
	}
	publication, _, err := s.store.PublishPageProject(store.PublishPageProjectInput{
		ProjectID: project.ID, RevisionID: revision.ID, BuildID: build.ID,
		ExpectedWorkingRevisionID: revision.ID,
		Action:                    store.PagePublicationPublish, ApprovalID: "admin-session",
		ActorID: "admin", Origin: store.PageOriginAdmin,
		RequestID: "delivery-publication", DeliveryStatus: store.PageDeliveryQueued,
	})
	if err != nil {
		t.Fatalf("publish fixture: %v", err)
	}

	const jobID = "cloudflare-test-job"
	ids, err := s.beginQueuedPageProjectDeliveries(jobID)
	if err != nil || len(ids) != 1 || ids[0] != publication.ID {
		t.Fatalf("begin delivery: ids=%v err=%v", ids, err)
	}
	queued, err := s.store.GetPagePublication(publication.ID)
	if err != nil || queued.DeploymentJobID != jobID ||
		queued.DeliveryStatus != store.PageDeliveryQueued {
		t.Fatalf("queued publication: row=%+v err=%v", queued, err)
	}
	s.finishPageProjectDeliveries(ids, jobID, store.PageDeliveryLive)
	live, err := s.store.GetPagePublication(publication.ID)
	if err != nil || live.DeliveryStatus != store.PageDeliveryLive ||
		live.DeploymentJobID != jobID {
		t.Fatalf("live publication: row=%+v err=%v", live, err)
	}
	// A stale worker cannot overwrite the completed state.
	s.finishPageProjectDeliveries(ids, "another-job", store.PageDeliveryFailed)
	stillLive, _ := s.store.GetPagePublication(publication.ID)
	if stillLive.DeliveryStatus != store.PageDeliveryLive {
		t.Fatalf("stale delivery completion changed state: %+v", stillLive)
	}
}

func configureManualCloudflareForInvalidationTest(t *testing.T, s *Server) {
	t.Helper()
	settings := map[string]string{
		cloudflareAPITokenKey:     "test-token",
		cloudflareDeployModeKey:   cloudflareModeWorkerAssets,
		cloudflareWorkerNameKey:   "gcms-test",
		cloudflareRoutePatternKey: "www.example.test/*",
		cloudflareDomainsKey: encodeCloudflareDomains([]CloudflareDomain{{
			Host: "www.example.test", Primary: true,
		}}),
		cloudflareAutoSyncKey: "0",
		cloudflareSyncModeKey: cloudflareSyncModeManual,
	}
	for key, value := range settings {
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
}
