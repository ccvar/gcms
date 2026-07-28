package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cms.ccvar.com/internal/store"
)

const (
	compositionContactRetentionSetting = "contact.form_retention_days"
	compositionContactDefaultRetention = 30
	compositionContactMaxBody          = 32 << 10
	compositionContactActionPath       = "/api/forms/contact"
)

var compositionContactInboxMu sync.Mutex

type compositionContactRecord struct {
	ID         string            `json:"id"`
	ReceivedAt string            `json:"received_at"`
	ProjectID  int64             `json:"project_id"`
	RevisionID int64             `json:"revision_id"`
	PageID     int64             `json:"page_id"`
	SectionID  string            `json:"section_id"`
	Lang       string            `json:"lang"`
	Fields     map[string]string `json:"fields"`
	SourceHash string            `json:"source_hash"`
}

func compositionContactRetentionDays(s *Server) int {
	days, err := strconv.Atoi(strings.TrimSpace(s.store.Setting(compositionContactRetentionSetting)))
	if err != nil || days <= 0 {
		return compositionContactDefaultRetention
	}
	if days > 365 {
		return 365
	}
	return days
}

func compositionContactPropsForSection(
	manifest *CompositionManifest,
	sectionID string,
) (*compositionContactFormProps, bool) {
	var out *compositionContactFormProps
	walkCompositionSections(manifest.Sections, func(section *CompositionSection, _ string) {
		if out != nil || section.ID != sectionID || section.Type != "form.contact" {
			return
		}
		var props compositionContactFormProps
		if json.Unmarshal(section.Props, &props) == nil {
			out = &props
		}
	})
	return out, out != nil
}

func compositionManifestHasContactForm(manifest *CompositionManifest) bool {
	found := false
	if manifest == nil {
		return false
	}
	walkCompositionSections(manifest.Sections, func(section *CompositionSection, _ string) {
		if section != nil && section.Type == "form.contact" {
			found = true
		}
	})
	return found
}

func compositionContactOriginBase(cfg CloudflareConfig) string {
	raw := strings.TrimRight(normalizeCloudflareOrigin(cfg.OriginURL), "/")
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return raw
}

// validateCompositionContactStaticExport rejects an export before any output
// tree is created when a published contact form cannot post to a distinct GCMS
// origin. A relative action on the public static host would be a false-success:
// Cloudflare owns no /api/forms/contact handler there.
func (s *Server) validateCompositionContactStaticExport(cfg CloudflareConfig) error {
	projects, err := s.store.ListPageProjects()
	if err != nil {
		return err
	}
	hasContact := false
	for _, project := range projects {
		if project == nil || project.Mode != store.PageModeComposition ||
			project.PublishedRevisionID <= 0 {
			continue
		}
		revision, err := s.store.GetPageProjectRevision(project.PublishedRevisionID)
		if err != nil {
			return err
		}
		if revision == nil || revision.ProjectID != project.ID ||
			revision.RevisionKind != store.PageRevisionComposition {
			continue
		}
		var manifest CompositionManifest
		if err := json.Unmarshal([]byte(revision.ManifestJSON), &manifest); err != nil {
			return fmt.Errorf("读取自由页面联系表单配置失败: %w", err)
		}
		if compositionManifestHasContactForm(&manifest) {
			hasContact = true
			break
		}
	}
	if !hasContact {
		return nil
	}
	origin := compositionContactOriginBase(cfg)
	if origin == "" {
		return errors.New("包含联系表单的静态页面必须配置合法的 GCMS OriginURL")
	}
	parsedOrigin, _ := url.Parse(origin)
	if parsedOrigin == nil || parsedOrigin.Scheme != "https" {
		return errors.New("联系表单 GCMS OriginURL 必须使用 HTTPS")
	}
	originHost := baseURLHost(origin)
	for _, domain := range cfg.publicDomains() {
		if sameCloudflareDNSName(originHost, domain.Host) {
			return errors.New("联系表单 OriginURL 必须与 Cloudflare 公共域名不同")
		}
	}
	if cfg.usingPages() {
		project := normalizeCloudflarePagesProjectName(cfg.PagesProjectName)
		if strings.TrimSpace(cfg.PagesProjectName) == "" {
			project = cloudflareDefaultProjectNameForHost(cfg.primaryHost())
		}
		if project != "" &&
			sameCloudflareDNSName(originHost, project+".pages.dev") {
			return errors.New("联系表单 OriginURL 必须与 Cloudflare 公共域名不同")
		}
	}
	return nil
}

