package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

const (
	controlPreviousThemeSettingKey = "control.theme.previous"
	controlUIRequestHeader         = "X-GCMS-Control-UI"
	controlUIPilotValue            = "pilot"
)

type controlThemeOption struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Localized   bool   `json:"localized"`
	Max         int    `json:"max,omitempty"`
	EnabledKey  string `json:"enabled_key,omitempty"`
	Example     string `json:"example,omitempty"`
}

type controlTheme struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Category    string               `json:"category"`
	Layout      string               `json:"layout"`
	Options     []controlThemeOption `json:"options"`
	Selected    bool                 `json:"selected"`
}

func controlCurrentTheme(st *store.Store) string {
	if st == nil {
		return "editorial"
	}
	theme := strings.TrimSpace(st.Setting("theme"))
	if !validTheme(theme) {
		return "editorial"
	}
	return theme
}

func controlThemePayload(theme ThemeOption, selected string) controlTheme {
	options := make([]controlThemeOption, 0, len(themeOptionSpecs(theme.ID)))
	for _, option := range themeOptionSpecs(theme.ID) {
		options = append(options, controlThemeOption{
			Key:         option.Key,
			Type:        option.Type,
			Label:       option.Label,
			Description: option.Desc,
			Localized:   option.Localized,
			Max:         option.Max,
			EnabledKey:  option.EnabledKey,
			Example:     option.Example,
		})
	}
	return controlTheme{
		ID:          theme.ID,
		Name:        theme.Name,
		Description: theme.Desc,
		Category:    theme.Category,
		Layout:      layoutForTheme(theme.ID),
		Options:     options,
		Selected:    theme.ID == selected,
	}
}

// servePlatformControlThemes returns the server theme catalog. selected reflects
// the platform default site's current theme; per-site selection is exposed by
// servePlatformControlSiteTheme.
func (s *Server) servePlatformControlThemes(w http.ResponseWriter, r *http.Request, themeID string) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET。")
		return
	}
	if _, ok := s.requirePlatformControlKey(w, r, apiScopeThemesRead); !ok {
		return
	}
	selected := controlCurrentTheme(s.store)
	themeID = strings.TrimSpace(themeID)
	if themeID != "" {
		for _, theme := range Themes {
			if theme.ID == themeID {
				writeJSON(w, http.StatusOK, map[string]any{"theme": controlThemePayload(theme, selected)})
				return
			}
		}
		apiError(w, http.StatusNotFound, "theme_not_found", "外观主题不存在。")
		return
	}

	items := make([]controlTheme, 0, len(Themes))
	for _, theme := range Themes {
		items = append(items, controlThemePayload(theme, selected))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":          items,
		"selected_theme": selected,
	})
}

func (s *Server) controlConfigurationSite(w http.ResponseWriter, r *http.Request, scope string, siteID int64) (*platform.PlatformAutomationKey, *platform.Site, *SiteRuntime, bool) {
	key, ok := s.requirePlatformControlKey(w, r, scope)
	if !ok {
		return nil, nil, nil, false
	}
	// CanManageSite intentionally checks membership only. A disabled site remains
	// configurable when it is within the key's all/allowlist membership.
	if !key.CanManageSite(siteID) {
		apiError(w, http.StatusForbidden, "site_forbidden", "当前平台密钥不能管理这个站点。")
		return nil, nil, nil, false
	}
	site, found, err := s.platform.GetSite(siteID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "site_lookup_failed", "无法读取站点。")
		return nil, nil, nil, false
	}
	if !found || site == nil {
		apiError(w, http.StatusNotFound, "site_not_found", "站点不存在。")
		return nil, nil, nil, false
	}
	pool := s.platformRuntimePool()
	runtime, found := pool.runtimeByID(siteID)
	if !found || runtime == nil || runtime.Store == nil || runtime.server == nil {
		apiError(w, http.StatusServiceUnavailable, "site_runtime_unavailable", "站点运行时尚未就绪。")
		return nil, nil, nil, false
	}
	return key, site, runtime, true
}

type controlThemeMutationInput struct {
	ThemeID  string `json:"theme_id"`
	Action   string `json:"action,omitempty"`
	Rollback bool   `json:"rollback,omitempty"`
}

