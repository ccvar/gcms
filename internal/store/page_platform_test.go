package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPagePlatformMigrationIsIdempotentAndDoesNotBackfillPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open new database: %v", err)
	}
	marker := createPagePlatformTestPost(t, st, "legacy-marker", "Legacy marker")
	before, err := st.GetPostByID(marker.ID)
	if err != nil {
		t.Fatalf("read pre-migration marker: %v", err)
	}
	var pagesBefore int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type='page'`).Scan(&pagesBefore); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	dropPagePlatformSchema(t, st)
	if err := st.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade legacy database: %v", err)
	}
	var projects, version int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM page_projects`).Scan(&projects); err != nil {
		t.Fatalf("count page projects: %v", err)
	}
	if projects != 0 {
		t.Fatalf("standard pages were unexpectedly backfilled: %d", projects)
	}
	if err := st.db.QueryRow(`
		SELECT version FROM store_schema_migrations WHERE name='page_platform'`,
	).Scan(&version); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != pagePlatformSchemaVersion {
		t.Fatalf("migration version=%d want %d", version, pagePlatformSchemaVersion)
	}
	after, err := st.GetPostByID(marker.ID)
	if err != nil {
		t.Fatalf("read upgraded marker: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy page fields changed during upgrade:\nbefore=%+v\nafter=%+v", before, after)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer st.Close()
	var pagesAfter, projectsAfter int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type='page'`).Scan(&pagesAfter); err != nil {
		t.Fatalf("count pages after reopen: %v", err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM page_projects`).Scan(&projectsAfter); err != nil {
		t.Fatalf("count projects after reopen: %v", err)
	}
	if pagesAfter != pagesBefore || projectsAfter != 0 {
		t.Fatalf("repeat migration changed standard data: pages %d→%d projects=%d",
			pagesBefore, pagesAfter, projectsAfter)
	}
}

