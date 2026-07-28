package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	PageModeComposition = "composition"
	PageModeApp         = "app"

	PageShellSite    = "site"
	PageShellMinimal = "minimal"
	PageShellNone    = "none"

	PageProjectBuildIdle       = "idle"
	PageProjectBuildValidating = "validating"
	PageProjectBuildReady      = "ready"
	PageProjectBuildFailed     = "failed"

	PageRevisionStandardBaseline = "standard_baseline"
	PageRevisionComposition      = "composition"
	PageRevisionApp              = "app"

	PageOriginAdmin   = "admin"
	PageOriginPilot   = "pilot"
	PageOriginAPI     = "api"
	PageOriginRestore = "restore"

	PageBuildQueued     = "queued"
	PageBuildValidating = "validating"
	PageBuildReady      = "ready"
	PageBuildFailed     = "failed"

	PageCapabilityRequested = "requested"
	PageCapabilityApproved  = "approved"
	PageCapabilityDenied    = "denied"
	PageCapabilityRevoked   = "revoked"

	PagePublicationPublish   = "publish"
	PagePublicationSchedule  = "schedule"
	PagePublicationRollback  = "rollback"
	PagePublicationUnpublish = "unpublish"

	PagePublicationPending   = "pending"
	PagePublicationApproved  = "approved"
	PagePublicationPublished = "published"
	PagePublicationCancelled = "cancelled"
	PagePublicationFailed    = "failed"

	PageDeliveryQueued = "queued"
	PageDeliveryLive   = "live"
	PageDeliveryFailed = "failed"
)

var (
	ErrPageProjectNotFound     = errors.New("page project not found")
	ErrPageProjectExists       = errors.New("page project already exists")
	ErrPagePostRequired        = errors.New("page project post must be a page")
	ErrPageRevisionNotFound    = errors.New("page project revision not found")
	ErrPageRevisionConflict    = errors.New("page project revision conflict")
	ErrPageIdempotencyConflict = errors.New("page project idempotency conflict")
	ErrPageBuildNotReady       = errors.New("page build is not ready")
	ErrPageRouteConflict       = errors.New("page route is already in use")
	ErrPageInvalid             = errors.New("invalid page project data")
)

// PageRevisionConflictError carries the two revision pointers needed by HTTP
// callers to produce a useful If-Match conflict response.
type PageRevisionConflictError struct {
	ExpectedRevisionID int64
	CurrentRevisionID  int64
}

func (e *PageRevisionConflictError) Error() string {
	return fmt.Sprintf("%v: expected revision %d, current revision %d",
		ErrPageRevisionConflict, e.ExpectedRevisionID, e.CurrentRevisionID)
}

func (e *PageRevisionConflictError) Unwrap() error { return ErrPageRevisionConflict }

type PageIdempotencyConflictError struct {
	RequestID string
}

func (e *PageIdempotencyConflictError) Error() string {
	return fmt.Sprintf("%v: request_id %q was already used for different input",
		ErrPageIdempotencyConflict, e.RequestID)
}

func (e *PageIdempotencyConflictError) Unwrap() error { return ErrPageIdempotencyConflict }

type PageRouteConflictError struct {
	Lang string
	Slug string
}

func (e *PageRouteConflictError) Error() string {
	return fmt.Sprintf("%v: %s/%s", ErrPageRouteConflict, e.Lang, e.Slug)
}

func (e *PageRouteConflictError) Unwrap() error { return ErrPageRouteConflict }

