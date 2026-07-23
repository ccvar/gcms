package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cms.ccvar.com/internal/store"
)

type controlCategoryDeleteNormalized struct {
	SiteID           int64  `json:"site_id"`
	Collection       string `json:"collection"`
	CategoryID       int64  `json:"category_id"`
	RemoveNavigation bool   `json:"remove_navigation"`
	ExpectedRevision string `json:"expected_revision,omitempty"`
}

type controlNavigationDeleteNormalized struct {
	SiteID           int64  `json:"site_id"`
	Index            int    `json:"index"`
	ExpectedURL      string `json:"expected_url,omitempty"`
	ExpectedRevision string `json:"expected_revision,omitempty"`
}

type controlNavigationReference struct {
	Index    int               `json:"index"`
	Position int               `json:"position"`
	URL      string            `json:"url"`
	Labels   map[string]string `json:"labels"`
}

func (s *Server) servePlatformControlSiteCategoryDelete(w http.ResponseWriter, r *http.Request, siteID int64, collection string, categoryID int64) {
	if r.Method != http.MethodDelete {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 DELETE。")
		return
	}
	collection = strings.Trim(strings.TrimSpace(collection), "/")
	key, site, runtime, ok := s.controlConfigurationSite(w, r, apiScopeCategoriesDelete, siteID)
	if !ok {
		return
	}
	kind, _, validCollection := runtime.server.apiCategoryWriteTarget(collection)
	if !validCollection {
		apiError(w, http.StatusNotFound, "collection_not_found", "该内容集合不存在或未启用分类。")
		return
	}
	normalized := controlCategoryDeleteNormalized{
		SiteID:           siteID,
		Collection:       collection,
		CategoryID:       categoryID,
		RemoveNavigation: parseAPIBool(r.URL.Query().Get("remove_navigation")),
		ExpectedRevision: strings.TrimSpace(r.URL.Query().Get("expected_revision")),
	}
	if !controlDryRun(r) && normalized.ExpectedRevision == "" {
		apiError(w, http.StatusUnprocessableEntity, "expected_revision_required", "正式删除必须带上预检查返回的 expected_revision。")
		return
	}
	fingerprint := controlMutationFingerprint("categories.delete", normalized)
	s.executeControlMutation(w, r, key, "categories.delete", fingerprint, func() (int, any, error) {
		category, err := runtime.Store.GetCategoryByID(categoryID)
		if err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "category_read_failed", "无法读取分类。")
		}
		if category == nil || category.Kind != kind {
			return 0, nil, newControlMutationError(http.StatusNotFound, "category_not_found", "分类不存在，或不属于指定内容集合。")
		}
		plan, rawNavigation, nextNavigation, removedNavigation, err := controlCategoryDeletePlan(runtime.server, collection, category, normalized.RemoveNavigation)
		if err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "category_impact_failed", "无法读取分类删除影响。")
		}
		plan["normalized_input"] = normalized
		if controlDryRun(r) {
			return http.StatusOK, plan, nil
		}
		impactRevision, _ := plan["impact_revision"].(string)
		if normalized.ExpectedRevision != impactRevision {
			mutationErr := newControlMutationError(http.StatusConflict, "category_impact_changed", "分类、关联内容或导航引用已经变化，请重新执行删除预检查。")
			mutationErr.Details = map[string]any{"expected_revision": normalized.ExpectedRevision, "current_revision": impactRevision, "rerun_dry_run": true}
			return 0, nil, mutationErr
		}

		expectedNavigation := &rawNavigation
		var replacementNavigation *string
		if normalized.RemoveNavigation && removedNavigation > 0 {
			replacementNavigation = &nextNavigation
		}
		contentRevision, _ := plan["content_revision"].(string)
		contextRevision, _ := plan["category_context_revision"].(string)
		deleted, uncategorized, err := runtime.Store.DeleteCategoryWithNavigation(category, contentRevision, contextRevision, expectedNavigation, replacementNavigation)
		if errors.Is(err, store.ErrCategoryChanged) {
			mutationErr := newControlMutationError(http.StatusConflict, "category_impact_changed", "分类、互译关系或关联内容已经变化，请重新执行删除预检查。")
			mutationErr.Details = map[string]any{"rerun_dry_run": true}
			return 0, nil, mutationErr
		}
		if errors.Is(err, store.ErrSettingChanged) {
			mutationErr := newControlMutationError(http.StatusConflict, "navigation_changed", "导航已发生变化，请重新执行删除预检查。")
			mutationErr.Details = map[string]any{"rerun_dry_run": true}
			return 0, nil, mutationErr
		}
		if err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "category_delete_failed", "分类删除未完成。")
		}
		if !deleted {
			return 0, nil, newControlMutationError(http.StatusNotFound, "category_not_found", "分类已不存在，请刷新后重试。")
		}
		runtime.server.clearGeneratedCaches()
		_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_categories_delete", kind+"-category", category.ID, "已通过统一控制层删除分类 "+category.Name)
		return http.StatusOK, map[string]any{
			"deleted":                     true,
			"operation":                   "categories.delete",
			"category":                    controlCategoryItem(runtime.server, collection, category),
			"uncategorized_content_count": uncategorized,
			"removed_navigation_count":    removedNavigation,
			"navigation_removed":          removedNavigation > 0,
			"warnings":                    plan["warnings"],
		}, nil
	})
}