func TestPagePlatformUpgradePreservesHistoricalSchemaGenerations(t *testing.T) {
	tests := []struct {
		name           string
		prepareHistory func(t *testing.T, st *Store)
	}{
		{
			name: "pre_page_platform",
			prepareHistory: func(t *testing.T, st *Store) {
				dropPagePlatformSchema(t, st)
			},
		},
		{
			name: "page_platform_v1",
			prepareHistory: func(t *testing.T, st *Store) {
				downgradePagePlatformFixture(t, st, 1)
			},
		},
		{
			name: "page_platform_v2",
			prepareHistory: func(t *testing.T, st *Store) {
				downgradePagePlatformFixture(t, st, 2)
			},
		},
		{
			name: "page_platform_v3",
			prepareHistory: func(t *testing.T, st *Store) {
				downgradePagePlatformFixture(t, st, 3)
			},
		},
		{
			name: "page_platform_v4",
			prepareHistory: func(t *testing.T, st *Store) {
				downgradePagePlatformFixture(t, st, 4)
			},
		},
		{
			name: "page_platform_v5",
			prepareHistory: func(t *testing.T, st *Store) {
				downgradePagePlatformFixture(t, st, 5)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cms.db")
			st, err := Open(path)
			if err != nil {
				t.Fatalf("open fixture database: %v", err)
			}
			marker := createPagePlatformTestPost(
				t,
				st,
				"historical-"+strings.ReplaceAll(tc.name, "_", "-"),
				"历史数据库升级标记 "+tc.name,
			)
			before, err := st.GetPostByID(marker.ID)
			if err != nil || before == nil {
				_ = st.Close()
				t.Fatalf("read fixture marker: post=%+v err=%v", before, err)
			}
			tc.prepareHistory(t, st)
			if err := st.Close(); err != nil {
				t.Fatalf("close historical fixture: %v", err)
			}

			upgraded, err := Open(path)
			if err != nil {
				t.Fatalf("upgrade %s: %v", tc.name, err)
			}
			defer upgraded.Close()

			after, err := upgraded.GetPostByID(marker.ID)
			if err != nil || after == nil {
				t.Fatalf("read upgraded marker: post=%+v err=%v", after, err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("legacy page changed during %s upgrade:\nbefore=%+v\nafter=%+v",
					tc.name, before, after)
			}
			var version, projects int
			if err := upgraded.db.QueryRow(`
				SELECT version FROM store_schema_migrations
				WHERE name='page_platform'`).Scan(&version); err != nil {
				t.Fatalf("read upgraded version: %v", err)
			}
			if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM page_projects`).Scan(&projects); err != nil {
				t.Fatalf("count upgraded projects: %v", err)
			}
			if version != pagePlatformSchemaVersion || projects != 0 {
				t.Fatalf("upgrade result version=%d projects=%d", version, projects)
			}
		})
	}
}

func TestPagePlatformMigrationFutureVersionStopsBeforeV1DDL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.db.Exec(`DROP INDEX idx_page_revisions_request`); err != nil {
		t.Fatalf("drop index marker: %v", err)
	}
	if _, err := st.db.Exec(`DROP TABLE page_assets`); err != nil {
		t.Fatalf("drop V1 table marker: %v", err)
	}
	if _, err := st.db.Exec(`
		UPDATE store_schema_migrations SET version=?
		WHERE name='page_platform'`, pagePlatformSchemaVersion+1); err != nil {
		t.Fatalf("set future version: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("future schema should fail closed, got %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer db.Close()
	var indexCount, tableCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND name='idx_page_revisions_request'`).Scan(&indexCount); err != nil {
		t.Fatalf("inspect index: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='page_assets'`).Scan(&tableCount); err != nil {
		t.Fatalf("inspect table: %v", err)
	}
	if indexCount != 0 || tableCount != 0 {
		t.Fatal("older binary applied V1 DDL before rejecting future schema")
	}
}

func TestPagePlatformMigrationFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.db.Exec(`DROP INDEX idx_page_revisions_request`); err != nil {
		t.Fatalf("drop marker index: %v", err)
	}
	if _, err := st.db.Exec(`DROP TABLE page_assets`); err != nil {
		t.Fatalf("drop page_assets: %v", err)
	}
	if _, err := st.db.Exec(`CREATE TABLE page_assets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create malformed page_assets: %v", err)
	}
	if _, err := st.db.Exec(`
		UPDATE store_schema_migrations SET version=0 WHERE name='page_platform'`); err != nil {
		t.Fatalf("reset migration version: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "页面平台迁移失败") {
		t.Fatalf("malformed schema should block startup, got %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer db.Close()
	var version, indexCount int
	if err := db.QueryRow(`
		SELECT version FROM store_schema_migrations WHERE name='page_platform'`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 0 {
		t.Fatalf("failed migration advanced version to %d", version)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND name='idx_page_revisions_request'`).Scan(&indexCount); err != nil {
		t.Fatalf("inspect rollback marker: %v", err)
	}
	if indexCount != 0 {
		t.Fatal("DDL before migration failure was not rolled back")
	}
}

func TestCanonicalJSONHashIsStable(t *testing.T) {
	left, leftHash, err := CanonicalJSONHash(`{
		"sections": [{"props":{"title":"Hello","count":3},"type":"hero.split"}],
		"schema_version": 1
	}`)
	if err != nil {
		t.Fatalf("canonical left: %v", err)
	}
	right, rightHash, err := CanonicalJSONHash(
		`{"schema_version":1,"sections":[{"type":"hero.split","props":{"count":3,"title":"Hello"}}]}`,
	)
	if err != nil {
		t.Fatalf("canonical right: %v", err)
	}
	if left != right || leftHash != rightHash {
		t.Fatalf("equivalent JSON is unstable:\n%s\n%s\n%s\n%s", left, right, leftHash, rightHash)
	}
	if len(leftHash) != 64 {
		t.Fatalf("unexpected hash length: %q", leftHash)
	}
}

func TestPageProjectRevisionIdempotencyAndConflict(t *testing.T) {
	st := openPagePlatformTestStore(t)
	post := createPagePlatformTestPost(t, st, "manual-page", "Manual page")
	project := createPagePlatformTestProject(t, st, post.ID)

	meta := PageRevisionMetaFromPost(post)
	meta.Slug = "campaign"
	meta.Title = "Campaign draft"
	metaJSON, err := meta.CanonicalJSON()
	if err != nil {
		t.Fatalf("metadata JSON: %v", err)
	}
	input := CreatePageRevisionInput{
		ProjectID:      project.ID,
		BaseRevisionID: 0,
		RevisionKind:   PageRevisionComposition,
		PageMetaJSON:   metaJSON,
		ManifestJSON:   `{"sections":[],"schema_version":1}`,
		Origin:         PageOriginPilot,
		ActorID:        "pilot-key-1",
		ConversationID: "conv-1",
		RequestID:      "revision-request-1",
		Summary:        "initial draft",
	}
	first, created, err := st.CreatePageProjectRevision(input)
	if err != nil || !created {
		t.Fatalf("create first revision: created=%v err=%v", created, err)
	}
	if first.RevisionNo != 1 || first.ParentRevisionID != 0 {
		t.Fatalf("unexpected first revision: %+v", first)
	}
	project, err = st.GetPageProject(project.ID)
	if err != nil || project.WorkingRevisionID != first.ID {
		t.Fatalf("working pointer not advanced: project=%+v err=%v", project, err)
	}
	if project.ETag() != PageRevisionETag(first.ID) {
		t.Fatalf("unexpected ETag: %q", project.ETag())
	}
	reservation, err := st.GetPageRouteReservation(project.ID)
	if err != nil || reservation == nil || reservation.RevisionID != first.ID ||
		reservation.Lang != meta.Lang || reservation.Slug != meta.Slug {
		t.Fatalf("route not reserved with revision: row=%+v err=%v", reservation, err)
	}

	// Formatting and object key order do not alter the request identity.
	retry := input
	retry.ManifestJSON = "{\n  \"schema_version\": 1,\n  \"sections\": []\n}"
	same, created, err := st.CreatePageProjectRevision(retry)
	if err != nil || created || same.ID != first.ID {
		t.Fatalf("idempotent retry: revision=%+v created=%v err=%v", same, created, err)
	}
	changed := input
	changed.ManifestJSON = `{"schema_version":1,"sections":[{"id":"hero","type":"hero.centered"}]}`
	if _, _, err := st.CreatePageProjectRevision(changed); !errors.Is(err, ErrPageIdempotencyConflict) {
		t.Fatalf("same request with changed payload: %v", err)
	}
	stale := input
	stale.RequestID = "revision-request-2"
	if _, _, err := st.CreatePageProjectRevision(stale); !errors.Is(err, ErrPageRevisionConflict) {
		t.Fatalf("stale base should conflict: %v", err)
	} else {
		var conflict *PageRevisionConflictError
		if !errors.As(err, &conflict) || conflict.CurrentRevisionID != first.ID {
			t.Fatalf("conflict lacks current revision: %#v", err)
		}
	}

	secondInput := input
	secondInput.BaseRevisionID = first.ID
	secondInput.RequestID = "revision-request-3"
	secondInput.Summary = "second draft"
	secondInput.ManifestJSON = `{"schema_version":1,"sections":[{"id":"hero","type":"hero.centered"}]}`
	second, created, err := st.CreatePageProjectRevision(secondInput)
	if err != nil || !created || second.RevisionNo != 2 || second.ParentRevisionID != first.ID {
		t.Fatalf("create second revision: revision=%+v created=%v err=%v", second, created, err)
	}
	revisions, err := st.ListPageProjectRevisions(project.ID, 10)
	if err != nil || len(revisions) != 2 || revisions[0].ID != second.ID {
		t.Fatalf("list revisions: rows=%+v err=%v", revisions, err)
	}
	storedPost, err := st.GetPostByID(post.ID)
	if err != nil || storedPost.Slug != post.Slug || storedPost.Title != post.Title {
		t.Fatalf("draft revision leaked metadata to posts: post=%+v err=%v", storedPost, err)
	}
}

func TestPageBuildCreationIsDurablyIdempotentAndConcurrentSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	post := createPagePlatformTestPost(t, st, "durable-build", "Durable build")
	project := createPagePlatformTestProject(t, st, post.ID)
	metaJSON, err := PageRevisionMetaFromPost(post).CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical page metadata: %v", err)
	}
	revision, created, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: PageRevisionComposition,
		PageMetaJSON: metaJSON, ManifestJSON: `{"schema_version":1,"sections":[]}`,
		Origin: PageOriginAPI, ActorID: "build-test", RequestID: "build-test-revision",
	})
	if err != nil || !created {
		t.Fatalf("create revision: revision=%+v created=%v err=%v", revision, created, err)
	}
	buildInput := func(artifact string) CreatePageBuildInput {
		return CreatePageBuildInput{
			ProjectID: project.ID, RevisionID: revision.ID, Status: PageBuildReady,
			ArtifactRef: "composition:ssr/" + artifact, ArtifactHash: artifact,
			DiagnosticsJSON: `[]`, RuntimeVersion: "composition-v1",
		}
	}

	firstArtifact := SHA256Hex([]byte("first durable render"))
	firstHash := SHA256Hex([]byte("canonical request one"))
	first, wasCreated, wasReplayed, err := st.CreatePageBuildIdempotent(CreatePageBuildIdempotentInput{
		CreatePageBuildInput: buildInput(firstArtifact),
		RequestID:            "durable-build-request", RequestHash: firstHash,
	})
	if err != nil || !wasCreated || wasReplayed {
		t.Fatalf("first build: build=%+v created=%v replayed=%v err=%v",
			first, wasCreated, wasReplayed, err)
	}
	replayed, wasCreated, wasReplayed, err := st.CreatePageBuildIdempotent(CreatePageBuildIdempotentInput{
		CreatePageBuildInput: buildInput(firstArtifact),
		RequestID:            "durable-build-request", RequestHash: firstHash,
	})
	if err != nil || wasCreated || !wasReplayed || replayed.ID != first.ID {
		t.Fatalf("same-key replay: build=%+v created=%v replayed=%v err=%v",
			replayed, wasCreated, wasReplayed, err)
	}
	if _, _, _, err := st.CreatePageBuildIdempotent(CreatePageBuildIdempotentInput{
		CreatePageBuildInput: buildInput(SHA256Hex([]byte("changed render"))),
		RequestID:            "durable-build-request",
		RequestHash:          SHA256Hex([]byte("canonical request changed")),
	}); !errors.Is(err, ErrPageIdempotencyConflict) {
		t.Fatalf("changed same-key request should conflict: %v", err)
	}
	reused, wasCreated, wasReplayed, err := st.CreatePageBuildIdempotent(CreatePageBuildIdempotentInput{
		CreatePageBuildInput: buildInput(firstArtifact),
		RequestID:            "fresh-key-same-build", RequestHash: firstHash,
	})
	if err != nil || wasCreated || wasReplayed || reused.ID != first.ID {
		t.Fatalf("fresh-key content reuse: build=%+v created=%v replayed=%v err=%v",
			reused, wasCreated, wasReplayed, err)
	}

	const workers = 12
	concurrentArtifact := SHA256Hex([]byte("concurrent durable render"))
	concurrentHash := SHA256Hex([]byte("canonical concurrent request"))
	type result struct {
		build    *PageBuild
		created  bool
		replayed bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			build, created, replayed, err := st.CreatePageBuildIdempotent(CreatePageBuildIdempotentInput{
				CreatePageBuildInput: buildInput(concurrentArtifact),
				RequestID:            "concurrent-build-request", RequestHash: concurrentHash,
			})
			results <- result{build: build, created: created, replayed: replayed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var concurrentID int64
	createdCount := 0
	replayedCount := 0
	for result := range results {
		if result.err != nil || result.build == nil {
			t.Fatalf("concurrent build: build=%+v created=%v err=%v",
				result.build, result.created, result.err)
		}
		if concurrentID == 0 {
			concurrentID = result.build.ID
		}
		if result.build.ID != concurrentID {
			t.Fatalf("concurrent key created multiple builds: got %d and %d",
				concurrentID, result.build.ID)
		}
		if result.created {
			createdCount++
		}
		if result.replayed {
			replayedCount++
		}
	}
	if createdCount != 1 || replayedCount != workers-1 {
		t.Fatalf("concurrent key created/replayed=%d/%d want 1/%d",
			createdCount, replayedCount, workers-1)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close before replay: %v", err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	afterRestart, wasCreated, wasReplayed, err := st.CreatePageBuildIdempotent(CreatePageBuildIdempotentInput{
		CreatePageBuildInput: buildInput(concurrentArtifact),
		RequestID:            "concurrent-build-request", RequestHash: concurrentHash,
	})
	if err != nil || wasCreated || !wasReplayed || afterRestart.ID != concurrentID {
		t.Fatalf("restart replay: build=%+v created=%v replayed=%v err=%v",
			afterRestart, wasCreated, wasReplayed, err)
	}
	var builds, receipts int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM page_builds WHERE project_id=?`, project.ID).Scan(&builds); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM page_build_create_receipts WHERE project_id=?`, project.ID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if builds != 2 || receipts != 3 {
		t.Fatalf("durable rows: builds=%d receipts=%d want 2/3", builds, receipts)
	}
}

