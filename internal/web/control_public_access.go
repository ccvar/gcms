package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
)

// controlPublicAccessInput is deliberately provider-agnostic. GCMS owns the DNS
// provider credentials and the proxy/runtime configuration; Pilot only supplies
// the desired public host set and whether GCMS should use its stored provider.
type controlPublicAccessInput struct {
	PrimaryDomain   string   `json:"primary_domain"`
	RedirectDomains []string `json:"redirect_domains"`
	AutoDNS         *bool    `json:"auto_dns,omitempty"`
	CloudflareProxy *bool    `json:"cloudflare_proxy,omitempty"`
}

type controlPublicAccessClearInput struct {
	ExpectedPrimaryDomain     string `json:"expected_primary_domain"`
	ExpectedGeneration        string `json:"expected_generation"`
	ExpectedDomainFingerprint string `json:"expected_domain_fingerprint"`
}

func (s *Server) publicAccessStatus(ctx context.Context, siteID int64, site *platform.Site) map[string]any {
	domains, err := s.controlDomainsForSite(siteID)
	if err != nil {
		return map[string]any{"site_id": siteID, "error": "domain_lookup_failed"}
	}
	primary := ""
	for _, domain := range domains {
		if domain != nil && domain.IsPrimary && domain.Enabled {
			primary = domain.Host
			break
		}
	}
	status := map[string]any{
		"site_id": siteID,
		"site_status": func() string {
			if site == nil {
				return ""
			}
			return site.Status
		}(),
		"domain":               controlDomainResponse(siteID, domains),
		"provider":             "manual",
		"dns":                  map[string]any{"status": "not_configured"},
		"caddy":                map[string]any{"available": false, "status": "unknown"},
		"https":                map[string]any{"status": "not_checked"},
		"can_clear_unverified": false,
	}
	if primary == "" {
		status["cloudflare_proxy"] = s.controlPublicAccessProxyView(siteID, primary, false, false)
		return status
	}

	info := s.detectDomainDNS(ctx, primary)
	proxyActual := info.Proxied
	if !proxyActual {
		proxyCtx, proxyCancel := context.WithTimeout(ctx, 6*time.Second)
		proxyActual = s.controlPublicAccessProxyActual(proxyCtx, siteID, primary, false)
		proxyCancel()
		if proxyActual {
			info.Proxied = true
			info.PointsToServer = "via_cloudflare"
		}
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 9*time.Second)
	verified := verifyDomainReachable(verifyCtx, primary)
	cancel()
	status["provider"] = info.Provider
	status["dns"] = map[string]any{
		"status":       info.PointsToServer,
		"provider":     info.Provider,
		"name_servers": info.NameServers,
		"a_records":    info.ARecords,
		"proxied":      info.Proxied,
	}
	status["cloudflare_proxy"] = s.controlPublicAccessProxyView(siteID, primary, proxyActual, verified.OK)
	proxy := detectReverseProxy()
	status["caddy"] = map[string]any{
		"available":     proxy.Kind == "caddy",
		"kind":          proxy.Kind,
		"running":       proxy.Running,
		"integrated":    proxy.GcmsIntegrated,
		"can_auto_sync": caddyAutoSyncAvailable(),
	}
	status["https"] = map[string]any{
		"status": map[bool]string{true: "ok", false: "pending"}[verified.OK],
		"ok":     verified.OK,
		"reason": verified.Reason,
	}
	if state, ok := s.loadControlPublicAccessProxyState(siteID); ok && state.PrimaryDomain == primary {
		stage := controlPublicAccessSummaryStage(state)
		status["generation"] = state.Generation
		status["domain_fingerprint"] = controlPublicAccessDomainFingerprint(siteID, domains)
		status["verified_at"] = state.VerifiedAt
		status["can_clear_unverified"] = !verified.OK &&
			state.VerifiedAt == 0 &&
			state.AccessState == publicAccessProgressAttention &&
			(stage == "dns" || stage == "https")
	}
	return status
}

