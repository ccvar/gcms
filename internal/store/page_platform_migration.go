package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

const pagePlatformSchemaVersion = 6

const pagePlatformMigrationTableDDL = `CREATE TABLE IF NOT EXISTS store_schema_migrations (
	name       TEXT PRIMARY KEY,
	version    INTEGER NOT NULL,
	applied_at TEXT NOT NULL
)`

// pagePlatformSchemaV1 intentionally lives outside the legacy bootstrap schema.
// Unlike the historic best-effort column additions, this migration is atomic and
// validated before its version marker is committed.
var pagePlatformSchemaV1 = []string{
	`CREATE TABLE IF NOT EXISTS page_projects (
		id                    INTEGER PRIMARY KEY AUTOINCREMENT,
		post_id               INTEGER NOT NULL UNIQUE REFERENCES posts(id) ON DELETE CASCADE,
		mode                  TEXT NOT NULL CHECK(mode IN ('composition','app')),
		schema_version        INTEGER NOT NULL CHECK(schema_version > 0),
		working_revision_id   INTEGER REFERENCES page_project_revisions(id) ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED,
		published_revision_id INTEGER REFERENCES page_project_revisions(id) ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED,
		shell_mode            TEXT NOT NULL CHECK(shell_mode IN ('site','minimal','none')),
		build_status          TEXT NOT NULL CHECK(build_status IN ('idle','validating','ready','failed')),
		created_by            TEXT NOT NULL CHECK(created_by IN ('admin','api','pilot','restore')),
		created_at            TEXT NOT NULL,
		updated_at            TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS page_project_revisions (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id         INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		revision_no        INTEGER NOT NULL CHECK(revision_no > 0),
		parent_revision_id INTEGER REFERENCES page_project_revisions(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
		revision_kind      TEXT NOT NULL CHECK(revision_kind IN ('standard_baseline','composition','app')),
		page_meta_json     TEXT NOT NULL,
		page_meta_hash     TEXT NOT NULL,
		manifest_json      TEXT NOT NULL,
		manifest_hash      TEXT NOT NULL,
		standard_content   TEXT NOT NULL DEFAULT '',
		source_bundle_ref  TEXT NOT NULL DEFAULT '',
		source_hash        TEXT NOT NULL DEFAULT '',
		origin             TEXT NOT NULL CHECK(origin IN ('admin','pilot','api','restore')),
		actor_id           TEXT NOT NULL DEFAULT '',
		conversation_id    TEXT NOT NULL DEFAULT '',
		request_id         TEXT CHECK(request_id IS NULL OR length(request_id) > 0),
		summary            TEXT NOT NULL DEFAULT '',
		validation_json    TEXT NOT NULL DEFAULT '{}',
		created_at         TEXT NOT NULL,
		UNIQUE(project_id, revision_no)
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_page_revisions_request
		ON page_project_revisions(project_id, request_id)
		WHERE request_id IS NOT NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_page_revisions_baseline
		ON page_project_revisions(project_id)
		WHERE revision_kind='standard_baseline'`,
	`CREATE INDEX IF NOT EXISTS idx_page_revisions_project_created
		ON page_project_revisions(project_id, revision_no DESC)`,
	`CREATE TABLE IF NOT EXISTS page_builds (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id       INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		revision_id      INTEGER NOT NULL REFERENCES page_project_revisions(id) ON DELETE CASCADE,
		status           TEXT NOT NULL CHECK(status IN ('queued','validating','ready','failed')),
		artifact_ref     TEXT NOT NULL DEFAULT '',
		artifact_hash    TEXT NOT NULL DEFAULT '',
		diagnostics_json TEXT NOT NULL DEFAULT '[]',
		runtime_version  TEXT NOT NULL,
		started_at       TEXT,
		finished_at      TEXT,
		created_at       TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_page_builds_revision
		ON page_builds(project_id, revision_id, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_page_builds_status
		ON page_builds(status, created_at)`,
	`CREATE TABLE IF NOT EXISTS page_assets (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id      INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		logical_key     TEXT NOT NULL,
		version_no      INTEGER NOT NULL CHECK(version_no > 0),
		storage_ref     TEXT NOT NULL,
		media_type      TEXT NOT NULL,
		byte_size       INTEGER NOT NULL CHECK(byte_size >= 0),
		sha256          TEXT NOT NULL,
		origin          TEXT NOT NULL CHECK(origin IN ('upload','pilot','generated','library')),
		provenance_json TEXT NOT NULL DEFAULT '{}',
		width           INTEGER CHECK(width IS NULL OR width >= 0),
		height          INTEGER CHECK(height IS NULL OR height >= 0),
		created_at      TEXT NOT NULL,
		UNIQUE(project_id, logical_key, version_no),
		UNIQUE(project_id, sha256)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_page_assets_project_key
		ON page_assets(project_id, logical_key, version_no DESC)`,
	`CREATE TABLE IF NOT EXISTS page_capability_grants (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id   INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		capability   TEXT NOT NULL,
		config_json  TEXT NOT NULL DEFAULT '{}',
		status       TEXT NOT NULL CHECK(status IN ('requested','approved','denied','revoked')),
		requested_by TEXT NOT NULL,
		approved_by  TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL,
		UNIQUE(project_id, capability)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_page_capabilities_status
		ON page_capability_grants(project_id, status)`,
	`CREATE TABLE IF NOT EXISTS page_publications (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id         INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		revision_id        INTEGER NOT NULL REFERENCES page_project_revisions(id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
		action             TEXT NOT NULL CHECK(action IN ('publish','schedule','rollback','unpublish')),
		status             TEXT NOT NULL CHECK(status IN ('pending','approved','published','cancelled','failed')),
		approval_id        TEXT,
		scheduled_at       TEXT,
		published_at       TEXT,
		actor_id           TEXT NOT NULL,
		origin             TEXT NOT NULL CHECK(origin IN ('admin','pilot','api','restore','scheduler')),
		request_id         TEXT CHECK(request_id IS NULL OR length(request_id) > 0),
		deployment_job_id  TEXT,
		delivery_status    TEXT NOT NULL DEFAULT '' CHECK(delivery_status IN ('','queued','live','failed')),
		page_meta_hash     TEXT NOT NULL,
		manifest_hash      TEXT NOT NULL,
		data_snapshot_hash TEXT NOT NULL DEFAULT '',
		artifact_hash      TEXT NOT NULL DEFAULT '',
		runtime_version    TEXT NOT NULL,
		created_at         TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_page_publications_request
		ON page_publications(project_id, request_id)
		WHERE request_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_page_publications_project
		ON page_publications(project_id, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_page_publications_schedule
		ON page_publications(status, scheduled_at)
		WHERE scheduled_at IS NOT NULL`,
	`CREATE TABLE IF NOT EXISTS page_route_reservations (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id  INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		revision_id INTEGER NOT NULL REFERENCES page_project_revisions(id) ON DELETE CASCADE,
		lang        TEXT NOT NULL,
		slug        TEXT NOT NULL,
		created_at  TEXT NOT NULL,
		UNIQUE(lang, slug)
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_page_route_project
		ON page_route_reservations(project_id)`,

	// Cross-table checks which cannot be expressed by a simple SQLite FK.
	`CREATE TRIGGER IF NOT EXISTS page_projects_post_type_insert
		BEFORE INSERT ON page_projects
		WHEN NOT EXISTS (SELECT 1 FROM posts WHERE id=NEW.post_id AND type='page')
		BEGIN
			SELECT RAISE(ABORT, 'page_project_post_must_be_page');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_projects_post_type_update
		BEFORE UPDATE OF post_id ON page_projects
		WHEN NOT EXISTS (SELECT 1 FROM posts WHERE id=NEW.post_id AND type='page')
		BEGIN
			SELECT RAISE(ABORT, 'page_project_post_must_be_page');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_revision_parent_project_insert
		BEFORE INSERT ON page_project_revisions
		WHEN NEW.parent_revision_id IS NOT NULL
		 AND NOT EXISTS (
			SELECT 1 FROM page_project_revisions
			WHERE id=NEW.parent_revision_id AND project_id=NEW.project_id
		 )
		BEGIN
			SELECT RAISE(ABORT, 'page_revision_parent_project_mismatch');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_revision_kind_insert
		BEFORE INSERT ON page_project_revisions
		WHEN NEW.revision_kind <> 'standard_baseline'
		 AND NOT EXISTS (
			SELECT 1 FROM page_projects
			WHERE id=NEW.project_id AND mode=NEW.revision_kind
		 )
		BEGIN
			SELECT RAISE(ABORT, 'page_revision_kind_mismatch');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_project_working_revision_update
		BEFORE UPDATE OF working_revision_id ON page_projects
		WHEN NEW.working_revision_id IS NOT NULL
		 AND NOT EXISTS (
			SELECT 1 FROM page_project_revisions
			WHERE id=NEW.working_revision_id AND project_id=NEW.id
		 )
		BEGIN
			SELECT RAISE(ABORT, 'page_working_revision_project_mismatch');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_project_published_revision_update
		BEFORE UPDATE OF published_revision_id ON page_projects
		WHEN NEW.published_revision_id IS NOT NULL
		 AND NOT EXISTS (
			SELECT 1 FROM page_project_revisions
			WHERE id=NEW.published_revision_id AND project_id=NEW.id
		 )
		BEGIN
			SELECT RAISE(ABORT, 'page_published_revision_project_mismatch');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_build_revision_project_insert
		BEFORE INSERT ON page_builds
		WHEN NOT EXISTS (
			SELECT 1 FROM page_project_revisions
			WHERE id=NEW.revision_id AND project_id=NEW.project_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'page_build_revision_project_mismatch');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_publication_revision_project_insert
		BEFORE INSERT ON page_publications
		WHEN NOT EXISTS (
			SELECT 1 FROM page_project_revisions
			WHERE id=NEW.revision_id AND project_id=NEW.project_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'page_publication_revision_project_mismatch');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_route_revision_project_insert
		BEFORE INSERT ON page_route_reservations
		WHEN NOT EXISTS (
			SELECT 1 FROM page_project_revisions
			WHERE id=NEW.revision_id AND project_id=NEW.project_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'page_route_revision_project_mismatch');
		END`,
}