func TestCompositionRevisionUpdatesShellModeInTheSameTransaction(t *testing.T) {
	st := openPagePlatformTestStore(t)
	post := createPagePlatformTestPost(t, st, "shell-source", "Shell source")
	project := createPagePlatformTestProject(t, st, post.ID)
	meta := PageRevisionMetaFromPost(post)
	meta.Slug = "shell-campaign"
	metaJSON, err := meta.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: PageRevisionComposition,
		PageMetaJSON: metaJSON,
		ManifestJSON: `{"mode":"composition","schema_version":1,"shell":{"mode":"minimal"}}`,
		Origin:       PageOriginAdmin, RequestID: "shell-mode-minimal",
	})
	if err != nil || !created {
		t.Fatalf("create shell revision: revision=%+v created=%v err=%v", first, created, err)
	}
	project, err = st.GetPageProject(project.ID)
	if err != nil || project.ShellMode != PageShellMinimal ||
		project.WorkingRevisionID != first.ID {
		t.Fatalf("shell mode was not committed with revision: project=%+v err=%v", project, err)
	}

	blocker := createPagePlatformTestPost(t, st, "already-owned-route", "Route owner")
	_ = blocker
	conflictingMeta := meta
	conflictingMeta.Slug = "already-owned-route"
	conflictingMetaJSON, err := conflictingMeta.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: first.ID,
		RevisionKind: PageRevisionComposition, PageMetaJSON: conflictingMetaJSON,
		ManifestJSON: `{"mode":"composition","schema_version":1,"shell":{"mode":"none"}}`,
		Origin:       PageOriginAdmin, RequestID: "shell-mode-conflict",
	})
	if !errors.Is(err, ErrPageRouteConflict) {
		t.Fatalf("route-conflicting shell revision = %v", err)
	}
	project, err = st.GetPageProject(project.ID)
	if err != nil || project.ShellMode != PageShellMinimal ||
		project.WorkingRevisionID != first.ID {
		t.Fatalf("failed revision leaked shell/pointer update: project=%+v err=%v", project, err)
	}
}