type PageProject struct {
	ID                  int64
	PostID              int64
	Mode                string
	SchemaVersion       int
	WorkingRevisionID   int64
	PublishedRevisionID int64
	ShellMode           string
	BuildStatus         string
	CreatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ETag is the opaque concurrency token for the current work revision.
func (p *PageProject) ETag() string {
	if p == nil {
		return ""
	}
	return PageRevisionETag(p.WorkingRevisionID)
}

func PageRevisionETag(revisionID int64) string {
	return `"revision-` + strconv.FormatInt(revisionID, 10) + `"`
}

type PageProjectRevision struct {
	ID               int64
	ProjectID        int64
	RevisionNo       int
	ParentRevisionID int64
	RevisionKind     string
	PageMetaJSON     string
	PageMetaHash     string
	ManifestJSON     string
	ManifestHash     string
	StandardContent  string
	SourceBundleRef  string
	SourceHash       string
	Origin           string
	ActorID          string
	ConversationID   string
	RequestID        string
	Summary          string
	ValidationJSON   string
	CreatedAt        time.Time
}

type PageBuild struct {
	ID              int64
	ProjectID       int64
	RevisionID      int64
	Status          string
	ArtifactRef     string
	ArtifactHash    string
	DiagnosticsJSON string
	RuntimeVersion  string
	StartedAt       time.Time
	FinishedAt      time.Time
	CreatedAt       time.Time
}

type PageAsset struct {
	ID             int64
	ProjectID      int64
	LogicalKey     string
	VersionNo      int
	StorageRef     string
	MediaType      string
	ByteSize       int64
	SHA256         string
	Origin         string
	ProvenanceJSON string
	Width          int
	Height         int
	CreatedAt      time.Time
}

type PageCapabilityGrant struct {
	ID          int64
	ProjectID   int64
	Capability  string
	ConfigJSON  string
	Status      string
	RequestedBy string
	ApprovedBy  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type PagePublication struct {
	ID               int64
	ProjectID        int64
	RevisionID       int64
	Action           string
	Status           string
	ApprovalID       string
	ScheduledAt      time.Time
	PublishedAt      time.Time
	ActorID          string
	Origin           string
	RequestID        string
	DeploymentJobID  string
	DeliveryStatus   string
	PageMetaHash     string
	ManifestHash     string
	DataSnapshotHash string
	ArtifactHash     string
	RuntimeVersion   string
	CreatedAt        time.Time
}

type PageRouteReservation struct {
	ID         int64
	ProjectID  int64
	RevisionID int64
	Lang       string
	Slug       string
	CreatedAt  time.Time
}

// PageRevisionMeta is the complete public metadata snapshot copied to posts
// only when its revision is published.
type PageRevisionMeta struct {
	Slug              string `json:"slug"`
	Title             string `json:"title"`
	Excerpt           string `json:"excerpt,omitempty"`
	MetaDesc          string `json:"meta_desc,omitempty"`
	Keywords          string `json:"keywords,omitempty"`
	CoverImage        string `json:"cover_image,omitempty"`
	Author            string `json:"author,omitempty"`
	Lang              string `json:"lang"`
	TransGroup        string `json:"trans_group,omitempty"`
	RobotsOverride    string `json:"robots_override,omitempty"`
	CanonicalOverride string `json:"canonical_override,omitempty"`
}

func PageRevisionMetaFromPost(p *Post) PageRevisionMeta {
	if p == nil {
		return PageRevisionMeta{}
	}
	return PageRevisionMeta{
		Slug:              p.Slug,
		Title:             p.Title,
		Excerpt:           p.Excerpt,
		MetaDesc:          p.MetaDesc,
		Keywords:          p.Keywords,
		CoverImage:        p.CoverImage,
		Author:            p.Author,
		Lang:              p.Lang,
		TransGroup:        p.TransGroup,
		RobotsOverride:    p.RobotsOverride,
		CanonicalOverride: p.CanonicalOverride,
	}
}

func (m PageRevisionMeta) CanonicalJSON() (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	canonical, _, err := CanonicalJSONHash(string(raw))
	return canonical, err
}

// CanonicalJSONHash validates JSON, removes insignificant formatting and sorts
// object keys through encoding/json's deterministic map encoding.
func CanonicalJSONHash(raw string) (canonical string, hash string, err error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", "", fmt.Errorf("%w: invalid JSON: %v", ErrPageInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", "", fmt.Errorf("%w: JSON contains multiple values", ErrPageInvalid)
		}
		return "", "", fmt.Errorf("%w: invalid JSON suffix: %v", ErrPageInvalid, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", "", fmt.Errorf("%w: canonicalize JSON: %v", ErrPageInvalid, err)
	}
	return string(encoded), SHA256Hex(encoded), nil
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateSHA256(value, field string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || value != strings.ToLower(value) {
		return fmt.Errorf("%w: %s must be a lowercase SHA-256 hex digest", ErrPageInvalid, field)
	}
	return nil
}

func canonicalJSONObject(raw, field string) (string, string, error) {
	canonical, hash, err := CanonicalJSONHash(raw)
	if err != nil {
		return "", "", err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(canonical), &object); err != nil || object == nil {
		return "", "", fmt.Errorf("%w: %s must be a JSON object", ErrPageInvalid, field)
	}
	return canonical, hash, nil
}

func canonicalJSONArray(raw, field string) (string, error) {
	canonical, _, err := CanonicalJSONHash(raw)
	if err != nil {
		return "", err
	}
	var array []json.RawMessage
	if err := json.Unmarshal([]byte(canonical), &array); err != nil || array == nil {
		return "", fmt.Errorf("%w: %s must be a JSON array", ErrPageInvalid, field)
	}
	return canonical, nil
}

func parsePageRevisionMeta(canonical string) (PageRevisionMeta, error) {
	var meta PageRevisionMeta
	if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
		return PageRevisionMeta{}, fmt.Errorf("%w: page_meta: %v", ErrPageInvalid, err)
	}
	if meta.Slug == "" || meta.Lang == "" ||
		meta.Slug != strings.TrimSpace(meta.Slug) ||
		meta.Lang != strings.TrimSpace(meta.Lang) {
		return PageRevisionMeta{}, fmt.Errorf("%w: page_meta.slug and page_meta.lang are required", ErrPageInvalid)
	}
	return meta, nil
}

type CreatePageProjectInput struct {
	PostID        int64
	Mode          string
	SchemaVersion int
	ShellMode     string
	CreatedBy     string
}

func (s *Store) CreatePageProject(input CreatePageProjectInput) (*PageProject, error) {
	if input.PostID <= 0 || !validPageMode(input.Mode) || input.SchemaVersion <= 0 ||
		!validPageShell(input.ShellMode) || !validPageOrigin(input.CreatedBy) {
		return nil, fmt.Errorf("%w: invalid project attributes", ErrPageInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var postType string
	if err := tx.QueryRow(`SELECT type FROM posts WHERE id=?`, input.PostID).Scan(&postType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPagePostRequired
		}
		return nil, err
	}
	if postType != "page" {
		return nil, ErrPagePostRequired
	}
	var existingID int64
	err = tx.QueryRow(`SELECT id FROM page_projects WHERE post_id=?`, input.PostID).Scan(&existingID)
	if err == nil {
		return nil, ErrPageProjectExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	now := fmtTime(time.Now())
	result, err := tx.Exec(`
		INSERT INTO page_projects(
			post_id,mode,schema_version,shell_mode,build_status,created_by,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?)`,
		input.PostID, input.Mode, input.SchemaVersion, input.ShellMode,
		PageProjectBuildIdle, input.CreatedBy, now, now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	project, err := getPageProject(tx, `WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return project, nil
}

func (s *Store) GetPageProject(id int64) (*PageProject, error) {
	project, err := getPageProject(s.db, `WHERE id=?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return project, err
}

func (s *Store) GetPageProjectByPostID(postID int64) (*PageProject, error) {
	project, err := getPageProject(s.db, `WHERE post_id=?`, postID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return project, err
}

func (s *Store) ListPageProjects() ([]*PageProject, error) {
	rows, err := s.db.Query(`SELECT ` + pageProjectColumns + ` FROM page_projects ORDER BY updated_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []*PageProject
	for rows.Next() {
		project, err := scanPageProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

const pageProjectColumns = `id,post_id,mode,schema_version,working_revision_id,
	published_revision_id,shell_mode,build_status,created_by,created_at,updated_at`

type pageProjectQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getPageProject(q pageProjectQueryer, where string, args ...any) (*PageProject, error) {
	return scanPageProject(q.QueryRow(`SELECT `+pageProjectColumns+` FROM page_projects `+where, args...))
}

func scanPageProject(sc interface{ Scan(...any) error }) (*PageProject, error) {
	var project PageProject
	var working, published sql.NullInt64
	var created, updated string
	if err := sc.Scan(
		&project.ID, &project.PostID, &project.Mode, &project.SchemaVersion, &working,
		&published, &project.ShellMode, &project.BuildStatus, &project.CreatedBy,
		&created, &updated,
	); err != nil {
		return nil, err
	}
	project.WorkingRevisionID = working.Int64
	project.PublishedRevisionID = published.Int64
	project.CreatedAt = parseRequiredTime(created)
	project.UpdatedAt = parseRequiredTime(updated)
	return &project, nil
}

type CreatePageRevisionInput struct {
	ProjectID       int64
	BaseRevisionID  int64
	RevisionKind    string
	PageMetaJSON    string
	ManifestJSON    string
	StandardContent string
	SourceBundleRef string
	SourceHash      string
	Origin          string
	ActorID         string
	ConversationID  string
	RequestID       string
	Summary         string
	ValidationJSON  string
}

type normalizedPageRevisionInput struct {
	CreatePageRevisionInput
	PageMetaHash string
	ManifestHash string
	Meta         PageRevisionMeta
	ShellMode    string
}

func normalizePageRevisionInput(input CreatePageRevisionInput) (normalizedPageRevisionInput, error) {
	if input.ProjectID <= 0 || input.BaseRevisionID < 0 ||
		!validRevisionKind(input.RevisionKind) || !validPageOrigin(input.Origin) {
		return normalizedPageRevisionInput{}, fmt.Errorf("%w: invalid revision attributes", ErrPageInvalid)
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.ConversationID = strings.TrimSpace(input.ConversationID)
	pageMeta, pageMetaHash, err := canonicalJSONObject(input.PageMetaJSON, "page_meta")
	if err != nil {
		return normalizedPageRevisionInput{}, err
	}
	meta, err := parsePageRevisionMeta(pageMeta)
	if err != nil {
		return normalizedPageRevisionInput{}, err
	}
	if meta.TransGroup == "" {
		meta.TransGroup = meta.Lang + ":" + meta.Slug
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(pageMeta), &fields); err != nil {
			return normalizedPageRevisionInput{}, err
		}
		fields["trans_group"], _ = json.Marshal(meta.TransGroup)
		raw, err := json.Marshal(fields)
		if err != nil {
			return normalizedPageRevisionInput{}, err
		}
		pageMeta, pageMetaHash, err = CanonicalJSONHash(string(raw))
		if err != nil {
			return normalizedPageRevisionInput{}, err
		}
	}
	manifest := strings.TrimSpace(input.ManifestJSON)
	if manifest == "" {
		manifest = "{}"
	}
	manifest, manifestHash, err := canonicalJSONObject(manifest, "manifest")
	if err != nil {
		return normalizedPageRevisionInput{}, err
	}
	shellMode := ""
	if input.RevisionKind == PageRevisionComposition {
		var envelope struct {
			Shell struct {
				Mode string `json:"mode"`
			} `json:"shell"`
		}
		if err := json.Unmarshal([]byte(manifest), &envelope); err != nil {
			return normalizedPageRevisionInput{}, err
		}
		shellMode = strings.TrimSpace(envelope.Shell.Mode)
		if shellMode != "" && shellMode != PageShellSite &&
			shellMode != PageShellMinimal && shellMode != PageShellNone {
			return normalizedPageRevisionInput{}, fmt.Errorf("%w: invalid composition shell mode", ErrPageInvalid)
		}
	}
	validation := strings.TrimSpace(input.ValidationJSON)
	if validation == "" {
		validation = "{}"
	}
	validation, _, err = canonicalJSONObject(validation, "validation")
	if err != nil {
		return normalizedPageRevisionInput{}, err
	}
	input.PageMetaJSON = pageMeta
	input.ManifestJSON = manifest
	input.ValidationJSON = validation
	input.SourceHash = strings.TrimSpace(input.SourceHash)
	if err := validateSHA256(input.SourceHash, "source_hash", true); err != nil {
		return normalizedPageRevisionInput{}, err
	}
	if (input.SourceBundleRef == "") != (input.SourceHash == "") {
		return normalizedPageRevisionInput{}, fmt.Errorf("%w: source_bundle_ref and source_hash must be provided together", ErrPageInvalid)
	}
	if input.RevisionKind == PageRevisionStandardBaseline {
		if input.StandardContent == "" {
			// Empty standard pages are valid, so only disallow source data on the baseline.
		}
		if input.SourceBundleRef != "" || input.SourceHash != "" {
			return normalizedPageRevisionInput{}, fmt.Errorf("%w: standard baseline cannot contain an app source bundle", ErrPageInvalid)
		}
	}
	return normalizedPageRevisionInput{
		CreatePageRevisionInput: input,
		PageMetaHash:            pageMetaHash,
		ManifestHash:            manifestHash,
		Meta:                    meta,
		ShellMode:               shellMode,
	}, nil
}

// CreatePageProjectRevision appends an immutable revision and advances the
// working pointer in one transaction. A repeated RequestID returns the original
// revision only when every semantic input field still matches.
func (s *Store) CreatePageProjectRevision(input CreatePageRevisionInput) (*PageProjectRevision, bool, error) {
	normalized, err := normalizePageRevisionInput(input)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	project, err := getPageProject(tx, `WHERE id=?`, normalized.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrPageProjectNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if normalized.RevisionKind != PageRevisionStandardBaseline &&
		normalized.RevisionKind != project.Mode {
		return nil, false, fmt.Errorf("%w: revision kind %q does not match project mode %q",
			ErrPageInvalid, normalized.RevisionKind, project.Mode)
	}

	if normalized.RequestID != "" {
		existing, err := getPageProjectRevisionByRequest(tx, normalized.ProjectID, normalized.RequestID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
		if err == nil {
			if !pageRevisionMatchesInput(existing, normalized) {
				return nil, false, &PageIdempotencyConflictError{RequestID: normalized.RequestID}
			}
			return existing, false, nil
		}
	}

	if project.WorkingRevisionID != normalized.BaseRevisionID {
		return nil, false, &PageRevisionConflictError{
			ExpectedRevisionID: normalized.BaseRevisionID,
			CurrentRevisionID:  project.WorkingRevisionID,
		}
	}
	if normalized.RevisionKind == PageRevisionStandardBaseline {
		var count int
		if err := tx.QueryRow(`
			SELECT COUNT(*) FROM page_project_revisions
			WHERE project_id=? AND revision_kind='standard_baseline'`,
			normalized.ProjectID).Scan(&count); err != nil {
			return nil, false, err
		}
		if count != 0 {
			return nil, false, fmt.Errorf("%w: project already has a standard baseline", ErrPageInvalid)
		}
	}

	var revisionNo int
	if err := tx.QueryRow(`
		SELECT COALESCE(MAX(revision_no),0)+1
		FROM page_project_revisions WHERE project_id=?`,
		normalized.ProjectID).Scan(&revisionNo); err != nil {
		return nil, false, err
	}
	now := fmtTime(time.Now())
	result, err := tx.Exec(`
		INSERT INTO page_project_revisions(
			project_id,revision_no,parent_revision_id,revision_kind,
			page_meta_json,page_meta_hash,manifest_json,manifest_hash,
			standard_content,source_bundle_ref,source_hash,origin,actor_id,
			conversation_id,request_id,summary,validation_json,created_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		normalized.ProjectID, revisionNo, nullPositiveInt64(normalized.BaseRevisionID),
		normalized.RevisionKind, normalized.PageMetaJSON, normalized.PageMetaHash,
		normalized.ManifestJSON, normalized.ManifestHash, normalized.StandardContent,
		normalized.SourceBundleRef, normalized.SourceHash, normalized.Origin,
		normalized.ActorID, normalized.ConversationID, nullNonEmpty(normalized.RequestID),
		normalized.Summary, normalized.ValidationJSON, now)
	if err != nil {
		if normalized.RequestID != "" && strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, false, &PageIdempotencyConflictError{RequestID: normalized.RequestID}
		}
		return nil, false, err
	}
	revisionID, err := result.LastInsertId()
	if err != nil {
		return nil, false, err
	}

	if err := reservePageRouteTx(
		tx, normalized.ProjectID, revisionID, project.PostID,
		normalized.Meta.Lang, normalized.Meta.Slug, now,
	); err != nil {
		return nil, false, err
	}
	shellMode := project.ShellMode
	if normalized.RevisionKind == PageRevisionComposition && normalized.ShellMode != "" {
		shellMode = normalized.ShellMode
	}
	updateResult, err := tx.Exec(`
		UPDATE page_projects
		SET working_revision_id=?,shell_mode=?,build_status=?,updated_at=?
		WHERE id=? AND COALESCE(working_revision_id,0)=?`,
		revisionID, shellMode, PageProjectBuildIdle, now,
		normalized.ProjectID, normalized.BaseRevisionID)
	if err != nil {
		return nil, false, err
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if affected != 1 {
		current, readErr := getPageProject(tx, `WHERE id=?`, normalized.ProjectID)
		if readErr != nil {
			return nil, false, readErr
		}
		return nil, false, &PageRevisionConflictError{
			ExpectedRevisionID: normalized.BaseRevisionID,
			CurrentRevisionID:  current.WorkingRevisionID,
		}
	}
	revision, err := getPageProjectRevision(tx, revisionID)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return revision, true, nil
}

func pageRevisionMatchesInput(revision *PageProjectRevision, input normalizedPageRevisionInput) bool {
	if revision == nil {
		return false
	}
	return revision.ProjectID == input.ProjectID &&
		revision.ParentRevisionID == input.BaseRevisionID &&
		revision.RevisionKind == input.RevisionKind &&
		revision.PageMetaJSON == input.PageMetaJSON &&
		revision.PageMetaHash == input.PageMetaHash &&
		revision.ManifestJSON == input.ManifestJSON &&
		revision.ManifestHash == input.ManifestHash &&
		revision.StandardContent == input.StandardContent &&
		revision.SourceBundleRef == input.SourceBundleRef &&
		revision.SourceHash == input.SourceHash &&
		revision.Origin == input.Origin &&
		revision.ActorID == input.ActorID &&
		revision.ConversationID == input.ConversationID &&
		revision.RequestID == input.RequestID &&
		revision.Summary == input.Summary &&
		revision.ValidationJSON == input.ValidationJSON
}

func (s *Store) GetPageProjectRevision(id int64) (*PageProjectRevision, error) {
	revision, err := getPageProjectRevision(s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return revision, err
}

func (s *Store) ListPageProjectRevisions(projectID int64, limit int) ([]*PageProjectRevision, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT `+pageRevisionColumns+`
		FROM page_project_revisions
		WHERE project_id=?
		ORDER BY revision_no DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revisions []*PageProjectRevision
	for rows.Next() {
		revision, err := scanPageProjectRevision(rows)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

const pageRevisionColumns = `id,project_id,revision_no,parent_revision_id,revision_kind,
	page_meta_json,page_meta_hash,manifest_json,manifest_hash,standard_content,
	source_bundle_ref,source_hash,origin,actor_id,conversation_id,request_id,
	summary,validation_json,created_at`

func getPageProjectRevision(q pageProjectQueryer, revisionID int64) (*PageProjectRevision, error) {
	return scanPageProjectRevision(q.QueryRow(`
		SELECT `+pageRevisionColumns+`
		FROM page_project_revisions WHERE id=?`, revisionID))
}

func getPageProjectRevisionByRequest(q pageProjectQueryer, projectID int64, requestID string) (*PageProjectRevision, error) {
	return scanPageProjectRevision(q.QueryRow(`
		SELECT `+pageRevisionColumns+`
		FROM page_project_revisions
		WHERE project_id=? AND request_id=?`, projectID, requestID))
}

func scanPageProjectRevision(sc interface{ Scan(...any) error }) (*PageProjectRevision, error) {
	var revision PageProjectRevision
	var parent sql.NullInt64
	var requestID sql.NullString
	var created string
	if err := sc.Scan(
		&revision.ID, &revision.ProjectID, &revision.RevisionNo, &parent,
		&revision.RevisionKind, &revision.PageMetaJSON, &revision.PageMetaHash,
		&revision.ManifestJSON, &revision.ManifestHash, &revision.StandardContent,
		&revision.SourceBundleRef, &revision.SourceHash, &revision.Origin,
		&revision.ActorID, &revision.ConversationID, &requestID, &revision.Summary,
		&revision.ValidationJSON, &created,
	); err != nil {
		return nil, err
	}
	revision.ParentRevisionID = parent.Int64
	revision.RequestID = requestID.String
	revision.CreatedAt = parseRequiredTime(created)
	return &revision, nil
}

func reservePageRouteTx(
	tx *sql.Tx,
	projectID, revisionID, postID int64,
	lang, slug, createdAt string,
) error {
	lang = strings.TrimSpace(lang)
	slug = strings.TrimSpace(slug)
	if lang == "" || slug == "" {
		return fmt.Errorf("%w: route language and slug are required", ErrPageInvalid)
	}
	if PageRouteSlugReserved(slug) {
		return &PageRouteConflictError{Lang: lang, Slug: slug}
	}
	// A custom content type owns its top-level collection prefix even while it
	// is disabled. Reserving it here prevents a later enable operation from
	// silently shadowing an already published page project.
	var contentTypePrefix string
	err := tx.QueryRow(`
		SELECT url_prefix FROM content_types
		WHERE lower(trim(url_prefix))=lower(?) OR lower(trim(key))=lower(?)
		LIMIT 1`, slug, slug).Scan(&contentTypePrefix)
	if err == nil {
		return &PageRouteConflictError{Lang: lang, Slug: slug}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var conflictID int64
	err = tx.QueryRow(`
		SELECT id FROM posts
		WHERE lang=? AND slug=? AND id<>?
		LIMIT 1`, lang, slug, postID).Scan(&conflictID)
	if err == nil {
		return &PageRouteConflictError{Lang: lang, Slug: slug}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var reservedBy int64
	err = tx.QueryRow(`
		SELECT project_id FROM page_route_reservations
		WHERE lang=? AND slug=? AND project_id<>?
		LIMIT 1`, lang, slug, projectID).Scan(&reservedBy)
	if err == nil {
		return &PageRouteConflictError{Lang: lang, Slug: slug}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM page_route_reservations WHERE project_id=?`, projectID); err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT INTO page_route_reservations(project_id,revision_id,lang,slug,created_at)
		VALUES(?,?,?,?,?)`, projectID, revisionID, lang, slug, createdAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return &PageRouteConflictError{Lang: lang, Slug: slug}
	}
	return err
}

// PageRouteSlugReserved reports top-level paths owned by GCMS routing, static
// assets, SEO endpoints, or built-in extension collections. It is applied only
// to new page-project revisions; legacy standard pages are never rewritten.
func PageRouteSlugReserved(slug string) bool {
	slug = strings.ToLower(strings.Trim(strings.TrimSpace(slug), "/"))
	switch slug {
	case "admin", "api", "preview", "_gcms",
		"assets", "uploads",
		"posts", "category", "links", "page",
		"api-docs", "search",
		"sitemap.xml", "rss.xml", "robots.txt", "favicon.ico",
		"products", "docs", "events", "gallery":
		return true
	default:
		return false
	}
}

func (s *Store) GetPageRouteReservation(projectID int64) (*PageRouteReservation, error) {
	var reservation PageRouteReservation
	var created string
	err := s.db.QueryRow(`
		SELECT id,project_id,revision_id,lang,slug,created_at
		FROM page_route_reservations WHERE project_id=?`, projectID).
		Scan(&reservation.ID, &reservation.ProjectID, &reservation.RevisionID,
			&reservation.Lang, &reservation.Slug, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	reservation.CreatedAt = parseRequiredTime(created)
	return &reservation, nil
}

// PageRoutePrefixInUse reports whether a top-level custom-content prefix would
// shadow an existing standard page or an advanced page's reserved candidate
// route. Content-type creation uses this for a friendly fail-closed error; V4
// database triggers enforce the same invariant transactionally.
func (s *Store) PageRoutePrefixInUse(prefix string) (bool, error) {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return true, nil
	}
	var used int
	err := s.db.QueryRow(`
		SELECT CASE WHEN
			EXISTS (
				SELECT 1 FROM posts
				WHERE type='page' AND lower(trim(slug))=lower(?)
			)
			OR EXISTS (
				SELECT 1 FROM page_route_reservations
				WHERE lower(trim(slug))=lower(?)
			)
		THEN 1 ELSE 0 END`,
		prefix, prefix,
	).Scan(&used)
	return used != 0, err
}

// PageSlugUnavailable checks every owner of a page's top-level route. The
// reservation belonging to exceptPostID's own project is ignored so an
// advanced page can atomically publish its reserved candidate.
func (s *Store) PageSlugUnavailable(lang, slug string, exceptPostID int64) (bool, error) {
	lang = strings.TrimSpace(lang)
	slug = strings.Trim(strings.TrimSpace(slug), "/")
	if lang == "" || slug == "" || PageRouteSlugReserved(slug) {
		return true, nil
	}
	var used int
	err := s.db.QueryRow(`
		SELECT CASE WHEN
			EXISTS (
				SELECT 1 FROM posts
				WHERE lang=? AND slug=? AND id<>?
			)
			OR EXISTS (
				SELECT 1 FROM content_types
				WHERE lower(trim(key))=lower(?)
				   OR lower(trim(url_prefix))=lower(?)
			)
			OR EXISTS (
				SELECT 1 FROM page_route_reservations r
				WHERE r.lang=? AND r.slug=?
				  AND NOT EXISTS (
					SELECT 1 FROM page_projects p
					WHERE p.id=r.project_id AND p.post_id=?
				  )
			)
		THEN 1 ELSE 0 END`,
		lang, slug, exceptPostID,
		slug, slug,
		lang, slug, exceptPostID,
	).Scan(&used)
	return used != 0, err
}

func validPageMode(value string) bool {
	return value == PageModeComposition || value == PageModeApp
}

func validPageShell(value string) bool {
	return value == PageShellSite || value == PageShellMinimal || value == PageShellNone
}

func validRevisionKind(value string) bool {
	return value == PageRevisionStandardBaseline ||
		value == PageRevisionComposition ||
		value == PageRevisionApp
}

func validPageOrigin(value string) bool {
	return value == PageOriginAdmin || value == PageOriginPilot ||
		value == PageOriginAPI || value == PageOriginRestore
}

func parseRequiredTime(value string) time.Time {
	return parseTime(sql.NullString{String: value, Valid: value != ""})
}

func nullPositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullNonEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