// V2 records asset-upload idempotency independently from content-addressed
// asset deduplication. A single blob may legitimately be uploaded under
// several request IDs, while reusing one request ID for different multipart
// input must fail even across process restarts.
var pagePlatformSchemaV2 = []string{
	`CREATE TABLE IF NOT EXISTS page_asset_upload_requests (
		project_id   INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		request_id   TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		asset_id      INTEGER NOT NULL REFERENCES page_assets(id) ON DELETE CASCADE,
		created_at    TEXT NOT NULL,
		PRIMARY KEY(project_id, request_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_page_asset_upload_requests_asset
		ON page_asset_upload_requests(asset_id)`,
}

// V3 makes capability mutations idempotent across process restarts. Approval
// tokens remain one-time and process-private, so a byte-identical retry must be
// recognized from durable storage before attempting to consume that token
// again.
var pagePlatformSchemaV3 = []string{
	`CREATE TABLE IF NOT EXISTS page_capability_mutation_receipts (
		project_id    INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		request_id    TEXT NOT NULL,
		request_hash  TEXT NOT NULL,
		operation     TEXT NOT NULL,
		capability    TEXT NOT NULL,
		grant_json    TEXT NOT NULL,
		created_at    TEXT NOT NULL,
		PRIMARY KEY(project_id, request_id)
	)`,
}