func TestPageProjectRevisionRejectsSystemAndContentTypeRoutes(t *testing.T) {
	st := openPagePlatformTestStore(t)
	post := createPagePlatformTestPost(t, st, "route-source", "Route source")
	project := createPagePlatformTestProject(t, st, post.ID)

	for _, slug := range []string{"admin", "posts", "products", "api-docs"} {
		meta := PageRevisionMetaFromPost(post)
		meta.Slug = slug
		metaJSON, err := meta.CanonicalJSON()
		if err != nil {
			t.Fatalf("canonical metadata for %s: %v", slug, err)
		}
		_, _, err = st.CreatePageProjectRevision(CreatePageRevisionInput{
			ProjectID: project.ID, RevisionKind: PageRevisionComposition,
			PageMetaJSON: metaJSON, ManifestJSON: `{"schema_version":1}`,
			Origin: PageOriginAdmin, ActorID: "admin", RequestID: "reserved-" + slug,
		})
		if !errors.Is(err, ErrPageRouteConflict) {
			t.Fatalf("reserved slug %q should conflict, got %v", slug, err)
		}
	}

	if err := st.SaveContentType(&ContentTypeRow{
		Key: "case-study", URLPrefix: "case-studies", Fields: "[]",
	}); err != nil {
		t.Fatalf("save custom content type: %v", err)
	}
	meta := PageRevisionMetaFromPost(post)
	meta.Slug = "case-studies"
	metaJSON, err := meta.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical custom prefix metadata: %v", err)
	}
	_, _, err = st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: PageRevisionComposition,
		PageMetaJSON: metaJSON, ManifestJSON: `{"schema_version":1}`,
		Origin: PageOriginAdmin, ActorID: "admin", RequestID: "reserved-custom-prefix",
	})
	if !errors.Is(err, ErrPageRouteConflict) {
		t.Fatalf("custom content-type prefix should conflict, got %v", err)
	}
}

