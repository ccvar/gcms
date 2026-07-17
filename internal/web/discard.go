package web

// discard.go AI 报废申请（标记删除）：AI 无删除权（托管红线），只能给「草稿」打
// 建议弃用标记 + 理由；删除永远由管理员在后台执行。
//
// 冻结契约（Pilot 侧并行开发，勿改）：
//   - POST   /{collection}/{id}/discard  body {"reason":"..."}（必填 ≤200 字）
//   - DELETE /{collection}/{id}/discard  撤销标记
//   - 标记非草稿 → 409 {"error":"not_draft"}
//   - 内容列表/详情 JSON 带 discarded(bool) 与 discard_reason 字段
//   - 重复标记＝更新理由（幂等）；任何发布路径自动清除标记（store 层保证）。
//
// admin v1 与平台镜像共用同一处理器（路由形状照 /{collection}/{id}/preview-url 先例）。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// discardReasonMaxRunes 报废理由长度上限（按字符数，冻结契约 ≤200 字）。
const discardReasonMaxRunes = 200

type apiDiscardInput struct {
	Reason string `json:"reason"`
}

// apiDiscardContent POST /{collection}/{id}/discard —— AI 报废申请。
// scope：{collection}:write（content:write 通配同样放行）。只能标记草稿（服务端硬校验），
// 重复标记更新理由与时间；标记零破坏（不动正文/updated_at），撤销即恢复原样。
func (s *Server) apiDiscardContent(w http.ResponseWriter, r *http.Request) {
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
	var in apiDiscardInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		apiError(w, http.StatusBadRequest, "bad_request", "reason 必填：写明为何建议弃用这篇草稿。")
		return
	}
	if len([]rune(reason)) > discardReasonMaxRunes {
		apiError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("reason 过长（最多 %d 字）。", discardReasonMaxRunes))
		return
	}
	if existing.Status != "draft" {
		apiError(w, http.StatusConflict, "not_draft", "只能标记草稿。已发布或定时内容不接受报废申请，请交由管理员处理。")
		return
	}
	marked, err := s.store.SetDiscard(existing.ID, reason)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if !marked {
		// 并发兜底：读时是草稿、写入瞬间已被发布——store 层 WHERE status='draft' 拒绝。
		apiError(w, http.StatusConflict, "not_draft", "只能标记草稿。已发布或定时内容不接受报废申请，请交由管理员处理。")
		return
	}
	updated, _ := s.store.GetPostByID(existing.ID)
	s.recordAutomationLog(auth, "discard", kind, existing.ID,
		fmt.Sprintf("报废申请（%s）：%s——%s", s.apiKindLabel(kind), existing.Title, reason))
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(updated, true)})
}

// apiUndiscardContent DELETE /{collection}/{id}/discard —— 撤销报废申请（幂等）。
func (s *Server) apiUndiscardContent(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.ClearDiscard(existing.ID); err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	updated, _ := s.store.GetPostByID(existing.ID)
	s.recordAutomationLog(auth, "undiscard", kind, existing.ID,
		fmt.Sprintf("撤销报废申请（%s）：%s", s.apiKindLabel(kind), existing.Title))
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(updated, true)})
}

// ---------- 后台：恢复（撤标记）与批量清空 ----------

// adminPostUndiscard 文章列表行内「恢复」：撤销 AI 报废标记，内容原样保留。
func (s *Server) adminPostUndiscard(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	p, _ := s.store.GetPostByID(id)
	if p == nil || p.Type != "post" {
		s.notFound(w, r)
		return
	}
	if err := s.store.ClearDiscard(id); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, s.adminListRedirect("/admin/posts", r), http.StatusSeeOther)
}

// adminPostDiscardPurge 文章列表「清空待清理」：批量删除当前语种下 AI 标记弃用且仍为
// 草稿的文章（只删标记中的草稿；已发布/定时内容即使有残留标记也不动）。
func (s *Server) adminPostDiscardPurge(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := strings.TrimSpace(r.FormValue("lang"))
	if !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	n, err := s.store.PurgeDiscardedDrafts("post", lang)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, appendListQuery(s.adminListRedirect("/admin/posts", r), "purged="+strconv.FormatInt(n, 10)), http.StatusSeeOther)
}

// adminExtUndiscard 扩展类型列表行内「恢复」（与 adminPostUndiscard 同构）。
func (s *Server) adminExtUndiscard(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	p, _ := s.store.GetPostByID(id)
	if p == nil || p.Type != ct.Key {
		s.notFound(w, r)
		return
	}
	if err := s.store.ClearDiscard(id); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, s.adminListRedirect("/admin/ext/"+ct.Key, r), http.StatusSeeOther)
}

// adminExtDiscardPurge 扩展类型列表「清空待清理」（与 adminPostDiscardPurge 同构）。
func (s *Server) adminExtDiscardPurge(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := strings.TrimSpace(r.FormValue("lang"))
	if !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	n, err := s.store.PurgeDiscardedDrafts(ct.Key, lang)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, appendListQuery(s.adminListRedirect("/admin/ext/"+ct.Key, r), "purged="+strconv.FormatInt(n, 10)), http.StatusSeeOther)
}

// appendListQuery 给 adminListRedirect 生成的 URL 追加一个查询参数（自动接 ? / &）。
func appendListQuery(u, kv string) string {
	if strings.Contains(u, "?") {
		return u + "&" + kv
	}
	return u + "?" + kv
}
