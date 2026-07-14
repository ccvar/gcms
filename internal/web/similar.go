package web

// similar.go 查重端点：GET /{collection}/similar?title=...[&lang=xx][&limit=5]
// （admin v1 与 platform v1 镜像同一处理器）。AI 在写新内容前先按标题查站内近似内容
// （含已发布 + 草稿），避免重复选题。只需该集合的读权限（read scope）。

import (
	"net/http"
	"strconv"
	"strings"
)

const (
	similarDefaultLimit = 5
	similarLimitMax     = 20
)

// similarClampLimit 解析 limit 并钳制到 1..20，缺省 / 非法回落 5。
func similarClampLimit(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return similarDefaultLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return similarDefaultLimit
	}
	if n > similarLimitMax {
		return similarLimitMax
	}
	return n
}

// apiSimilarContent 响应 {ok, rows:[{id,title,slug,status,lang,score}]}，score 归一化 0..1（两位小数）。
func (s *Server) apiSimilarContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "read")); !ok {
		return
	}
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		apiError(w, http.StatusBadRequest, "bad_request", "title 不能为空。")
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "all" {
		lang = "" // 空 = 全语种
	}
	if lang != "" && !s.langEnabled(lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	limit := similarClampLimit(r.URL.Query().Get("limit"))
	rows, err := s.store.SimilarByTitle(kind, lang, title, limit)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"id": row.ID, "title": row.Title, "slug": row.Slug,
			"status": row.Status, "lang": row.Lang, "score": row.Score,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rows": out})
}