func TestPagePublicationIsAtomicAndIdempotent(t *testing.T) {
	st := openPagePlatformTestStore(t)
	post := createPagePlatformTestPost(t, st, "public-old", "Old public metadata")
	project := createPagePlatformTestProject(t, st, post.ID)

	meta := PageRevisionMetaFromPost(post)
	meta.Slug = "public-candidate"
	meta.Title = "Published campaign"
	metaJSON, _ := meta.CanonicalJSON()
	revision, _, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID:      project.ID,
		RevisionKind:   PageRevisionComposition,
		PageMetaJSON:   metaJSON,
		ManifestJSON:   `{"schema_version":1,"sections":[]}`,
		Origin:         PageOriginAdmin,
		ActorID:        "admin",
		RequestID:      "create-publication-revision",
		ValidationJSON: `{"valid":true}`,
	})
	if err != nil {
		t.Fatalf("create revision: %v", err)
	}
	publishInput := PublishPageProjectInput{
		ProjectID:                 project.ID,
		RevisionID:                revision.ID,
		ExpectedWorkingRevisionID: revision.ID,
		Action:                    PagePublicationPublish,
		ApprovalID:                "approval-1",
		ActorID:                   "admin",
		Origin:                    PageOriginAdmin,
		RequestID:                 "publish-request-1",
		DeliveryStatus:            PageDeliveryQueued,
	}
	if _, _, err := st.PublishPageProject(publishInput); !errors.Is(err, ErrPageBuildNotReady) {
		t.Fatalf("publication without a ready build: %v", err)
	}
	assertPageNotPublished(t, st, project.ID, post.ID, post.Slug, post.Title)

	foreignPost := createPagePlatformTestPost(t, st, "foreign-project", "Foreign")
	foreignProject := createPagePlatformTestProject(t, st, foreignPost.ID)
	foreignMetaJSON, _ := PageRevisionMetaFromPost(foreignPost).CanonicalJSON()
	foreignRevision, _, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID: foreignProject.ID, RevisionKind: PageRevisionComposition,
		PageMetaJSON: foreignMetaJSON, ManifestJSON: `{"schema_version":1}`,
		Origin: PageOriginAdmin, ActorID: "admin", RequestID: "foreign-revision",
	})
	if err != nil {
		t.Fatalf("create foreign revision: %v", err)
	}
	foreignBuild, err := st.CreatePageBuild(CreatePageBuildInput{
		ProjectID: foreignProject.ID, RevisionID: foreignRevision.ID,
		Status: PageBuildReady, RuntimeVersion: "composition-v1",
	})
	if err != nil {
		t.Fatalf("create foreign build: %v", err)
	}
	publishInput.BuildID = foreignBuild.ID
	if _, _, err := st.PublishPageProject(publishInput); !errors.Is(err, ErrPageBuildNotReady) {
		t.Fatalf("publication with another project's build: %v", err)
	}
	assertPageNotPublished(t, st, project.ID, post.ID, post.Slug, post.Title)

	build, err := st.CreatePageBuild(CreatePageBuildInput{
		ProjectID:       project.ID,
		RevisionID:      revision.ID,
		Status:          PageBuildReady,
		DiagnosticsJSON: `[]`,
		RuntimeVersion:  "composition-v1",
	})
	if err != nil {
		t.Fatalf("create ready build: %v", err)
	}
	publishInput.BuildID = build.ID

	// Simulate an older binary that predates the V4 write-time trigger. The
	// publication transaction must still detect this late conflict and roll
	// back every public-state mutation.
	if _, err := st.db.Exec(`DROP TRIGGER page_content_type_route_insert`); err != nil {
		t.Fatalf("drop insert route trigger: %v", err)
	}
	conflicting := createPagePlatformTestPost(t, st, meta.Slug, "Conflicting page")
	if _, _, err := st.PublishPageProject(publishInput); !errors.Is(err, ErrPageRouteConflict) {
		t.Fatalf("late route conflict should abort publication: %v", err)
	}
	assertPageNotPublished(t, st, project.ID, post.ID, post.Slug, post.Title)
	if err := st.DeletePost(conflicting.ID); err != nil {
		t.Fatalf("delete conflicting page: %v", err)
	}
	if _, err := st.db.Exec(pagePlatformSchemaV4[2]); err != nil {
		t.Fatalf("restore insert route trigger: %v", err)
	}

	publication, created, err := st.PublishPageProject(publishInput)
	if err != nil || !created {
		t.Fatalf("publish: publication=%+v created=%v err=%v", publication, created, err)
	}
	project, err = st.GetPageProject(project.ID)
	if err != nil || project.PublishedRevisionID != revision.ID {
		t.Fatalf("published pointer: project=%+v err=%v", project, err)
	}
	publishedPost, err := st.GetPostByID(post.ID)
	if err != nil || publishedPost.Status != "published" ||
		publishedPost.Slug != meta.Slug || publishedPost.Title != meta.Title {
		t.Fatalf("public post metadata not switched: post=%+v err=%v", publishedPost, err)
	}
	if reservation, err := st.GetPageRouteReservation(project.ID); err != nil || reservation != nil {
		t.Fatalf("published route reservation not released: row=%+v err=%v", reservation, err)
	}

	same, created, err := st.PublishPageProject(publishInput)
	if err != nil || created || same.ID != publication.ID {
		t.Fatalf("idempotent publish retry: row=%+v created=%v err=%v", same, created, err)
	}
	changed := publishInput
	changed.Action = PagePublicationRollback
	if _, _, err := st.PublishPageProject(changed); !errors.Is(err, ErrPageIdempotencyConflict) {
		t.Fatalf("changed retry should conflict: %v", err)
	}
	publications, err := st.ListPagePublications(project.ID, 10)
	if err != nil || len(publications) != 1 {
		t.Fatalf("publication retry duplicated history: rows=%+v err=%v", publications, err)
	}
	if err := st.DeletePost(post.ID); err != nil {
		t.Fatalf("delete page with project history: %v", err)
	}
	if deletedProject, err := st.GetPageProject(project.ID); err != nil || deletedProject != nil {
		t.Fatalf("page project did not cascade with page deletion: project=%+v err=%v", deletedProject, err)
	}
}

