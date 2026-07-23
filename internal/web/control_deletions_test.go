package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

func controlImpactRevision(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		ImpactRevision string `json:"impact_revision"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ImpactRevision == "" {
		t.Fatalf("decode impact revision: revision=%q err=%v body=%s", out.ImpactRevision, err, body)
	}
	return out.ImpactRevision
}

func TestPlatformControlCategoryDeleteReportsImpactUnlocksAndPreservesContent(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	setPlatformTestPassword(t, ps, controlTestPassword)
	token := "gcmsp_categorydelete123"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeCategoriesDelete}, ","), nil)

	runtime, ok := srv.platformRuntimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil {
		t.Fatal("blog runtime missing")
	}
	group := "guides-translations"
	categoryID, err := runtime.Store.CreateCategory(&store.Category{
		Slug: "pilot-delete-guides", Name: "指南", Lang: "zh", Kind: "post", TransGroup: group,
	})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	translationID, err := runtime.Store.CreateCategory(&store.Category{
		Slug: "guides-en", Name: "Guides", Lang: "en", Kind: "post", TransGroup: group,
	})
	if err != nil {
		t.Fatalf("create translated category: %v", err)
	}
	var postIDs []int64
	for index, status := range []string{"published", "draft", "scheduled"} {
		postID, err := runtime.Store.CreatePost(&store.Post{
			Type: "post", Lang: "zh", Slug: "guide-" + strconv.Itoa(index), Title: "指南 " + strconv.Itoa(index),
			Status: status, CategoryID: sql.NullInt64{Int64: categoryID, Valid: true},
		})
		if err != nil {
			t.Fatalf("create %s post: %v", status, err)
		}
		postIDs = append(postIDs, postID)
	}
	if err := runtime.Store.SetSetting("nav_menu", `[{"url":"/category/pilot-delete-guides","labels":{"zh":"指南"}},{"url":"/about","labels":{"zh":"关于"}}]`); err != nil {
		t.Fatalf("seed navigation: %v", err)
	}

	basePath := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/categories/posts/" + strconv.FormatInt(categoryID, 10)
	plan := controlConfigurationAPIReq(t, h, http.MethodDelete, basePath+"?dry_run=1", token, nil, controlConfigurationRequest{})
	if plan.Code != http.StatusOK {
		t.Fatalf("category plan = %d %s", plan.Code, plan.Body.String())
	}
	for _, needle := range []string{`"will_become_uncategorized":3`, `"published":1`, `"draft":1`, `"scheduled":1`, `"reference_count":1`, `"cleanup_recommended":true`, `"deletes_only_target":true`} {
		if !strings.Contains(plan.Body.String(), needle) {
			t.Fatalf("category plan missing %s: %s", needle, plan.Body.String())
		}
	}
	if category, _ := runtime.Store.GetCategoryByID(categoryID); category == nil {
		t.Fatal("dry-run deleted category")
	}
	impactRevision := controlImpactRevision(t, plan.Body.Bytes())

	noUnlockPath := basePath + "?remove_navigation=1&expected_revision=" + url.QueryEscape(impactRevision)
	noUnlock := controlConfigurationAPIReq(t, h, http.MethodDelete, noUnlockPath, token, nil,
		controlConfigurationRequest{Confirm: "categories.delete", IdempotencyKey: "category-delete-guides-1"})
	if noUnlock.Code != http.StatusForbidden || !strings.Contains(noUnlock.Body.String(), "unlock_required") || !strings.Contains(noUnlock.Body.String(), "categories.delete") {
		t.Fatalf("category delete without unlock = %d %s", noUnlock.Code, noUnlock.Body.String())
	}
	unlock := controlConfigurationUnlock(t, h, token, controlTestPassword, "categories.delete")
	changedPost, err := runtime.Store.GetPostByID(postIDs[0])
	if err != nil || changedPost == nil {
		t.Fatalf("read post before impact change: post=%#v err=%v", changedPost, err)
	}
	changedPost.Title += "（已更新）"
	if err := runtime.Store.UpdatePost(changedPost); err != nil {
		t.Fatalf("update post before apply: %v", err)
	}
	staleApply := controlConfigurationAPIReq(t, h, http.MethodDelete, noUnlockPath, token, nil,
		controlConfigurationRequest{Confirm: "categories.delete", IdempotencyKey: "category-delete-stale-1", UnlockToken: unlock})
	if staleApply.Code != http.StatusConflict || !strings.Contains(staleApply.Body.String(), "category_impact_changed") {
		t.Fatalf("stale category impact = %d %s", staleApply.Code, staleApply.Body.String())
	}
	refreshedPlan := controlConfigurationAPIReq(t, h, http.MethodDelete, basePath+"?dry_run=1&remove_navigation=1", token, nil, controlConfigurationRequest{})
	if refreshedPlan.Code != http.StatusOK {
		t.Fatalf("refreshed category plan = %d %s", refreshedPlan.Code, refreshedPlan.Body.String())
	}
	impactRevision = controlImpactRevision(t, refreshedPlan.Body.Bytes())
	applyPath := basePath + "?remove_navigation=1&expected_revision=" + url.QueryEscape(impactRevision)
	apply := controlConfigurationAPIReq(t, h, http.MethodDelete, applyPath, token, nil,
		controlConfigurationRequest{Confirm: "categories.delete", IdempotencyKey: "category-delete-guides-2", UnlockToken: unlock})
	if apply.Code != http.StatusOK {
		t.Fatalf("category delete = %d %s", apply.Code, apply.Body.String())
	}
	for _, needle := range []string{`"deleted":true`, `"uncategorized_content_count":3`, `"removed_navigation_count":1`} {
		if !strings.Contains(apply.Body.String(), needle) {
			t.Fatalf("category delete missing %s: %s", needle, apply.Body.String())
		}
	}
	if category, _ := runtime.Store.GetCategoryByID(categoryID); category != nil {
		t.Fatalf("category still exists: %#v", category)
	}
	if translation, _ := runtime.Store.GetCategoryByID(translationID); translation == nil {
		t.Fatal("translated category was deleted")
	}
	for _, postID := range postIDs {
		post, err := runtime.Store.GetPostByID(postID)
		if err != nil || post == nil {
			t.Fatalf("read post %d: post=%#v err=%v", postID, post, err)
		}
		if post.CategoryID.Valid {
			t.Fatalf("post %d category was not cleared: %#v", postID, post.CategoryID)
		}
	}
	if navigation := runtime.Store.Setting("nav_menu"); strings.Contains(navigation, "/category/pilot-delete-guides") || !strings.Contains(navigation, "/about") {
		t.Fatalf("navigation cleanup = %s", navigation)
	}

	replay := controlConfigurationAPIReq(t, h, http.MethodDelete, applyPath, token, nil,
		controlConfigurationRequest{Confirm: "categories.delete", IdempotencyKey: "category-delete-guides-2", UnlockToken: unlock})
	if replay.Code != http.StatusOK || replay.Header().Get(controlIdempotencyReplayedHeader) != "true" {
		t.Fatalf("category replay = %d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}

	for _, target := range []struct {
		collection string
		kind       string
		slug       string
		name       string
	}{
		{collection: "links", kind: "link", slug: "pilot-delete-resources", name: "资源"},
		{collection: "products", kind: "product", slug: "pilot-delete-hardware", name: "硬件"},
	} {
		categoryID, err := runtime.Store.CreateCategory(&store.Category{Slug: target.slug, Name: target.name, Lang: "zh", Kind: target.kind})
		if err != nil {
			t.Fatalf("create %s category: %v", target.kind, err)
		}
		path := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/categories/" + target.collection + "/" + strconv.FormatInt(categoryID, 10)
		categoryPlan := controlConfigurationAPIReq(t, h, http.MethodDelete, path+"?dry_run=1", token, nil, controlConfigurationRequest{})
		if categoryPlan.Code != http.StatusOK || !strings.Contains(categoryPlan.Body.String(), `"collection":"`+target.collection+`"`) || !strings.Contains(categoryPlan.Body.String(), `"kind":"`+target.kind+`"`) {
			t.Fatalf("%s category plan = %d %s", target.kind, categoryPlan.Code, categoryPlan.Body.String())
		}
		targetRevision := controlImpactRevision(t, categoryPlan.Body.Bytes())
		categoryApply := controlConfigurationAPIReq(t, h, http.MethodDelete, path+"?expected_revision="+url.QueryEscape(targetRevision), token, nil,
			controlConfigurationRequest{Confirm: "categories.delete", IdempotencyKey: "delete-" + target.kind + "-category", UnlockToken: unlock})
		if categoryApply.Code != http.StatusOK || !strings.Contains(categoryApply.Body.String(), `"deleted":true`) {
			t.Fatalf("%s category delete = %d %s", target.kind, categoryApply.Code, categoryApply.Body.String())
		}
	}
}

func TestPlatformControlCategoryDeleteRejectsChangedTranslationsAndNavigationReferences(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	setPlatformTestPassword(t, ps, controlTestPassword)
	token := "gcmsp_categoryimpact1"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeCategoriesDelete}, ","), nil)

	runtime, ok := srv.platformRuntimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil {
		t.Fatal("blog runtime missing")
	}
	group := "category-impact-translations"
	categoryID, err := runtime.Store.CreateCategory(&store.Category{
		Slug: "impact-guides", Name: "影响指南", Lang: "zh", Kind: "post", TransGroup: group,
	})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	if err := runtime.Store.SetSetting("nav_menu", `[{"url":"/about","labels":{"zh":"关于"}}]`); err != nil {
		t.Fatalf("seed navigation: %v", err)
	}
	basePath := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/categories/posts/" + strconv.FormatInt(categoryID, 10)
	plan := controlConfigurationAPIReq(t, h, http.MethodDelete, basePath+"?dry_run=1", token, nil, controlConfigurationRequest{})
	if plan.Code != http.StatusOK {
		t.Fatalf("category plan = %d %s", plan.Code, plan.Body.String())
	}
	plannedRevision := controlImpactRevision(t, plan.Body.Bytes())

	if _, err := runtime.Store.CreateCategory(&store.Category{
		Slug: "impact-guides-en", Name: "Impact guides", Lang: "en", Kind: "post", TransGroup: group,
	}); err != nil {
		t.Fatalf("create translation after plan: %v", err)
	}
	if err := runtime.Store.SetSetting("nav_menu", `[{"url":"/about","labels":{"zh":"关于"}},{"url":"/category/impact-guides","labels":{"zh":"影响指南"}}]`); err != nil {
		t.Fatalf("add navigation reference after plan: %v", err)
	}
	unlock := controlConfigurationUnlock(t, h, token, controlTestPassword, "categories.delete")
	apply := controlConfigurationAPIReq(t, h, http.MethodDelete,
		basePath+"?expected_revision="+url.QueryEscape(plannedRevision), token, nil,
		controlConfigurationRequest{Confirm: "categories.delete", IdempotencyKey: "category-impact-changed", UnlockToken: unlock})
	if apply.Code != http.StatusConflict || !strings.Contains(apply.Body.String(), "category_impact_changed") {
		t.Fatalf("changed translation/reference impact = %d %s", apply.Code, apply.Body.String())
	}
	if category, _ := runtime.Store.GetCategoryByID(categoryID); category == nil {
		t.Fatal("stale impact deleted category")
	}
}

func TestDeleteCategoryTransactionRejectsChangedCategoryContext(t *testing.T) {
	srv, _, _, _, blogSite := setupPlatformAutomation(t)
	runtime, ok := srv.platformRuntimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil {
		t.Fatal("blog runtime missing")
	}
	categoryID, err := runtime.Store.CreateCategory(&store.Category{
		Slug: "transaction-guard", Name: "事务保护", Lang: "zh", Kind: "post", TransGroup: "transaction-guard",
	})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	category, err := runtime.Store.GetCategoryByID(categoryID)
	if err != nil || category == nil {
		t.Fatalf("read category: category=%#v err=%v", category, err)
	}
	usage, err := runtime.Store.CategoryUsageForDelete(categoryID, 0)
	if err != nil {
		t.Fatalf("read category usage: %v", err)
	}
	contextRevision, err := runtime.Store.CategoryDeleteContextRevision(category.Kind)
	if err != nil {
		t.Fatalf("read category context: %v", err)
	}
	rawNavigation := runtime.Store.Setting("nav_menu")
	if _, err := runtime.Store.CreateCategory(&store.Category{
		Slug: "transaction-peer", Name: "并发分类", Lang: "en", Kind: "post", TransGroup: "transaction-peer",
	}); err != nil {
		t.Fatalf("change category context: %v", err)
	}
	deleted, _, err := runtime.Store.DeleteCategoryWithNavigation(category, usage.Revision, contextRevision, &rawNavigation, nil)
	if !errors.Is(err, store.ErrCategoryChanged) || deleted {
		t.Fatalf("stale transaction context: deleted=%v err=%v", deleted, err)
	}
	if current, _ := runtime.Store.GetCategoryByID(categoryID); current == nil {
		t.Fatal("transaction guard did not preserve category")
	}
}

func TestPlatformControlNavigationDeleteAndPlatformPatchGuard(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	setPlatformTestPassword(t, ps, controlTestPassword)
	runtime, ok := srv.platformRuntimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil {
		t.Fatal("blog runtime missing")
	}
	if err := runtime.Store.SetSetting("nav_menu", `[{"url":"/pricing","labels":{"zh":"价格"}},{"url":"/about","labels":{"zh":"关于"}}]`); err != nil {
		t.Fatalf("seed navigation: %v", err)
	}

	patchToken := "gcmsp_navpatchguard123"
	createControlConfigurationKey(t, ps, patchToken, platform.KeyMembershipAll,
		strings.Join([]string{apiScopeNavigationRead, apiScopeNavigationWrite}, ","), nil)
	patchBody, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"url": "/about", "labels": map[string]string{"zh": "关于"}}}})
	patch := platformAPIReq(t, h, http.MethodPatch,
		"/api/platform/v1/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/navigation", patchToken, patchBody)
	if patch.Code != http.StatusForbidden || !strings.Contains(patch.Body.String(), "navigation_delete_requires_control") {
		t.Fatalf("destructive navigation PATCH = %d %s", patch.Code, patch.Body.String())
	}
	if navigation := runtime.Store.Setting("nav_menu"); !strings.Contains(navigation, "/pricing") {
		t.Fatalf("blocked PATCH still deleted item: %s", navigation)
	}

	token := "gcmsp_navigationdelete1"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeNavigationDelete}, ","), nil)
	basePath := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/navigation/0"
	plan := controlConfigurationAPIReq(t, h, http.MethodDelete, basePath+"?dry_run=1", token, nil, controlConfigurationRequest{})
	if plan.Code != http.StatusOK || !strings.Contains(plan.Body.String(), `"expected_url":"/pricing"`) || !strings.Contains(plan.Body.String(), `"position":1`) {
		t.Fatalf("navigation plan = %d %s", plan.Code, plan.Body.String())
	}
	impactRevision := controlImpactRevision(t, plan.Body.Bytes())
	unlock := controlConfigurationUnlock(t, h, token, controlTestPassword, "navigation.delete")

	missingExpected := controlConfigurationAPIReq(t, h, http.MethodDelete, basePath, token, nil,
		controlConfigurationRequest{Confirm: "navigation.delete", IdempotencyKey: "navigation-delete-1", UnlockToken: unlock})
	if missingExpected.Code != http.StatusUnprocessableEntity || !strings.Contains(missingExpected.Body.String(), "expected_url_required") {
		t.Fatalf("navigation delete without expected_url = %d %s", missingExpected.Code, missingExpected.Body.String())
	}
	stale := controlConfigurationAPIReq(t, h, http.MethodDelete,
		basePath+"?expected_url="+url.QueryEscape("/other")+"&expected_revision="+url.QueryEscape(impactRevision), token, nil,
		controlConfigurationRequest{Confirm: "navigation.delete", IdempotencyKey: "navigation-delete-1", UnlockToken: unlock})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "navigation_changed") {
		t.Fatalf("stale navigation delete = %d %s", stale.Code, stale.Body.String())
	}
	apply := controlConfigurationAPIReq(t, h, http.MethodDelete,
		basePath+"?expected_url="+url.QueryEscape("/pricing")+"&expected_revision="+url.QueryEscape(impactRevision), token, nil,
		controlConfigurationRequest{Confirm: "navigation.delete", IdempotencyKey: "navigation-delete-1", UnlockToken: unlock})
	if apply.Code != http.StatusOK || !strings.Contains(apply.Body.String(), `"remaining_count":1`) {
		t.Fatalf("navigation delete = %d %s", apply.Code, apply.Body.String())
	}
	if navigation := runtime.Store.Setting("nav_menu"); strings.Contains(navigation, "/pricing") || !strings.Contains(navigation, "/about") {
		t.Fatalf("navigation after delete = %s", navigation)
	}

	lastPath := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/navigation/0"
	lastPlan := controlConfigurationAPIReq(t, h, http.MethodDelete, lastPath+"?dry_run=1", token, nil, controlConfigurationRequest{})
	if lastPlan.Code != http.StatusOK || !strings.Contains(lastPlan.Body.String(), `"remaining_count":0`) ||
		!strings.Contains(lastPlan.Body.String(), "不会重新显示默认导航") {
		t.Fatalf("last navigation plan = %d %s", lastPlan.Code, lastPlan.Body.String())
	}
	lastRevision := controlImpactRevision(t, lastPlan.Body.Bytes())
	lastApply := controlConfigurationAPIReq(t, h, http.MethodDelete,
		lastPath+"?expected_url="+url.QueryEscape("/about")+"&expected_revision="+url.QueryEscape(lastRevision), token, nil,
		controlConfigurationRequest{Confirm: "navigation.delete", IdempotencyKey: "navigation-delete-last", UnlockToken: unlock})
	if lastApply.Code != http.StatusOK || !strings.Contains(lastApply.Body.String(), `"remaining_count":0`) {
		t.Fatalf("last navigation delete = %d %s", lastApply.Code, lastApply.Body.String())
	}
	if navigation := runtime.Store.Setting("nav_menu"); navigation != "[]" {
		t.Fatalf("empty navigation must remain explicit, got %q", navigation)
	}
	tr := i18n.New().Tr("zh", "zh")
	if items := runtime.server.menuItems(httptest.NewRequest(http.MethodGet, "http://example.test/zh/", nil), "zh", tr, "home"); len(items) != 0 {
		t.Fatalf("explicit empty navigation fell back to defaults: %#v", items)
	}
}

func TestPlatformControlNavigationMaterializesAndDeletesVisibleDefaults(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	setPlatformTestPassword(t, ps, controlTestPassword)
	runtime, ok := srv.platformRuntimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil {
		t.Fatal("blog runtime missing")
	}
	if err := runtime.Store.SetSetting("nav_menu", ""); err != nil {
		t.Fatalf("clear navigation setting: %v", err)
	}
	token := "gcmsp_defaultnavdelete"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAll,
		strings.Join([]string{
			apiScopeControlRead, apiScopeControlUnlock, apiScopeNavigationDelete,
			apiScopeNavigationRead, apiScopeNavigationWrite,
		}, ","), nil)
	sitePath := "/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10)
	get := platformAPIReq(t, h, http.MethodGet, sitePath+"/navigation", token, nil)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"configured":false`) ||
		!strings.Contains(get.Body.String(), `"source":"defaults"`) {
		t.Fatalf("default navigation response = %d %s", get.Code, get.Body.String())
	}
	var navigation struct {
		Items []apiNavigationItem `json:"items"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &navigation); err != nil || len(navigation.Items) != 4 {
		t.Fatalf("decode visible defaults: items=%#v err=%v body=%s", navigation.Items, err, get.Body.String())
	}

	patchBody, _ := json.Marshal(map[string]any{"items": navigation.Items[:3]})
	patch := platformAPIReq(t, h, http.MethodPatch, sitePath+"/navigation", token, patchBody)
	if patch.Code != http.StatusForbidden || !strings.Contains(patch.Body.String(), "navigation_delete_requires_control") {
		t.Fatalf("default navigation PATCH bypass = %d %s", patch.Code, patch.Body.String())
	}

	controlPath := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/navigation/3"
	plan := controlConfigurationAPIReq(t, h, http.MethodDelete, controlPath+"?dry_run=1", token, nil, controlConfigurationRequest{})
	if plan.Code != http.StatusOK || !strings.Contains(plan.Body.String(), `"navigation_source":"defaults"`) ||
		!strings.Contains(plan.Body.String(), `"expected_url":"/about"`) {
		t.Fatalf("default navigation delete plan = %d %s", plan.Code, plan.Body.String())
	}
	revision := controlImpactRevision(t, plan.Body.Bytes())
	unlock := controlConfigurationUnlock(t, h, token, controlTestPassword, "navigation.delete")
	apply := controlConfigurationAPIReq(t, h, http.MethodDelete,
		controlPath+"?expected_url="+url.QueryEscape("/about")+"&expected_revision="+url.QueryEscape(revision), token, nil,
		controlConfigurationRequest{Confirm: "navigation.delete", IdempotencyKey: "delete-default-about", UnlockToken: unlock})
	if apply.Code != http.StatusOK || !strings.Contains(apply.Body.String(), `"remaining_count":3`) {
		t.Fatalf("default navigation delete = %d %s", apply.Code, apply.Body.String())
	}
	if navigation := runtime.Store.Setting("nav_menu"); navigation == "" || !strings.HasPrefix(navigation, "[") || strings.Contains(navigation, `"/about"`) {
		t.Fatalf("default navigation was not materialized after delete: %q", navigation)
	}
	after := platformAPIReq(t, h, http.MethodGet, sitePath+"/navigation", token, nil)
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"configured":true`) ||
		!strings.Contains(after.Body.String(), `"source":"configured"`) {
		t.Fatalf("materialized navigation response = %d %s", after.Code, after.Body.String())
	}
}

