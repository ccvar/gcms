package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

func revisionTestPost(t *testing.T, s *Server, status string) int64 {
	t.Helper()
	id, err := s.store.CreatePost(&store.Post{
		Type:    "post",
		Lang:    "zh",
		Slug:    "revision-target",
		Title:   "原始标题",
		Content: "原始正文",
		Extra:   `{"note":"v1"}`,
		Status:  status,
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	return id
}

func TestUpdatePostSnapshotsRevision(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	id := revisionTestPost(t, s, "draft")

	p, _ := s.store.GetPostByID(id)
	p.Title = "后台改标题"
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("admin update: %v", err)
	}
	p, _ = s.store.GetPostByID(id)
	p.Title = "API 改标题"
	if err := s.store.UpdatePostFrom(p, store.PostRevisionSourceAPI); err != nil {
		t.Fatalf("api update: %v", err)
	}

	revs, err := s.store.PostRevisions(id)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2", len(revs))
	}
	// 新到旧：第一条是 API 更新前的快照（后台标题），第二条是最初的原始标题。
	if revs[0].Source != store.PostRevisionSourceAPI || revs[1].Source != store.PostRevisionSourceAdmin {
		t.Fatalf("revision sources = %s, %s; want api, admin", revs[0].Source, revs[1].Source)
	}
	var snapNew, snapOld store.Post
	if err := json.Unmarshal([]byte(revs[0].Snapshot), &snapNew); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	_ = json.Unmarshal([]byte(revs[1].Snapshot), &snapOld)
	if snapNew.Title != "后台改标题" || snapOld.Title != "原始标题" {
		t.Fatalf("snapshot titles = %q, %q", snapNew.Title, snapOld.Title)
	}
	if snapOld.Extra != `{"note":"v1"}` {
		t.Fatalf("snapshot extra = %q", snapOld.Extra)
	}
}

func TestPostRevisionsCappedAtTwenty(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	id := revisionTestPost(t, s, "draft")
	for i := 0; i < 25; i++ {
		p, _ := s.store.GetPostByID(id)
		p.Title = fmt.Sprintf("标题 v%d", i)
		if err := s.store.UpdatePost(p); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	revs, err := s.store.PostRevisions(id)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != store.PostRevisionKeep {
		t.Fatalf("revisions = %d, want %d", len(revs), store.PostRevisionKeep)
	}
	// 最旧的（原始标题、v0..v3）已被裁剪，最老一条应是 v4 之前的快照，即标题 v4。
	var oldest store.Post
	_ = json.Unmarshal([]byte(revs[len(revs)-1].Snapshot), &oldest)
	if oldest.Title != "标题 v4" {
		t.Fatalf("oldest kept snapshot title = %q, want 标题 v4", oldest.Title)
	}
}

func TestDeletePostCascadesRevisions(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	id := revisionTestPost(t, s, "draft")
	p, _ := s.store.GetPostByID(id)
	p.Title = "改一次"
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("update: %v", err)
	}
	if revs, _ := s.store.PostRevisions(id); len(revs) != 1 {
		t.Fatalf("revisions before delete = %d, want 1", len(revs))
	}
	if err := s.store.DeletePost(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	revs, err := s.store.PostRevisions(id)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after delete = %d, want 0（应级联清理）", len(revs))
	}
}