func (s *Server) compositionContactCloudflareConfig(r *http.Request) CloudflareConfig {
	if r != nil {
		if cfg, ok := staticExportCloudflareConfig(r.Context()); ok {
			return cfg
		}
	}
	return s.cloudflareConfigForRequest(r)
}

// compositionContactAction keeps the source-site action relative. During a
// Cloudflare static render it points only at the configured GCMS origin, so a
// static document never pretends that the public asset host owns a dynamic
// form endpoint.
func (s *Server) compositionContactAction(r *http.Request) string {
	if r == nil {
		return compositionContactActionPath
	}
	cfg := s.compositionContactCloudflareConfig(r)
	requestHost := normalizeCloudflareDomainHost(requestHost(r))
	public := false
	for _, domain := range cfg.publicDomains() {
		if requestHost != "" && sameCloudflareDNSName(requestHost, domain.Host) {
			public = true
			break
		}
	}
	origin := compositionContactOriginBase(cfg)
	if public && origin != "" &&
		!sameCloudflareDNSName(baseURLHost(origin), requestHost) {
		return origin + compositionContactActionPath
	}
	return compositionContactActionPath
}

// compositionContactRequestOrigin returns the trusted public origin to use
// after a successful cross-origin form POST. An empty origin means the source
// page was same-origin and should receive a relative redirect.
func (s *Server) compositionContactRequestOrigin(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return "", fetchSite != "cross-site"
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	originHost := normalizeCloudflareDomainHost(parsed.Host)
	targetHost := normalizeCloudflareDomainHost(requestHost(r))
	if originHost == "" || targetHost == "" {
		return "", false
	}
	if sameCloudflareDNSName(originHost, targetHost) {
		if fetchSite == "cross-site" {
			return "", false
		}
		return "", true
	}

	cfg := s.cloudflareConfigForRequest(r)
	if parsed.Scheme != "https" {
		return "", false
	}
	sourceOrigin := compositionContactOriginBase(cfg)
	if sourceOrigin == "" ||
		!sameCloudflareDNSName(targetHost, baseURLHost(sourceOrigin)) {
		return "", false
	}
	for _, domain := range cfg.publicDomains() {
		if sameCloudflareDNSName(originHost, domain.Host) {
			return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String(), true
		}
	}
	return "", false
}

func compositionContactValue(form url.Values, key string, maxRunes int, multiline bool) (string, error) {
	values, ok := form[key]
	if !ok {
		return "", nil
	}
	if len(values) != 1 {
		return "", errors.New("字段重复")
	}
	value := strings.TrimSpace(values[0])
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') ||
		len([]rune(value)) > maxRunes {
		return "", errors.New("字段内容无效或过长")
	}
	if !multiline && strings.ContainsAny(value, "\r\n") {
		return "", errors.New("单行字段不能包含换行")
	}
	return value, nil
}

func (s *Server) writeCompositionContactRecord(record compositionContactRecord) error {
	root := s.store.PageProjectStorageDir()
	if strings.TrimSpace(root) == "" {
		return errors.New("page project storage is unavailable")
	}
	dir := filepath.Join(root, "forms", "contact")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	now := time.Now().UTC()
	filename := filepath.Join(dir, "contact-"+now.Format("2006-01-02")+".jsonl")
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	compositionContactInboxMu.Lock()
	defer compositionContactInboxMu.Unlock()
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}

	cutoff := now.AddDate(0, 0, -compositionContactRetentionDays(s))
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "contact-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		dateText := strings.TrimSuffix(strings.TrimPrefix(name, "contact-"), ".jsonl")
		date, parseErr := time.Parse("2006-01-02", dateText)
		if parseErr == nil && date.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}

func compositionContactRespond(
	w http.ResponseWriter,
	r *http.Request,
	post *store.Post,
	returnOrigin string,
) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
		return
	}
	target := "/"
	if post != nil {
		target = "/" + post.Lang + publicContentPath(post.Type, post.Slug)
	}
	if returnOrigin != "" {
		target = strings.TrimRight(returnOrigin, "/") + target
	}
	separator := "?"
	if strings.Contains(target, "?") {
		separator = "&"
	}
	http.Redirect(w, r, target+separator+"contact=sent#contact", http.StatusSeeOther)
}