func controlCategoryDeletePlan(s *Server, collection string, category *store.Category, removeNavigation bool) (map[string]any, string, string, int, error) {
	usage, err := s.store.CategoryUsageForDelete(category.ID, 5)
	if err != nil {
		return nil, "", "", 0, err
	}
	categoryContextRevision, err := s.store.CategoryDeleteContextRevision(category.Kind)
	if err != nil {
		return nil, "", "", 0, err
	}
	translations, err := s.store.CategoryTranslations(category.TransGroup)
	if err != nil {
		return nil, "", "", 0, err
	}
	translationItems := make([]map[string]any, 0, len(translations))
	for _, item := range translations {
		if item == nil || item.Kind != category.Kind {
			continue
		}
		itemUsage, usageErr := s.store.CategoryUsageForDelete(item.ID, 0)
		if usageErr != nil {
			return nil, "", "", 0, usageErr
		}
		translationItems = append(translationItems, map[string]any{
			"id": item.ID, "name": item.Name, "slug": item.Slug, "lang": item.Lang,
			"path": controlCategoryPrimaryPath(s, collection, item), "content_count": itemUsage.Total,
			"target": item.ID == category.ID,
		})
	}

	rows, rawNavigation, _ := s.effectiveMenuRows()
	paths := controlCategoryPublicPaths(s, collection, category)
	references := controlCategoryNavigationReferences(rows, paths)
	pathStillUsed, err := controlCategoryPathStillUsed(s, collection, category, paths)
	if err != nil {
		return nil, "", "", 0, err
	}

	nextRows := rows
	removedNavigation := 0
	if removeNavigation && len(references) > 0 {
		remove := make(map[int]bool, len(references))
		for _, reference := range references {
			remove[reference.Index] = true
		}
		nextRows = make([]MenuRow, 0, len(rows)-len(remove))
		for index, row := range rows {
			if remove[index] {
				removedNavigation++
				continue
			}
			nextRows = append(nextRows, row)
		}
	}
	nextJSONBytes, err := json.Marshal(nextRows)
	if err != nil {
		return nil, "", "", 0, err
	}

	samples := make([]map[string]any, 0, len(usage.Items))
	for _, item := range usage.Items {
		samples = append(samples, map[string]any{
			"id": item.ID, "title": item.Title, "type": item.Type, "lang": item.Lang,
			"status": item.Status, "discarded": item.Discarded,
		})
	}
	warnings := []string{"只删除当前分类记录；同一互译组中的其他语种分类会保留。"}
	if usage.Total > 0 {
		warnings = append(warnings, strconv.Itoa(usage.Total)+" 条关联内容不会被删除，但会变为未分类。")
	}
	if len(references) > 0 && !removeNavigation {
		warnings = append(warnings, "导航中仍有 "+strconv.Itoa(len(references))+" 个入口指向该分类；请确认是否同时移除。")
	}
	if removeNavigation && removedNavigation > 0 {
		warnings = append(warnings, "将同时移除 "+strconv.Itoa(removedNavigation)+" 个精确匹配的导航入口。")
	}
	if removeNavigation && removedNavigation > 0 && len(nextRows) == 0 {
		warnings = append(warnings, "移除后前台导航将为空；系统会保留显式空导航，不会重新显示默认导航。")
	}
	if pathStillUsed {
		warnings = append(warnings, "其他语种或分类仍使用相同公开路径，不建议自动移除共享导航入口。")
	}
	impactRevision := controlRequestHash(controlMutationFingerprint("categories.delete.plan", map[string]any{
		"category":              controlCategoryItem(s, collection, category),
		"content_revision":      usage.Revision,
		"translations":          translationItems,
		"navigation_references": references,
		"path_still_used":       pathStillUsed,
		"remove_navigation":     removeNavigation,
	}))
	plan := map[string]any{
		"dry_run":                   true,
		"operation":                 "categories.delete",
		"normalized_input":          controlCategoryDeleteNormalized{Collection: collection, CategoryID: category.ID, RemoveNavigation: removeNavigation},
		"category":                  controlCategoryItem(s, collection, category),
		"category_context_revision": categoryContextRevision,
		"content": map[string]any{
			"total": usage.Total, "published": usage.Published, "draft": usage.Draft,
			"scheduled": usage.Scheduled, "discarded": usage.Discarded, "other": usage.Other,
			"will_become_uncategorized": usage.Total, "samples": samples,
		},
		"translations": map[string]any{
			"deletes_only_target": true,
			"items":               translationItems,
		},
		"navigation": map[string]any{
			"references":          references,
			"reference_count":     len(references),
			"path_still_used":     pathStillUsed,
			"cleanup_recommended": len(references) > 0 && !pathStillUsed,
			"remove_requested":    removeNavigation,
		},
		"impact": map[string]any{
			"deletes_content":         false,
			"deletes_translation_set": false,
			"category_page_removed":   true,
			"recoverable":             false,
		},
		"impact_revision":  impactRevision,
		"content_revision": usage.Revision,
		"warnings":         warnings,
	}
	return plan, rawNavigation, string(nextJSONBytes), removedNavigation, nil
}

