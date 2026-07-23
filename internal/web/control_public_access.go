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
		"domain":   controlDomainResponse(siteID, domains),
		"provider": "manual",
		"dns":      map[string]any{"status": "not_configured"},
		"caddy":    map[string]any{"available": false, "status": "unknown"},
		"https":    map[string]any{"status": "not_checked"},
	}
	if primary == "" {
		status["cloudflare_proxy"] = s.controlPublicAccessProxyView(siteID, primary, false)
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
	status["provider"] = info.Provider
	status["dns"] = map[string]any{
		"status":       info.PointsToServer,
		"provider":     info.Provider,
		"name_servers": info.NameServers,
		"a_records":    info.ARecords,
		"proxied":      info.Proxied,
	}
	status["cloudflare_proxy"] = s.controlPublicAccessProxyView(siteID, primary, proxyActual)
	proxy := detectReverseProxy()
	status["caddy"] = map[string]any{
		"available":     proxy.Kind == "caddy",
		"kind":          proxy.Kind,
		"running":       proxy.Running,
		"integrated":    proxy.GcmsIntegrated,
		"can_auto_sync": caddyAutoSyncAvailable(),
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	verified := verifyDomainReachable(verifyCtx, primary)
	status["https"] = map[string]any{
		"status": map[bool]string{true: "ok", false: "pending"}[verified.OK],
		"ok":     verified.OK,
		"reason": verified.Reason,
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
			autoDNS := in.AutoDNS == nil || *in.AutoDNS
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
	default:
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 或 POST。")
	}
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