func (s *Server) servePlatformControlSiteTheme(w http.ResponseWriter, r *http.Request, siteID int64) {
	switch r.Method {
	case http.MethodGet:
		_, site, runtime, ok := s.controlConfigurationSite(w, r, apiScopeThemesRead, siteID)
		if !ok {
			return
		}
		current := controlCurrentTheme(runtime.Store)
		previous := strings.TrimSpace(runtime.Store.Setting(controlPreviousThemeSettingKey))
		if !validTheme(previous) {
			previous = ""
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"site_id":        site.ID,
			"site_status":    site.Status,
			"theme":          current,
			"layout":         layoutForTheme(current),
			"previous_theme": previous,
			"can_rollback":   previous != "",
		})
	case http.MethodPut:
		key, site, runtime, ok := s.controlConfigurationSite(w, r, apiScopeThemesApply, siteID)
		if !ok {
			return
		}
		var in controlThemeMutationInput
		if !decodeAPIJSON(w, r, &in) {
			return
		}
		action := strings.ToLower(strings.TrimSpace(in.Action))
		rollback := in.Rollback || action == "rollback"
		if action != "" && action != "apply" && action != "rollback" {
			apiError(w, http.StatusBadRequest, "invalid_theme_action", "action 只能是 apply 或 rollback。")
			return
		}
		target := strings.TrimSpace(in.ThemeID)
		if !rollback && !validTheme(target) {
			apiError(w, http.StatusBadRequest, "invalid_theme", "请选择有效的外观主题。")
			return
		}

		// The request fingerprint intentionally excludes mutable current/previous
		// state, so a completed apply or rollback can be replayed idempotently.
		fingerprint := fmt.Sprintf("site=%d|action=%s|theme=%s", site.ID, map[bool]string{true: "rollback", false: "apply"}[rollback], target)
		s.executeControlMutation(w, r, key, "themes.apply", fingerprint, func() (int, any, error) {
			from := controlCurrentTheme(runtime.Store)
			to := target
			if rollback {
				to = strings.TrimSpace(runtime.Store.Setting(controlPreviousThemeSettingKey))
				if !validTheme(to) {
					return 0, nil, newControlMutationError(http.StatusConflict, "theme_rollback_unavailable", "没有可恢复的上一个主题。")
				}
			}
			response := map[string]any{
				"ok":             true,
				"dry_run":        controlDryRun(r),
				"site_id":        site.ID,
				"site_status":    site.Status,
				"action":         map[bool]string{true: "rollback", false: "apply"}[rollback],
				"previous_theme": from,
				"theme":          to,
				"layout":         layoutForTheme(to),
				"changed":        from != to,
			}
			if controlDryRun(r) || from == to {
				return http.StatusOK, response, nil
			}

			oldRollback := runtime.Store.Setting(controlPreviousThemeSettingKey)
			if !rollback {
				if err := runtime.Store.SetSetting(controlPreviousThemeSettingKey, from); err != nil {
					return 0, nil, newControlMutationError(http.StatusInternalServerError, "theme_snapshot_failed", "无法保存当前主题，未应用新主题。")
				}
			}
			if err := runtime.Store.SetSetting("theme", to); err != nil {
				if !rollback {
					_ = runtime.Store.SetSetting(controlPreviousThemeSettingKey, oldRollback)
				}
				return 0, nil, newControlMutationError(http.StatusInternalServerError, "theme_apply_failed", "无法应用外观主题。")
			}
			if rollback {
				_ = runtime.Store.SetSetting(controlPreviousThemeSettingKey, "")
			}
			runtime.server.clearGeneratedCaches()
			_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_themes_apply", "theme", 0,
				"已通过统一控制层将站点主题从 "+from+" 调整为 "+to)
			response["rolled_back"] = rollback
			return http.StatusOK, response, nil
		})
	default:
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 或 PUT。")
	}
}

type controlDomainsInput struct {
	PrimaryDomain   string   `json:"primary_domain"`
	RedirectDomains []string `json:"redirect_domains"`
}

type controlDomainRef struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
}

type controlNormalizedDomains struct {
	PrimaryDomain   *controlDomainRef  `json:"primary_domain"`
	RedirectDomains []controlDomainRef `json:"redirect_domains"`
}

func controlExternalDomainRequirements() map[string]any {
	return map[string]any{
		"owner":             "pilot",
		"performed_by_gcms": false,
		"steps": []string{
			"将 DNS 记录解析到运行 GCMS 的服务器",
			"配置 Caddy 或其他反向代理",
			"签发并验证 HTTPS 证书",
		},
		"note": "GCMS 只保存站点域名与跳转关系，不会修改 Cloudflare、DNS、Caddy 或 HTTPS。",
	}
}

func (s *Server) controlDomainsForSite(siteID int64) ([]*platform.SiteDomain, error) {
	all, err := s.platform.SiteDomains()
	if err != nil {
		return nil, err
	}
	out := make([]*platform.SiteDomain, 0)
	for _, domain := range all {
		if domain != nil && domain.SiteID == siteID && domain.Enabled {
			out = append(out, domain)
		}
	}
	return out, nil
}

