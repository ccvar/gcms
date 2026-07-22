package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

const controlCloudflareAuthorizationsKey = "cloudflare.authorizations_json"

type controlCloudflareAuthorizationZone struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccountID   string `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`
}

// controlCloudflareAuthorization is persisted only in the GCMS platform store.
// Token is deliberately omitted from every response sent to Pilot.
type controlCloudflareAuthorization struct {
	ID          string                               `json:"id"`
	Label       string                               `json:"label"`
	Token       string                               `json:"token"`
	AccountID   string                               `json:"account_id,omitempty"`
	AccountName string                               `json:"account_name,omitempty"`
	Zones       []controlCloudflareAuthorizationZone `json:"zones,omitempty"`
	Source      string                               `json:"source,omitempty"`
	CreatedAt   string                               `json:"created_at,omitempty"`
	UpdatedAt   string                               `json:"updated_at,omitempty"`
}

type controlCloudflareInput struct {
	APIToken string `json:"api_token"`
	Label    string `json:"label"`
	RemoveID string `json:"remove_id"`
	ClearID  string `json:"clear_id"`
}

type controlSiteDeploymentInput struct {
	Action          string   `json:"action"`
	PrimaryDomain   string   `json:"primary_domain"`
	AliasDomains    []string `json:"alias_domains"`
	RedirectAliases *bool    `json:"redirect_aliases"`
	AuthorizationID string   `json:"authorization_id"`
	AutoSync        *bool    `json:"auto_sync"`
}

func controlCloudflareAuthorizationID(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return "cf_" + hex.EncodeToString(sum[:])[:12]
}

