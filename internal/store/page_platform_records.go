package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type CreatePageBuildInput struct {
	ProjectID       int64
	RevisionID      int64
	Status          string
	ArtifactRef     string
	ArtifactHash    string
	DiagnosticsJSON string
	RuntimeVersion  string
	StartedAt       time.Time
	FinishedAt      time.Time
}

type CreatePageBuildIdempotentInput struct {
	CreatePageBuildInput
	RequestID   string
	RequestHash string
}

func normalizeCreatePageBuildInput(input CreatePageBuildInput) (CreatePageBuildInput, error) {
	if input.ProjectID <= 0 || input.RevisionID <= 0 {
		return input, fmt.Errorf("%w: project_id and revision_id are required", ErrPageInvalid)
	}
	if input.Status == "" {
		input.Status = PageBuildQueued
	}
	if !validPageBuildStatus(input.Status) || strings.TrimSpace(input.RuntimeVersion) == "" {
		return input, fmt.Errorf("%w: invalid build status or runtime version", ErrPageInvalid)
	}
	input.RuntimeVersion = strings.TrimSpace(input.RuntimeVersion)
	input.ArtifactRef = strings.TrimSpace(input.ArtifactRef)
	diagnostics := strings.TrimSpace(input.DiagnosticsJSON)
	if diagnostics == "" {
		diagnostics = "[]"
	}
	var err error
	if input.DiagnosticsJSON, err = canonicalJSONArray(diagnostics, "diagnostics"); err != nil {
		return input, err
	}
	input.ArtifactHash = strings.TrimSpace(input.ArtifactHash)
	if err := validateSHA256(input.ArtifactHash, "artifact_hash", true); err != nil {
		return input, err
	}
	if input.ArtifactRef == "" && input.ArtifactHash != "" {
		return input, fmt.Errorf("%w: artifact_hash requires artifact_ref", ErrPageInvalid)
	}
	return input, nil
}