// V4 closes the top-level route ownership race in both directions. Friendly
// checks run in the web layer, while these triggers remain the transactional
// guard for concurrent or direct store writers.
var pagePlatformSchemaV4 = []string{
	`CREATE TRIGGER IF NOT EXISTS content_type_page_route_insert
		BEFORE INSERT ON content_types
		WHEN EXISTS (
			SELECT 1 FROM posts
			WHERE type='page'
			  AND (
				lower(trim(slug))=lower(trim(NEW.key))
				OR lower(trim(slug))=lower(trim(NEW.url_prefix))
			  )
		)
		OR EXISTS (
			SELECT 1 FROM page_route_reservations
			WHERE lower(trim(slug))=lower(trim(NEW.key))
			   OR lower(trim(slug))=lower(trim(NEW.url_prefix))
		)
		BEGIN
			SELECT RAISE(ABORT, 'content_type_page_route_conflict');
		END`,
	`CREATE TRIGGER IF NOT EXISTS content_type_page_route_update
		BEFORE UPDATE OF key,url_prefix ON content_types
		WHEN (
			lower(trim(NEW.key))<>lower(trim(OLD.key))
			OR lower(trim(NEW.url_prefix))<>lower(trim(OLD.url_prefix))
		)
		AND (
			EXISTS (
				SELECT 1 FROM posts
				WHERE type='page'
				  AND (
					lower(trim(slug))=lower(trim(NEW.key))
					OR lower(trim(slug))=lower(trim(NEW.url_prefix))
				  )
			)
			OR EXISTS (
				SELECT 1 FROM page_route_reservations
				WHERE lower(trim(slug))=lower(trim(NEW.key))
				   OR lower(trim(slug))=lower(trim(NEW.url_prefix))
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'content_type_page_route_conflict');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_content_type_route_insert
		BEFORE INSERT ON posts
		WHEN NEW.type='page'
		AND (
			EXISTS (
				SELECT 1 FROM content_types
				WHERE lower(trim(key))=lower(trim(NEW.slug))
				   OR lower(trim(url_prefix))=lower(trim(NEW.slug))
			)
			OR EXISTS (
				SELECT 1 FROM page_route_reservations r
				WHERE r.lang=NEW.lang AND r.slug=NEW.slug
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'page_route_conflict');
		END`,
	`CREATE TRIGGER IF NOT EXISTS page_content_type_route_update
		BEFORE UPDATE OF type,lang,slug ON posts
		WHEN NEW.type='page'
		AND (
			NEW.type<>OLD.type
			OR NEW.lang<>OLD.lang
			OR NEW.slug<>OLD.slug
		)
		AND (
			EXISTS (
				SELECT 1 FROM content_types
				WHERE lower(trim(key))=lower(trim(NEW.slug))
				   OR lower(trim(url_prefix))=lower(trim(NEW.slug))
			)
			OR EXISTS (
				SELECT 1 FROM page_route_reservations r
				WHERE r.lang=NEW.lang AND r.slug=NEW.slug
				  AND NOT EXISTS (
					SELECT 1 FROM page_projects p
					WHERE p.id=r.project_id AND p.post_id=NEW.id
				  )
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'page_route_conflict');
		END`,
}