func normalizeControlCloudflareAuthorizations(items []controlCloudflareAuthorization) []controlCloudflareAuthorization {
	seen := map[string]bool{}
	out := make([]controlCloudflareAuthorization, 0, len(items))
	for _, item := range items {
		item.Token = strings.TrimSpace(item.Token)
		if item.Token == "" {
			continue
		}
		item.ID = controlCloudflareAuthorizationID(item.Token)
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		item.Label = strings.TrimSpace(item.Label)
		item.AccountID = strings.TrimSpace(item.AccountID)
		item.AccountName = strings.TrimSpace(item.AccountName)
		item.Source = strings.TrimSpace(item.Source)
		if item.Source == "" {
			item.Source = "gcms"
		}
		zoneSeen := map[string]bool{}
		zones := make([]controlCloudflareAuthorizationZone, 0, len(item.Zones))
		for _, zone := range item.Zones {
			zone.ID = strings.TrimSpace(zone.ID)
			zone.Name = strings.ToLower(strings.TrimSpace(zone.Name))
			if zone.ID == "" || zone.Name == "" || zoneSeen[zone.ID] {
				continue
			}
			zoneSeen[zone.ID] = true
			zones = append(zones, zone)
		}
		sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
		item.Zones = zones
		if item.Label == "" {
			item.Label = item.AccountName
		}
		if item.Label == "" && len(item.Zones) > 0 {
			item.Label = item.Zones[0].Name
		}
		if item.Label == "" {
			item.Label = "Cloudflare 授权"
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func (s *Server) readControlCloudflareAuthorizations() []controlCloudflareAuthorization {
	if s == nil || s.platform == nil {
		return nil
	}
	items := []controlCloudflareAuthorization{}
	raw := strings.TrimSpace(s.platform.Setting(controlCloudflareAuthorizationsKey))
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &items)
	}
	// Compatibility: installations before control-v1 stored one token for DNS and
	// another for deployment. Expose them as normal authorizations without moving
	// or deleting the legacy settings.
	legacy := []struct {
		token string
		label string
	}{
		{s.platform.Setting(platformCFDNSTokenKey), "Cloudflare DNS 授权"},
		{s.platform.Setting(cloudflareAPITokenKey), "Cloudflare 部署授权"},
	}
	for _, candidate := range legacy {
		token := strings.TrimSpace(candidate.token)
		if token == "" {
			continue
		}
		zoneID := strings.TrimSpace(s.platform.Setting(cloudflareZoneIDKey))
		zoneName := strings.TrimSpace(s.platform.Setting(cloudflareZoneNameKey))
		zones := []controlCloudflareAuthorizationZone{}
		if zoneID != "" && zoneName != "" {
			zones = append(zones, controlCloudflareAuthorizationZone{
				ID: zoneID, Name: zoneName,
				AccountID:   strings.TrimSpace(s.platform.Setting(cloudflareAccountIDKey)),
				AccountName: strings.TrimSpace(s.platform.Setting(cloudflareAccountNameKey)),
			})
		}
		items = append(items, controlCloudflareAuthorization{
			Label: candidate.label, Token: token, Source: "legacy",
			AccountID:   strings.TrimSpace(s.platform.Setting(cloudflareAccountIDKey)),
			AccountName: strings.TrimSpace(s.platform.Setting(cloudflareAccountNameKey)),
			Zones:       zones,
		})
	}
	return normalizeControlCloudflareAuthorizations(items)
}

func (s *Server) writeControlCloudflareAuthorizations(items []controlCloudflareAuthorization) error {
	items = normalizeControlCloudflareAuthorizations(items)
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return s.platform.SetSetting(controlCloudflareAuthorizationsKey, string(raw))
}

func controlCloudflareAuthorizationSafeItem(item controlCloudflareAuthorization) map[string]any {
	zones := make([]map[string]any, 0, len(item.Zones))
	for _, zone := range item.Zones {
		zones = append(zones, map[string]any{
			"id": zone.ID, "name": zone.Name,
			"account_id": zone.AccountID, "account_name": zone.AccountName,
		})
	}
	return map[string]any{
		"id": item.ID, "label": item.Label, "source": item.Source,
		"configured": true, "account_id": item.AccountID, "account": item.AccountName,
		"zone_count": len(zones), "zones": zones,
		"created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
	}
}

func (s *Server) applyControlCloudflare(ctx context.Context, input controlCloudflareInput) (string, string) {
	if s == nil || s.platform == nil {
		return "platform_api_disabled", "未启用平台模式。"
	}
	items := s.readControlCloudflareAuthorizations()
	removeID := strings.TrimSpace(input.RemoveID)
	if removeID == "" {
		removeID = strings.TrimSpace(input.ClearID)
	}
	if removeID != "" {
		filtered := items[:0]
		found := false
		for _, item := range items {
			if item.ID == removeID {
				found = true
				continue
			}
			filtered = append(filtered, item)
		}
		if !found {
			return "cloudflare_authorization_not_found", "要移除的 Cloudflare 授权不存在。"
		}
		items = append([]controlCloudflareAuthorization(nil), filtered...)
		if err := s.writeControlCloudflareAuthorizations(items); err != nil {
			return "store_error", "移除 Cloudflare 授权失败。"
		}
		return s.syncLegacyCloudflareAuthorization(items)
	}

	token := strings.TrimSpace(input.APIToken)
	if token == "" {
		return "cloudflare_token_required", "请输入 Cloudflare API Token。"
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := verifyCloudflareAPIToken(verifyCtx, token); err != nil {
		return "cloudflare_token_invalid", err.Error()
	}
	zones, err := listCloudflareZones(verifyCtx, token)
	if err != nil {
		return "cloudflare_zone_permission_required", "Token 已验证，但无法读取 Zone；请授予 Zone Read、DNS Edit 和 Workers/Pages Edit 权限。"
	}
	accounts, accountErr := listCloudflareAccounts(verifyCtx, token)

	now := time.Now().UTC().Format(time.RFC3339)
	auth := controlCloudflareAuthorization{
		ID: controlCloudflareAuthorizationID(token), Label: strings.TrimSpace(input.Label), Token: token,
		Source: "pilot", CreatedAt: now, UpdatedAt: now,
	}
	accountByID := map[string]cloudflareAccount{}
	for _, account := range accounts {
		accountByID[account.ID] = account
	}
	for _, zone := range zones {
		accountName := zone.Account.Name
		if accountName == "" {
			accountName = accountByID[zone.Account.ID].Name
		}
		auth.Zones = append(auth.Zones, controlCloudflareAuthorizationZone{
			ID: zone.ID, Name: zone.Name, AccountID: zone.Account.ID, AccountName: accountName,
		})
	}
	if len(accounts) == 1 {
		auth.AccountID, auth.AccountName = accounts[0].ID, accounts[0].Name
	} else if len(zones) > 0 {
		auth.AccountID, auth.AccountName = zones[0].Account.ID, zones[0].Account.Name
	}
	if accountErr != nil && len(zones) == 0 {
		return "cloudflare_account_permission_required", "Token 已验证，但无法读取账号或 Zone，请检查资源范围。"
	}

	replaced := false
	for i := range items {
		if items[i].ID != auth.ID {
			continue
		}
		auth.CreatedAt = items[i].CreatedAt
		items[i] = auth
		replaced = true
		break
	}
	if !replaced {
		items = append(items, auth)
	}
	if err := s.writeControlCloudflareAuthorizations(items); err != nil {
		return "store_error", "保存 Cloudflare 授权失败。"
	}
	return s.syncLegacyCloudflareAuthorization(items)
}

func (s *Server) syncLegacyCloudflareAuthorization(items []controlCloudflareAuthorization) (string, string) {
	token, accountID, accountName, zoneID, zoneName := "", "", "", "", ""
	if len(items) > 0 {
		item := items[len(items)-1]
		token, accountID, accountName = item.Token, item.AccountID, item.AccountName
		if len(item.Zones) == 1 {
			zoneID, zoneName = item.Zones[0].ID, item.Zones[0].Name
		}
	}
	settings := map[string]string{
		platformCFDNSTokenKey: token, cloudflareAPITokenKey: token,
		cloudflareAccountIDKey: accountID, cloudflareAccountNameKey: accountName,
		cloudflareZoneIDKey: zoneID, cloudflareZoneNameKey: zoneName,
	}
	for key, value := range settings {
		if err := s.platform.SetSetting(key, value); err != nil {
			return "store_error", "同步 Cloudflare 兼容配置失败。"
		}
	}
	return "", ""
}

func (s *Server) selectControlCloudflareAuthorization(ctx context.Context, authorizationID, host string) (controlCloudflareAuthorization, cloudflareDetectedTarget, error) {
	items := s.readControlCloudflareAuthorizations()
	host = normalizeCloudflareDomainHost(host)
	for _, item := range items {
		if strings.TrimSpace(authorizationID) != "" && item.ID != strings.TrimSpace(authorizationID) {
			continue
		}
		zones := make([]cloudflareZone, 0, len(item.Zones))
		for _, zone := range item.Zones {
			zones = append(zones, cloudflareZone{ID: zone.ID, Name: zone.Name, Account: cloudflareAccount{ID: zone.AccountID, Name: zone.AccountName}})
		}
		if matched := matchCloudflareZone(host, zones); matched.ID != "" {
			return item, cloudflareDetectedTarget{AccountID: matched.Account.ID, AccountName: matched.Account.Name, ZoneID: matched.ID, ZoneName: matched.Name}, nil
		}
		if host == "" && len(zones) == 1 {
			zone := zones[0]
			return item, cloudflareDetectedTarget{AccountID: zone.Account.ID, AccountName: zone.Account.Name, ZoneID: zone.ID, ZoneName: zone.Name}, nil
		}
	}
	for i, item := range items {
		if strings.TrimSpace(authorizationID) != "" && item.ID != strings.TrimSpace(authorizationID) {
			continue
		}
		target, err := discoverCloudflareTarget(ctx, item.Token, normalizeCloudflareRoutePattern(host))
		if err != nil || target.ZoneID == "" {
			continue
		}
		items[i].AccountID, items[i].AccountName = target.AccountID, target.AccountName
		items[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		zoneFound := false
		for _, zone := range items[i].Zones {
			if zone.ID == target.ZoneID {
				zoneFound = true
				break
			}
		}
		if !zoneFound {
			items[i].Zones = append(items[i].Zones, controlCloudflareAuthorizationZone{
				ID: target.ZoneID, Name: target.ZoneName, AccountID: target.AccountID, AccountName: target.AccountName,
			})
		}
		_ = s.writeControlCloudflareAuthorizations(items)
		return items[i], target, nil
	}
	if strings.TrimSpace(authorizationID) != "" {
		return controlCloudflareAuthorization{}, cloudflareDetectedTarget{}, fmt.Errorf("选择的 Cloudflare 授权无法管理该域名")
	}
	return controlCloudflareAuthorization{}, cloudflareDetectedTarget{}, fmt.Errorf("没有找到可管理 %s 的 Cloudflare 授权", host)
}

type controlCloudflareSiteHandle struct {
	key     *platform.PlatformAutomationKey
	site    *platform.Site
	runtime *SiteRuntime
	server  *Server
	close   func()
}

func (s *Server) controlCloudflareSite(w http.ResponseWriter, r *http.Request, scope string, siteID int64) (*controlCloudflareSiteHandle, bool) {
	key, ok := s.requirePlatformControlKey(w, r, scope)
	if !ok {
		return nil, false
	}
	if !key.CanManageSite(siteID) {
		apiError(w, http.StatusForbidden, "site_forbidden", "当前平台密钥不能管理这个站点。")
		return nil, false
	}
	site, found, err := s.platform.GetSite(siteID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "site_lookup_failed", "无法读取站点。")
		return nil, false
	}
	if !found || site == nil {
		apiError(w, http.StatusNotFound, "site_not_found", "站点不存在。")
		return nil, false
	}
	if pool := s.platformRuntimePool(); pool != nil {
		if rt, found := pool.runtimeByID(siteID); found && rt != nil && rt.Store != nil {
			server := rt.server
			if server == nil {
				server = &Server{store: rt.Store, platform: s.platform, platformSiteID: site.ID, uploadDir: rt.UploadDir, baseURL: rt.BaseURL, cloudflareStatusFile: cloudflareStatusPathForRuntime(rt), rootServer: s}
			}
			return &controlCloudflareSiteHandle{key: key, site: site, runtime: rt, server: server, close: func() {}}, true
		}
	}
	if strings.TrimSpace(site.DBPath) == "" {
		apiError(w, http.StatusServiceUnavailable, "site_store_unavailable", "站点数据库尚未就绪。")
		return nil, false
	}
	opened, err := store.Open(site.DBPath)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "site_store_unavailable", "无法打开站点数据库。")
		return nil, false
	}
	rt := &SiteRuntime{Site: site, Store: opened, UploadDir: site.UploadDir}
	server := &Server{store: opened, platform: s.platform, platformSiteID: site.ID, uploadDir: site.UploadDir, cloudflareStatusFile: cloudflareStatusPathForRuntime(rt), rootServer: s}
	return &controlCloudflareSiteHandle{key: key, site: site, runtime: rt, server: server, close: func() { _ = opened.Close() }}, true
}