func controlCategoryItem(s *Server, collection string, category *store.Category) map[string]any {
	return map[string]any{
		"id": category.ID, "name": category.Name, "slug": category.Slug, "lang": category.Lang,
		"kind": category.Kind, "collection": collection, "trans_group": category.TransGroup,
		"path": controlCategoryPrimaryPath(s, collection, category),
	}
}

func controlCategoryPrimaryPath(s *Server, collection string, category *store.Category) string {
	paths := controlCategoryPublicPaths(s, collection, category)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func controlCategoryPublicPaths(s *Server, collection string, category *store.Category) []string {
	if category == nil {
		return nil
	}
	slug := strings.Trim(strings.TrimSpace(category.Slug), "/")
	if slug == "" {
		return nil
	}
	switch category.Kind {
	case "post":
		return []string{"/category/" + slug}
	case "link":
		return []string{"/links/cat/" + slug, "/links?cat=" + url.QueryEscape(slug)}
	default:
		ct := s.extTypeByPrefix(collection)
		if ct == nil || strings.Trim(ct.URLPrefix, "/") == "" {
			return nil
		}
		return []string{"/" + strings.Trim(ct.URLPrefix, "/") + "/cat/" + slug}
	}
}

func controlCategoryNavigationReferences(rows []MenuRow, paths []string) []controlNavigationReference {
	references := make([]controlNavigationReference, 0)
	for index, row := range rows {
		if !controlMenuURLInPaths(row.URL, paths) {
			continue
		}
		labels := make(map[string]string, len(row.Labels))
		for lang, label := range row.Labels {
			labels[lang] = label
		}
		references = append(references, controlNavigationReference{
			Index: index, Position: index + 1, URL: row.URL, Labels: labels,
		})
	}
	return references
}

func controlMenuURLInPaths(raw string, paths []string) bool {
	path, full, ok := menuURLParts(raw)
	if !ok {
		return false
	}
	for _, candidate := range paths {
		candidatePath, candidateFull, candidateOK := menuURLParts(candidate)
		if !candidateOK {
			continue
		}
		if strings.Contains(candidate, "?") {
			if full == candidateFull {
				return true
			}
			continue
		}
		if strings.TrimRight(path, "/") == strings.TrimRight(candidatePath, "/") {
			return true
		}
	}
	return false
}

func controlCategoryPathStillUsed(s *Server, collection string, target *store.Category, targetPaths []string) (bool, error) {
	categories, err := s.store.AllCategories(target.Kind)
	if err != nil {
		return false, err
	}
	for _, category := range categories {
		if category == nil || category.ID == target.ID {
			continue
		}
		for _, candidate := range controlCategoryPublicPaths(s, collection, category) {
			if controlMenuURLInPaths(candidate, targetPaths) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Server) servePlatformControlSiteNavigationDelete(w http.ResponseWriter, r *http.Request, siteID int64, index int) {
	if r.Method != http.MethodDelete {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 DELETE。")
		return
	}
	key, site, runtime, ok := s.controlConfigurationSite(w, r, apiScopeNavigationDelete, siteID)
	if !ok {
		return
	}
	expectedURL := strings.TrimSpace(r.URL.Query().Get("expected_url"))
	expectedRevision := strings.TrimSpace(r.URL.Query().Get("expected_revision"))
	if !controlDryRun(r) && expectedURL == "" {
		apiError(w, http.StatusUnprocessableEntity, "expected_url_required", "正式删除必须带上预检查返回的 expected_url。")
		return
	}
	if !controlDryRun(r) && expectedRevision == "" {
		apiError(w, http.StatusUnprocessableEntity, "expected_revision_required", "正式删除必须带上预检查返回的 expected_revision。")
		return
	}
	normalized := controlNavigationDeleteNormalized{SiteID: siteID, Index: index, ExpectedURL: expectedURL, ExpectedRevision: expectedRevision}
	fingerprint := controlMutationFingerprint("navigation.delete", normalized)
	s.executeControlMutation(w, r, key, "navigation.delete", fingerprint, func() (int, any, error) {
		rows, rawNavigation, configured := runtime.server.effectiveMenuRows()
		if index < 0 || index >= len(rows) {
			return 0, nil, newControlMutationError(http.StatusNotFound, "navigation_item_not_found", "导航项不存在，请刷新导航列表后重试。")
		}
		target := rows[index]
		impactRevision := controlRequestHash(controlMutationFingerprint("navigation.delete.plan", map[string]any{
			"site_id": siteID, "index": index, "navigation_setting": rawNavigation,
			"effective_navigation": rows, "configured": configured,
		}))
		if expectedURL != "" && target.URL != expectedURL {
			mutationErr := newControlMutationError(http.StatusConflict, "navigation_changed", "导航顺序或目标已经变化，请重新执行删除预检查。")
			mutationErr.Details = map[string]any{"expected_url": expectedURL, "current_url": target.URL, "rerun_dry_run": true}
			return 0, nil, mutationErr
		}
		if expectedRevision != "" && expectedRevision != impactRevision {
			mutationErr := newControlMutationError(http.StatusConflict, "navigation_changed", "导航内容已经变化，请重新执行删除预检查。")
			mutationErr.Details = map[string]any{"expected_revision": expectedRevision, "current_revision": impactRevision, "rerun_dry_run": true}
			return 0, nil, mutationErr
		}
		item := controlNavigationReference{Index: index, Position: index + 1, URL: target.URL, Labels: target.Labels}
		warnings := []string{"只移除这个前台导航入口，不会删除它指向的页面或内容。"}
		if len(rows) == 1 {
			warnings = append(warnings, "删除后前台导航将为空；系统会保留显式空导航，不会重新显示默认导航。")
		}
		if controlDryRun(r) {
			return http.StatusOK, map[string]any{
				"dry_run": true, "operation": "navigation.delete",
				"normalized_input": controlNavigationDeleteNormalized{SiteID: siteID, Index: index, ExpectedURL: target.URL, ExpectedRevision: impactRevision},
				"item":             item,
				"impact_revision":  impactRevision,
				"remaining_count":  len(rows) - 1,
				"navigation_source": func() string {
					if configured {
						return "configured"
					}
					return "defaults"
				}(),
				"impact": map[string]any{
					"removes_navigation_item": true,
					"deletes_linked_content":  false,
					"recoverable":             false,
				},
				"warnings": warnings,
			}, nil
		}
		nextRows := make([]MenuRow, 0, len(rows)-1)
		nextRows = append(nextRows, rows[:index]...)
		nextRows = append(nextRows, rows[index+1:]...)
		nextJSON, err := json.Marshal(nextRows)
		if err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "navigation_encode_failed", "无法保存导航。")
		}
		updated, err := runtime.Store.CompareAndSetSetting("nav_menu", rawNavigation, string(nextJSON))
		if err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "navigation_delete_failed", "导航删除未完成。")
		}
		if !updated {
			mutationErr := newControlMutationError(http.StatusConflict, "navigation_changed", "导航已经变化，请重新执行删除预检查。")
			mutationErr.Details = map[string]any{"rerun_dry_run": true}
			return 0, nil, mutationErr
		}
		runtime.server.clearGeneratedCaches()
		_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_navigation_delete", "navigation", int64(index), "已通过统一控制层删除导航入口 "+target.URL)
		return http.StatusOK, map[string]any{
			"deleted": true, "operation": "navigation.delete", "item": item,
			"remaining_count": len(nextRows),
			"warnings":        warnings,
		}, nil
	})
}

// navigationRowsRemoved 判断平台全量 PATCH 是否减少了原有 URL 的出现次数。
// 标签修改和纯排序不会触发；URL 替换属于“删旧加新”，必须走受控删除。
func navigationRowsRemoved(current, next []MenuRow) bool {
	counts := make(map[string]int, len(next))
	for _, row := range next {
		counts[strings.TrimSpace(row.URL)]++
	}
	for _, row := range current {
		url := strings.TrimSpace(row.URL)
		if counts[url] == 0 {
			return true
		}
		counts[url]--
	}
	return false
}
