package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

type controlSiteItem struct {
	ID                          int64  `json:"id"`
	Slug                        string `json:"slug"`
	Name                        string `json:"name"`
	Status                      string `json:"status"`
	IsDefault                   bool   `json:"is_default"`
	ManagementAutomationEnabled bool   `json:"management_automation_enabled"`
	SiteKind                    string `json:"site_kind,omitempty"`
	CreatedAt                   string `json:"created_at,omitempty"`
	UpdatedAt                   string `json:"updated_at,omitempty"`
}

type controlSiteCreateInput struct {
	Slug                        string `json:"slug"`
	Name                        string `json:"name"`
	SeedMode                    string `json:"seed_mode"`
	SiteKind                    string `json:"site_kind"`
	ManagementAutomationEnabled *bool  `json:"management_automation_enabled"`
}

type controlSiteCreateNormalized struct {
	Slug                        string `json:"slug"`
	Name                        string `json:"name"`
	SeedMode                    string `json:"seed_mode"`
	SiteKind                    string `json:"site_kind"`
	ManagementAutomationEnabled bool   `json:"management_automation_enabled"`
}

type controlSitePatchInput struct {
	Name                        *string `json:"name"`
	Status                      *string `json:"status"`
	ManagementAutomationEnabled *bool   `json:"management_automation_enabled"`
}

type controlSitePatchNormalized struct {
	SiteID                      int64   `json:"site_id"`
	Name                        *string `json:"name,omitempty"`
	Status                      *string `json:"status,omitempty"`
	ManagementAutomationEnabled *bool   `json:"management_automation_enabled,omitempty"`
}

type controlSiteDeleteNormalized struct {
	SiteID int64 `json:"site_id"`
}