func controlDomainResponse(siteID int64, domains []*platform.SiteDomain) map[string]any {
	primary := (*controlDomainRef)(nil)
	redirects := make([]controlDomainRef, 0)
	for _, domain := range domains {
		if domain == nil {
			continue
		}
		ref := controlDomainRef{Scheme: domain.Scheme, Host: domain.Host}
		if domain.IsPrimary {
			primary = &ref
		} else if domain.RedirectToPrimary {
			redirects = append(redirects, ref)
		}
	}
	sort.Slice(redirects, func(i, j int) bool { return redirects[i].Host < redirects[j].Host })
	return map[string]any{
		"site_id":          siteID,
		"primary_domain":   primary,
		"redirect_domains": redirects,
	}
}

func (s *Server) normalizeControlDomains(site *platform.Site, in controlDomainsInput) (controlNormalizedDomains, []platform.SiteDomainSpec, *controlMutationError) {
	normalized := controlNormalizedDomains{RedirectDomains: []controlDomainRef{}}
	specs := make([]platform.SiteDomainSpec, 0, 1+len(in.RedirectDomains))
	seen := make(map[string]bool, 1+len(in.RedirectDomains))
	primaryRaw := strings.TrimSpace(in.PrimaryDomain)
	if primaryRaw == "" && len(in.RedirectDomains) > 0 {
		return normalized, nil, newControlMutationError(http.StatusBadRequest, "primary_domain_required", "请先设置主域名，再添加跳转域名。")
	}
	if primaryRaw != "" {
		scheme, host, err := parseSiteDomainInput(primaryRaw)
		if err != nil {
			return normalized, nil, newControlMutationError(http.StatusBadRequest, "invalid_primary_domain", err.Error())
		}
		if s.domainConflictsWithPlatformHost(site, host) {
			return normalized, nil, newControlMutationError(http.StatusConflict, "platform_host_conflict", "该域名是平台默认 Host，不能绑定给这个站点。")
		}
		seen[host] = true
		normalized.PrimaryDomain = &controlDomainRef{Scheme: scheme, Host: host}
		specs = append(specs, platform.SiteDomainSpec{Scheme: scheme, Host: host, Primary: true})
	}
	for _, raw := range in.RedirectDomains {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return normalized, nil, newControlMutationError(http.StatusBadRequest, "invalid_redirect_domain", "跳转域名不能为空。")
		}
		scheme, host, err := parseSiteDomainInput(raw)
		if err != nil {
			return normalized, nil, newControlMutationError(http.StatusBadRequest, "invalid_redirect_domain", err.Error())
		}
		if s.domainConflictsWithPlatformHost(site, host) {
			return normalized, nil, newControlMutationError(http.StatusConflict, "platform_host_conflict", "跳转域名是平台默认 Host，不能绑定给这个站点。")
		}
		if seen[host] {
			return normalized, nil, newControlMutationError(http.StatusConflict, "duplicate_domain", "主域名和跳转域名不能重复："+host)
		}
		seen[host] = true
		normalized.RedirectDomains = append(normalized.RedirectDomains, controlDomainRef{Scheme: scheme, Host: host})
	}
	sort.Slice(normalized.RedirectDomains, func(i, j int) bool { return normalized.RedirectDomains[i].Host < normalized.RedirectDomains[j].Host })
	if len(normalized.RedirectDomains) > 0 {
		specs = specs[:0]
		if normalized.PrimaryDomain != nil {
			specs = append(specs, platform.SiteDomainSpec{Scheme: normalized.PrimaryDomain.Scheme, Host: normalized.PrimaryDomain.Host, Primary: true})
		}
		for _, domain := range normalized.RedirectDomains {
			specs = append(specs, platform.SiteDomainSpec{Scheme: domain.Scheme, Host: domain.Host, Redirect: true})
		}
	}

	all, err := s.platform.SiteDomains()
	if err != nil {
		return normalized, nil, newControlMutationError(http.StatusInternalServerError, "domain_lookup_failed", "无法检查现有域名绑定。")
	}
	for _, domain := range all {
		if domain == nil || domain.SiteID == site.ID {
			continue
		}
		host := normalizeRuntimeHost(domain.Host)
		if seen[host] {
			err := newControlMutationError(http.StatusConflict, "domain_conflict", "域名已被其他站点使用："+host)
			err.Details = map[string]any{"host": host}
			return normalized, nil, err
		}
	}
	return normalized, specs, nil
}