func createPageBuildTx(tx *sql.Tx, input CreatePageBuildInput) (*PageBuild, error) {
	project, err := getPageProject(tx, `WHERE id=?`, input.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPageProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	revision, err := getPageProjectRevision(tx, input.RevisionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && revision.ProjectID != input.ProjectID) {
		return nil, ErrPageRevisionNotFound
	}
	if err != nil {
		return nil, err
	}
	if input.Status == PageBuildReady && project.Mode == PageModeApp &&
		(input.ArtifactRef == "" || input.ArtifactHash == "") {
		return nil, fmt.Errorf("%w: ready app build requires an immutable artifact", ErrPageInvalid)
	}

	now := fmtTime(time.Now())
	result, err := tx.Exec(`
		INSERT INTO page_builds(
			project_id,revision_id,status,artifact_ref,artifact_hash,diagnostics_json,
			runtime_version,started_at,finished_at,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		input.ProjectID, input.RevisionID, input.Status, input.ArtifactRef,
		input.ArtifactHash, input.DiagnosticsJSON, input.RuntimeVersion,
		nullTime(input.StartedAt), nullTime(input.FinishedAt), now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		UPDATE page_projects
		SET build_status=?,updated_at=?
		WHERE id=? AND working_revision_id=?`,
		projectBuildStatus(input.Status), now, input.ProjectID, input.RevisionID); err != nil {
		return nil, err
	}
	build, err := getPageBuild(tx, id)
	if err != nil {
		return nil, err
	}
	return build, nil
}

func (s *Store) CreatePageBuild(input CreatePageBuildInput) (*PageBuild, error) {
	normalized, err := normalizeCreatePageBuildInput(input)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	build, err := createPageBuildTx(tx, normalized)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return build, nil
}

// CreatePageBuildIdempotent creates or reuses a ready build and records the
// resolved build under (project_id, request_id) in the same transaction.
//
// The receipt INSERT is deliberately the transaction's first database
// operation. SQLite therefore serializes concurrent writers at the durable
// primary key before either can create a build. An identical retry returns the
// originally recorded build; the same key with a different canonical request
// hash fails with ErrPageIdempotencyConflict.
func (s *Store) CreatePageBuildIdempotent(
	input CreatePageBuildIdempotentInput,
) (*PageBuild, bool, bool, error) {
	normalized, err := normalizeCreatePageBuildInput(input.CreatePageBuildInput)
	if err != nil {
		return nil, false, false, err
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	if input.RequestID == "" || len(input.RequestID) > 200 {
		return nil, false, false, fmt.Errorf("%w: invalid build request id", ErrPageInvalid)
	}
	if err := validateSHA256(input.RequestHash, "request_hash", false); err != nil {
		return nil, false, false, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, false, err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.Exec(`
		INSERT INTO page_build_create_receipts(
			project_id,request_id,request_hash,build_id,created_at
		) VALUES(?,?,?,?,?)
		ON CONFLICT(project_id,request_id) DO NOTHING`,
		normalized.ProjectID, input.RequestID, input.RequestHash, nil, fmtTime(time.Now()),
	)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return nil, false, false, ErrPageProjectNotFound
		}
		return nil, false, false, err
	}
	reserved, err := result.RowsAffected()
	if err != nil {
		return nil, false, false, err
	}
	if reserved == 0 {
		var requestHash string
		var buildID sql.NullInt64
		if err := tx.QueryRow(`
			SELECT request_hash,build_id
			FROM page_build_create_receipts
			WHERE project_id=? AND request_id=?`,
			normalized.ProjectID, input.RequestID,
		).Scan(&requestHash, &buildID); err != nil {
			return nil, false, false, err
		}
		if requestHash != input.RequestHash {
			return nil, false, false, &PageIdempotencyConflictError{RequestID: input.RequestID}
		}
		if !buildID.Valid || buildID.Int64 <= 0 {
			return nil, false, false, fmt.Errorf("%w: incomplete build idempotency receipt", ErrPageInvalid)
		}
		build, err := getPageBuild(tx, buildID.Int64)
		if err != nil {
			return nil, false, false, err
		}
		return build, false, true, nil
	}

	build, err := reusableReadyPageBuildTx(tx, normalized)
	created := false
	if errors.Is(err, sql.ErrNoRows) {
		build, err = createPageBuildTx(tx, normalized)
		created = err == nil
	}
	if err != nil {
		return nil, false, false, err
	}
	update, err := tx.Exec(`
		UPDATE page_build_create_receipts
		SET build_id=?
		WHERE project_id=? AND request_id=? AND request_hash=? AND build_id IS NULL`,
		build.ID, normalized.ProjectID, input.RequestID, input.RequestHash,
	)
	if err != nil {
		return nil, false, false, err
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return nil, false, false, err
	}
	if affected != 1 {
		return nil, false, false, fmt.Errorf("%w: build receipt changed while creating", ErrPageRevisionConflict)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, false, err
	}
	return build, created, false, nil
}

// GetPageBuildCreateReceipt looks up a durable build response without
// reserving a new key. It lets expensive, side-effecting preparation avoid
// running again on a known replay while CreatePageBuildIdempotent remains the
// authoritative race-safe reservation.
func (s *Store) GetPageBuildCreateReceipt(
	projectID int64,
	requestID, requestHash string,
) (*PageBuild, bool, error) {
	requestID = strings.TrimSpace(requestID)
	requestHash = strings.TrimSpace(requestHash)
	if projectID <= 0 || requestID == "" || len(requestID) > 200 {
		return nil, false, fmt.Errorf("%w: invalid build request id", ErrPageInvalid)
	}
	if err := validateSHA256(requestHash, "request_hash", false); err != nil {
		return nil, false, err
	}
	var storedHash string
	var buildID sql.NullInt64
	err := s.db.QueryRow(`
		SELECT request_hash,build_id
		FROM page_build_create_receipts
		WHERE project_id=? AND request_id=?`,
		projectID, requestID,
	).Scan(&storedHash, &buildID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if storedHash != requestHash {
		return nil, false, &PageIdempotencyConflictError{RequestID: requestID}
	}
	if !buildID.Valid || buildID.Int64 <= 0 {
		return nil, false, fmt.Errorf("%w: incomplete build idempotency receipt", ErrPageInvalid)
	}
	build, err := getPageBuild(s.db, buildID.Int64)
	if err != nil {
		return nil, false, err
	}
	return build, true, nil
}

func reusableReadyPageBuildTx(tx *sql.Tx, input CreatePageBuildInput) (*PageBuild, error) {
	if input.Status != PageBuildReady {
		return nil, sql.ErrNoRows
	}
	return scanPageBuild(tx.QueryRow(`
		SELECT `+pageBuildColumns+` FROM page_builds
		WHERE project_id=? AND revision_id=? AND status=?
		  AND artifact_hash=? AND runtime_version=?
		ORDER BY id DESC LIMIT 1`,
		input.ProjectID, input.RevisionID, PageBuildReady,
		input.ArtifactHash, input.RuntimeVersion,
	))
}

type UpdatePageBuildInput struct {
	ID              int64
	ExpectedStatus  string
	Status          string
	ArtifactRef     string
	ArtifactHash    string
	DiagnosticsJSON string
	RuntimeVersion  string
	StartedAt       time.Time
	FinishedAt      time.Time
}

func (s *Store) UpdatePageBuild(input UpdatePageBuildInput) (*PageBuild, error) {
	if input.ID <= 0 || !validPageBuildStatus(input.Status) {
		return nil, fmt.Errorf("%w: invalid build update", ErrPageInvalid)
	}
	if input.ExpectedStatus != "" && !validPageBuildStatus(input.ExpectedStatus) {
		return nil, fmt.Errorf("%w: invalid expected build status", ErrPageInvalid)
	}
	diagnostics := strings.TrimSpace(input.DiagnosticsJSON)
	if diagnostics == "" {
		diagnostics = "[]"
	}
	var err error
	if input.DiagnosticsJSON, err = canonicalJSONArray(diagnostics, "diagnostics"); err != nil {
		return nil, err
	}
	input.ArtifactHash = strings.TrimSpace(input.ArtifactHash)
	if err := validateSHA256(input.ArtifactHash, "artifact_hash", true); err != nil {
		return nil, err
	}
	if input.ArtifactRef == "" && input.ArtifactHash != "" {
		return nil, fmt.Errorf("%w: artifact_hash requires artifact_ref", ErrPageInvalid)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getPageBuild(tx, input.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	if input.ExpectedStatus != "" && current.Status != input.ExpectedStatus {
		return nil, fmt.Errorf("%w: build status changed from %s to %s",
			ErrPageRevisionConflict, input.ExpectedStatus, current.Status)
	}
	if current.Status == PageBuildReady || current.Status == PageBuildFailed {
		return nil, fmt.Errorf("%w: terminal build records are immutable", ErrPageInvalid)
	}
	if !validPageBuildTransition(current.Status, input.Status) {
		return nil, fmt.Errorf("%w: build cannot transition from %s to %s",
			ErrPageInvalid, current.Status, input.Status)
	}
	var mode string
	if err := tx.QueryRow(`SELECT mode FROM page_projects WHERE id=?`, current.ProjectID).Scan(&mode); err != nil {
		return nil, err
	}
	if input.Status == PageBuildReady && mode == PageModeApp &&
		(input.ArtifactRef == "" || input.ArtifactHash == "") {
		return nil, fmt.Errorf("%w: ready app build requires an immutable artifact", ErrPageInvalid)
	}
	runtimeVersion := input.RuntimeVersion
	if runtimeVersion == "" {
		runtimeVersion = current.RuntimeVersion
	}
	result, err := tx.Exec(`
		UPDATE page_builds
		SET status=?,artifact_ref=?,artifact_hash=?,diagnostics_json=?,
			runtime_version=?,started_at=?,finished_at=?
		WHERE id=? AND status=?`,
		input.Status, input.ArtifactRef, input.ArtifactHash, input.DiagnosticsJSON,
		runtimeVersion, nullTime(input.StartedAt), nullTime(input.FinishedAt),
		input.ID, current.Status)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, fmt.Errorf("%w: build changed while updating", ErrPageRevisionConflict)
	}
	if _, err := tx.Exec(`
		UPDATE page_projects
		SET build_status=?,updated_at=?
		WHERE id=? AND working_revision_id=?`,
		projectBuildStatus(input.Status), fmtTime(time.Now()),
		current.ProjectID, current.RevisionID); err != nil {
		return nil, err
	}
	updated, err := getPageBuild(tx, input.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) GetPageBuild(id int64) (*PageBuild, error) {
	build, err := getPageBuild(s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return build, err
}

func (s *Store) ListPageBuilds(projectID, revisionID int64, limit int) ([]*PageBuild, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + pageBuildColumns + ` FROM page_builds WHERE project_id=?`
	args := []any{projectID}
	if revisionID > 0 {
		query += ` AND revision_id=?`
		args = append(args, revisionID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var builds []*PageBuild
	for rows.Next() {
		build, err := scanPageBuild(rows)
		if err != nil {
			return nil, err
		}
		builds = append(builds, build)
	}
	return builds, rows.Err()
}

const pageBuildColumns = `id,project_id,revision_id,status,artifact_ref,artifact_hash,
	diagnostics_json,runtime_version,started_at,finished_at,created_at`

func getPageBuild(q pageProjectQueryer, id int64) (*PageBuild, error) {
	return scanPageBuild(q.QueryRow(`SELECT `+pageBuildColumns+` FROM page_builds WHERE id=?`, id))
}

func scanPageBuild(sc interface{ Scan(...any) error }) (*PageBuild, error) {
	var build PageBuild
	var started, finished sql.NullString
	var created string
	if err := sc.Scan(
		&build.ID, &build.ProjectID, &build.RevisionID, &build.Status,
		&build.ArtifactRef, &build.ArtifactHash, &build.DiagnosticsJSON,
		&build.RuntimeVersion, &started, &finished, &created,
	); err != nil {
		return nil, err
	}
	build.StartedAt = parseTime(started)
	build.FinishedAt = parseTime(finished)
	build.CreatedAt = parseRequiredTime(created)
	return &build, nil
}

func validPageBuildStatus(status string) bool {
	return status == PageBuildQueued || status == PageBuildValidating ||
		status == PageBuildReady || status == PageBuildFailed
}

func validPageBuildTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case PageBuildQueued:
		return to == PageBuildValidating || to == PageBuildReady || to == PageBuildFailed
	case PageBuildValidating:
		return to == PageBuildReady || to == PageBuildFailed
	default:
		return false
	}
}

func projectBuildStatus(buildStatus string) string {
	switch buildStatus {
	case PageBuildReady:
		return PageProjectBuildReady
	case PageBuildFailed:
		return PageProjectBuildFailed
	default:
		return PageProjectBuildValidating
	}
}

type CreatePageAssetInput struct {
	ProjectID      int64
	RequestID      string
	RequestHash    string
	LogicalKey     string
	StorageRef     string
	MediaType      string
	ByteSize       int64
	SHA256         string
	Origin         string
	ProvenanceJSON string
	Width          int
	Height         int
}

func (s *Store) CreatePageAsset(input CreatePageAssetInput) (*PageAsset, bool, error) {
	input.LogicalKey = strings.TrimSpace(input.LogicalKey)
	input.StorageRef = strings.TrimSpace(input.StorageRef)
	input.MediaType = strings.TrimSpace(input.MediaType)
	input.SHA256 = strings.TrimSpace(input.SHA256)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	if input.ProjectID <= 0 || input.LogicalKey == "" || input.StorageRef == "" ||
		input.MediaType == "" || input.ByteSize < 0 || input.Width < 0 || input.Height < 0 ||
		!validPageAssetOrigin(input.Origin) {
		return nil, false, fmt.Errorf("%w: invalid asset attributes", ErrPageInvalid)
	}
	if (input.RequestID == "") != (input.RequestHash == "") || len(input.RequestID) > 200 {
		return nil, false, fmt.Errorf("%w: invalid asset request identity", ErrPageInvalid)
	}
	if input.RequestHash != "" {
		if err := validateSHA256(input.RequestHash, "request_hash", false); err != nil {
			return nil, false, err
		}
	}
	if err := validateSHA256(input.SHA256, "sha256", false); err != nil {
		return nil, false, err
	}
	provenance := strings.TrimSpace(input.ProvenanceJSON)
	if provenance == "" {
		provenance = "{}"
	}
	var err error
	if input.ProvenanceJSON, _, err = canonicalJSONObject(provenance, "provenance"); err != nil {
		return nil, false, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := getPageProject(tx, `WHERE id=?`, input.ProjectID); errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrPageProjectNotFound
	} else if err != nil {
		return nil, false, err
	}
	if input.RequestID != "" {
		var requestHash string
		var assetID int64
		err := tx.QueryRow(`
			SELECT request_hash,asset_id
			FROM page_asset_upload_requests
			WHERE project_id=? AND request_id=?`,
			input.ProjectID, input.RequestID,
		).Scan(&requestHash, &assetID)
		if err == nil {
			if requestHash != input.RequestHash {
				return nil, false, &PageIdempotencyConflictError{RequestID: input.RequestID}
			}
			asset, assetErr := getPageAsset(tx, assetID)
			if assetErr != nil {
				return nil, false, assetErr
			}
			return asset, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	existing, err := getPageAssetByHash(tx, input.ProjectID, input.SHA256)
	if err == nil {
		if err := recordPageAssetUploadRequest(tx, input, existing.ID); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var version int
	if err := tx.QueryRow(`
		SELECT COALESCE(MAX(version_no),0)+1 FROM page_assets
		WHERE project_id=? AND logical_key=?`,
		input.ProjectID, input.LogicalKey).Scan(&version); err != nil {
		return nil, false, err
	}
	result, err := tx.Exec(`
		INSERT INTO page_assets(
			project_id,logical_key,version_no,storage_ref,media_type,byte_size,
			sha256,origin,provenance_json,width,height,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		input.ProjectID, input.LogicalKey, version, input.StorageRef, input.MediaType,
		input.ByteSize, input.SHA256, input.Origin, input.ProvenanceJSON,
		nullNonNegativeDimension(input.Width), nullNonNegativeDimension(input.Height),
		fmtTime(time.Now()))
	if err != nil {
		return nil, false, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	asset, err := getPageAsset(tx, id)
	if err != nil {
		return nil, false, err
	}
	if err := recordPageAssetUploadRequest(tx, input, asset.ID); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return asset, true, nil
}

func recordPageAssetUploadRequest(
	tx *sql.Tx,
	input CreatePageAssetInput,
	assetID int64,
) error {
	if input.RequestID == "" {
		return nil
	}
	_, err := tx.Exec(`
		INSERT INTO page_asset_upload_requests(
			project_id,request_id,request_hash,asset_id,created_at
		) VALUES(?,?,?,?,?)`,
		input.ProjectID, input.RequestID, input.RequestHash, assetID, fmtTime(time.Now()),
	)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		var requestHash string
		var existingAssetID int64
		readErr := tx.QueryRow(`
			SELECT request_hash,asset_id
			FROM page_asset_upload_requests
			WHERE project_id=? AND request_id=?`,
			input.ProjectID, input.RequestID,
		).Scan(&requestHash, &existingAssetID)
		if readErr == nil && requestHash == input.RequestHash && existingAssetID == assetID {
			return nil
		}
		return &PageIdempotencyConflictError{RequestID: input.RequestID}
	}
	return err
}

func (s *Store) GetPageAsset(id int64) (*PageAsset, error) {
	asset, err := getPageAsset(s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return asset, err
}

func (s *Store) ListPageAssets(projectID int64) ([]*PageAsset, error) {
	rows, err := s.db.Query(`
		SELECT `+pageAssetColumns+` FROM page_assets
		WHERE project_id=? ORDER BY logical_key,version_no DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []*PageAsset
	for rows.Next() {
		asset, err := scanPageAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
}

const pageAssetColumns = `id,project_id,logical_key,version_no,storage_ref,media_type,
	byte_size,sha256,origin,provenance_json,width,height,created_at`

func getPageAsset(q pageProjectQueryer, id int64) (*PageAsset, error) {
	return scanPageAsset(q.QueryRow(`SELECT `+pageAssetColumns+` FROM page_assets WHERE id=?`, id))
}

func getPageAssetByHash(q pageProjectQueryer, projectID int64, hash string) (*PageAsset, error) {
	return scanPageAsset(q.QueryRow(`
		SELECT `+pageAssetColumns+` FROM page_assets
		WHERE project_id=? AND sha256=?`, projectID, hash))
}

func scanPageAsset(sc interface{ Scan(...any) error }) (*PageAsset, error) {
	var asset PageAsset
	var width, height sql.NullInt64
	var created string
	if err := sc.Scan(
		&asset.ID, &asset.ProjectID, &asset.LogicalKey, &asset.VersionNo,
		&asset.StorageRef, &asset.MediaType, &asset.ByteSize, &asset.SHA256,
		&asset.Origin, &asset.ProvenanceJSON, &width, &height, &created,
	); err != nil {
		return nil, err
	}
	asset.Width = int(width.Int64)
	asset.Height = int(height.Int64)
	asset.CreatedAt = parseRequiredTime(created)
	return &asset, nil
}

func validPageAssetOrigin(origin string) bool {
	return origin == "upload" || origin == "pilot" ||
		origin == "generated" || origin == "library"
}

func nullNonNegativeDimension(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

type UpsertPageCapabilityGrantInput struct {
	ProjectID                 int64
	Capability                string
	ConfigJSON                string
	Status                    string
	RequestedBy               string
	ApprovedBy                string
	ExpectedWorkingRevisionID int64
	ExpectedCurrentStatus     string
	ExpectedCurrentConfigJSON string
}

func normalizePageCapabilityGrantInput(input UpsertPageCapabilityGrantInput) (UpsertPageCapabilityGrantInput, error) {
	input.Capability = strings.TrimSpace(input.Capability)
	input.RequestedBy = strings.TrimSpace(input.RequestedBy)
	if input.ProjectID <= 0 || input.Capability == "" || input.RequestedBy == "" ||
		!validPageCapabilityStatus(input.Status) {
		return input, fmt.Errorf("%w: invalid capability grant", ErrPageInvalid)
	}
	if input.Status == PageCapabilityApproved && strings.TrimSpace(input.ApprovedBy) == "" {
		return input, fmt.Errorf("%w: approved capability requires approved_by", ErrPageInvalid)
	}
	if input.ExpectedWorkingRevisionID < 0 ||
		(input.ExpectedCurrentStatus != "" && !validPageCapabilityStatus(input.ExpectedCurrentStatus)) {
		return input, fmt.Errorf("%w: invalid expected capability state", ErrPageInvalid)
	}
	config := strings.TrimSpace(input.ConfigJSON)
	if config == "" {
		config = "{}"
	}
	var err error
	if input.ConfigJSON, _, err = canonicalJSONObject(config, "capability config"); err != nil {
		return input, err
	}
	if strings.TrimSpace(input.ExpectedCurrentConfigJSON) != "" {
		if input.ExpectedCurrentConfigJSON, _, err = canonicalJSONObject(
			input.ExpectedCurrentConfigJSON, "expected capability config",
		); err != nil {
			return input, err
		}
	}
	return input, nil
}

func (s *Store) UpsertPageCapabilityGrant(input UpsertPageCapabilityGrantInput) (*PageCapabilityGrant, error) {
	grant, _, err := s.upsertPageCapabilityGrant(input, "", "", "")
	return grant, err
}

// UpsertPageCapabilityGrantIdempotent records the exact grant snapshot in the
// same transaction as the mutation. A retry after restart can therefore
// return the original result without consuming a second approval token or
// reapplying an older decision over a newer grant state.
func (s *Store) UpsertPageCapabilityGrantIdempotent(
	input UpsertPageCapabilityGrantInput,
	operation string,
	requestID string,
	requestHash string,
) (*PageCapabilityGrant, bool, error) {
	return s.upsertPageCapabilityGrant(input, operation, requestID, requestHash)
}

func (s *Store) upsertPageCapabilityGrant(
	input UpsertPageCapabilityGrantInput,
	operation string,
	requestID string,
	requestHash string,
) (*PageCapabilityGrant, bool, error) {
	var err error
	input, err = normalizePageCapabilityGrantInput(input)
	if err != nil {
		return nil, false, err
	}
	operation = strings.TrimSpace(operation)
	requestID = strings.TrimSpace(requestID)
	requestHash = strings.TrimSpace(requestHash)
	if (requestID == "") != (requestHash == "") ||
		(requestID != "" && operation == "") {
		return nil, false, fmt.Errorf("%w: invalid capability idempotency receipt", ErrPageInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if requestID != "" {
		grant, found, err := getPageCapabilityMutationReceipt(
			tx, input.ProjectID, requestID, requestHash,
		)
		if err != nil || found {
			return grant, false, err
		}
	}
	project, err := getPageProject(tx, `WHERE id=?`, input.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrPageProjectNotFound
	} else if err != nil {
		return nil, false, err
	}
	if input.ExpectedWorkingRevisionID > 0 &&
		project.WorkingRevisionID != input.ExpectedWorkingRevisionID {
		return nil, false, &PageRevisionConflictError{
			ExpectedRevisionID: input.ExpectedWorkingRevisionID,
			CurrentRevisionID:  project.WorkingRevisionID,
		}
	}
	if input.ExpectedCurrentStatus != "" ||
		strings.TrimSpace(input.ExpectedCurrentConfigJSON) != "" {
		current, err := getPageCapabilityGrant(tx, input.ProjectID, input.Capability)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, fmt.Errorf("%w: expected capability grant is missing", ErrPageInvalid)
		}
		if err != nil {
			return nil, false, err
		}
		if input.ExpectedCurrentStatus != "" &&
			current.Status != input.ExpectedCurrentStatus {
			return nil, false, fmt.Errorf("%w: capability status changed", ErrPageInvalid)
		}
		if strings.TrimSpace(input.ExpectedCurrentConfigJSON) != "" &&
			current.ConfigJSON != input.ExpectedCurrentConfigJSON {
			return nil, false, fmt.Errorf("%w: capability config changed", ErrPageInvalid)
		}
	}
	now := fmtTime(time.Now())
	if _, err := tx.Exec(`
		INSERT INTO page_capability_grants(
			project_id,capability,config_json,status,requested_by,approved_by,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id,capability) DO UPDATE SET
			config_json=excluded.config_json,
			status=excluded.status,
			requested_by=excluded.requested_by,
			approved_by=excluded.approved_by,
			updated_at=excluded.updated_at`,
		input.ProjectID, input.Capability, input.ConfigJSON, input.Status,
		input.RequestedBy, input.ApprovedBy, now, now); err != nil {
		return nil, false, err
	}
	grant, err := getPageCapabilityGrant(tx, input.ProjectID, input.Capability)
	if err != nil {
		return nil, false, err
	}
	if requestID != "" {
		raw, err := json.Marshal(grant)
		if err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(`
			INSERT INTO page_capability_mutation_receipts(
				project_id,request_id,request_hash,operation,capability,grant_json,created_at
			) VALUES(?,?,?,?,?,?,?)`,
			input.ProjectID, requestID, requestHash, operation, input.Capability,
			string(raw), now,
		); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return nil, false, &PageIdempotencyConflictError{RequestID: requestID}
			}
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return grant, true, nil
}

// GetPageCapabilityMutationReceipt checks a durable mutation receipt without
// changing grant state. A hash mismatch is an idempotency conflict.
func (s *Store) GetPageCapabilityMutationReceipt(
	projectID int64,
	requestID string,
	requestHash string,
) (*PageCapabilityGrant, bool, error) {
	if projectID <= 0 || strings.TrimSpace(requestID) == "" ||
		strings.TrimSpace(requestHash) == "" {
		return nil, false, fmt.Errorf("%w: invalid capability receipt lookup", ErrPageInvalid)
	}
	return getPageCapabilityMutationReceipt(s.db, projectID,
		strings.TrimSpace(requestID), strings.TrimSpace(requestHash))
}

func getPageCapabilityMutationReceipt(
	q pageProjectQueryer,
	projectID int64,
	requestID string,
	requestHash string,
) (*PageCapabilityGrant, bool, error) {
	var storedHash, grantJSON string
	err := q.QueryRow(`
		SELECT request_hash,grant_json
		FROM page_capability_mutation_receipts
		WHERE project_id=? AND request_id=?`,
		projectID, requestID,
	).Scan(&storedHash, &grantJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if storedHash != requestHash {
		return nil, false, &PageIdempotencyConflictError{RequestID: requestID}
	}
	var grant PageCapabilityGrant
	if err := json.Unmarshal([]byte(grantJSON), &grant); err != nil {
		return nil, false, fmt.Errorf("decode capability mutation receipt: %w", err)
	}
	if grant.ProjectID != projectID || grant.ID <= 0 || grant.Capability == "" {
		return nil, false, fmt.Errorf("%w: corrupt capability mutation receipt", ErrPageInvalid)
	}
	return &grant, true, nil
}

func (s *Store) GetPageCapabilityGrant(projectID int64, capability string) (*PageCapabilityGrant, error) {
	grant, err := getPageCapabilityGrant(s.db, projectID, capability)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return grant, err
}

func (s *Store) ListPageCapabilityGrants(projectID int64) ([]*PageCapabilityGrant, error) {
	rows, err := s.db.Query(`
		SELECT `+pageCapabilityColumns+`
		FROM page_capability_grants WHERE project_id=?
		ORDER BY capability`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []*PageCapabilityGrant
	for rows.Next() {
		grant, err := scanPageCapabilityGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

const pageCapabilityColumns = `id,project_id,capability,config_json,status,
	requested_by,approved_by,created_at,updated_at`

func getPageCapabilityGrant(q pageProjectQueryer, projectID int64, capability string) (*PageCapabilityGrant, error) {
	return scanPageCapabilityGrant(q.QueryRow(`
		SELECT `+pageCapabilityColumns+` FROM page_capability_grants
		WHERE project_id=? AND capability=?`, projectID, capability))
}

func scanPageCapabilityGrant(sc interface{ Scan(...any) error }) (*PageCapabilityGrant, error) {
	var grant PageCapabilityGrant
	var created, updated string
	if err := sc.Scan(
		&grant.ID, &grant.ProjectID, &grant.Capability, &grant.ConfigJSON,
		&grant.Status, &grant.RequestedBy, &grant.ApprovedBy, &created, &updated,
	); err != nil {
		return nil, err
	}
	grant.CreatedAt = parseRequiredTime(created)
	grant.UpdatedAt = parseRequiredTime(updated)
	return &grant, nil
}

func validPageCapabilityStatus(status string) bool {
	return status == PageCapabilityRequested || status == PageCapabilityApproved ||
		status == PageCapabilityDenied || status == PageCapabilityRevoked
}
