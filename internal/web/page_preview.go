package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/store"
)

const (
	standardPagePreviewTokenVersion = 1
	standardPagePreviewTokenKind    = "standard_page"
)

// pagePreviewClaims is deliberately separate from frontendPreviewClaims.
//
// The existing content-preview token predates page projects and only covers
// articles and links. A separate signing domain keeps standard-page previews
// from being replayed as another preview kind, while the reserved project and
// build fields let the token contract evolve without reusing the standard-page
// renderer for future composition/app revisions.
type pagePreviewClaims struct {
	Version         int    `json:"v"`
	Kind            string `json:"kind"`
	SiteID          int64  `json:"site_id,omitempty"`
	PageID          int64  `json:"page_id"`
	ContentRevision string `json:"content_revision"`
	Expires         int64  `json:"exp"`
	ProjectRevision string `json:"project_revision,omitempty"`
	BuildID         string `json:"build_id,omitempty"`
}

type pagePreviewURLResponse struct {
	PreviewURL      string `json:"preview_url"`
	ContentRevision string `json:"content_revision"`
	ExpiresAt       string `json:"expires_at"`
	TTLSeconds      int64  `json:"ttl_seconds"`
	ProjectRevision string `json:"project_revision,omitempty"`
	BuildID         string `json:"build_id,omitempty"`
}

// registerPagePreviewRoutes is kept in this file so Phase 0 only needs one
// explicit call from the central route table. Literal "pages" patterns are
// more specific than the legacy {collection} patterns and therefore preserve
// the existing article/link handlers unchanged.
func (s *Server) registerPagePreviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/admin/v1/pages/{id}/preview-url", s.apiCreateStandardPagePreviewURL)
	mux.HandleFunc("POST /api/platform/v1/sites/{siteID}/pages/{id}/preview-url", s.apiCreateStandardPagePreviewURL)
	mux.HandleFunc("GET /preview/pages/{id}", s.frontendStandardPagePreview)
}

// apiCreateStandardPagePreviewURL issues a short-lived, content-bound URL for
// a standard page. It does not change the page status or public route.
func (s *Server) apiCreateStandardPagePreviewURL(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScope("pages", "read")); !ok {
		return
	}
	p, ok := s.apiContentByID(w, r, "page")
	if !ok {
		return
	}

	expires := time.Now().Add(frontendPreviewTTL)
	previewURL, revision, err := s.standardPagePreviewURL(r, p, expires)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "sign_failed", "生成页面预览链接失败。")
		return
	}
	writeJSON(w, http.StatusCreated, pagePreviewURLResponse{
		PreviewURL:      previewURL,
		ContentRevision: revision,
		ExpiresAt:       apiTime(expires),
		TTLSeconds:      int64(frontendPreviewTTL.Seconds()),
	})
}

func (s *Server) standardPagePreviewURL(r *http.Request, p *store.Post, expires time.Time) (string, string, error) {
	if p == nil || p.ID <= 0 || p.Type != "page" || expires.IsZero() {
		return "", "", fmt.Errorf("页面预览参数无效")
	}
	revision := previewRevision(p)
	if revision == "" {
		return "", "", fmt.Errorf("页面内容修订摘要为空")
	}
	token, err := s.signPagePreviewToken(pagePreviewClaims{
		Version:         standardPagePreviewTokenVersion,
		Kind:            standardPagePreviewTokenKind,
		SiteID:          s.platformSiteID,
		PageID:          p.ID,
		ContentRevision: revision,
		Expires:         expires.Unix(),
	})
	if err != nil {
		return "", "", err
	}

	base, sitePrefixed := s.frontendPreviewBase(r)
	path := fmt.Sprintf("/preview/pages/%d?token=%s", p.ID, url.QueryEscape(token))
	if sitePrefixed {
		path = fmt.Sprintf("/preview/sites/%d/pages/%d?token=%s", s.platformSiteID, p.ID, url.QueryEscape(token))
	}
	return absWithBase(base, path), revision, nil
}