// V5 makes page build creation a durable, request-bound operation. The
// nullable build_id is used only while the owning transaction creates (or
// selects) the immutable build; no successful Store operation commits a
// receipt without a build. Reserving the receipt before any reads also makes
// the primary key the cross-goroutine/process serialization point.
var pagePlatformSchemaV5 = []string{
	`CREATE TABLE IF NOT EXISTS page_build_create_receipts (
		project_id   INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		request_id   TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		build_id     INTEGER REFERENCES page_builds(id) ON DELETE CASCADE,
		created_at   TEXT NOT NULL,
		PRIMARY KEY(project_id, request_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_page_build_create_receipts_build
		ON page_build_create_receipts(build_id)`,
}

// V6 persists the exact, normalized automation request separately from the
// publication row. This receipt is immutable and therefore remains usable
// after the work pointer, live bindings, route state, delivery job, or native
// unlock state changes.
var pagePlatformSchemaV6 = []string{
	`CREATE TABLE IF NOT EXISTS page_publication_mutation_receipts (
		project_id     INTEGER NOT NULL REFERENCES page_projects(id) ON DELETE CASCADE,
		request_id     TEXT NOT NULL,
		request_hash   TEXT NOT NULL,
		publication_id INTEGER NOT NULL REFERENCES page_publications(id) ON DELETE CASCADE,
		created_at     TEXT NOT NULL,
		PRIMARY KEY(project_id, request_id),
		UNIQUE(publication_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_page_publication_receipts_publication
		ON page_publication_mutation_receipts(publication_id)`,
}