func TestPageAndCustomContentRouteOwnershipIsMutuallyExclusive(t *testing.T) {
	t.Run("custom type cannot shadow a standard page", func(t *testing.T) {
		st := openPagePlatformTestStore(t)
		createPagePlatformTestPost(t, st, "pricing", "Pricing")

		err := st.SaveContentType(&ContentTypeRow{
			Key: "pricing", Name: `{"zh":"价格"}`, URLPrefix: "pricing", Fields: "[]",
		})
		if !errors.Is(err, ErrPageRouteConflict) {
			t.Fatalf("custom type should not shadow page route: %v", err)
		}
	})

	t.Run("custom type cannot shadow an advanced candidate", func(t *testing.T) {
		st := openPagePlatformTestStore(t)
		post := createPagePlatformTestPost(t, st, "candidate-source", "Candidate")
		project := createPagePlatformTestProject(t, st, post.ID)
		meta := PageRevisionMetaFromPost(post)
		meta.Slug = "launch"
		metaJSON, err := meta.CanonicalJSON()
		if err != nil {
			t.Fatalf("canonical metadata: %v", err)
		}
		if _, _, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
			ProjectID: project.ID, RevisionKind: PageRevisionComposition,
			PageMetaJSON: metaJSON, ManifestJSON: `{"schema_version":1}`,
			Origin: PageOriginAdmin, ActorID: "admin", RequestID: "candidate-launch",
		}); err != nil {
			t.Fatalf("create candidate revision: %v", err)
		}

		err = st.SaveContentType(&ContentTypeRow{
			Key: "launch", Name: `{"zh":"发布"}`, URLPrefix: "launch", Fields: "[]",
		})
		if !errors.Is(err, ErrPageRouteConflict) {
			t.Fatalf("custom type should not shadow candidate route: %v", err)
		}
	})

	t.Run("standard page cannot shadow a custom type", func(t *testing.T) {
		st := openPagePlatformTestStore(t)
		if err := st.SaveContentType(&ContentTypeRow{
			Key: "catalog", Name: `{"zh":"目录"}`, URLPrefix: "catalog", Fields: "[]",
		}); err != nil {
			t.Fatalf("save content type: %v", err)
		}
		post := &Post{
			Type: "page", Slug: "catalog", Title: "Catalog", Content: "body",
			Status: "draft", EditorMode: "markdown", Lang: "zh", Author: "tester",
		}
		if _, err := st.CreatePost(post); !errors.Is(err, ErrPageRouteConflict) {
			t.Fatalf("page should not shadow custom type route: %v", err)
		}
	})

	t.Run("publication rechecks a custom type inserted by an older writer", func(t *testing.T) {
		st := openPagePlatformTestStore(t)
		post := createPagePlatformTestPost(t, st, "old-route", "Old route")
		project := createPagePlatformTestProject(t, st, post.ID)
		meta := PageRevisionMetaFromPost(post)
		meta.Slug = "showcase"
		metaJSON, err := meta.CanonicalJSON()
		if err != nil {
			t.Fatalf("canonical metadata: %v", err)
		}
		revision, _, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
			ProjectID: project.ID, RevisionKind: PageRevisionComposition,
			PageMetaJSON: metaJSON, ManifestJSON: `{"schema_version":1}`,
			Origin: PageOriginAdmin, ActorID: "admin", RequestID: "showcase-revision",
		})
		if err != nil {
			t.Fatalf("create revision: %v", err)
		}
		build, err := st.CreatePageBuild(CreatePageBuildInput{
			ProjectID: project.ID, RevisionID: revision.ID,
			Status: PageBuildReady, RuntimeVersion: "composition-v1",
		})
		if err != nil {
			t.Fatalf("create build: %v", err)
		}
		if _, err := st.db.Exec(`DROP TRIGGER content_type_page_route_insert`); err != nil {
			t.Fatalf("drop content type trigger: %v", err)
		}
		if err := st.SaveContentType(&ContentTypeRow{
			Key: "showcase", Name: `{"zh":"展示"}`, URLPrefix: "showcase", Fields: "[]",
		}); err != nil {
			t.Fatalf("simulate legacy content type creation: %v", err)
		}

		_, _, err = st.PublishPageProject(PublishPageProjectInput{
			ProjectID: project.ID, RevisionID: revision.ID, BuildID: build.ID,
			ExpectedWorkingRevisionID: revision.ID, Action: PagePublicationPublish,
			ActorID: "admin", Origin: PageOriginAdmin, RequestID: "publish-showcase",
			DeliveryStatus: PageDeliveryQueued,
		})
		if !errors.Is(err, ErrPageRouteConflict) {
			t.Fatalf("late custom-type conflict should abort publication: %v", err)
		}
		assertPageNotPublished(t, st, project.ID, post.ID, post.Slug, post.Title)
	})
}

func TestPageBuildAssetCapabilityCRUD(t *testing.T) {
	st := openPagePlatformTestStore(t)
	post := createPagePlatformTestPost(t, st, "records-page", "Records")
	project := createPagePlatformTestProject(t, st, post.ID)
	metaJSON, _ := PageRevisionMetaFromPost(post).CanonicalJSON()
	revision, _, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: PageRevisionComposition,
		PageMetaJSON: metaJSON, ManifestJSON: `{"schema_version":1}`,
		Origin: PageOriginAPI, ActorID: "key-1", RequestID: "records-revision",
	})
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	build, err := st.CreatePageBuild(CreatePageBuildInput{
		ProjectID: project.ID, RevisionID: revision.ID, Status: PageBuildQueued,
		RuntimeVersion: "composition-v1",
	})
	if err != nil {
		t.Fatalf("create build: %v", err)
	}
	build, err = st.UpdatePageBuild(UpdatePageBuildInput{
		ID: build.ID, ExpectedStatus: PageBuildQueued, Status: PageBuildReady,
		RuntimeVersion: "composition-v1", DiagnosticsJSON: `[]`,
	})
	if err != nil || build.Status != PageBuildReady {
		t.Fatalf("complete build: build=%+v err=%v", build, err)
	}
	assetsHash1 := SHA256Hex([]byte("asset-one"))
	assetRequestHash := SHA256Hex([]byte("upload-request-one"))
	assetInput := CreatePageAssetInput{
		ProjectID: project.ID, LogicalKey: "hero", StorageRef: "blobs/" + assetsHash1,
		MediaType: "image/png", ByteSize: 9, SHA256: assetsHash1,
		Origin: "upload", ProvenanceJSON: `{"source":"admin"}`,
		RequestID: "asset-upload-one", RequestHash: assetRequestHash,
	}
	asset1, created, err := st.CreatePageAsset(assetInput)
	if err != nil || !created || asset1.VersionNo != 1 {
		t.Fatalf("create asset: asset=%+v created=%v err=%v", asset1, created, err)
	}
	replayedAsset, created, err := st.CreatePageAsset(assetInput)
	if err != nil || created || replayedAsset.ID != asset1.ID {
		t.Fatalf("asset request replay: asset=%+v created=%v err=%v", replayedAsset, created, err)
	}
	conflictingInput := assetInput
	conflictingInput.RequestHash = SHA256Hex([]byte("different multipart request"))
	if _, _, err := st.CreatePageAsset(conflictingInput); !errors.Is(err, ErrPageIdempotencyConflict) {
		t.Fatalf("asset request conflict = %v", err)
	}
	sameAsset, created, err := st.CreatePageAsset(CreatePageAssetInput{
		ProjectID: project.ID, LogicalKey: "another-name", StorageRef: "blobs/" + assetsHash1,
		MediaType: "image/png", ByteSize: 9, SHA256: assetsHash1,
		Origin: "upload", ProvenanceJSON: `{}`,
	})
	if err != nil || created || sameAsset.ID != asset1.ID {
		t.Fatalf("asset hash dedupe: asset=%+v created=%v err=%v", sameAsset, created, err)
	}
	assetsHash2 := SHA256Hex([]byte("asset-two"))
	asset2, created, err := st.CreatePageAsset(CreatePageAssetInput{
		ProjectID: project.ID, LogicalKey: "hero", StorageRef: "blobs/" + assetsHash2,
		MediaType: "image/png", ByteSize: 9, SHA256: assetsHash2,
		Origin: "generated", ProvenanceJSON: `{}`,
	})
	if err != nil || !created || asset2.VersionNo != 2 {
		t.Fatalf("version asset: asset=%+v created=%v err=%v", asset2, created, err)
	}
	grant, err := st.UpsertPageCapabilityGrant(UpsertPageCapabilityGrantInput{
		ProjectID: project.ID, Capability: "content.read", ConfigJSON: `{"types":["product"]}`,
		Status: PageCapabilityRequested, RequestedBy: "pilot-key-1",
	})
	if err != nil || grant.Status != PageCapabilityRequested {
		t.Fatalf("request capability: grant=%+v err=%v", grant, err)
	}
	grant, err = st.UpsertPageCapabilityGrant(UpsertPageCapabilityGrantInput{
		ProjectID: project.ID, Capability: "content.read", ConfigJSON: `{"types":["product"]}`,
		Status: PageCapabilityApproved, RequestedBy: "pilot-key-1", ApprovedBy: "admin",
	})
	if err != nil || grant.Status != PageCapabilityApproved || grant.ApprovedBy != "admin" {
		t.Fatalf("approve capability: grant=%+v err=%v", grant, err)
	}
}

func TestPageCapabilityMutationReceiptPersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	post := createPagePlatformTestPost(t, st, "cap-receipt", "Capability Receipt")
	project, err := st.CreatePageProject(CreatePageProjectInput{
		PostID: post.ID, Mode: PageModeApp, SchemaVersion: 1,
		ShellMode: PageShellSite, CreatedBy: PageOriginAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := UpsertPageCapabilityGrantInput{
		ProjectID: project.ID, Capability: "client.storage",
		ConfigJSON: `{"max_bytes":4096}`, Status: PageCapabilityRequested,
		RequestedBy: "automation-key:1",
	}
	requestHash := SHA256Hex([]byte("request client.storage 4096"))
	first, executed, err := st.UpsertPageCapabilityGrantIdempotent(
		input, "request", "cap-receipt-1", requestHash,
	)
	if err != nil || !executed || first.Status != PageCapabilityRequested {
		t.Fatalf("first receipt mutation: grant=%+v executed=%v err=%v", first, executed, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	receipt, found, err := st.GetPageCapabilityMutationReceipt(
		project.ID, "cap-receipt-1", requestHash,
	)
	if err != nil || !found || receipt.Status != PageCapabilityRequested ||
		receipt.ID != first.ID {
		t.Fatalf("durable receipt: grant=%+v found=%v err=%v", receipt, found, err)
	}
	replayed, executed, err := st.UpsertPageCapabilityGrantIdempotent(
		input, "request", "cap-receipt-1", requestHash,
	)
	if err != nil || executed || replayed.ID != first.ID {
		t.Fatalf("receipt replay: grant=%+v executed=%v err=%v", replayed, executed, err)
	}
	if _, _, err := st.GetPageCapabilityMutationReceipt(
		project.ID, "cap-receipt-1", SHA256Hex([]byte("different")),
	); !errors.Is(err, ErrPageIdempotencyConflict) {
		t.Fatalf("receipt conflict = %v", err)
	}
}

func TestPageCapabilityApprovalChecksRevisionAndRequestedConfigInTransaction(t *testing.T) {
	st := openPagePlatformTestStore(t)
	post := createPagePlatformTestPost(t, st, "cap-approval-cas", "Capability CAS")
	project, err := st.CreatePageProject(CreatePageProjectInput{
		PostID: post.ID, Mode: PageModeApp, SchemaVersion: 1,
		ShellMode: PageShellSite, CreatedBy: PageOriginAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := st.CreatePageProjectRevision(CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: PageRevisionApp,
		PageMetaJSON:    `{"lang":"zh","slug":"cap-approval-cas","title":"Capability CAS"}`,
		ManifestJSON:    `{"schema_version":1,"mode":"app"}`,
		SourceBundleRef: "page-projects/1/source", SourceHash: strings.Repeat("a", 64),
		Origin: PageOriginAPI, ActorID: "automation-key:1", RequestID: "cap-cas-revision",
		ValidationJSON: `{"valid":true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err = st.GetPageProject(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	requested, err := st.UpsertPageCapabilityGrant(UpsertPageCapabilityGrantInput{
		ProjectID: project.ID, Capability: "client.storage",
		ConfigJSON: `{"max_bytes":4096}`, Status: PageCapabilityRequested,
		RequestedBy: "automation-key:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	approve := UpsertPageCapabilityGrantInput{
		ProjectID: project.ID, Capability: requested.Capability,
		ConfigJSON: requested.ConfigJSON, Status: PageCapabilityApproved,
		RequestedBy: requested.RequestedBy, ApprovedBy: "automation-key:1",
		ExpectedWorkingRevisionID: revision.ID,
		ExpectedCurrentStatus:     PageCapabilityRequested,
		ExpectedCurrentConfigJSON: requested.ConfigJSON,
	}
	wrongConfig := approve
	wrongConfig.ExpectedCurrentConfigJSON = `{"max_bytes":8192}`
	if _, _, err := st.UpsertPageCapabilityGrantIdempotent(
		wrongConfig, "apply", "cap-cas-wrong-config", SHA256Hex([]byte("wrong-config")),
	); !errors.Is(err, ErrPageInvalid) {
		t.Fatalf("wrong expected config error=%v", err)
	}
	wrongRevision := approve
	wrongRevision.ExpectedWorkingRevisionID = revision.ID + 1
	if _, _, err := st.UpsertPageCapabilityGrantIdempotent(
		wrongRevision, "apply", "cap-cas-wrong-revision", SHA256Hex([]byte("wrong-revision")),
	); !errors.Is(err, ErrPageRevisionConflict) {
		t.Fatalf("wrong expected revision error=%v", err)
	}
	current, err := st.GetPageCapabilityGrant(project.ID, requested.Capability)
	if err != nil || current == nil || current.Status != PageCapabilityRequested ||
		current.ConfigJSON != requested.ConfigJSON {
		t.Fatalf("failed CAS mutated grant: %#v err=%v", current, err)
	}
}

func openPagePlatformTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func createPagePlatformTestPost(t *testing.T, st *Store, slug, title string) *Post {
	t.Helper()
	post := &Post{
		Type:       "page",
		Slug:       slug,
		Title:      title,
		Content:    "legacy standard content",
		Status:     "draft",
		EditorMode: "markdown",
		Lang:       "zh",
		Author:     "tester",
	}
	id, err := st.CreatePost(post)
	if err != nil {
		t.Fatalf("create page %s: %v", slug, err)
	}
	post, err = st.GetPostByID(id)
	if err != nil || post == nil {
		t.Fatalf("read page %s: post=%+v err=%v", slug, post, err)
	}
	return post
}

func createPagePlatformTestProject(t *testing.T, st *Store, postID int64) *PageProject {
	t.Helper()
	project, err := st.CreatePageProject(CreatePageProjectInput{
		PostID: postID, Mode: PageModeComposition, SchemaVersion: 1,
		ShellMode: PageShellSite, CreatedBy: PageOriginAdmin,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func assertPageNotPublished(
	t *testing.T,
	st *Store,
	projectID, postID int64,
	wantSlug, wantTitle string,
) {
	t.Helper()
	project, err := st.GetPageProject(projectID)
	if err != nil || project.PublishedRevisionID != 0 {
		t.Fatalf("failed publication changed pointer: project=%+v err=%v", project, err)
	}
	post, err := st.GetPostByID(postID)
	if err != nil || post.Status != "draft" ||
		post.Slug != wantSlug || post.Title != wantTitle {
		t.Fatalf("failed publication changed post: post=%+v err=%v", post, err)
	}
	publications, err := st.ListPagePublications(projectID, 10)
	if err != nil || len(publications) != 0 {
		t.Fatalf("failed publication left history: rows=%+v err=%v", publications, err)
	}
}

func dropPagePlatformSchema(t *testing.T, st *Store) {
	t.Helper()
	conn, err := st.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire migration fixture connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable fixture foreign keys: %v", err)
	}
	for _, trigger := range pagePlatformRequiredTriggers {
		if _, err := conn.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS `+trigger); err != nil {
			t.Fatalf("drop fixture trigger %s: %v", trigger, err)
		}
	}
	for _, table := range []string{
		"page_publication_mutation_receipts",
		"page_route_reservations",
		"page_publications",
		"page_capability_mutation_receipts",
		"page_capability_grants",
		"page_asset_upload_requests",
		"page_assets",
		"page_build_create_receipts",
		"page_builds",
		"page_project_revisions",
		"page_projects",
		"store_schema_migrations",
	} {
		if _, err := conn.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+table); err != nil {
			t.Fatalf("drop fixture table %s: %v", table, err)
		}
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatalf("restore fixture foreign keys: %v", err)
	}
}

// downgradePagePlatformFixture removes every object introduced after version.
// Keeping newer tables around would make an "old schema" test pass without
// proving that the current migration can actually recreate those objects.
func downgradePagePlatformFixture(t *testing.T, st *Store, version int) {
	t.Helper()
	if version < 1 || version >= pagePlatformSchemaVersion {
		t.Fatalf("unsupported historical fixture version %d", version)
	}
	conn, err := st.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire historical fixture connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatalf("disable historical fixture foreign keys: %v", err)
	}
	if version < 6 {
		if _, err := conn.ExecContext(context.Background(),
			`DROP TABLE IF EXISTS page_publication_mutation_receipts`); err != nil {
			t.Fatalf("drop v6 publication receipts: %v", err)
		}
	}
	if version < 5 {
		if _, err := conn.ExecContext(context.Background(),
			`DROP TABLE IF EXISTS page_build_create_receipts`); err != nil {
			t.Fatalf("drop v5 build receipts: %v", err)
		}
	}
	if version < 4 {
		for _, trigger := range []string{
			"content_type_page_route_insert",
			"content_type_page_route_update",
			"page_content_type_route_insert",
			"page_content_type_route_update",
		} {
			if _, err := conn.ExecContext(context.Background(),
				`DROP TRIGGER IF EXISTS `+trigger); err != nil {
				t.Fatalf("drop v4 trigger %s: %v", trigger, err)
			}
		}
	}
	if version < 3 {
		if _, err := conn.ExecContext(context.Background(),
			`DROP TABLE IF EXISTS page_capability_mutation_receipts`); err != nil {
			t.Fatalf("drop v3 capability receipts: %v", err)
		}
	}
	if version < 2 {
		if _, err := conn.ExecContext(context.Background(),
			`DROP TABLE IF EXISTS page_asset_upload_requests`); err != nil {
			t.Fatalf("drop v2 upload receipts: %v", err)
		}
	}
	if _, err := conn.ExecContext(context.Background(), `
		UPDATE store_schema_migrations SET version=?
		WHERE name='page_platform'`, version); err != nil {
		t.Fatalf("mark v%d fixture: %v", version, err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatalf("restore historical fixture foreign keys: %v", err)
	}
}
