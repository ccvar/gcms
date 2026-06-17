package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/store"
)

type automationAuth struct {
	key    *store.AutomationKey
	scopes map[string]bool
}

const (
	apiIPRateLimit    = 240
	apiTokenRateLimit = 120
	apiRateWindow     = time.Minute
)

type apiRateLimiter struct {
	mu   sync.Mutex
	hits map[string]apiRateEntry
}

type apiRateEntry struct {
	count int
	reset time.Time
}

func newAPIRateLimiter() *apiRateLimiter {
	return &apiRateLimiter{hits: map[string]apiRateEntry{}}
}

func (l *apiRateLimiter) allow(key string, max int, window time.Duration) (time.Duration, bool) {
	if l == nil || key == "" || max <= 0 {
		return 0, true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	e := l.hits[key]
	if e.reset.IsZero() || now.After(e.reset) {
		l.hits[key] = apiRateEntry{count: 1, reset: now.Add(window)}
		if len(l.hits) > 4096 {
			for k, v := range l.hits {
				if now.After(v.reset) {
					delete(l.hits, k)
				}
			}
		}
		return 0, true
	}
	if e.count >= max {
		return time.Until(e.reset), false
	}
	e.count++
	l.hits[key] = e
	return 0, true
}

func (s *Server) checkAPIRateLimit(w http.ResponseWriter, r *http.Request, token string) bool {
	if s.apiLimiter == nil {
		return true
	}
	if retry, ok := s.apiLimiter.allow("ip:"+clientIP(r), apiIPRateLimit, apiRateWindow); !ok {
		apiRateLimitError(w, retry)
		return false
	}
	if token != "" {
		if retry, ok := s.apiLimiter.allow("token:"+apiTokenRateKey(token), apiTokenRateLimit, apiRateWindow); !ok {
			apiRateLimitError(w, retry)
			return false
		}
	}
	return true
}

func apiTokenRateKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:8])
}