type controlArchivedSiteItem struct {
	ID             int64  `json:"id"`
	OriginalSiteID int64  `json:"original_site_id"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	ArchivedAt     string `json:"archived_at,omitempty"`
	Recoverable    bool   `json:"recoverable"`
}

func controlTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func controlSiteSafeItem(site *platform.Site, kind string) controlSiteItem {
	if site == nil {
		return controlSiteItem{}
	}
	return controlSiteItem{
		ID:                          site.ID,
		Slug:                        site.Slug,
		Name:                        site.Name,
		Status:                      site.Status,
		IsDefault:                   site.IsDefault,
		ManagementAutomationEnabled: site.ManagementAutomationEnabled,
		SiteKind:                    kind,
		CreatedAt:                   controlTimeString(site.CreatedAt),
		UpdatedAt:                   controlTimeString(site.UpdatedAt),
	}
}

func controlArchivedSiteSafeItem(site *platform.ArchivedSite) controlArchivedSiteItem {
	if site == nil {
		return controlArchivedSiteItem{}
	}
	return controlArchivedSiteItem{
		ID:             site.ID,
		OriginalSiteID: site.OriginalSiteID,
		Slug:           site.Slug,
		Name:           site.Name,
		ArchivedAt:     controlTimeString(site.ArchivedAt),
		Recoverable:    true,
	}
}

func controlMutationFingerprint(operation string, normalized any) string {
	data, err := json.Marshal(normalized)
	if err != nil {
		return operation
	}
	return operation + ":" + string(data)
}

func controlSiteMembershipAllowed(w http.ResponseWriter, key *platform.PlatformAutomationKey, siteID int64) bool {
	if key == nil || !key.CanManageSite(siteID) {
		apiError(w, http.StatusForbidden, "membership_scope", "当前平台密钥无权管理该站点。")
		return false
	}
	return true
}

func (s *Server) servePlatformControlSites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.servePlatformControlSiteList(w, r)
	case http.MethodPost:
		s.servePlatformControlSiteCreate(w, r)
	default:
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 或 POST。")
	}
}

func (s *Server) servePlatformControlSite(w http.ResponseWriter, r *http.Request, siteID int64) {
	if siteID <= 0 {
		apiError(w, http.StatusBadRequest, "invalid_site_id", "站点 ID 无效。")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.servePlatformControlSiteGet(w, r, siteID)
	case http.MethodPatch:
		s.servePlatformControlSitePatch(w, r, siteID)
	case http.MethodDelete:
		s.servePlatformControlSiteDelete(w, r, siteID)
	default:
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET、PATCH 或 DELETE。")
	}
}

func (s *Server) servePlatformControlSiteList(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requirePlatformControlKey(w, r, apiScopeControlRead)
	if !ok {
		return
	}
	sites, err := s.platform.Sites()
	if err != nil {
		apiError(w, http.StatusInternalServerError, "site_list_failed", "无法读取站点列表。")
		return
	}
	items := make([]controlSiteItem, 0, len(sites))
	for _, site := range sites {
		if site == nil || !key.CanManageSite(site.ID) {
			continue
		}
		items = append(items, controlSiteSafeItem(site, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) servePlatformControlSiteGet(w http.ResponseWriter, r *http.Request, siteID int64) {
	key, ok := s.requirePlatformControlKey(w, r, apiScopeControlRead)
	if !ok || !controlSiteMembershipAllowed(w, key, siteID) {
		return
	}
	site, found, err := s.platform.GetSite(siteID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "site_read_failed", "无法读取站点。")
		return
	}
	if !found || site == nil {
		apiError(w, http.StatusNotFound, "site_not_found", "站点不存在。")
		return
	}
	kind, warnings := controlSiteKind(site)
	writeJSON(w, http.StatusOK, map[string]any{
		"item":     controlSiteSafeItem(site, kind),
		"warnings": warnings,
	})
}

func normalizeControlSiteCreate(in controlSiteCreateInput) (controlSiteCreateNormalized, *controlMutationError) {
	out := controlSiteCreateNormalized{
		Slug:                        strings.TrimSpace(in.Slug),
		Name:                        strings.TrimSpace(in.Name),
		SeedMode:                    strings.ToLower(strings.TrimSpace(in.SeedMode)),
		SiteKind:                    strings.ToLower(strings.TrimSpace(in.SiteKind)),
		ManagementAutomationEnabled: true,
	}
	if in.ManagementAutomationEnabled != nil {
		out.ManagementAutomationEnabled = *in.ManagementAutomationEnabled
	}
	if out.Name == "" {
		out.Name = out.Slug
	}
	if out.SeedMode == "" {
		out.SeedMode = "empty"
	}
	if out.SeedMode != "empty" && out.SeedMode != "demo" {
		return out, newControlMutationError(http.StatusBadRequest, "invalid_seed_mode", "seed_mode 只能是 empty 或 demo。")
	}
	switch out.SiteKind {
	case "", siteKindContent:
		out.SiteKind = siteKindContent
	case siteKindFactory, siteKindDTC:
	default:
		return out, newControlMutationError(http.StatusBadRequest, "invalid_site_kind", "site_kind 只能是 content、factory 或 dtc。")
	}
	if out.Slug == "" {
		return out, newControlMutationError(http.StatusBadRequest, "invalid_slug", "站点标记不能为空。")
	}
	if out.Name == "" {
		return out, newControlMutationError(http.StatusBadRequest, "invalid_name", "站点名称不能为空。")
	}
	return out, nil
}

func (s *Server) controlSiteCreatePreflight(in controlSiteCreateNormalized) (string, string, *controlMutationError) {
	dbPath, uploadDir, err := s.newSiteStoragePaths(in.Slug)
	if err != nil {
		return "", "", newControlMutationError(http.StatusBadRequest, "invalid_slug", err.Error())
	}
	sites, err := s.platform.Sites()
	if err != nil {
		return "", "", newControlMutationError(http.StatusInternalServerError, "site_list_failed", "无法检查站点标记。")
	}
	for _, site := range sites {
		if site != nil && site.Slug == in.Slug {
			return "", "", newControlMutationError(http.StatusConflict, "site_slug_conflict", "站点标记已存在，请换一个。")
		}
	}
	root := filepath.Dir(dbPath)
	if _, err := os.Lstat(root); err == nil {
		return "", "", newControlMutationError(http.StatusConflict, "site_storage_conflict", "该站点的标准存储目录已存在，请先检查目录。")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", newControlMutationError(http.StatusInternalServerError, "site_storage_check_failed", "无法检查站点存储目录。")
	}
	return dbPath, uploadDir, nil
}

func controlSiteCreateDryRun(in controlSiteCreateNormalized) map[string]any {
	warnings := []string{}
	if in.SeedMode == "demo" {
		warnings = append(warnings, "将写入 GCMS 演示内容。")
	}
	if !in.ManagementAutomationEnabled {
		warnings = append(warnings, "站点创建后不会开放给常规平台自动化接口，可稍后重新开启。")
	}
	return map[string]any{
		"dry_run":          true,
		"operation":        "sites.create",
		"normalized_input": in,
		"impact": map[string]any{
			"creates_site":             true,
			"creates_standard_storage": true,
			"seed_mode":                in.SeedMode,
		},
		"warnings": warnings,
	}
}

func (s *Server) servePlatformControlSiteCreate(w http.ResponseWriter, r *http.Request) {
	key, ok := s.requirePlatformControlKey(w, r, apiScopeSitesCreate)
	if !ok {
		return
	}
	if key.MembershipMode != platform.KeyMembershipAll {
		apiError(w, http.StatusForbidden, "membership_scope", "只有覆盖全部站点的平台密钥可以创建新站点。")
		return
	}
	var in controlSiteCreateInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	normalized, validationErr := normalizeControlSiteCreate(in)
	if validationErr != nil {
		writeControlMutationError(w, validationErr)
		return
	}
	fingerprint := controlMutationFingerprint("sites.create", normalized)
	s.executeControlMutation(w, r, key, "sites.create", fingerprint, func() (int, any, error) {
		dbPath, uploadDir, preflightErr := s.controlSiteCreatePreflight(normalized)
		if preflightErr != nil {
			return 0, nil, preflightErr
		}
		if controlDryRun(r) {
			return http.StatusOK, controlSiteCreateDryRun(normalized), nil
		}
		return s.executeControlSiteCreate(key, normalized, dbPath, uploadDir)
	})
}

func (s *Server) executeControlSiteCreate(key *platform.PlatformAutomationKey, in controlSiteCreateNormalized, dbPath, uploadDir string) (int, any, error) {
	root := filepath.Dir(dbPath)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_storage_create_failed", "无法创建站点存储父目录。")
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, nil, newControlMutationError(http.StatusConflict, "site_storage_conflict", "该站点的标准存储目录已存在，请先检查目录。")
		}
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_storage_create_failed", "无法创建站点存储目录。")
	}
	cleanupRoot := true
	defer func() {
		if cleanupRoot {
			_ = os.RemoveAll(root)
		}
	}()
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_storage_create_failed", "无法创建上传目录。")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_store_create_failed", "无法初始化站点数据库。")
	}
	closed := false
	closeStore := func() error {
		if closed {
			return nil
		}
		closed = true
		return st.Close()
	}
	defer func() { _ = closeStore() }()
	if err := st.SetSetting("site.name", in.Name); err != nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_seed_failed", "无法写入站点名称。")
	}
	if in.SeedMode == "empty" {
		if err := st.ClearDemoContent(); err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_seed_failed", "无法清理演示内容。")
		}
		if err := st.EnsureEmptySiteBasePages(); err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_seed_failed", "无法创建空站基础页面。")
		}
	}
	if err := applySiteKindPreset(st, s.i18n, in.SiteKind, in.SeedMode != "empty"); err != nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_preset_failed", "无法应用站点类型预设。")
	}
	if err := closeStore(); err != nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_store_close_failed", "无法完成站点数据库初始化。")
	}
	site, err := s.platform.CreateSite(in.Slug, in.Name, dbPath, uploadDir, in.ManagementAutomationEnabled)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return 0, nil, newControlMutationError(http.StatusConflict, "site_slug_conflict", "站点标记已存在，请换一个。")
		}
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_create_failed", "无法创建平台站点记录。")
	}
	// 平台记录一旦创建，只有完整回滚成功后才能删除目录；否则必须保留现场，
	// 避免平台记录仍存在但数据库被 defer 清掉。
	cleanupRoot = false
	if err := s.reloadRuntimePool(); err != nil {
		rollbackErr := s.rollbackControlSiteCreate(site, root)
		mutationErr := newControlMutationError(http.StatusInternalServerError, "runtime_reload_failed", "站点创建后无法加载运行时，已回滚本次创建。")
		if rollbackErr != nil {
			mutationErr.Message = "站点运行时加载失败，且自动回滚未完全完成，请人工检查。"
			mutationErr.Details = map[string]any{"manual_check_required": true}
		}
		return 0, nil, mutationErr
	}
	_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_sites_create", "site", site.ID, "已通过统一控制层创建站点 "+site.Slug)
	return http.StatusCreated, map[string]any{
		"item":     controlSiteSafeItem(site, in.SiteKind),
		"created":  true,
		"warnings": []string{},
	}, nil
}

func (s *Server) rollbackControlSiteCreate(site *platform.Site, root string) error {
	if site == nil {
		return nil
	}
	if err := s.platform.SetSiteStatus(site.ID, "disabled"); err != nil {
		return err
	}
	archived, err := s.platform.ArchiveSite(site.ID, root)
	if err != nil {
		return err
	}
	if err := s.platform.DeleteArchivedSite(archived.ID); err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return s.reloadRuntimePool()
}

func normalizeControlSitePatch(siteID int64, in controlSitePatchInput) (controlSitePatchNormalized, *controlMutationError) {
	out := controlSitePatchNormalized{SiteID: siteID}
	if in.Name != nil {
		value := strings.TrimSpace(*in.Name)
		if value == "" {
			return out, newControlMutationError(http.StatusBadRequest, "invalid_name", "站点名称不能为空。")
		}
		out.Name = &value
	}
	if in.Status != nil {
		value := strings.ToLower(strings.TrimSpace(*in.Status))
		if value != "enabled" && value != "disabled" {
			return out, newControlMutationError(http.StatusBadRequest, "invalid_status", "status 只能是 enabled 或 disabled。")
		}
		out.Status = &value
	}
	if in.ManagementAutomationEnabled != nil {
		value := *in.ManagementAutomationEnabled
		out.ManagementAutomationEnabled = &value
	}
	if out.Name == nil && out.Status == nil && out.ManagementAutomationEnabled == nil {
		return out, newControlMutationError(http.StatusBadRequest, "empty_patch", "至少提供 name、status 或 management_automation_enabled 中的一项。")
	}
	return out, nil
}

func controlSitePatchDryRun(site *platform.Site, in controlSitePatchNormalized) map[string]any {
	changes := make([]map[string]any, 0, 3)
	if in.Name != nil {
		changes = append(changes, map[string]any{"field": "name", "from": site.Name, "to": *in.Name})
	}
	if in.Status != nil {
		changes = append(changes, map[string]any{"field": "status", "from": site.Status, "to": *in.Status})
	}
	if in.ManagementAutomationEnabled != nil {
		changes = append(changes, map[string]any{"field": "management_automation_enabled", "from": site.ManagementAutomationEnabled, "to": *in.ManagementAutomationEnabled})
	}
	return map[string]any{
		"dry_run":          true,
		"operation":        "sites.update",
		"normalized_input": in,
		"impact":           map[string]any{"changes": changes, "reloads_runtime": true},
		"warnings":         []string{},
	}
}

func (s *Server) servePlatformControlSitePatch(w http.ResponseWriter, r *http.Request, siteID int64) {
	key, ok := s.requirePlatformControlKey(w, r, apiScopeSitesUpdate)
	if !ok || !controlSiteMembershipAllowed(w, key, siteID) {
		return
	}
	var in controlSitePatchInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	normalized, validationErr := normalizeControlSitePatch(siteID, in)
	if validationErr != nil {
		writeControlMutationError(w, validationErr)
		return
	}
	fingerprint := controlMutationFingerprint("sites.update", normalized)
	s.executeControlMutation(w, r, key, "sites.update", fingerprint, func() (int, any, error) {
		site, found, err := s.platform.GetSite(siteID)
		if err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_read_failed", "无法读取站点。")
		}
		if !found || site == nil {
			return 0, nil, newControlMutationError(http.StatusNotFound, "site_not_found", "站点不存在。")
		}
		if site.IsDefault && normalized.Status != nil && *normalized.Status == "disabled" {
			return 0, nil, newControlMutationError(http.StatusConflict, "default_site_protected", "默认站点不能关闭。")
		}
		if controlDryRun(r) {
			return http.StatusOK, controlSitePatchDryRun(site, normalized), nil
		}
		return s.executeControlSitePatch(key, site, normalized)
	})
}

func (s *Server) executeControlSitePatch(key *platform.PlatformAutomationKey, site *platform.Site, in controlSitePatchNormalized) (int, any, error) {
	if site == nil {
		return 0, nil, newControlMutationError(http.StatusNotFound, "site_not_found", "站点不存在。")
	}
	var siteStore *store.Store
	var oldSettingName string
	if in.Name != nil {
		var err error
		siteStore, err = store.Open(site.DBPath)
		if err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_store_open_failed", "无法打开站点数据库。")
		}
		defer siteStore.Close()
		oldSettingName = siteStore.Setting("site.name")
	}
	nameChanged := false
	statusChanged := false
	automationChanged := false
	rollback := func() {
		if automationChanged {
			_ = s.platform.SetSiteAutomation(site.ID, site.ManagementAutomationEnabled)
		}
		if statusChanged {
			_ = s.platform.SetSiteStatus(site.ID, site.Status)
		}
		if nameChanged {
			_ = s.platform.SetSiteName(site.ID, site.Name)
			if siteStore != nil {
				_ = siteStore.SetSetting("site.name", oldSettingName)
			}
		}
	}
	if in.Name != nil && *in.Name != site.Name {
		if err := s.platform.SetSiteName(site.ID, *in.Name); err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_update_failed", "无法更新站点名称。")
		}
		nameChanged = true
		if err := siteStore.SetSetting("site.name", *in.Name); err != nil {
			rollback()
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_update_failed", "无法同步站点显示名称。")
		}
	}
	if in.Status != nil && *in.Status != site.Status {
		if err := s.platform.SetSiteStatus(site.ID, *in.Status); err != nil {
			rollback()
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_update_failed", "无法更新站点状态。")
		}
		statusChanged = true
	}
	if in.ManagementAutomationEnabled != nil && *in.ManagementAutomationEnabled != site.ManagementAutomationEnabled {
		if err := s.platform.SetSiteAutomation(site.ID, *in.ManagementAutomationEnabled); err != nil {
			rollback()
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_update_failed", "无法更新站点自动化状态。")
		}
		automationChanged = true
	}
	if err := s.reloadRuntimePool(); err != nil {
		rollback()
		_ = s.reloadRuntimePool()
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "runtime_reload_failed", "站点修改后无法加载运行时，已回滚本次修改。")
	}
	updated, found, err := s.platform.GetSite(site.ID)
	if err != nil || !found || updated == nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_read_failed", "站点已修改，但无法读取最新状态。")
	}
	_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_sites_update", "site", site.ID, "已通过统一控制层更新站点 "+site.Slug)
	kind, warnings := controlSiteKind(updated)
	return http.StatusOK, map[string]any{"item": controlSiteSafeItem(updated, kind), "updated": true, "warnings": warnings}, nil
}

func controlSiteDeleteDryRun(site *platform.Site) map[string]any {
	return map[string]any{
		"dry_run":          true,
		"operation":        "sites.delete",
		"normalized_input": controlSiteDeleteNormalized{SiteID: site.ID},
		"impact": map[string]any{
			"removes_active_site": true,
			"archives_storage":    true,
			"preserves_data":      true,
			"recoverable":         true,
		},
		"warnings": []string{"站点将从活动列表移除，但数据会保留在归档目录中。"},
	}
}

func (s *Server) servePlatformControlSiteDelete(w http.ResponseWriter, r *http.Request, siteID int64) {
	key, ok := s.requirePlatformControlKey(w, r, apiScopeSitesDelete)
	if !ok || !controlSiteMembershipAllowed(w, key, siteID) {
		return
	}
	normalized := controlSiteDeleteNormalized{SiteID: siteID}
	fingerprint := controlMutationFingerprint("sites.delete", normalized)
	s.executeControlMutation(w, r, key, "sites.delete", fingerprint, func() (int, any, error) {
		site, found, err := s.platform.GetSite(siteID)
		if err != nil {
			return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_read_failed", "无法读取站点。")
		}
		if !found || site == nil {
			return 0, nil, newControlMutationError(http.StatusNotFound, "site_not_found", "站点不存在。")
		}
		if site.IsDefault {
			return 0, nil, newControlMutationError(http.StatusConflict, "default_site_protected", "默认站点不能归档删除。")
		}
		if site.Status != "disabled" {
			return 0, nil, newControlMutationError(http.StatusConflict, "site_must_be_disabled", "请先关闭站点，再执行归档删除。")
		}
		sourceRoot, validationErr := controlSiteArchiveSource(site)
		if validationErr != nil {
			return 0, nil, validationErr
		}
		if controlDryRun(r) {
			return http.StatusOK, controlSiteDeleteDryRun(site), nil
		}
		return s.executeControlSiteDelete(key, site, sourceRoot)
	})
}

func controlSiteArchiveSource(site *platform.Site) (string, *controlMutationError) {
	sourceRoot, err := standardSiteStorageRoot(site)
	if err != nil {
		return "", newControlMutationError(http.StatusConflict, "nonstandard_site_storage", err.Error())
	}
	info, err := os.Stat(sourceRoot)
	if err != nil || !info.IsDir() {
		return "", newControlMutationError(http.StatusConflict, "site_storage_unavailable", "站点标准存储目录不存在或不可用。")
	}
	return sourceRoot, nil
}

func (s *Server) executeControlSiteDelete(key *platform.PlatformAutomationKey, site *platform.Site, sourceRoot string) (int, any, error) {
	archivePath, err := s.newArchivedSitePath(site)
	if err != nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "archive_path_failed", "无法创建站点归档路径。")
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "archive_path_failed", "无法创建站点归档目录。")
	}
	s.detachSiteRuntime(site.ID)
	if err := os.Rename(sourceRoot, archivePath); err != nil {
		_ = s.reloadRuntimePool()
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "archive_move_failed", "无法把站点数据移动到归档目录。")
	}
	archived, err := s.platform.ArchiveSite(site.ID, archivePath)
	if err != nil {
		_ = os.Rename(archivePath, sourceRoot)
		_ = s.reloadRuntimePool()
		return 0, nil, newControlMutationError(http.StatusInternalServerError, "site_archive_failed", "无法归档站点记录，站点数据已回滚。")
	}
	if err := writeArchivedSiteManifest(archived); err != nil {
		rollbackErr := s.rollbackControlSiteArchive(archived, sourceRoot, archivePath)
		mutationErr := newControlMutationError(http.StatusInternalServerError, "archive_manifest_failed", "无法写入归档清单，已回滚本次归档。")
		if rollbackErr != nil {
			mutationErr.Message = "归档清单写入失败，且自动回滚未完全完成，请人工检查。"
			mutationErr.Details = map[string]any{"manual_check_required": true}
		}
		return 0, nil, mutationErr
	}
	if err := s.reloadRuntimePool(); err != nil {
		rollbackErr := s.rollbackControlSiteArchive(archived, sourceRoot, archivePath)
		mutationErr := newControlMutationError(http.StatusInternalServerError, "runtime_reload_failed", "站点归档后无法刷新运行时，已回滚本次归档。")
		if rollbackErr != nil {
			mutationErr.Message = "站点运行时刷新失败，且自动回滚未完全完成，请人工检查。"
			mutationErr.Details = map[string]any{"manual_check_required": true}
		}
		return 0, nil, mutationErr
	}
	_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_sites_delete", "site", site.ID, "已通过统一控制层归档站点 "+site.Slug)
	return http.StatusOK, map[string]any{
		"archived":    controlArchivedSiteSafeItem(archived),
		"deleted":     true,
		"recoverable": true,
		"warnings":    []string{},
	}, nil
}

func (s *Server) rollbackControlSiteArchive(archived *platform.ArchivedSite, sourceRoot, archivePath string) error {
	if archived == nil {
		return errors.New("归档记录不存在")
	}
	if err := os.Rename(archivePath, sourceRoot); err != nil {
		return err
	}
	if _, err := s.platform.RestoreArchivedSite(archived.ID); err != nil {
		// 平台记录仍是归档态时，尽量把目录也放回归档位置，保持两边一致。
		_ = os.Rename(sourceRoot, archivePath)
		return err
	}
	_ = os.Remove(filepath.Join(sourceRoot, "archive.json"))
	return s.reloadRuntimePool()
}

func controlSiteKind(site *platform.Site) (string, []string) {
	if site == nil || strings.TrimSpace(site.DBPath) == "" {
		return "", []string{"无法读取站点类型。"}
	}
	st, err := store.Open(site.DBPath)
	if err != nil {
		return "", []string{"无法读取站点类型。"}
	}
	defer st.Close()
	return siteKindOf(st), []string{}
}
