package web

// 内容修订历史：store 在每次更新前自动快照旧值（见 store.UpdatePostFrom），
// 这里提供后台「历史版本」入口与自动化 API 的列表 / 回滚端点。
// 恢复走 UpdatePostFrom，因此恢复前会自动把“当前状态”再快照一条，可反悔。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cms.ccvar.com/internal/store"
)

const revisionPreviewRunes = 200 // API 列表里正文预览的截断长度

// RevisionView 后台编辑页「历史版本」抽屉里的一行。
type RevisionView struct {
	ID        int64
	CreatedAt time.Time
	Source    string // admin | api
	Title     string
	Status    string
}

// revisionViews 读取某篇内容的修订列表并解析快照标题 / 状态，供编辑页渲染。
func (s *Server) revisionViews(postID int64) []RevisionView {
	revs, err := s.store.PostRevisions(postID)
	if err != nil {
		return nil
	}
	out := make([]RevisionView, 0, len(revs))
	for _, rev := range revs {
		v := RevisionView{ID: rev.ID, CreatedAt: rev.CreatedAt, Source: rev.Source}
		var snap store.Post
		if err := json.Unmarshal([]byte(rev.Snapshot), &snap); err == nil {
			v.Title = snap.Title
			v.Status = snap.Status
		}
		out = append(out, v)
	}
	return out
}

// restorePostRevision 把快照整字段还原到 p（保留 ID / 类型 / 语种 / 创建时间）。
// UpdatePostFrom 会先把当前状态快照一条，因此恢复操作本身也可回滚。
func (s *Server) restorePostRevision(p *store.Post, rev *store.PostRevision, source string) error {
	var snap store.Post
	if err := json.Unmarshal([]byte(rev.Snapshot), &snap); err != nil {
		return fmt.Errorf("解析修订快照失败: %w", err)
	}
	restored := snap
	restored.ID = p.ID
	restored.Type = p.Type
	restored.Lang = p.Lang
	restored.CreatedAt = p.CreatedAt
	// 旧 slug 可能已被其它内容占用：按现行规则去重，避免恢复失败。
	restored.Slug = s.uniqueSlug(restored.Lang, restored.Slug, restored.ID)
	if err := s.store.UpdatePostFrom(&restored, source); err != nil {
		return err
	}
	s.clearGeneratedCaches()
	return nil
}

// truncateRunes 按字符数截断文本（API 修订列表的正文预览）。
func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}

// apiListRevisions GET /{collection}/{id}/revisions：修订列表（新到旧），
// 正文只给前 200 字预览。走该集合的 read scope。
func (s *Server) apiListRevisions(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "read")); !ok {
		return
	}
	existing, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	revs, err := s.store.PostRevisions(existing.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	items := make([]map[string]any, 0, len(revs))
	for _, rev := range revs {
		var snap store.Post
		_ = json.Unmarshal([]byte(rev.Snapshot), &snap)
		items = append(items, map[string]any{
			"id":              rev.ID,
			"created_at":      rev.CreatedAt.UTC().Format(time.RFC3339),
			"source":          rev.Source,
			"title":           snap.Title,
			"status":          snap.Status,
			"content_preview": truncateRunes(snap.Content, revisionPreviewRunes),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// apiRestoreRevision POST /{collection}/{id}/revisions/{rid}/restore：整字段回滚到指定修订。
// 走该集合的 write scope；涉及已发布 / 定时内容（当前或目标状态非草稿）还需要发布权限，
// 与 PATCH 更新的权限语义一致。
func (s *Server) apiRestoreRevision(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
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
	rid, err := strconv.ParseInt(r.PathValue("rid"), 10, 64)
	if err != nil || rid <= 0 {
		apiError(w, http.StatusBadRequest, "bad_id", "修订 ID 无效。")
		return
	}
	rev, found, err := s.store.GetPostRevision(rid)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !found || rev.PostID != existing.ID {
		apiError(w, http.StatusNotFound, "not_found", "修订不存在或不属于这篇内容。")
		return
	}
	var snap store.Post
	if err := json.Unmarshal([]byte(rev.Snapshot), &snap); err != nil {
		apiError(w, http.StatusInternalServerError, "bad_snapshot", "修订快照损坏，无法恢复。")
		return
	}
	if (existing.Status != "draft" || snap.Status != "draft") && !automationScopeAllowed(auth.scopes, apiScope(collection, "publish")) {
		apiError(w, http.StatusForbidden, "missing_scope", "恢复涉及已发布或定时内容，需要该类内容的发布权限。")
		return
	}
	if err := s.restorePostRevision(existing, rev, store.PostRevisionSourceAPI); err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	updated, _ := s.store.GetPostByID(existing.ID)
	s.recordAutomationLog(auth, "restore", kind, existing.ID, fmt.Sprintf("restore %s#%d ← revision#%d", kind, existing.ID, rev.ID))
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(updated, true)})
}

// adminEditURLForPost 该内容在后台的编辑页地址（历史版本恢复后回跳用）。
func adminEditURLForPost(p *store.Post) string {
	switch p.Type {
	case "post":
		return fmt.Sprintf("/admin/posts/%d/edit", p.ID)
	case "page":
		return fmt.Sprintf("/admin/pages/%d/edit", p.ID)
	case "link":
		return fmt.Sprintf("/admin/links/%d/edit", p.ID)
	default:
		return fmt.Sprintf("/admin/ext/%s/%d/edit", p.Type, p.ID)
	}
}

// adminRestoreRevision 后台「恢复此版本」：恢复前自动把当前状态快照一条（可反悔），
// 然后整字段还原并回到编辑页。
func (s *Server) adminRestoreRevision(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	rid := atoi64(r.PathValue("rid"))
	rev, found, err := s.store.GetPostRevision(rid)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !found {
		s.notFound(w, r)
		return
	}
	p, err := s.store.GetPostByID(rev.PostID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		s.notFound(w, r)
		return
	}
	if err := s.restorePostRevision(p, rev, store.PostRevisionSourceAdmin); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, adminEditURLForPost(p)+"?restored=1", http.StatusSeeOther)
}