func (s *Server) servePlatformControlSitePublicAccess(w http.ResponseWriter, r *http.Request, siteID int64) {
	switch r.Method {
	case http.MethodGet:
		_, site, _, ok := s.controlConfigurationSite(w, r, apiScopeDomainsRead, siteID)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, s.publicAccessStatus(ctx, siteID, site))
	case http.MethodPost:
		key, site, _, ok := s.controlConfigurationSite(w, r, apiScopeDomainsWrite, siteID)
		if !ok {
			return
		}
		if site.IsDefault {
			writeControlMutationError(w, newControlMutationError(http.StatusConflict, "default_site_public_access", "默认站点使用 GCMS 平台域名，不能单独配置访问域名。"))
			return
		}
		var in controlPublicAccessInput
		if !decodeAPIJSON(w, r, &in) {
			return
		}
		normalized, specs, validationErr := s.normalizeControlDomains(site, controlDomainsInput{
			PrimaryDomain: in.PrimaryDomain, RedirectDomains: in.RedirectDomains,
		})
		if validationErr != nil {
			writeControlMutationError(w, validationErr)
			return
		}
		if normalized.PrimaryDomain == nil {
			writeControlMutationError(w, newControlMutationError(http.StatusBadRequest, "primary_domain_required", "请先填写要接入的主域名。"))
			return
		}
		fingerprint := controlDomainsFingerprint(site.ID, normalized) + "|public-access"
		s.executeControlMutation(w, r, key, "public_access.apply", fingerprint, func() (int, any, error) {
			response := map[string]any{
				"ok": true, "dry_run": controlDryRun(r), "site_id": site.ID,
				"normalized_input": normalized,
				"owner":            "gcms",
				"external_requirements": map[string]any{
					"owner": "gcms",
					"dns":   "使用 GCMS 自己保存的 DNS/Cloudflare 配置；非 Cloudflare 域名需由用户在域名服务商处解析",
					"caddy": "由 GCMS 直接同步并重载（权限不足时返回明确待办）",
					"https": "由 Caddy 自动签发并通过验证",
				},
			}
			autoDNS := in.AutoDNS == nil || *in.AutoDNS
			if autoDNS {
				if preflightErr := s.preflightCloudflareDNSForSpecs(r.Context(), specs); preflightErr != nil {
					return 0, nil, preflightErr
				}
				response["dns_preflight"] = "ok"
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
			messages := make([]string, 0, 3)
			proxyRequested := false
			if autoDNS {
				if in.CloudflareProxy != nil {
					proxyRequested = *in.CloudflareProxy
				} else {
					proxyRequested = strings.TrimSpace(s.platform.Setting(platformCFProxiedKey)) != "0"
				}
			}
			proxyState := newControlPublicAccessProxyState(primaryHostFromDomainSpecs(specs), proxyRequested)
			proxyStateSaved := s.saveControlPublicAccessProxyState(site.ID, proxyState) == nil
			monitorReady := true
			if autoDNS {
				// Always expose the origin through grey-cloud first. Orange-cloud
				// is a later transition after Caddy has a working certificate.
				greyCloud := false
				msg, dnsErr := s.applyCloudflareDNSForSpecs(r.Context(), specs, &greyCloud)
				if msg != "" {
					messages = append(messages, msg)
				}
				if dnsErr != nil {
					monitorReady = false
					if proxyStateSaved {
						proxyState.AccessState = publicAccessProgressAttention
						proxyState.AccessStage = "dns"
						proxyState.AccessMessage = strings.TrimSpace(msg)
						if proxyState.AccessMessage == "" {
							proxyState.AccessMessage = "Cloudflare DNS 配置失败：" + dnsErr.Error()
						}
						if proxyRequested {
							proxyState.Status = publicAccessProxyFailed
							proxyState.Error = proxyState.AccessMessage
						}
						s.updateControlPublicAccessProxyState(site.ID, proxyState)
					}
				}
			}
			if msg := s.applyCaddySites(); msg != "" {
				messages = append(messages, msg)
			}
			// Public-access progress is independent from the orange-cloud choice.
			// Manual DNS and grey-cloud flows must still move through DNS / HTTPS
			// pending states so Pilot can keep the card actionable after closing.
			if proxyStateSaved && monitorReady {
				s.scheduleControlPublicAccessProxy(site.ID, proxyState.Generation)
			}
			response["messages"] = messages
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			defer cancel()
			response["status"] = s.publicAccessStatus(ctx, site.ID, site)
			_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_public_access_apply", "public-access", 0, "已通过 GCMS 控制层配置公网访问")
			return http.StatusOK, response, nil
		})
	case http.MethodDelete:
		key, site, _, ok := s.controlConfigurationSite(w, r, apiScopeDomainsWrite, siteID)
		if !ok {
			return
		}
		var in controlPublicAccessClearInput
		if !decodeAPIJSON(w, r, &in) {
			return
		}
		_, expectedPrimary, parseErr := parseSiteDomainInput(in.ExpectedPrimaryDomain)
		if parseErr != nil {
			writeControlMutationError(w, newControlMutationError(http.StatusBadRequest, "invalid_expected_domain", "待清除的域名无效，请刷新站点后重试。"))
			return
		}
		in.ExpectedPrimaryDomain = expectedPrimary
		in.ExpectedGeneration = strings.TrimSpace(in.ExpectedGeneration)
		in.ExpectedDomainFingerprint = strings.TrimSpace(in.ExpectedDomainFingerprint)
		if in.ExpectedGeneration == "" || in.ExpectedDomainFingerprint == "" {
			writeControlMutationError(w, newControlMutationError(http.StatusConflict, "public_access_state_stale", "公网访问状态缺少安全校验信息，请刷新站点后重试。"))
			return
		}
		fingerprint := controlMutationFingerprint("public_access.clear_unverified", map[string]any{
			"site_id":                     site.ID,
			"expected_primary_domain":     in.ExpectedPrimaryDomain,
			"expected_generation":         in.ExpectedGeneration,
			"expected_domain_fingerprint": in.ExpectedDomainFingerprint,
		})
		s.executeControlMutation(w, r, key, "public_access.clear_unverified", fingerprint, func() (int, any, error) {
			if site.IsDefault {
				return 0, nil, newControlMutationError(http.StatusConflict, "default_site_domain_locked", "默认站点使用 GCMS 平台域名，不能清除。")
			}
			currentDomains, err := s.controlDomainsForSite(site.ID)
			if err != nil {
				return 0, nil, newControlMutationError(http.StatusInternalServerError, "domain_snapshot_failed", "无法读取当前域名，未执行清除。")
			}
			currentSpecs := controlDomainSpecs(currentDomains)
			currentPrimary := primaryHostFromDomainSpecs(currentSpecs)
			currentFingerprint := controlPublicAccessDomainFingerprint(site.ID, currentDomains)
			state, stateOK := s.loadControlPublicAccessProxyState(site.ID)
			switch {
			case currentPrimary == "":
				return 0, nil, newControlMutationError(http.StatusConflict, "public_access_not_configured", "当前站点没有待清除的访问域名。")
			case currentPrimary != in.ExpectedPrimaryDomain:
				return 0, nil, newControlMutationError(http.StatusConflict, "public_access_domain_changed", "访问域名已变化，请刷新站点后重新操作。")
			case currentFingerprint != in.ExpectedDomainFingerprint:
				return 0, nil, newControlMutationError(http.StatusConflict, "public_access_domains_changed", "主域名或跳转域名已变化，请刷新站点后重新操作。")
			case !stateOK || state.PrimaryDomain != currentPrimary || state.Generation != in.ExpectedGeneration:
				return 0, nil, newControlMutationError(http.StatusConflict, "public_access_attempt_changed", "公网访问任务已变化，请刷新站点后重新操作。")
			}
			stage := controlPublicAccessSummaryStage(state)
			if state.VerifiedAt > 0 || state.AccessState == publicAccessProgressReady || stage == "ready" || stage == "proxy" {
				return 0, nil, newControlMutationError(http.StatusConflict, "public_access_already_live", "该域名已经成功接入，不能从“清除未生效域名”入口移除。")
			}
			if state.AccessState != publicAccessProgressAttention || (stage != "dns" && stage != "https") {
				return 0, nil, newControlMutationError(http.StatusConflict, "public_access_still_pending", "该域名仍在等待生效；请稍后检查，确认失败后再清除。")
			}
			verifyCtx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
			verified := verifyDomainReachable(verifyCtx, currentPrimary)
			cancel()
			if verified.OK {
				return 0, nil, newControlMutationError(http.StatusConflict, "public_access_already_live", "实时检查发现该域名已经由 GCMS 正常提供 HTTPS，已禁止清除。")
			}
			response := map[string]any{
				"ok":             true,
				"dry_run":        controlDryRun(r),
				"site_id":        site.ID,
				"cleared_domain": currentPrimary,
				"redirect_count": len(currentSpecs) - 1,
				"dns_preserved":  true,
			}
			if controlDryRun(r) {
				return http.StatusOK, response, nil
			}

			// First invalidate the old worker generation. A worker already
			// waiting on the shared control-mutation lock will observe the new
			// generation before it can touch Cloudflare again.
			tombstone := state
			tombstone.Generation = fmt.Sprintf("cleared-%d", time.Now().UnixNano())
			tombstone.AccessState = publicAccessProgressAttention
			tombstone.AccessMessage = "正在清除未生效域名。"
			if err := s.saveControlPublicAccessProxyState(site.ID, tombstone); err != nil {
				return 0, nil, newControlMutationError(http.StatusInternalServerError, "public_access_cancel_failed", "无法停止旧的公网访问任务，未执行清除。")
			}
			if err := s.platform.ReplaceSiteDomains(site.ID, nil); err != nil {
				_ = s.saveControlPublicAccessProxyState(site.ID, state)
				return 0, nil, newControlMutationError(http.StatusInternalServerError, "domain_clear_failed", "无法清除 GCMS 域名记录，原配置已保留。")
			}
			if err := s.reloadRuntimePool(); err != nil {
				_ = s.platform.ReplaceSiteDomains(site.ID, currentSpecs)
				_ = s.saveControlPublicAccessProxyState(site.ID, state)
				_ = s.reloadRuntimePool()
				return 0, nil, newControlMutationError(http.StatusInternalServerError, "runtime_reload_failed", "清除未生效，已尝试恢复原域名配置。")
			}
			messages := []string{"已清除 GCMS 站点域名与服务器路由。"}
			if msg := s.applyCaddySites(); msg != "" {
				messages = append(messages, msg)
			}
			if err := s.platform.SetSetting(controlPublicAccessProxySettingKey(site.ID), ""); err != nil {
				messages = append(messages, "旧的公网访问进度未能清空，但已与站点域名隔离。")
			}
			messages = append(messages, "为避免误删用户原有记录，Cloudflare 或其他 DNS 服务商中的记录不会自动删除；如曾创建，请按需移除。")
			response["messages"] = messages
			response["status"] = s.publicAccessStatus(r.Context(), site.ID, site)
			_ = s.platform.CreatePlatformAutomationLog(key.ID, site.ID, "control_public_access_clear_unverified", "public-access", 0, "已清除未验证成功的公网访问域名："+currentPrimary)
			return http.StatusOK, response, nil
		})
	default:
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET、POST 或 DELETE。")
	}
}