var pagePlatformExpectedColumns = map[string][]string{
	"store_schema_migrations": {
		"name", "version", "applied_at",
	},
	"page_projects": {
		"id", "post_id", "mode", "schema_version", "working_revision_id",
		"published_revision_id", "shell_mode", "build_status", "created_by",
		"created_at", "updated_at",
	},
	"page_project_revisions": {
		"id", "project_id", "revision_no", "parent_revision_id", "revision_kind",
		"page_meta_json", "page_meta_hash", "manifest_json", "manifest_hash",
		"standard_content", "source_bundle_ref", "source_hash", "origin", "actor_id",
		"conversation_id", "request_id", "summary", "validation_json", "created_at",
	},
	"page_builds": {
		"id", "project_id", "revision_id", "status", "artifact_ref", "artifact_hash",
		"diagnostics_json", "runtime_version", "started_at", "finished_at", "created_at",
	},
	"page_build_create_receipts": {
		"project_id", "request_id", "request_hash", "build_id", "created_at",
	},
	"page_assets": {
		"id", "project_id", "logical_key", "version_no", "storage_ref", "media_type",
		"byte_size", "sha256", "origin", "provenance_json", "width", "height", "created_at",
	},
	"page_asset_upload_requests": {
		"project_id", "request_id", "request_hash", "asset_id", "created_at",
	},
	"page_capability_grants": {
		"id", "project_id", "capability", "config_json", "status", "requested_by",
		"approved_by", "created_at", "updated_at",
	},
	"page_capability_mutation_receipts": {
		"project_id", "request_id", "request_hash", "operation", "capability",
		"grant_json", "created_at",
	},
	"page_publications": {
		"id", "project_id", "revision_id", "action", "status", "approval_id",
		"scheduled_at", "published_at", "actor_id", "origin", "request_id",
		"deployment_job_id", "delivery_status", "page_meta_hash", "manifest_hash",
		"data_snapshot_hash", "artifact_hash", "runtime_version", "created_at",
	},
	"page_publication_mutation_receipts": {
		"project_id", "request_id", "request_hash", "publication_id", "created_at",
	},
	"page_route_reservations": {
		"id", "project_id", "revision_id", "lang", "slug", "created_at",
	},
}

var pagePlatformRequiredIndexes = []string{
	"idx_page_revisions_request",
	"idx_page_revisions_baseline",
	"idx_page_revisions_project_created",
	"idx_page_builds_revision",
	"idx_page_builds_status",
	"idx_page_build_create_receipts_build",
	"idx_page_assets_project_key",
	"idx_page_asset_upload_requests_asset",
	"idx_page_capabilities_status",
	"idx_page_publications_request",
	"idx_page_publications_project",
	"idx_page_publications_schedule",
	"idx_page_publication_receipts_publication",
	"idx_page_route_project",
}

var pagePlatformRequiredTriggers = []string{
	"page_projects_post_type_insert",
	"page_projects_post_type_update",
	"page_revision_parent_project_insert",
	"page_revision_kind_insert",
	"page_project_working_revision_update",
	"page_project_published_revision_update",
	"page_build_revision_project_insert",
	"page_publication_revision_project_insert",
	"page_route_revision_project_insert",
	"content_type_page_route_insert",
	"content_type_page_route_update",
	"page_content_type_route_insert",
	"page_content_type_route_update",
}