func TestAPIRevisionListAndRestore(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:write")
	id := revisionTestPost(t, s, "draft")
	longBody := strings.Repeat("长", 300)
	p, _ := s.store.GetPostByID(id)
	p.Title = "改后的标题"
	p.Content = longBody
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("update: %v", err)
	}
	// 再改一次，让最新修订里的正文是 300 字长文，验证预览截断。
	p, _ = s.store.GetPostByID(id)
	p.Title = "第三个标题"
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("update2: %v", err)
	}

	list := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts/"+strconv.FormatInt(id, 10)+"/revisions", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listReq.SetPathValue("collection", "posts")
	listReq.SetPathValue("id", strconv.FormatInt(id, 10))
	s.apiListRevisions(list, listReq)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	var listOut struct {
		Items []struct {
			ID             int64  `json:"id"`
			Source         string `json:"source"`
			Title          string `json:"title"`
			ContentPreview string `json:"content_preview"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listOut); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listOut.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(listOut.Items))
	}
	if listOut.Items[0].Title != "改后的标题" || listOut.Items[1].Title != "原始标题" {
		t.Fatalf("item titles = %q, %q", listOut.Items[0].Title, listOut.Items[1].Title)
	}
	if got := len([]rune(listOut.Items[0].ContentPreview)); got != 200 {
		t.Fatalf("content preview runes = %d, want 200", got)
	}

	// 恢复到最旧的修订（原始标题）。
	oldestID := listOut.Items[1].ID
	restore := httptest.NewRecorder()
	restoreReq := httptest.NewRequest(http.MethodPost, "/api/admin/v1/posts/"+strconv.FormatInt(id, 10)+"/revisions/"+strconv.FormatInt(oldestID, 10)+"/restore", nil)
	restoreReq.Header.Set("Authorization", "Bearer "+token)
	restoreReq.SetPathValue("collection", "posts")
	restoreReq.SetPathValue("id", strconv.FormatInt(id, 10))
	restoreReq.SetPathValue("rid", strconv.FormatInt(oldestID, 10))
	s.apiRestoreRevision(restore, restoreReq)
	if restore.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", restore.Code, restore.Body.String())
	}
	after, _ := s.store.GetPostByID(id)
	if after.Title != "原始标题" || after.Content != "原始正文" {
		t.Fatalf("restored post = %q / %q", after.Title, after.Content)
	}
	// 恢复前会把“当前状态”（第三个标题）再快照一条，可反悔。
	revs, _ := s.store.PostRevisions(id)
	if len(revs) != 3 {
		t.Fatalf("revisions after restore = %d, want 3", len(revs))
	}
	var preRestore store.Post
	_ = json.Unmarshal([]byte(revs[0].Snapshot), &preRestore)
	if preRestore.Title != "第三个标题" {
		t.Fatalf("pre-restore snapshot title = %q, want 第三个标题", preRestore.Title)
	}
	if revs[0].Source != store.PostRevisionSourceAPI {
		t.Fatalf("pre-restore snapshot source = %s, want api", revs[0].Source)
	}
}

func TestAPIRevisionRestoreScopeAndOwnership(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:write")
	id := revisionTestPost(t, s, "published")
	p, _ := s.store.GetPostByID(id)
	p.Title = "已发布内容改标题"
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("update: %v", err)
	}
	revs, _ := s.store.PostRevisions(id)
	rid := revs[0].ID

	// 已发布内容的恢复需要发布权限：只有 write → 403。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.SetPathValue("collection", "posts")
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	req.SetPathValue("rid", strconv.FormatInt(rid, 10))
	s.apiRestoreRevision(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("restore published without publish scope = %d, want 403; body = %s", w.Code, w.Body.String())
	}

	// 修订不属于这篇内容 → 404。
	otherID, err := s.store.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "other-post", Title: "另一篇", Status: "draft"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/x", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.SetPathValue("collection", "posts")
	req2.SetPathValue("id", strconv.FormatInt(otherID, 10))
	req2.SetPathValue("rid", strconv.FormatInt(rid, 10))
	s.apiRestoreRevision(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("cross-post restore = %d, want 404; body = %s", w2.Code, w2.Body.String())
	}
}

func TestAdminRevisionEntryAndRestore(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()
	id, err := s.store.CreatePost(&store.Post{
		Type:   "post",
		Lang:   "zh",
		Slug:   "admin-revision-post",
		Title:  "后台原始标题",
		Status: "draft",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	p, _ := s.store.GetPostByID(id)
	p.Title = "后台修改后的标题"
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("update: %v", err)
	}
	revs, _ := s.store.PostRevisions(id)
	if len(revs) != 1 {
		t.Fatalf("revisions = %d, want 1", len(revs))
	}

	// 编辑页出现「历史版本」抽屉与恢复表单。
	editReq, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/posts/"+strconv.FormatInt(id, 10)+"/edit", nil)
	edit := httptest.NewRecorder()
	h.ServeHTTP(edit, editReq)
	if edit.Code != http.StatusOK {
		t.Fatalf("edit status = %d", edit.Code)
	}
	body := edit.Body.String()
	for _, want := range []string{"历史版本", "后台原始标题", fmt.Sprintf("/admin/revisions/%d/restore", revs[0].ID), "恢复此版本"} {
		if !strings.Contains(body, want) {
			t.Fatalf("edit page missing %q", want)
		}
	}

	// 恢复：回到编辑页并整字段还原；恢复前自动快照当前状态。
	restoreReq, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/revisions/%d/restore", revs[0].ID), url.Values{})
	restore := httptest.NewRecorder()
	h.ServeHTTP(restore, restoreReq)
	if restore.Code != http.StatusSeeOther {
		t.Fatalf("restore status = %d, body = %s", restore.Code, restore.Body.String())
	}
	if got, want := restore.Header().Get("Location"), fmt.Sprintf("/admin/posts/%d/edit?restored=1", id); got != want {
		t.Fatalf("restore Location = %q, want %q", got, want)
	}
	after, _ := s.store.GetPostByID(id)
	if after.Title != "后台原始标题" {
		t.Fatalf("restored title = %q", after.Title)
	}
	revsAfter, _ := s.store.PostRevisions(id)
	if len(revsAfter) != 2 {
		t.Fatalf("revisions after restore = %d, want 2", len(revsAfter))
	}
	var preRestore store.Post
	_ = json.Unmarshal([]byte(revsAfter[0].Snapshot), &preRestore)
	if preRestore.Title != "后台修改后的标题" || revsAfter[0].Source != store.PostRevisionSourceAdmin {
		t.Fatalf("pre-restore snapshot = %q / %s", preRestore.Title, revsAfter[0].Source)
	}
}