func (s *Server) preflightCloudflareDNSForSpecs(ctx context.Context, specs []platform.SiteDomainSpec) *controlMutationError {
	token := strings.TrimSpace(s.platform.Setting(platformCFDNSTokenKey))
	if token == "" {
		return newControlMutationError(http.StatusUnprocessableEntity, "cloudflare_not_authorized", "尚未配置可用的 Cloudflare DNS 授权；请先授权，或关闭自动 DNS 后手动解析。")
	}
	ip := s.serverPublicIP()
	if strings.TrimSpace(ip.IPv4) == "" && strings.TrimSpace(ip.IPv6) == "" {
		return newControlMutationError(http.StatusUnprocessableEntity, "server_public_ip_unavailable", "GCMS 未检测到服务器公网 IP，不能执行 Cloudflare 自动 DNS。")
	}
	zoneCache := map[string]cloudflareZone{}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, spec := range specs {
		host := strings.ToLower(strings.TrimSpace(spec.Host))
		if host == "" {
			continue
		}
		zone, err := findCloudflareZoneForHost(ctx, token, host, zoneCache)
		if err != nil {
			return newControlMutationError(http.StatusUnprocessableEntity, "cloudflare_zone_check_failed", "无法确认 Cloudflare 授权是否覆盖域名 "+host+"，未保存任何域名配置。")
		}
		if zone.ID == "" {
			mutationErr := newControlMutationError(http.StatusUnprocessableEntity, "cloudflare_zone_not_authorized", "当前 Cloudflare 授权不包含域名 "+host+"；未保存任何域名配置。")
			mutationErr.Details = map[string]any{"host": host}
			return mutationErr
		}
	}
	return nil
}