func (s *Server) signPagePreviewToken(claims pagePreviewClaims) (string, error) {
	if claims.Version != standardPagePreviewTokenVersion ||
		claims.Kind != standardPagePreviewTokenKind ||
		claims.PageID <= 0 ||
		strings.TrimSpace(claims.ContentRevision) == "" ||
		claims.Expires <= 0 {
		return "", fmt.Errorf("页面预览票据参数无效")
	}
	// Phase 0 only renders standard pages. The fields exist in the signed
	// contract for later project/app previews, but accepting them now would
	// falsely imply that a project revision or build had been rendered.
	if claims.ProjectRevision != "" || claims.BuildID != "" {
		return "", fmt.Errorf("页面工程预览尚未启用")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig, err := s.pagePreviewSignature(encodedPayload)
	if err != nil {
		return "", err
	}
	return encodedPayload + "." + sig, nil
}

func (s *Server) pagePreviewSignature(encodedPayload string) (string, error) {
	secret, err := s.previewSigningSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("standard-page-preview\x00"))
	_, _ = mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifyPagePreviewToken(token string) (pagePreviewClaims, string) {
	if len(token) == 0 || len(token) > 8<<10 {
		return pagePreviewClaims{}, "invalid"
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return pagePreviewClaims{}, "invalid"
	}
	want, err := s.pagePreviewSignature(parts[0])
	if err != nil || !hmac.Equal([]byte(want), []byte(parts[1])) {
		return pagePreviewClaims{}, "invalid"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return pagePreviewClaims{}, "invalid"
	}
	var claims pagePreviewClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return pagePreviewClaims{}, "invalid"
	}
	if claims.Expires <= 0 || !time.Now().Before(time.Unix(claims.Expires, 0)) {
		return pagePreviewClaims{}, "expired"
	}
	if claims.Version != standardPagePreviewTokenVersion ||
		claims.Kind != standardPagePreviewTokenKind ||
		claims.SiteID != s.platformSiteID ||
		claims.PageID <= 0 ||
		strings.TrimSpace(claims.ContentRevision) == "" ||
		claims.ProjectRevision != "" ||
		claims.BuildID != "" {
		return pagePreviewClaims{}, "invalid"
	}
	return claims, ""
}

// signedPreviewMediaTokenValid centralizes the authorization rule needed by
// the platform-prefixed /media/{token}/uploads/... bridge. The bridge already
// supports legacy article/link preview tokens; Phase 0 adds standard-page
// tokens without weakening either token verifier.
func (s *Server) signedPreviewMediaTokenValid(token string) bool {
	if _, state := s.verifyFrontendPreviewToken(token); state == "" {
		return true
	}
	_, state := s.verifyPagePreviewToken(token)
	return state == ""
}

// frontendStandardPagePreview validates the ticket and renders the current
// standard-page record through the existing page template. A changed content
// digest invalidates the old URL with 410 rather than showing newer content
// under an older approval/preview link.
func (s *Server) frontendStandardPagePreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.notFound(w, r)
		return
	}
	claims, state := s.verifyPagePreviewToken(strings.TrimSpace(r.URL.Query().Get("token")))
	if state == "expired" {
		http.Error(w, "页面预览链接已过期，请重新生成。", http.StatusGone)
		return
	}
	if state != "" || claims.PageID != id {
		s.notFound(w, r)
		return
	}
	p, err := s.store.GetPostByID(id)
	if err != nil {
		http.Error(w, "读取页面预览失败。", http.StatusInternalServerError)
		return
	}
	if p == nil || p.Type != "page" {
		s.notFound(w, r)
		return
	}
	if claims.ContentRevision != previewRevision(p) {
		http.Error(w, "页面内容已更新，请重新生成预览链接。", http.StatusGone)
		return
	}
	s.renderStandardPagePreview(w, r, p)
}

func (s *Server) renderStandardPagePreview(w http.ResponseWriter, r *http.Request, p *store.Post) {
	preview := *p
	if preview.PublishedAt.IsZero() {
		preview.PublishedAt = preview.UpdatedAt
		if preview.PublishedAt.IsZero() {
			preview.PublishedAt = preview.CreatedAt
		}
	}
	s.fillDefaultAuthor(&preview)
	p = &preview

	r = r.Clone(withPreviewNoindex(r.Context()))
	v := s.viewForLang(r, p.Lang, p.Slug)
	v.SEO = v.Site.Page(p)
	v.SEO.Robots = "noindex, nofollow"
	v.SEO.Alternates = nil
	v.Site.InjectHead = ""
	v.Site.InjectBody = ""
	v.Page = p
	v.ContentHTML, _ = s.renderedContent(p)
	s.rnd.Public(w, "page", http.StatusOK, v)
}
