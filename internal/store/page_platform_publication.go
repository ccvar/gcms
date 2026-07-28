package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PublishPageProjectInput struct {
	ProjectID                 int64
	RevisionID                int64
	BuildID                   int64
	ExpectedWorkingRevisionID int64
	Action                    string
	ApprovalID                string
	ActorID                   string
	Origin                    string
	RequestID                 string
	RequestHash               string
	DataSnapshotHash          string
	DeliveryStatus            string
	DeploymentJobID           string
}

// PublishPageProject atomically switches the public revision pointer, copies the
// revision's metadata snapshot to posts, releases the candidate route, and
// appends a publication record. Cloudflare delivery remains an independent
// status on that record.
func (s *Store) PublishPageProject(input PublishPageProjectInput) (*PagePublication, bool, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.DataSnapshotHash = strings.TrimSpace(input.DataSnapshotHash)
	if input.ProjectID <= 0 || input.RevisionID <= 0 ||
		input.ExpectedWorkingRevisionID < 0 ||
		(input.Action != PagePublicationPublish && input.Action != PagePublicationRollback) ||
		!validPageOrigin(input.Origin) || strings.TrimSpace(input.ActorID) == "" {
		return nil, false, fmt.Errorf("%w: invalid publication attributes", ErrPageInvalid)
	}
	if input.Action == PagePublicationPublish &&
		input.RevisionID != input.ExpectedWorkingRevisionID {
		return nil, false, fmt.Errorf("%w: publish must target the current work revision", ErrPageInvalid)
	}
	if err := validateSHA256(input.DataSnapshotHash, "data_snapshot_hash", true); err != nil {
		return nil, false, err
	}
	if input.RequestHash != "" {
		if input.RequestID == "" {
			return nil, false, fmt.Errorf("%w: publication request hash requires request id", ErrPageInvalid)
		}
		if err := validateSHA256(input.RequestHash, "request_hash", false); err != nil {
			return nil, false, err
		}
	}
	if input.DeliveryStatus == "" {
		input.DeliveryStatus = PageDeliveryQueued
	}
	if !validPageDeliveryStatus(input.DeliveryStatus) {
		return nil, false, fmt.Errorf("%w: invalid delivery status", ErrPageInvalid)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	project, err := getPageProject(tx, `WHERE id=?`, input.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrPageProjectNotFound
	}
	if err != nil {
		return nil, false, err
	}
	revision, err := getPageProjectRevision(tx, input.RevisionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && revision.ProjectID != input.ProjectID) {
		return nil, false, ErrPageRevisionNotFound
	}
	if err != nil {
		return nil, false, err
	}

	// Idempotent retries must be resolved before checking the mutable work
	// pointer; later edits must not turn an already committed retry into 409.
	if input.RequestID != "" {
		existing, err := getPagePublicationByRequest(tx, input.ProjectID, input.RequestID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
		if err == nil {
			if input.RequestHash != "" {
				var storedHash string
				receiptErr := tx.QueryRow(`
					SELECT request_hash
					FROM page_publication_mutation_receipts
					WHERE project_id=? AND request_id=? AND publication_id=?`,
					input.ProjectID, input.RequestID, existing.ID,
				).Scan(&storedHash)
				if receiptErr != nil || storedHash != input.RequestHash {
					if receiptErr != nil && !errors.Is(receiptErr, sql.ErrNoRows) {
						return nil, false, receiptErr
					}
					return nil, false, &PageIdempotencyConflictError{
						RequestID: input.RequestID,
					}
				}
			}
			if !pagePublicationMatchesInput(existing, input, revision) {
				return nil, false, &PageIdempotencyConflictError{RequestID: input.RequestID}
			}
			if input.BuildID > 0 && revision.RevisionKind != PageRevisionStandardBaseline {
				build, buildErr := readyBuildForPublication(tx, project, revision, input.BuildID)
				if buildErr != nil {
					return nil, false, buildErr
				}
				if build.ArtifactHash != existing.ArtifactHash ||
					build.RuntimeVersion != existing.RuntimeVersion {
					return nil, false, &PageIdempotencyConflictError{RequestID: input.RequestID}
				}
			}
			return existing, false, nil
		}
	}

	if project.WorkingRevisionID != input.ExpectedWorkingRevisionID {
		return nil, false, &PageRevisionConflictError{
			ExpectedRevisionID: input.ExpectedWorkingRevisionID,
			CurrentRevisionID:  project.WorkingRevisionID,
		}
	}
	build, err := readyBuildForPublication(tx, project, revision, input.BuildID)
	if err != nil {
		return nil, false, err
	}
	meta, err := parsePageRevisionMeta(revision.PageMetaJSON)
	if err != nil {
		return nil, false, err
	}
	if meta.TransGroup == "" {
		meta.TransGroup = meta.Lang + ":" + meta.Slug
	}
	if err := validatePagePublicationRoute(tx, project, meta); err != nil {
		return nil, false, err
	}
	now := fmtTime(time.Now())

	postUpdate, err := tx.Exec(`
		UPDATE posts SET
			slug=?,title=?,excerpt=?,meta_desc=?,keywords=?,cover_image=?,author=?,
			lang=?,trans_group=?,robots_override=?,canonical_override=?,
			status='published',published_at=COALESCE(published_at,?),updated_at=?,
			discard_reason='',discarded_at=NULL
		WHERE id=? AND type='page'`,
		meta.Slug, meta.Title, meta.Excerpt, meta.MetaDesc, meta.Keywords,
		meta.CoverImage, meta.Author, meta.Lang, meta.TransGroup,
		meta.RobotsOverride, meta.CanonicalOverride, now, now, project.PostID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") ||
			strings.Contains(err.Error(), "page_route_conflict") {
			return nil, false, &PageRouteConflictError{Lang: meta.Lang, Slug: meta.Slug}
		}
		return nil, false, err
	}
	affected, err := postUpdate.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if affected != 1 {
		return nil, false, ErrPagePostRequired
	}

	pointerUpdate, err := tx.Exec(`
		UPDATE page_projects
		SET published_revision_id=?,updated_at=?
		WHERE id=? AND COALESCE(working_revision_id,0)=?`,
		input.RevisionID, now, input.ProjectID, input.ExpectedWorkingRevisionID)
	if err != nil {
		return nil, false, err
	}
	affected, err = pointerUpdate.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if affected != 1 {
		current, readErr := getPageProject(tx, `WHERE id=?`, input.ProjectID)
		if readErr != nil {
			return nil, false, readErr
		}
		return nil, false, &PageRevisionConflictError{
			ExpectedRevisionID: input.ExpectedWorkingRevisionID,
			CurrentRevisionID:  current.WorkingRevisionID,
		}
	}

	result, err := tx.Exec(`
		INSERT INTO page_publications(
			project_id,revision_id,action,status,approval_id,published_at,
			actor_id,origin,request_id,deployment_job_id,delivery_status,
			page_meta_hash,manifest_hash,data_snapshot_hash,artifact_hash,
			runtime_version,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		input.ProjectID, input.RevisionID, input.Action, PagePublicationPublished,
		nullNonEmpty(input.ApprovalID), now, input.ActorID, input.Origin,
		nullNonEmpty(input.RequestID), nullNonEmpty(input.DeploymentJobID),
		input.DeliveryStatus, revision.PageMetaHash, revision.ManifestHash,
		input.DataSnapshotHash, build.ArtifactHash, build.RuntimeVersion, now)
	if err != nil {
		if input.RequestID != "" && strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, false, &PageIdempotencyConflictError{RequestID: input.RequestID}
		}
		return nil, false, err
	}
	publicationID, err := result.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`DELETE FROM page_route_reservations WHERE project_id=?`, input.ProjectID); err != nil {
		return nil, false, err
	}
	publication, err := getPagePublication(tx, publicationID)
	if err != nil {
		return nil, false, err
	}
	if input.RequestHash != "" {
		if _, err := tx.Exec(`
			INSERT INTO page_publication_mutation_receipts(
				project_id,request_id,request_hash,publication_id,created_at
			) VALUES(?,?,?,?,?)`,
			input.ProjectID, input.RequestID, input.RequestHash, publicationID, now,
		); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return nil, false, &PageIdempotencyConflictError{RequestID: input.RequestID}
			}
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return publication, true, nil
}

func validatePagePublicationRoute(
	tx *sql.Tx,
	project *PageProject,
	meta PageRevisionMeta,
) error {
	if project == nil || PageRouteSlugReserved(meta.Slug) {
		return &PageRouteConflictError{Lang: meta.Lang, Slug: meta.Slug}
	}
	var conflict int64
	err := tx.QueryRow(`
		SELECT id FROM posts
		WHERE type='page' AND lang=? AND slug=? AND id<>?
		LIMIT 1`, meta.Lang, meta.Slug, project.PostID).Scan(&conflict)
	if err == nil {
		return &PageRouteConflictError{Lang: meta.Lang, Slug: meta.Slug}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var prefix string
	err = tx.QueryRow(`
		SELECT url_prefix FROM content_types
		WHERE lower(trim(key))=lower(?)
		   OR lower(trim(url_prefix))=lower(?)
		LIMIT 1`, meta.Slug, meta.Slug).Scan(&prefix)
	if err == nil {
		return &PageRouteConflictError{Lang: meta.Lang, Slug: meta.Slug}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = tx.QueryRow(`
		SELECT project_id FROM page_route_reservations
		WHERE lang=? AND slug=? AND project_id<>?
		LIMIT 1`, meta.Lang, meta.Slug, project.ID).Scan(&conflict)
	if err == nil {
		return &PageRouteConflictError{Lang: meta.Lang, Slug: meta.Slug}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func readyBuildForPublication(
	tx *sql.Tx,
	project *PageProject,
	revision *PageProjectRevision,
	buildID int64,
) (*PageBuild, error) {
	if revision.RevisionKind == PageRevisionStandardBaseline {
		return &PageBuild{
			ProjectID:      project.ID,
			RevisionID:     revision.ID,
			Status:         PageBuildReady,
			RuntimeVersion: "standard-v1",
		}, nil
	}
	var (
		build *PageBuild
		err   error
	)
	if buildID > 0 {
		build, err = getPageBuild(tx, buildID)
	} else {
		build, err = scanPageBuild(tx.QueryRow(`
			SELECT `+pageBuildColumns+`
			FROM page_builds
			WHERE project_id=? AND revision_id=? AND status='ready'
			ORDER BY id DESC LIMIT 1`, project.ID, revision.ID))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPageBuildNotReady
	}
	if err != nil {
		return nil, err
	}
	if build.ProjectID != project.ID || build.RevisionID != revision.ID ||
		build.Status != PageBuildReady {
		return nil, ErrPageBuildNotReady
	}
	if project.Mode == PageModeApp && (build.ArtifactRef == "" || build.ArtifactHash == "") {
		return nil, ErrPageBuildNotReady
	}
	return build, nil
}

func pagePublicationMatchesInput(
	publication *PagePublication,
	input PublishPageProjectInput,
	revision *PageProjectRevision,
) bool {
	if publication == nil || revision == nil {
		return false
	}
	return publication.ProjectID == input.ProjectID &&
		publication.RevisionID == input.RevisionID &&
		publication.Action == input.Action &&
		publication.Status == PagePublicationPublished &&
		publication.ApprovalID == input.ApprovalID &&
		publication.ActorID == input.ActorID &&
		publication.Origin == input.Origin &&
		publication.RequestID == input.RequestID &&
		publication.PageMetaHash == revision.PageMetaHash &&
		publication.ManifestHash == revision.ManifestHash &&
		publication.DataSnapshotHash == input.DataSnapshotHash
}

func (s *Store) GetPagePublication(id int64) (*PagePublication, error) {
	publication, err := getPagePublication(s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return publication, err
}

// ReplayPagePublication resolves an immutable automation mutation receipt.
// The request hash covers the normalized request body, target, actor and
// original If-Match value, so this lookup is deliberately independent of all
// mutable validation/build/route/project state.
func (s *Store) ReplayPagePublication(
	projectID int64,
	requestID, requestHash, actorID, origin, action string,
) (*PagePublication, bool, error) {
	requestID = strings.TrimSpace(requestID)
	requestHash = strings.TrimSpace(requestHash)
	if projectID <= 0 || requestID == "" || requestHash == "" {
		return nil, false, nil
	}
	var (
		storedHash    string
		publicationID int64
	)
	err := s.db.QueryRow(`
		SELECT request_hash,publication_id
		FROM page_publication_mutation_receipts
		WHERE project_id=? AND request_id=?`,
		projectID, requestID,
	).Scan(&storedHash, &publicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if storedHash != requestHash {
		return nil, false, &PageIdempotencyConflictError{RequestID: requestID}
	}
	publication, err := s.GetPagePublication(publicationID)
	if err != nil {
		return nil, false, err
	}
	if publication == nil ||
		publication.ProjectID != projectID ||
		publication.RequestID != requestID ||
		publication.ActorID != strings.TrimSpace(actorID) ||
		publication.Origin != origin ||
		publication.Action != action ||
		publication.Status != PagePublicationPublished {
		return nil, false, &PageIdempotencyConflictError{RequestID: requestID}
	}
	return publication, true, nil
}

func (s *Store) ListPagePublications(projectID int64, limit int) ([]*PagePublication, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT `+pagePublicationColumns+`
		FROM page_publications
		WHERE project_id=?
		ORDER BY id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var publications []*PagePublication
	for rows.Next() {
		publication, err := scanPagePublication(rows)
		if err != nil {
			return nil, err
		}
		publications = append(publications, publication)
	}
	return publications, rows.Err()
}

func (s *Store) UpdatePagePublicationDelivery(
	id int64,
	deliveryStatus, deploymentJobID string,
) (*PagePublication, error) {
	if id <= 0 || !validPageDeliveryStatus(deliveryStatus) {
		return nil, fmt.Errorf("%w: invalid delivery update", ErrPageInvalid)
	}
	result, err := s.db.Exec(`
		UPDATE page_publications
		SET delivery_status=?,deployment_job_id=?
		WHERE id=?`,
		deliveryStatus, nullNonEmpty(deploymentJobID), id)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, sql.ErrNoRows
	}
	return s.GetPagePublication(id)
}

const pagePublicationColumns = `id,project_id,revision_id,action,status,approval_id,
	scheduled_at,published_at,actor_id,origin,request_id,deployment_job_id,
	delivery_status,page_meta_hash,manifest_hash,data_snapshot_hash,artifact_hash,
	runtime_version,created_at`

func getPagePublication(q pageProjectQueryer, id int64) (*PagePublication, error) {
	return scanPagePublication(q.QueryRow(`
		SELECT `+pagePublicationColumns+`
		FROM page_publications WHERE id=?`, id))
}

func getPagePublicationByRequest(
	q pageProjectQueryer,
	projectID int64,
	requestID string,
) (*PagePublication, error) {
	return scanPagePublication(q.QueryRow(`
		SELECT `+pagePublicationColumns+`
		FROM page_publications
		WHERE project_id=? AND request_id=?`, projectID, requestID))
}

func scanPagePublication(sc interface{ Scan(...any) error }) (*PagePublication, error) {
	var publication PagePublication
	var approvalID, requestID, deploymentJobID sql.NullString
	var scheduled, published sql.NullString
	var created string
	if err := sc.Scan(
		&publication.ID, &publication.ProjectID, &publication.RevisionID,
		&publication.Action, &publication.Status, &approvalID, &scheduled,
		&published, &publication.ActorID, &publication.Origin, &requestID,
		&deploymentJobID, &publication.DeliveryStatus, &publication.PageMetaHash,
		&publication.ManifestHash, &publication.DataSnapshotHash,
		&publication.ArtifactHash, &publication.RuntimeVersion, &created,
	); err != nil {
		return nil, err
	}
	publication.ApprovalID = approvalID.String
	publication.ScheduledAt = parseTime(scheduled)
	publication.PublishedAt = parseTime(published)
	publication.RequestID = requestID.String
	publication.DeploymentJobID = deploymentJobID.String
	publication.CreatedAt = parseRequiredTime(created)
	return &publication, nil
}

func validPageDeliveryStatus(status string) bool {
	return status == "" || status == PageDeliveryQueued ||
		status == PageDeliveryLive || status == PageDeliveryFailed
}