func (s *Server) applyCloudflareDNSForSpecs(ctx context.Context, specs []platform.SiteDomainSpec, proxy *bool) (string, error) {
	token := strings.TrimSpace(s.platform.Setting(platformCFDNSTokenKey))
	if token == "" {
		msg := "GCMS 尚未配置 Cloudflare DNS；请在 GCMS 的 Cloudflare 设置中授权，或手动添加 DNS 记录。"
		return msg, fmt.Errorf("cloudflare DNS token is not configured")
	}
	ip := s.serverPublicIP()
	if strings.TrimSpace(ip.IPv4) == "" && strings.TrimSpace(ip.IPv6) == "" {
		msg := "GCMS 未检测到服务器公网 IP，无法自动创建 DNS 记录。"
		return msg, fmt.Errorf("server public IP is unavailable")
	}
	proxied := strings.TrimSpace(s.platform.Setting(platformCFProxiedKey)) != "0"
	if proxy != nil {
		proxied = *proxy
	}
	hosts := make([]string, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Host) != "" {
			hosts = append(hosts, spec.Host)
		}
	}
	if len(hosts) == 0 {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	results, err := applyCloudflareDNS(ctx, token, ip.IPv4, ip.IPv6, hosts, proxied)
	if err != nil {
		return "读取 GCMS Cloudflare 域名失败：" + err.Error(), err
	}
	var okHosts, failed []string
	for _, result := range results {
		if result.OK {
			okHosts = append(okHosts, result.Host)
		} else {
			failed = append(failed, result.Host+"（"+result.Msg+"）")
		}
	}
	mode := "灰云"
	if proxied {
		mode = "橙云代理"
	}
	parts := make([]string, 0, 2)
	if len(okHosts) > 0 {
		parts = append(parts, "GCMS 已自动配置 "+strings.Join(okHosts, "、")+" 的 DNS（"+mode+"）。")
	}
	if len(failed) > 0 {
		parts = append(parts, "DNS 未处理："+strings.Join(failed, "；")+"。")
	}
	message := strings.Join(parts, " ")
	if len(failed) > 0 {
		return message, fmt.Errorf("cloudflare DNS failed for %d host(s)", len(failed))
	}
	return message, nil
}