func (s *Store) migratePagePlatform() (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(pagePlatformMigrationTableDDL); err != nil {
		return fmt.Errorf("create schema migration registry: %w", err)
	}
	var current int
	versionErr := tx.QueryRow(
		`SELECT version FROM store_schema_migrations WHERE name='page_platform'`,
	).Scan(&current)
	switch {
	case versionErr == sql.ErrNoRows:
		current = 0
	case versionErr != nil:
		return fmt.Errorf("read page schema version: %w", versionErr)
	case current > pagePlatformSchemaVersion:
		return fmt.Errorf("page schema version %d is newer than supported version %d", current, pagePlatformSchemaVersion)
	}

	for _, statement := range pagePlatformSchemaV1 {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("apply page schema: %w", err)
		}
	}
	for _, statement := range pagePlatformSchemaV2 {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("apply page schema v2: %w", err)
		}
	}
	for _, statement := range pagePlatformSchemaV3 {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("apply page schema v3: %w", err)
		}
	}
	for _, statement := range pagePlatformSchemaV4 {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("apply page schema v4: %w", err)
		}
	}
	for _, statement := range pagePlatformSchemaV5 {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("apply page schema v5: %w", err)
		}
	}
	for _, statement := range pagePlatformSchemaV6 {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("apply page schema v6: %w", err)
		}
	}
	if err = validatePagePlatformSchema(tx); err != nil {
		return err
	}
	if current < pagePlatformSchemaVersion {
		if _, err = tx.Exec(`
			INSERT INTO store_schema_migrations(name,version,applied_at)
			VALUES('page_platform',?,?)
			ON CONFLICT(name) DO UPDATE SET version=excluded.version,applied_at=excluded.applied_at`,
			pagePlatformSchemaVersion, fmtTime(time.Now())); err != nil {
			return fmt.Errorf("record page schema version: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit page schema: %w", err)
	}
	return nil
}

func validatePagePlatformSchema(tx *sql.Tx) error {
	tableNames := make([]string, 0, len(pagePlatformExpectedColumns))
	for name := range pagePlatformExpectedColumns {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)
	for _, table := range tableNames {
		columns, err := tableColumns(tx, table)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		var missing []string
		for _, expected := range pagePlatformExpectedColumns[table] {
			if _, ok := columns[expected]; !ok {
				missing = append(missing, expected)
			}
		}
		if len(missing) != 0 {
			return fmt.Errorf("table %s is incomplete; missing columns: %s", table, strings.Join(missing, ", "))
		}
	}

	for _, index := range pagePlatformRequiredIndexes {
		var name string
		err := tx.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`,
			index,
		).Scan(&name)
		if err == sql.ErrNoRows {
			return fmt.Errorf("required index %s is missing", index)
		}
		if err != nil {
			return fmt.Errorf("inspect index %s: %w", index, err)
		}
	}
	for _, trigger := range pagePlatformRequiredTriggers {
		var name string
		err := tx.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='trigger' AND name=?`,
			trigger,
		).Scan(&name)
		if err == sql.ErrNoRows {
			return fmt.Errorf("required trigger %s is missing", trigger)
		}
		if err != nil {
			return fmt.Errorf("inspect trigger %s: %w", trigger, err)
		}
	}

	for _, table := range tableNames {
		rows, err := tx.Query(`PRAGMA foreign_key_check(` + table + `)`)
		if err != nil {
			return fmt.Errorf("foreign key check %s: %w", table, err)
		}
		var violation string
		if rows.Next() {
			var child string
			var rowID sql.NullInt64
			var parent string
			var foreignKeyID int
			if scanErr := rows.Scan(&child, &rowID, &parent, &foreignKeyID); scanErr != nil {
				_ = rows.Close()
				return fmt.Errorf("foreign key check %s: %w", table, scanErr)
			}
			violation = fmt.Sprintf("%s row %d references %s (fk %d)", child, rowID.Int64, parent, foreignKeyID)
		}
		if closeErr := rows.Close(); closeErr != nil {
			return fmt.Errorf("foreign key check %s: %w", table, closeErr)
		}
		if violation != "" {
			return fmt.Errorf("foreign key violation: %s", violation)
		}
	}
	return nil
}

func tableColumns(tx *sql.Tx, table string) (map[string]struct{}, error) {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = struct{}{}
	}
	return columns, rows.Err()
}