func (s *Server) submitCompositionContact(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	returnOrigin, originOK := s.compositionContactRequestOrigin(r)
	if !originOK {
		apiError(w, http.StatusForbidden, "origin_forbidden", "跨站表单提交已拒绝。")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		apiError(w, http.StatusUnsupportedMediaType, "content_type_invalid",
			"联系表单只接受 application/x-www-form-urlencoded。")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, compositionContactMaxBody)
	if err := r.ParseForm(); err != nil {
		apiError(w, http.StatusBadRequest, "form_invalid", "联系表单格式无效。")
		return
	}
	projectID, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("_project_id")), 10, 64)
	if err != nil || projectID <= 0 {
		apiError(w, http.StatusBadRequest, "form_context_invalid", "联系表单页面上下文无效。")
		return
	}
	revisionID, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("_revision_id")), 10, 64)
	if err != nil || revisionID <= 0 {
		apiError(w, http.StatusBadRequest, "form_context_invalid", "联系表单修订上下文无效。")
		return
	}
	sectionID := strings.TrimSpace(r.PostForm.Get("_section_id"))
	if !compositionIDPattern.MatchString(sectionID) {
		apiError(w, http.StatusBadRequest, "form_context_invalid", "联系表单区块上下文无效。")
		return
	}
	project, err := s.store.GetPageProject(projectID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if project == nil || project.Mode != store.PageModeComposition ||
		project.PublishedRevisionID != revisionID {
		s.notFound(w, r)
		return
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if revision == nil || revision.ProjectID != project.ID ||
		revision.RevisionKind != store.PageRevisionComposition {
		s.notFound(w, r)
		return
	}
	post, err := s.store.GetPostByID(project.PostID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if post == nil || post.Type != "page" || post.Status != "published" {
		s.notFound(w, r)
		return
	}
	manifest, _, _, err := NormalizeCompositionManifest([]byte(revision.ManifestJSON))
	if err != nil {
		s.serverError(w, err)
		return
	}
	props, ok := compositionContactPropsForSection(manifest, sectionID)
	if !ok {
		s.notFound(w, r)
		return
	}

	allowed := map[string]bool{
		"_project_id": true, "_revision_id": true, "_section_id": true,
		"website": true, "privacy_consent": true,
	}
	for _, field := range props.Fields {
		allowed[field] = true
	}
	for key, values := range r.PostForm {
		if !allowed[key] || len(values) != 1 {
			apiError(w, http.StatusBadRequest, "form_field_invalid", "联系表单包含未知或重复字段。")
			return
		}
	}
	if strings.TrimSpace(r.PostForm.Get("website")) != "" {
		// Honeypot submissions receive the same success response, avoiding a
		// signal that helps bots tune around the spam rule.
		compositionContactRespond(w, r, post, returnOrigin)
		return
	}
	if retry, ok := s.apiLimiter.allow(
		"composition-contact:"+strconv.FormatInt(projectID, 10)+":"+clientIP(r),
		5, 10*time.Minute,
	); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(maxInt(1, int(retry.Seconds()))))
		apiError(w, http.StatusTooManyRequests, "rate_limited", "提交过于频繁，请稍后再试。")
		return
	}
	if props.PrivacyHref != "" {
		consent := strings.ToLower(strings.TrimSpace(r.PostForm.Get("privacy_consent")))
		if consent != "1" && consent != "on" && consent != "true" {
			apiError(w, http.StatusUnprocessableEntity, "privacy_consent_required", "请先确认隐私同意。")
			return
		}
	}
	fields := make(map[string]string, len(props.Fields))
	for _, field := range props.Fields {
		maxRunes := 500
		multiline := field == "message"
		if multiline {
			maxRunes = 4000
		}
		value, valueErr := compositionContactValue(r.PostForm, field, maxRunes, multiline)
		if valueErr != nil {
			apiError(w, http.StatusUnprocessableEntity, "form_field_invalid",
				fmt.Sprintf("%s：%v", field, valueErr))
			return
		}
		if (field == "name" || field == "email" || field == "message") && value == "" {
			apiError(w, http.StatusUnprocessableEntity, "form_field_required", field+" 不能为空。")
			return
		}
		if field == "email" && value != "" {
			address, parseErr := mail.ParseAddress(value)
			if parseErr != nil || !strings.EqualFold(address.Address, value) {
				apiError(w, http.StatusUnprocessableEntity, "form_field_invalid", "email 格式无效。")
				return
			}
		}
		fields[field] = value
	}
	secret, err := s.previewSigningSecret()
	if err != nil {
		s.serverError(w, err)
		return
	}
	source := sha256.Sum256(append(append([]byte(nil), secret...), []byte("\x00"+clientIP(r))...))
	record := compositionContactRecord{
		ID: randToken(), ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ProjectID: project.ID, RevisionID: revision.ID, PageID: post.ID,
		SectionID: sectionID, Lang: post.Lang, Fields: fields,
		SourceHash: hex.EncodeToString(source[:16]),
	}
	if err := s.writeCompositionContactRecord(record); err != nil {
		s.serverError(w, err)
		return
	}
	compositionContactRespond(w, r, post, returnOrigin)
}