func TestNavigationRowsRemovedAllowsReorderAndLabelChanges(t *testing.T) {
	current := []MenuRow{
		{URL: "/pricing", Labels: map[string]string{"zh": "价格"}},
		{URL: "/about", Labels: map[string]string{"zh": "关于"}},
	}
	reordered := []MenuRow{
		{URL: "/about", Labels: map[string]string{"zh": "关于我们"}},
		{URL: "/pricing", Labels: map[string]string{"zh": "套餐"}},
		{URL: "/contact", Labels: map[string]string{"zh": "联系"}},
	}
	if navigationRowsRemoved(current, reordered) {
		t.Fatal("reordering, relabeling and adding navigation items must remain a normal PATCH")
	}
	if !navigationRowsRemoved(current, reordered[:1]) {
		t.Fatal("removing an existing navigation URL must require controlled delete")
	}
}

func TestControlledDeleteScopeIssuanceAddsUnlockDependency(t *testing.T) {
	for _, scope := range []string{apiScopeCategoriesDelete, apiScopeNavigationDelete} {
		req := &http.Request{Method: http.MethodPost, Header: make(http.Header), Form: url.Values{"scopes": {scope}}}
		got := "," + strings.Join(automationScopesFromFormRequired(req), ",") + ","
		for _, want := range []string{apiScopeControlRead, apiScopeControlUnlock, scope} {
			if !strings.Contains(got, ","+want+",") {
				t.Fatalf("%s did not add dependency %s: %s", scope, want, got)
			}
		}
	}
}

func TestControlledDeleteOperationsRejectWrongMembership(t *testing.T) {
	_, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_deleteallowlist1"
	if _, err := ps.CreatePlatformKey("delete allowlist", token, token[:13], platform.KeyMembershipAllowlist,
		strings.Join([]string{apiScopeCategoriesDelete, apiScopeNavigationDelete}, ","), []int64{defaultSite.ID}, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	for _, path := range []string{
		"/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/categories/posts/1?dry_run=1",
		"/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/navigation/0?dry_run=1",
	} {
		rec := controlConfigurationAPIReq(t, h, http.MethodDelete, path, token, nil, controlConfigurationRequest{})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "site_forbidden") {
			t.Fatalf("membership guard %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