func controlCloudflareDomainPayload(domains []CloudflareDomain) []map[string]any {
	out := make([]map[string]any, 0, len(domains))
	for _, domain := range domains {
		out = append(out, map[string]any{"host": domain.Host, "primary": domain.Primary, "redirect_to_primary": domain.RedirectToPrimary})
	}
	return out
}

func controlPlatformDomainPayload(domains []*platform.SiteDomain) []map[string]any {
	out := make([]map[string]any, 0, len(domains))
	for _, domain := range domains {
		if domain == nil || !domain.Enabled {
			continue
		}
		out = append(out, map[string]any{
			"host": domain.Host, "scheme": domain.Scheme, "primary": domain.IsPrimary,
			"redirect_to_primary": domain.RedirectToPrimary,
			"created_at":          domain.CreatedAt.UTC().Format(time.RFC3339),
			"updated_at":          domain.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

func controlPrimaryPlatformDomain(domains []*platform.SiteDomain) string {
	for _, domain := range domains {
		if domain != nil && domain.Enabled && domain.IsPrimary {
			return strings.TrimSpace(domain.Host)
		}
	}
	for _, domain := range domains {
		if domain != nil && domain.Enabled {
			return strings.TrimSpace(domain.Host)
		}
	}
	return ""
}

func controlDeployChipPayload(chip *DeployChip) map[string]any {
	payload := map[string]any{
		"pending": true,
		"text":    "",
		"title":   "",
		"parts":   []map[string]string{},
	}
	if chip == nil {
		return payload
	}
	parts := make([]map[string]string, 0, len(chip.Parts))
	for _, part := range chip.Parts {
		parts = append(parts, map[string]string{"label": part.Label, "value": part.Value})
	}
	payload["pending"] = chip.Pending
	payload["text"] = chip.Text
	payload["title"] = chip.Title
	payload["parts"] = parts
	return payload
}

func (s *Server) controlSiteDeploymentResponse(handle *controlCloudflareSiteHandle) map[string]any {
	if handle == nil || handle.site == nil || handle.server == nil {
		return map[string]any{}
	}
	view := handle.server.cloudflareView()
	cfg, status := view.Config, view.Status
	platformDomains, _ := s.controlDomainsForSite(handle.site.ID)
	provider := "pending"
	if cfg.primaryHost() != "" || cloudflareStatusPublished(status) {
		provider = "cloudflare"
	} else if len(platformDomains) > 0 {
		provider = "server"
	}
	serviceStatus := "stopped"
	if handle.site.Status != "enabled" {
		serviceStatus = "disabled"
	} else if handle.runtime != nil && handle.runtime.Store != nil {
		serviceStatus = "running"
	}
	deploymentStatus := "pending"
	if status != nil && status.Running {
		deploymentStatus = "deploying"
	} else if status != nil && cloudflareStatusPublished(status) {
		deploymentStatus = "published"
	} else if cfg.configured() {
		deploymentStatus = "configured"
	}
	primaryDomain := strings.TrimSpace(cfg.primaryHost())
	domainPayload := controlCloudflareDomainPayload(cfg.Domains)
	if primaryDomain == "" {
		primaryDomain = controlPrimaryPlatformDomain(platformDomains)
		domainPayload = controlPlatformDomainPayload(platformDomains)
	}
	publicURL := ""
	if primaryDomain != "" {
		publicURL = "https://" + primaryDomain
	} else if href, _ := s.platformOfficialSiteURL(handle.site.ID); href != "" {
		publicURL = strings.TrimRight(href, "/")
	}
	contentAt, hasContent := time.Time{}, false
	if handle.runtime != nil && handle.runtime.Store != nil {
		contentAt, hasContent, _ = handle.runtime.Store.LastPublicUpdate()
	}
	chip := buildDeployChip(&i18n.AdminTr{}, deployChipInput{
		Site:        handle.site,
		Domains:     platformDomains,
		CFPublished: cloudflareStatusPublished(status),
		CFStatus:    status,
		ContentAt:   contentAt,
		HasContent:  hasContent,
	}, time.Now())
	statusPayload := map[string]any{
		"state": deploymentStatus, "service": serviceStatus,
		"running":    status != nil && status.Running,
		"published":  status != nil && cloudflareStatusPublished(status),
		"configured": cfg.configured(), "message": "", "step": "",
		"updated_at": "", "last_publish_at": "", "last_purge_at": "",
	}
	if status != nil {
		statusPayload["message"], statusPayload["step"] = status.Message, status.Step
		statusPayload["updated_at"], statusPayload["last_publish_at"], statusPayload["last_purge_at"] = status.UpdatedAt, status.LastDeployAt, status.LastPurgeAt
	}
	return map[string]any{
		"site_id": handle.site.ID, "provider": provider, "public_url": publicURL,
		"primary_domain": primaryDomain, "domains": domainPayload,
		"authorization_id": controlCloudflareAuthorizationID(cfg.APIToken),
		"account_id":       cfg.AccountID, "account": cfg.AccountName, "zone_id": cfg.ZoneID, "zone": cfg.ZoneName,
		"deploy_mode": cfg.DeployMode, "project": map[bool]string{true: cfg.PagesProjectName, false: cfg.WorkerName}[cfg.usingPages()],
		"auto_sync": cfg.AutoSync, "sync_mode": cfg.SyncMode, "status": statusPayload,
		"summary": controlDeployChipPayload(chip),
		"runtime": map[string]any{
			"db_path": handle.site.DBPath, "upload_dir": handle.site.UploadDir,
			"base_url": func() string {
				if handle.runtime == nil {
					return ""
				}
				return handle.runtime.BaseURL
			}(),
			"site_updated_at": handle.site.UpdatedAt.UTC().Format(time.RFC3339),
		},
	}
}

func (s *Server) servePlatformControlSiteDeployment(w http.ResponseWriter, r *http.Request, siteID int64) {
	switch r.Method {
	case http.MethodGet:
		handle, ok := s.controlCloudflareSite(w, r, apiScopeDomainsRead, siteID)
		if !ok {
			return
		}
		defer handle.close()
		writeJSON(w, http.StatusOK, s.controlSiteDeploymentResponse(handle))
	case http.MethodPut:
		handle, ok := s.controlCloudflareSite(w, r, apiScopeDomainsWrite, siteID)
		if !ok {
			return
		}
		defer handle.close()
		var input controlSiteDeploymentInput
		if !decodeAPIJSON(w, r, &input) {
			return
		}
		action := strings.ToLower(strings.TrimSpace(input.Action))
		if action == "" {
			action = "save"
		}
		switch action {
		case "save", "deploy", "unpublish", "purge", "clear":
		default:
			apiError(w, http.StatusBadRequest, "invalid_deployment_action", "action 只能是 save、deploy、unpublish、purge 或 clear。")
			return
		}
		fingerprint := fmt.Sprintf("site=%d|action=%s|domain=%s|aliases=%s|auth=%s", siteID, action, normalizeCloudflareDomainHost(input.PrimaryDomain), strings.Join(input.AliasDomains, ","), strings.TrimSpace(input.AuthorizationID))
		s.executeControlMutation(w, r, handle.key, "deployment.apply", fingerprint, func() (int, any, error) {
			cfg := handle.server.cloudflareConfig()
			if action == "clear" {
				if cloudflareStatusPublished(handle.server.readCloudflareStatus()) {
					return 0, nil, newControlMutationError(http.StatusConflict, "deployment_unpublish_required", "请先取消 Cloudflare 发布，再清空域名绑定。")
				}
				if !controlDryRun(r) {
					if err := handle.server.clearCloudflareBinding(); err != nil {
						return 0, nil, newControlMutationError(http.StatusInternalServerError, "deployment_clear_failed", "清空 Cloudflare 绑定失败。")
					}
					_ = os.Remove(handle.server.cloudflareStatusPath())
				}
				return http.StatusOK, map[string]any{"ok": true, "dry_run": controlDryRun(r), "action": action, "site_id": siteID}, nil
			}

			if primary := normalizeCloudflareDomainHost(input.PrimaryDomain); primary != "" {
				redirect := true
				if input.RedirectAliases != nil {
					redirect = *input.RedirectAliases
				}
				cfg.Domains = cloudflareDomainsFromForm(primary, input.AliasDomains, redirect)
				cfg.RoutePattern = normalizeCloudflareRoutePattern(primary)
				cfg.WorkerName = cloudflareDefaultProjectNameForHost(primary)
				cfg.PagesProjectName = cloudflareDefaultProjectNameForHost(primary)
			}
			if input.AutoSync != nil {
				cfg.AutoSync = *input.AutoSync
				if cfg.AutoSync {
					cfg.SyncMode = cloudflareSyncModeRealtime
				} else {
					cfg.SyncMode = cloudflareSyncModeManual
				}
			}
			if cfg.primaryHost() == "" {
				return 0, nil, newControlMutationError(http.StatusBadRequest, "primary_domain_required", "请填写站点主域名。")
			}
			if cfg.APIToken == "" || strings.TrimSpace(input.AuthorizationID) != "" {
				selectCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
				defer cancel()
				auth, target, err := s.selectControlCloudflareAuthorization(selectCtx, input.AuthorizationID, cfg.primaryHost())
				if err != nil {
					return 0, nil, newControlMutationError(http.StatusBadRequest, "cloudflare_authorization_unavailable", err.Error())
				}
				cfg.APIToken, cfg.AccountID, cfg.AccountName = auth.Token, target.AccountID, target.AccountName
				cfg.ZoneID, cfg.ZoneName = target.ZoneID, target.ZoneName
			}
			if cfg.WorkerName == "" {
				cfg.WorkerName = cloudflareDefaultProjectNameForHost(cfg.primaryHost())
			}
			if cfg.PagesProjectName == "" {
				cfg.PagesProjectName = cloudflareDefaultProjectNameForHost(cfg.primaryHost())
			}
			cfg.DeployMode = normalizeCloudflareDeployMode(cfg.DeployMode)
			cfg.SourceMode = cloudflareSourceModeRedirect
			if !controlDryRun(r) && (action == "save" || action == "deploy") {
				if err := saveControlCloudflareConfig(handle.runtime.Store, cfg); err != nil {
					return 0, nil, newControlMutationError(http.StatusInternalServerError, "deployment_save_failed", "保存 Cloudflare 站点配置失败。")
				}
			}
			if !controlDryRun(r) {
				switch action {
				case "deploy":
					if handle.runtime.server == nil {
						return 0, nil, newControlMutationError(http.StatusServiceUnavailable, "site_runtime_unavailable", "站点未运行，暂时不能开始发布。")
					}
					if err := handle.server.queueCloudflareDeploy(cfg); err != nil {
						return 0, nil, newControlMutationError(http.StatusConflict, "deployment_busy", err.Error())
					}
				case "unpublish":
					if handle.runtime.server == nil {
						return 0, nil, newControlMutationError(http.StatusServiceUnavailable, "site_runtime_unavailable", "站点未运行，暂时不能取消发布。")
					}
					if err := handle.server.queueCloudflareUnpublish(cfg); err != nil {
						return 0, nil, newControlMutationError(http.StatusConflict, "deployment_busy", err.Error())
					}
				case "purge":
					purgeCtx, cancel := context.WithTimeout(r.Context(), cloudflareAPITimeout)
					defer cancel()
					if err := handle.server.purgeCloudflareCache(purgeCtx, cfg, "已通过 Pilot 清理 Cloudflare 缓存。"); err != nil {
						return 0, nil, newControlMutationError(http.StatusBadGateway, "cloudflare_purge_failed", err.Error())
					}
				}
			}
			_ = s.platform.CreatePlatformAutomationLog(handle.key.ID, siteID, "control_deployment_"+action, "cloudflare", 0, "已通过 Pilot 更新 Cloudflare 站点部署")
			return http.StatusOK, map[string]any{"ok": true, "dry_run": controlDryRun(r), "action": action, "site_id": siteID}, nil
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 或 PUT。")
	}
}

func saveControlCloudflareConfig(st *store.Store, cfg CloudflareConfig) error {
	if st == nil {
		return fmt.Errorf("站点数据库不可用")
	}
	settings := map[string]string{
		cloudflareAccountIDKey: cfg.AccountID, cloudflareAccountNameKey: cfg.AccountName,
		cloudflareAPITokenKey: cfg.APIToken, cloudflareDeployModeKey: cfg.DeployMode,
		cloudflareWorkerNameKey: cfg.WorkerName, cloudflarePagesProjectKey: cfg.PagesProjectName,
		cloudflareZoneIDKey: cfg.ZoneID, cloudflareZoneNameKey: cfg.ZoneName,
		cloudflareRoutePatternKey: cfg.RoutePattern, cloudflareDomainsKey: encodeCloudflareDomains(cfg.Domains),
		cloudflareOriginURLKey: cfg.OriginURL, cloudflareSourceModeKey: cfg.SourceMode,
		cloudflareAutoSyncKey: map[bool]string{true: "1", false: "0"}[cfg.AutoSync],
		cloudflareSyncModeKey: cfg.SyncMode, cloudflareSyncTimeKey: normalizeCloudflareSyncTime(cfg.SyncTime),
	}
	for key, value := range settings {
		if err := st.SetSetting(key, value); err != nil {
			return err
		}
	}
	return nil
}

// discoverySiteDeployment is deliberately built from the same store and status
// file as the GCMS admin card. Pilot receives structured state instead of trying
// to infer a provider from URLs or labels.
func (s *Server) discoverySiteDeployment(pool *SiteRuntimePool, site *platform.Site, publicURL string) map[string]any {
	if site == nil {
		return map[string]any{"provider": "pending", "status": map[string]any{"state": "pending"}}
	}
	if pool == nil {
		return map[string]any{"provider": "pending", "status": map[string]any{"state": "unavailable", "service": "stopped"}}
	}
	rt, ok := pool.runtimeByID(site.ID)
	if !ok || rt == nil || rt.Store == nil {
		return map[string]any{"provider": "pending", "status": map[string]any{"state": "unavailable", "service": "stopped"}}
	}
	server := rt.server
	if server == nil {
		server = &Server{store: rt.Store, platform: s.platform, platformSiteID: site.ID, uploadDir: rt.UploadDir, baseURL: rt.BaseURL, cloudflareStatusFile: cloudflareStatusPathForRuntime(rt), rootServer: s}
	}
	handle := &controlCloudflareSiteHandle{site: site, runtime: rt, server: server}
	payload := s.controlSiteDeploymentResponse(handle)
	if strings.TrimSpace(publicURL) != "" && payload["public_url"] == "" {
		payload["public_url"] = publicURL
		if payload["provider"] == "pending" {
			payload["provider"] = "server"
		}
	}
	return payload
}