func apiRateLimitError(w http.ResponseWriter, retry time.Duration) {
	if retry < time.Second {
		retry = time.Second
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
	apiError(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试。")
}

type apiContentInput struct {
	ID          *int64  `json:"id,omitempty"`
	Type        *string `json:"type,omitempty"`
	Lang        *string `json:"lang,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Title       *string `json:"title,omitempty"`
	Excerpt     *string `json:"excerpt,omitempty"`
	Content     *string `json:"content,omitempty"`
	MetaDesc    *string `json:"meta_desc,omitempty"`
	Keywords    *string `json:"keywords,omitempty"`
	CoverImage  *string `json:"cover_image,omitempty"`
	Author      *string `json:"author,omitempty"`
	Status      *string `json:"status,omitempty"`
	EditorMode  *string `json:"editor_mode,omitempty"`
	LinkURL     *string `json:"link_url,omitempty"`
	TransGroup  *string `json:"trans_group,omitempty"`
	CategoryID  *int64  `json:"category_id,omitempty"`
	PublishedAt *string `json:"published_at,omitempty"`
}

type apiCategory struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Lang        string `json:"lang,omitempty"`
	TransGroup  string `json:"trans_group,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Count       int    `json:"count,omitempty"`
}

type apiLanguageItem struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Tag     string `json:"tag"`
	Default bool   `json:"default"`
}

type apiContentItem struct {
	ID          int64        `json:"id"`
	Type        string       `json:"type"`
	Lang        string       `json:"lang"`
	Slug        string       `json:"slug"`
	Title       string       `json:"title"`
	Excerpt     string       `json:"excerpt"`
	Content     string       `json:"content,omitempty"`
	MetaDesc    string       `json:"meta_desc"`
	Keywords    string       `json:"keywords"`
	CoverImage  string       `json:"cover_image"`
	Author      string       `json:"author"`
	Status      string       `json:"status"`
	Featured    bool         `json:"featured"`
	EditorMode  string       `json:"editor_mode"`
	LinkURL     string       `json:"link_url,omitempty"`
	TransGroup  string       `json:"trans_group"`
	CategoryID  *int64       `json:"category_id"`
	Category    *apiCategory `json:"category,omitempty"`
	URL         string       `json:"url"`
	PublishedAt string       `json:"published_at,omitempty"`
	CreatedAt   string       `json:"created_at,omitempty"`
	UpdatedAt   string       `json:"updated_at,omitempty"`
}

type apiContentPreview struct {
	Item                     apiContentItem `json:"item"`
	PreviewURL               string         `json:"preview_url"`
	FrontendPreviewURL       string         `json:"frontend_preview_url,omitempty"`
	FrontendPreviewExpiresAt string         `json:"frontend_preview_expires_at,omitempty"`
	PublicURL                string         `json:"public_url"`
	ContentHTML              string         `json:"content_html"`
	TOC                      []apiHeading   `json:"toc,omitempty"`
	Robots                   string         `json:"robots"`
}

type apiHeading struct {
	Level int    `json:"level"`
	ID    string `json:"id"`
	Text  string `json:"text"`
}

type apiPreviewURLResponse struct {
	PreviewURL string `json:"preview_url"`
	ExpiresAt  string `json:"expires_at"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

type frontendPreviewClaims struct {
	Collection string `json:"collection"`
	ID         int64  `json:"id"`
	Expires    int64  `json:"exp"`
	Updated    int64  `json:"updated"`
	Revision   string `json:"rev,omitempty"`
}

const (
	frontendPreviewTTL           = 2 * time.Hour
	frontendPreviewSecretSetting = "preview.secret"
)

func (s *Server) apiLanguages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, "languages:read"); !ok {
		return
	}
	def := s.defaultLang()
	locales := s.locales()
	items := make([]apiLanguageItem, 0, len(locales))
	for _, l := range locales {
		items = append(items, apiLanguageItem{Code: l.Code, Name: l.Name, Tag: l.Tag, Default: l.Code == def})
	}
	writeJSON(w, http.StatusOK, map[string]any{"default": def, "items": items})
}

func (s *Server) apiListCategories(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind := ""
	switch collection {
	case "posts":
		kind = "post"
	case "links":
		kind = "link"
	default:
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "categories")); !ok {
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = s.defaultLang()
	}
	if lang != "all" && !s.langEnabled(lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	var (
		cats []*store.Category
		err  error
	)
	if lang == "all" {
		cats, err = s.store.AllCategories(kind)
	} else {
		cats, err = s.store.ListCategories(lang, kind)
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	items := make([]apiCategory, 0, len(cats))
	for _, cat := range cats {
		items = append(items, apiCategoryItem(cat))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "lang": lang, "kind": kind})
}

func (s *Server) apiUploadMedia(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAutomationScope(w, r, "media:write")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		apiError(w, http.StatusBadRequest, "bad_multipart", "表单解析失败或文件过大。")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		apiError(w, http.StatusBadRequest, "missing_file", "未收到文件。")
		return
	}
	defer file.Close()
	result, err := s.saveUploadFile(file, hdr.Filename)
	if err != nil {
		switch err.Error() {
		case "upload_disabled":
			apiError(w, http.StatusServiceUnavailable, "upload_disabled", "上传未启用。")
		case "bad_type":
			apiError(w, http.StatusBadRequest, "bad_type", "仅支持 jpg、png、gif、webp、svg、ico、avif。")
		case "save_failed":
			apiError(w, http.StatusInternalServerError, "save_failed", "保存失败。")
		default:
			apiError(w, http.StatusBadRequest, "write_failed", "文件过大或写入失败。")
		}
		return
	}
	_ = s.store.CreateAutomationLog(auth.key.ID, "upload", "media", 0, "上传媒体："+result.URL)
	writeJSON(w, http.StatusCreated, map[string]any{"url": result.URL, "name": result.Name, "size": result.Size})
}

func (s *Server) apiListContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, apiScope(collection, "read"))
	if !ok {
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	transGroup := strings.TrimSpace(r.URL.Query().Get("trans_group"))
	if lang == "" {
		if transGroup != "" {
			lang = "all"
		} else {
			lang = s.defaultLang()
		}
	}
	if lang != "all" && !s.langEnabled(lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	limit := apiIntParam(r, "limit", 20)
	offset := apiIntParam(r, "offset", 0)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	items, err := s.store.ListContentForAutomation(kind, lang, status, query, slug, transGroup, offset, limit)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	out := make([]apiContentItem, 0, len(items))
	for _, p := range items {
		out = append(out, s.apiContentItem(p, false))
	}
	_ = auth
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "limit": limit, "offset": offset, "lang": lang, "q": query, "slug": slug, "trans_group": transGroup})
}

func (s *Server) apiGetContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "read")); !ok {
		return
	}
	p, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(p, true)})
}

func (s *Server) apiPreviewContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	if collection != "posts" && collection != "links" {
		apiError(w, http.StatusNotFound, "not_found", "草稿预览仅支持文章和链接。")
		return
	}
	kind, _ := apiContentKind(collection)
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "read")); !ok {
		return
	}
	p, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	preview := *p
	if preview.PublishedAt.IsZero() {
		preview.PublishedAt = preview.UpdatedAt
		if preview.PublishedAt.IsZero() {
			preview.PublishedAt = preview.CreatedAt
		}
	}
	html, toc := s.renderedContent(&preview)
	frontendURL, frontendExp, _ := s.frontendPreviewURL(r, collection, &preview, time.Now().Add(frontendPreviewTTL))
	writeJSON(w, http.StatusOK, map[string]any{
		"preview": apiContentPreview{
			Item:                     s.apiContentItem(&preview, true),
			PreviewURL:               s.absForRequest(r, fmt.Sprintf("/api/admin/v1/%s/%d/preview", collection, preview.ID)),
			FrontendPreviewURL:       frontendURL,
			FrontendPreviewExpiresAt: apiTime(frontendExp),
			PublicURL:                s.absForRequest(r, s.apiContentURL(&preview)),
			ContentHTML:              string(html),
			TOC:                      apiHeadings(toc),
			Robots:                   "noindex, nofollow",
		},
	})
}

func (s *Server) apiCreatePreviewURL(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	if collection != "posts" && collection != "links" {
		apiError(w, http.StatusNotFound, "not_found", "草稿预览仅支持文章和链接。")
		return
	}
	kind, _ := apiContentKind(collection)
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "read")); !ok {
		return
	}
	p, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	expires := time.Now().Add(frontendPreviewTTL)
	previewURL, expires, err := s.frontendPreviewURL(r, collection, p, expires)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "sign_failed", "生成预览链接失败。")
		return
	}
	writeJSON(w, http.StatusCreated, apiPreviewURLResponse{
		PreviewURL: previewURL,
		ExpiresAt:  apiTime(expires),
		TTLSeconds: int64(frontendPreviewTTL.Seconds()),
	})
}

func (s *Server) apiCreateContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, apiScope(collection, "write"))
	if !ok {
		return
	}
	var in apiContentInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	p := &store.Post{Type: kind, Status: "draft", Lang: s.defaultLang(), EditorMode: "markdown"}
	if in.Lang != nil && strings.TrimSpace(*in.Lang) != "" {
		p.Lang = strings.TrimSpace(*in.Lang)
	}
	if !s.langEnabled(p.Lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	publishNeeded, errMsg := s.applyAPIContentInput(p, &in, true)
	if errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_request", errMsg)
		return
	}
	if publishNeeded && !automationScopeAllowed(auth.scopes, apiScope(collection, "publish")) {
		apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能发布该类内容。")
		return
	}
	if errMsg := s.validateAPICategory(p); errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_category", errMsg)
		return
	}
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(p.Lang, p.Slug, 0)
	id, err := s.store.CreatePost(p)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	p.ID = id
	created, _ := s.store.GetPostByID(id)
	_ = s.store.CreateAutomationLog(auth.key.ID, "create", kind, id, fmt.Sprintf("创建%s：%s", apiKindName(kind), p.Title))
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusCreated, map[string]any{"item": s.apiContentItem(created, true)})
}

func (s *Server) apiUpdateContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, apiScope(collection, "write"))
	if !ok {
		return
	}
	existing, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	var in apiContentInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if existing.Status != "draft" && !automationScopeAllowed(auth.scopes, apiScope(collection, "publish")) {
		apiError(w, http.StatusForbidden, "missing_scope", "修改已发布或定时内容需要该类内容的发布权限。")
		return
	}
	next := *existing
	publishNeeded, errMsg := s.applyAPIContentInput(&next, &in, false)
	if errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_request", errMsg)
		return
	}
	if publishNeeded && !automationScopeAllowed(auth.scopes, apiScope(collection, "publish")) {
		apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能发布该类内容。")
		return
	}
	if errMsg := s.validateAPICategory(&next); errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_category", errMsg)
		return
	}
	s.fillDefaultAuthor(&next)
	next.Slug = s.uniqueSlug(next.Lang, next.Slug, next.ID)
	if err := s.store.UpdatePost(&next); err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	updated, _ := s.store.GetPostByID(next.ID)
	_ = s.store.CreateAutomationLog(auth.key.ID, "update", kind, next.ID, fmt.Sprintf("更新%s：%s", apiKindName(kind), next.Title))
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(updated, true)})
}

func (s *Server) requireAutomationToken(w http.ResponseWriter, r *http.Request) (*automationAuth, bool) {
	token := apiTokenFromRequest(r)
	if !s.checkAPIRateLimit(w, r, token) {
		return nil, false
	}
	if token == "" {
		apiError(w, http.StatusUnauthorized, "missing_token", "缺少访问密钥。")
		return nil, false
	}
	key, exists, err := s.store.GetAutomationKeyByToken(token)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return nil, false
	}
	if !exists {
		apiError(w, http.StatusUnauthorized, "invalid_token", "访问密钥无效或已吊销。")
		return nil, false
	}
	auth := &automationAuth{key: key, scopes: apiScopeMap(key.Scopes)}
	_ = s.store.TouchAutomationKey(key.ID)
	return auth, true
}

func (s *Server) requireAutomationScope(w http.ResponseWriter, r *http.Request, scope string) (*automationAuth, bool) {
	auth, ok := s.requireAutomationToken(w, r)
	if !ok {
		return nil, false
	}
	if !automationScopeAllowed(auth.scopes, scope) {
		apiError(w, http.StatusForbidden, "missing_scope", "访问权限不足。")
		return nil, false
	}
	return auth, true
}

func (s *Server) requireAutomationAnyScope(w http.ResponseWriter, r *http.Request, scopes ...string) (*automationAuth, bool) {
	auth, ok := s.requireAutomationToken(w, r)
	if !ok {
		return nil, false
	}
	for _, scope := range scopes {
		if automationScopeAllowed(auth.scopes, scope) {
			return auth, true
		}
	}
	apiError(w, http.StatusForbidden, "missing_scope", "访问权限不足。")
	return nil, false
}

func (s *Server) apiContentByID(w http.ResponseWriter, r *http.Request, kind string) (*store.Post, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(w, http.StatusBadRequest, "bad_id", "内容 ID 无效。")
		return nil, false
	}
	p, err := s.store.GetPostByID(id)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return nil, false
	}
	if p == nil || p.Type != kind {
		apiError(w, http.StatusNotFound, "not_found", "内容不存在。")
		return nil, false
	}
	return p, true
}

func (s *Server) frontendPreviewURL(r *http.Request, collection string, p *store.Post, expires time.Time) (string, time.Time, error) {
	token, err := s.signFrontendPreviewToken(frontendPreviewClaims{
		Collection: collection,
		ID:         p.ID,
		Expires:    expires.Unix(),
		Updated:    previewUpdatedUnix(p),
		Revision:   previewRevision(p),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	path := fmt.Sprintf("/preview/%s/%d?token=%s", collection, p.ID, url.QueryEscape(token))
	return s.absForRequest(r, path), expires, nil
}

func previewUpdatedUnix(p *store.Post) int64 {
	if p == nil || p.UpdatedAt.IsZero() {
		return 0
	}
	return p.UpdatedAt.UTC().Unix()
}

func previewRevision(p *store.Post) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	write := func(s string) {
		b.WriteString(s)
		b.WriteByte('\x00')
	}
	write(p.Type)
	write(strconv.FormatInt(p.ID, 10))
	write(p.Lang)
	write(p.Slug)
	write(p.Title)
	write(p.Excerpt)
	write(p.Content)
	write(p.MetaDesc)
	write(p.Keywords)
	write(p.CoverImage)
	write(p.Author)
	write(p.Status)
	write(p.EditorMode)
	write(p.LinkURL)
	write(p.TransGroup)
	if p.CategoryID.Valid {
		write(strconv.FormatInt(p.CategoryID.Int64, 10))
	}
	write(p.PublishedAt.UTC().Format(time.RFC3339Nano))
	write(p.UpdatedAt.UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", sum[:8])
}

func (s *Server) previewSigningSecret() ([]byte, error) {
	secret := strings.TrimSpace(s.store.Setting(frontendPreviewSecretSetting))
	if secret == "" {
		secret = randToken()
		if err := s.store.SetSetting(frontendPreviewSecretSetting, secret); err != nil {
			return nil, err
		}
	}
	return []byte(secret), nil
}

func (s *Server) signFrontendPreviewToken(claims frontendPreviewClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig, err := s.frontendPreviewSignature(encodedPayload)
	if err != nil {
		return "", err
	}
	return encodedPayload + "." + sig, nil
}

func (s *Server) frontendPreviewSignature(encodedPayload string) (string, error) {
	secret, err := s.previewSigningSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifyFrontendPreviewToken(token string) (frontendPreviewClaims, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return frontendPreviewClaims{}, "invalid"
	}
	want, err := s.frontendPreviewSignature(parts[0])
	if err != nil || !hmac.Equal([]byte(want), []byte(parts[1])) {
		return frontendPreviewClaims{}, "invalid"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return frontendPreviewClaims{}, "invalid"
	}
	var claims frontendPreviewClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return frontendPreviewClaims{}, "invalid"
	}
	if claims.Expires <= 0 || time.Now().After(time.Unix(claims.Expires, 0)) {
		return frontendPreviewClaims{}, "expired"
	}
	if claims.ID <= 0 || (claims.Collection != "posts" && claims.Collection != "links") {
		return frontendPreviewClaims{}, "invalid"
	}
	return claims, ""
}

func (s *Server) applyAPIContentInput(p *store.Post, in *apiContentInput, creating bool) (bool, string) {
	if in.Type != nil && strings.TrimSpace(*in.Type) != "" && strings.TrimSpace(*in.Type) != p.Type {
		return false, "不能通过该接口修改内容类型。"
	}
	if in.Title != nil {
		p.Title = strings.TrimSpace(*in.Title)
	}
	if in.Excerpt != nil {
		p.Excerpt = strings.TrimSpace(*in.Excerpt)
	}
	if in.Content != nil {
		p.Content = *in.Content
	}
	if in.MetaDesc != nil {
		p.MetaDesc = strings.TrimSpace(*in.MetaDesc)
	}
	if in.Keywords != nil {
		p.Keywords = strings.TrimSpace(*in.Keywords)
	}
	if in.CoverImage != nil {
		p.CoverImage = strings.TrimSpace(*in.CoverImage)
	}
	if in.Author != nil {
		p.Author = strings.TrimSpace(*in.Author)
	}
	if in.EditorMode != nil {
		switch strings.TrimSpace(*in.EditorMode) {
		case "", "markdown":
			p.EditorMode = "markdown"
		case "rich":
			p.EditorMode = "rich"
		default:
			return false, "editor_mode 只能是 markdown 或 rich。"
		}
	}
	if in.LinkURL != nil {
		p.LinkURL = strings.TrimSpace(*in.LinkURL)
	}
	if in.TransGroup != nil && creating {
		p.TransGroup = strings.TrimSpace(*in.TransGroup)
	}
	if in.Slug != nil {
		p.Slug = slugify(strings.TrimSpace(*in.Slug))
	}
	if p.Slug == "" && creating {
		p.Slug = slugify(p.Title)
	}
	if p.Slug == "" && creating {
		p.Slug = p.Type + "-" + strconv.FormatInt(time.Now().Unix(), 36)
	}
	if in.CategoryID != nil {
		if p.Type == "page" {
			return false, "页面不支持 category_id。"
		}
		if *in.CategoryID > 0 {
			p.CategoryID = sql.NullInt64{Int64: *in.CategoryID, Valid: true}
		} else {
			p.CategoryID = sql.NullInt64{}
		}
	}
	publishNeeded := false
	if in.Status != nil {
		status := strings.TrimSpace(*in.Status)
		switch status {
		case "", "draft":
			p.Status = "draft"
		case "published", "scheduled":
			p.Status = status
			publishNeeded = true
		default:
			return false, "status 只能是 draft、published 或 scheduled。"
		}
	}
	if in.PublishedAt != nil {
		t, err := parseAPITime(strings.TrimSpace(*in.PublishedAt))
		if err != nil {
			return false, "published_at 需要使用 RFC3339 或 2006-01-02T15:04 格式。"
		}
		p.PublishedAt = t
	}
	if p.Status == "scheduled" {
		publishNeeded = true
		if p.PublishedAt.IsZero() {
			return false, "定时发布需要 published_at。"
		}
	}
	if p.Status == "published" {
		publishNeeded = true
	}
	if p.Type == "link" && p.LinkURL == "" && p.Status != "draft" {
		return false, "发布链接时 link_url 不能为空。"
	}
	if p.Title == "" {
		return false, "标题不能为空。"
	}
	return publishNeeded, ""
}

func (s *Server) validateAPICategory(p *store.Post) string {
	if !p.CategoryID.Valid {
		return ""
	}
	cat, err := s.store.GetCategoryByID(p.CategoryID.Int64)
	if err != nil {
		return err.Error()
	}
	if cat == nil {
		return "分类不存在。"
	}
	want := "post"
	if p.Type == "link" {
		want = "link"
	}
	if cat.Kind != want || cat.Lang != p.Lang {
		return "分类语种或类型与内容不匹配。"
	}
	return ""
}

func (s *Server) apiContentItem(p *store.Post, includeContent bool) apiContentItem {
	author := strings.TrimSpace(p.Author)
	if author == "" && (p.Type == "post" || p.Type == "link") {
		author = s.defaultContentAuthor(p.Type, p.Lang)
	}
	var categoryID *int64
	if p.CategoryID.Valid {
		v := p.CategoryID.Int64
		categoryID = &v
	}
	var cat *apiCategory
	if p.Category != nil {
		v := apiCategoryItem(p.Category)
		cat = &v
	}
	item := apiContentItem{
		ID: p.ID, Type: p.Type, Lang: p.Lang, Slug: p.Slug, Title: p.Title, Excerpt: p.Excerpt,
		MetaDesc: p.MetaDesc, Keywords: p.Keywords, CoverImage: p.CoverImage, Author: author,
		Status: p.Status, Featured: p.Featured, EditorMode: p.EditorMode, LinkURL: p.LinkURL,
		TransGroup: p.TransGroup, CategoryID: categoryID, Category: cat, URL: s.apiContentURL(p),
		PublishedAt: apiTime(p.PublishedAt), CreatedAt: apiTime(p.CreatedAt), UpdatedAt: apiTime(p.UpdatedAt),
	}
	if includeContent {
		item.Content = p.Content
	}
	return item
}

func apiCategoryItem(c *store.Category) apiCategory {
	if c == nil {
		return apiCategory{}
	}
	return apiCategory{
		ID: c.ID, Slug: c.Slug, Name: c.Name, Description: c.Description,
		Lang: c.Lang, TransGroup: c.TransGroup, Kind: c.Kind, Count: c.Count,
	}
}

func apiHeadings(toc []Heading) []apiHeading {
	if len(toc) == 0 {
		return nil
	}
	out := make([]apiHeading, 0, len(toc))
	for _, h := range toc {
		out = append(out, apiHeading{Level: h.Level, ID: h.ID, Text: h.Text})
	}
	return out
}

func (s *Server) apiContentURL(p *store.Post) string {
	base := "/" + p.Lang
	switch p.Type {
	case "post":
		return base + "/posts/" + p.Slug
	case "link":
		return base + "/links/" + p.Slug
	default:
		return base + "/" + p.Slug
	}
}

func apiContentKind(collection string) (string, bool) {
	switch collection {
	case "posts":
		return "post", true
	case "pages":
		return "page", true
	case "links":
		return "link", true
	default:
		return "", false
	}
}

func apiKindName(kind string) string {
	switch kind {
	case "page":
		return "页面"
	case "link":
		return "链接"
	default:
		return "文章"
	}
}

func apiScopeMap(scopes string) map[string]bool {
	out := map[string]bool{}
	for _, s := range strings.Split(scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out[s] = true
		}
	}
	return out
}

func apiScope(collection, action string) string {
	return collection + ":" + action
}

func automationScopeActions(resource string) []string {
	switch resource {
	case "posts", "links":
		return []string{"read", "categories", "write", "publish"}
	default:
		return []string{"read", "write", "publish"}
	}
}

func automationScopeAllowed(scopes map[string]bool, scope string) bool {
	if scopes[scope] {
		return true
	}
	parts := strings.Split(scope, ":")
	return len(parts) == 2 && scopes["content:"+parts[1]]
}

func apiTokenFromRequest(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-GCMS-API-Key")); token != "" {
		return token
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func decodeAPIJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 请求体无效："+err.Error())
		return false
	}
	return true
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": code, "message": message})
}

func apiIntParam(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil {
		return def
	}
	if n < 0 {
		return def
	}
	return n
}

func parseAPITime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", s, time.Local)
}

func apiTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