func controlDomainSpecs(domains []*platform.SiteDomain) []platform.SiteDomainSpec {
	specs := make([]platform.SiteDomainSpec, 0, len(domains))
	for _, domain := range domains {
		if domain == nil {
			continue
		}
		specs = append(specs, platform.SiteDomainSpec{
			Scheme: domain.Scheme, Host: domain.Host, Primary: domain.IsPrimary, Redirect: domain.RedirectToPrimary,
		})
	}
	return specs
}

func controlDomainsFingerprint(siteID int64, normalized controlNormalizedDomains) string {
	primary := ""
	if normalized.PrimaryDomain != nil {
		primary = normalized.PrimaryDomain.Scheme + "://" + normalized.PrimaryDomain.Host
	}
	redirects := make([]string, 0, len(normalized.RedirectDomains))
	for _, domain := range normalized.RedirectDomains {
		redirects = append(redirects, domain.Scheme+"://"+domain.Host)
	}
	return fmt.Sprintf("site=%d|primary=%s|redirects=%s", siteID, primary, strings.Join(redirects, ","))
}

func (s *Server) servePlatformControlSiteDomains(w http.ResponseWriter, r *http.Request, siteID int64) {
	switch r.Method {
	case http.MethodGet:
		_, site, _, ok := s.controlConfigurationSite(w, r, apiScopeDomainsRead, siteID)
		if !ok {
			return
		}
		domains, err := s.controlDomainsForSite(site.ID)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "domain_lookup_failed", "无法读取域名配置。")
			return
		}
		writeJSON(w, http.StatusOK, controlDomainResponse(site.ID, domains))
	case http.MethodPut:
		key, site, _, ok := s.controlConfigurationSite(w, r, apiScopeDomainsWrite, siteID)
		if !ok {
			return
		}
		var in controlDomainsInput
		if !decodeAPIJSON(w, r, &in) {
			return
		}
		normalized, specs, validationErr := s.normalizeControlDomains(site, in)
		if validationErr != nil {
			writeControlMutationError(w, validationErr)
			return
		}
		fingerprint := controlDomainsFingerprint(site.ID, normalized)
		s.executeControlMutation(w, r, key, "domains.apply", fingerprint, func() (int, any, error) {
			impact := []string{
				"替换该站点在 GCMS 中保存的主域名和跳转域名",
				"重新加载 GCMS 多站点 Host 路由",
			}
			response := map[string]any{
				"ok":                    true,
				"dry_run":               controlDryRun(r),
				"site_id":               site.ID,
				"site_status":           site.Status,
				"normalized_input":      normalized,
				"impact":                impact,
				"external_requirements": controlExternalDomainRequirements(),
			}
			if controlDryRun(r) {
				return http.StatusOK, response, nil
			}
			oldDomains, err := s.controlDomainsForSite(site.ID)
			if err != nil {
				return 0, nil, newControlMutationError(http.StatusInternalServerError, "domain_snapshot_failed", "无法保存当前域名状态，未修改配置。")
			}
			if err := s.platform.ReplaceSiteDomains(site.ID, specs); err != nil {
				return 0, nil, newControlMutationError(http.StatusConflict, "domain_apply_failed", "无法保存域名配置，域名可能已被其他站点使用。")
			}
			if err := s.reloadRuntimePool(); err != nil {
				_ = s.platform.ReplaceSiteDomains(site.ID, controlDomainSpecs(oldDomains))
				_ = s.reloadRuntimePool()
				return 0, nil, newControlMutationError(http.StatusInternalServerError, "runtime_reload_failed", "域名配置未生效，已尝试恢复原配置。")
			}
			_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_domains_apply", "domain", 0,
				"已通过统一控制层更新站点内部域名配置")
			response["domains"] = controlDomainResponse(site.ID, func() []*platform.SiteDomain {
				current, _ := s.controlDomainsForSite(site.ID)
				return current
			}())
			return http.StatusOK, response, nil
		})
	default:
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 或 PUT。")
	}
}

func controlPasswordStatus(s *Server) map[string]any {
	isDefault := s != nil && s.adminPasswordIsDefault()
	return map[string]any{
		"password_status":                   map[bool]string{true: "default", false: "changed"}[isDefault],
		"default":                           isDefault,
		"changed":                           !isDefault,
		"initial_password_change_available": isDefault,
		"initial_password_change_transport": "gcms_cli",
		"password_write_api_available":      false,
	}
}

func (s *Server) servePlatformControlSecurity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		apiError(w, http.StatusMethodNotAllowed, "password_write_not_available", "控制 API 不提供密码写入；初始密码只能由 Pilot 通过服务器上的 GCMS 专用 CLI 设置。")
		return
	}
	if _, ok := s.requirePlatformControlKey(w, r, apiScopeControlRead); !ok {
		return
	}
	writeJSON(w, http.StatusOK, controlPasswordStatus(s))
}
