package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
)

const (
	controlPublicAccessProxySettingPrefix = "control.public_access.cloudflare_proxy."

	publicAccessProxyDisabled = "disabled"
	publicAccessProxyPending  = "pending"
	publicAccessProxyEnabling = "enabling"
	publicAccessProxyEnabled  = "enabled"
	publicAccessProxyFailed   = "failed"

	publicAccessProgressPending   = "pending"
	publicAccessProgressAttention = "attention"
	publicAccessProgressReady     = "ready"
)

// controlPublicAccessProxyState keeps the user's desired Cloudflare state
// separate from the DNS record's current state. In particular, Requested=true
// means "turn orange-cloud on after origin HTTPS is ready", not "turn it on
// while the certificate is still being provisioned".
type controlPublicAccessProxyState struct {
	Requested     bool   `json:"requested"`
	Status        string `json:"status"`
	PrimaryDomain string `json:"primary_domain,omitempty"`
	Generation    string `json:"generation,omitempty"`
	ProxyApplied  bool   `json:"proxy_applied,omitempty"`
	Error         string `json:"error,omitempty"`
	AccessState   string `json:"access_state,omitempty"`
	AccessStage   string `json:"access_stage,omitempty"`
	AccessMessage string `json:"access_message,omitempty"`
	UpdatedAt     int64  `json:"updated_at"`
}

type controlPublicAccessProxyView struct {
	Requested bool   `json:"requested"`
	Actual    bool   `json:"actual"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

type discoveryPublicAccessSummary struct {
	State     string `json:"state"` // pending | attention | ready
	Stage     string `json:"stage"` // dns | https | proxy | ready
	Host      string `json:"host,omitempty"`
	Message   string `json:"message,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

func controlPublicAccessProxySettingKey(siteID int64) string {
	return fmt.Sprintf("%s%d", controlPublicAccessProxySettingPrefix, siteID)
}

func newControlPublicAccessProxyState(primary string, requested bool) controlPublicAccessProxyState {
	status := publicAccessProxyDisabled
	if requested {
		status = publicAccessProxyPending
	}
	return controlPublicAccessProxyState{
		Requested:     requested,
		Status:        status,
		PrimaryDomain: strings.ToLower(strings.TrimSpace(primary)),
		Generation:    fmt.Sprintf("%d", time.Now().UnixNano()),
		AccessState:   publicAccessProgressPending,
		AccessStage:   "dns",
		AccessMessage: "正在等待 DNS 生效。",
		UpdatedAt:     time.Now().Unix(),
	}
}

func (s *Server) loadControlPublicAccessProxyState(siteID int64) (controlPublicAccessProxyState, bool) {
	if s == nil || s.platform == nil || siteID <= 0 {
		return controlPublicAccessProxyState{}, false
	}
	raw := strings.TrimSpace(s.platform.Setting(controlPublicAccessProxySettingKey(siteID)))
	if raw == "" {
		return controlPublicAccessProxyState{}, false
	}
	var state controlPublicAccessProxyState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return controlPublicAccessProxyState{}, false
	}
	state.PrimaryDomain = strings.ToLower(strings.TrimSpace(state.PrimaryDomain))
	if state.Status == "" {
		if state.Requested {
			state.Status = publicAccessProxyPending
		} else {
			state.Status = publicAccessProxyDisabled
		}
	}
	return state, true
}

func (s *Server) saveControlPublicAccessProxyState(siteID int64, state controlPublicAccessProxyState) error {
	if s == nil || s.platform == nil || siteID <= 0 {
		return nil
	}
	state.PrimaryDomain = strings.ToLower(strings.TrimSpace(state.PrimaryDomain))
	state.UpdatedAt = time.Now().Unix()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.platform.SetSetting(controlPublicAccessProxySettingKey(siteID), string(raw))
}

// updateControlPublicAccessProxyState only writes if this worker still owns the
// current generation. A later apply can therefore cancel an older background
// attempt without the old goroutine overwriting the new user intent.
func (s *Server) updateControlPublicAccessProxyState(siteID int64, next controlPublicAccessProxyState) bool {
	current, ok := s.loadControlPublicAccessProxyState(siteID)
	if !ok || current.Generation == "" || current.Generation != next.Generation {
		return false
	}
	return s.saveControlPublicAccessProxyState(siteID, next) == nil
}

func (s *Server) controlPublicAccessProxyView(siteID int64, primary string, actual bool) controlPublicAccessProxyView {
	primary = strings.ToLower(strings.TrimSpace(primary))
	state, ok := s.loadControlPublicAccessProxyState(siteID)
	if !ok || (state.PrimaryDomain != "" && primary != "" && state.PrimaryDomain != primary) {
		if actual {
			return controlPublicAccessProxyView{Requested: true, Actual: true, Status: publicAccessProxyEnabled}
		}
		return controlPublicAccessProxyView{Actual: false, Status: publicAccessProxyDisabled}
	}
	status := state.Status
	// A status read also verifies HTTPS through the currently resolved route.
	// If DNS is visibly proxied, the public-access handler can combine this with
	// its HTTPS result and safely treat the proxy transition as complete.
	if state.Requested && actual {
		status = publicAccessProxyEnabled
		needsSave := state.Status != publicAccessProxyEnabled ||
			state.AccessState != publicAccessProgressReady ||
			state.AccessStage != "ready" ||
			strings.TrimSpace(state.AccessMessage) != "" ||
			strings.TrimSpace(state.Error) != ""
		state.Error = ""
		state.AccessState = publicAccessProgressReady
		state.AccessStage = "ready"
		state.AccessMessage = ""
		if needsSave {
			state.Status = publicAccessProxyEnabled
			_ = s.saveControlPublicAccessProxyState(siteID, state)
		}
	} else if state.Requested && !actual && status == publicAccessProxyEnabled {
		status = publicAccessProxyFailed
		if strings.TrimSpace(state.Error) == "" {
			state.Error = "检测到当前 DNS 未启用 Cloudflare 橙云，可单独重试开启。"
		}
		state.Status = publicAccessProxyFailed
		state.AccessState = publicAccessProgressAttention
		state.AccessStage = "proxy"
		state.AccessMessage = state.Error
		_ = s.saveControlPublicAccessProxyState(siteID, state)
	}
	return controlPublicAccessProxyView{
		Requested: state.Requested,
		Actual:    actual,
		Status:    status,
		Error:     strings.TrimSpace(state.Error),
		UpdatedAt: state.UpdatedAt,
	}
}

func controlPublicAccessSummaryStage(state controlPublicAccessProxyState) string {
	if stage := strings.ToLower(strings.TrimSpace(state.AccessStage)); stage != "" {
		return stage
	}
	message := strings.ToLower(strings.TrimSpace(state.Error))
	switch {
	case state.Status == publicAccessProxyEnabled:
		return "ready"
	case state.ProxyApplied || state.Status == publicAccessProxyEnabling ||
		strings.Contains(message, "橙云") || strings.Contains(message, "cloudflare"):
		return "proxy"
	case strings.Contains(message, "https") || strings.Contains(message, "证书"):
		return "https"
	default:
		return "dns"
	}
}

// discoverySitePublicAccess exposes only the persisted, non-secret progress needed by
// Pilot's site card. It deliberately performs no DNS or HTTPS request, so discovery stays
// fast even with many sites; the background worker owns those checks.
func (s *Server) discoverySitePublicAccess(siteID int64, domains []*platform.SiteDomain) *discoveryPublicAccessSummary {
	primary := ""
	for _, domain := range domains {
		if domain != nil && domain.Enabled && domain.IsPrimary {
			primary = strings.ToLower(strings.TrimSpace(domain.Host))
			break
		}
	}
	if primary == "" {
		return nil
	}
	state, ok := s.loadControlPublicAccessProxyState(siteID)
	if !ok || state.PrimaryDomain != primary {
		return nil
	}
	summary := &discoveryPublicAccessSummary{
		Host:      primary,
		Stage:     controlPublicAccessSummaryStage(state),
		Message:   strings.TrimSpace(state.AccessMessage),
		UpdatedAt: state.UpdatedAt,
	}
	if summary.Message == "" {
		summary.Message = strings.TrimSpace(state.Error)
	}
	switch strings.ToLower(strings.TrimSpace(state.AccessState)) {
	case publicAccessProgressPending:
		summary.State = publicAccessProgressPending
		return summary
	case publicAccessProgressAttention:
		summary.State = publicAccessProgressAttention
		return summary
	case publicAccessProgressReady:
		summary.State = publicAccessProgressReady
		summary.Stage = "ready"
		return summary
	}
	// Backward compatibility for states written before access progress was
	// separated from the user's orange-cloud preference.
	switch state.Status {
	case publicAccessProxyPending, publicAccessProxyEnabling:
		summary.State = publicAccessProgressPending
	case publicAccessProxyFailed:
		summary.State = publicAccessProgressAttention
	case publicAccessProxyEnabled:
		summary.State = publicAccessProgressReady
	default:
		return nil
	}
	return summary
}

func cloudflareDNSRecordsProxied(records []cloudflareDNSRecord) (bool, bool) {
	known := false
	for _, record := range records {
		if !cloudflareDNSRouteRecord(record.Type) {
			continue
		}
		known = true
		if !cloudflareRecordProxied(record) {
			return false, true
		}
	}
	return known, known
}

// controlPublicAccessProxyActual prefers Cloudflare's record state while a
// requested proxy transition is still settling. Public DNS can remain cached
// on the origin address for a short time after Cloudflare has already accepted
// the orange-cloud update, which otherwise leaves the UI incorrectly waiting.
func (s *Server) controlPublicAccessProxyActual(ctx context.Context, siteID int64, primary string, dnsActual bool) bool {
	if dnsActual {
		return true
	}
	state, ok := s.loadControlPublicAccessProxyState(siteID)
	if !ok || !state.Requested {
		return false
	}
	token := strings.TrimSpace(s.platform.Setting(platformCFDNSTokenKey))
	primary = strings.ToLower(strings.TrimSpace(primary))
	if token == "" || primary == "" {
		return false
	}
	zone, err := findCloudflareZoneForHost(ctx, token, primary, map[string]cloudflareZone{})
	if err != nil || zone.ID == "" {
		return false
	}
	records, err := listCloudflareDNSRecords(ctx, CloudflareConfig{APIToken: token, ZoneID: zone.ID}, primary)
	if err != nil {
		return false
	}
	proxied, known := cloudflareDNSRecordsProxied(records)
	return known && proxied
}

func primaryHostFromDomainSpecs(specs []platform.SiteDomainSpec) string {
	for _, spec := range specs {
		if spec.Primary {
			return strings.ToLower(strings.TrimSpace(spec.Host))
		}
	}
	for _, spec := range specs {
		if strings.TrimSpace(spec.Host) != "" {
			return strings.ToLower(strings.TrimSpace(spec.Host))
		}
	}
	return ""
}

func (s *Server) scheduleControlPublicAccessProxy(siteID int64, generation string) {
	if s == nil || s.platform == nil || siteID <= 0 || strings.TrimSpace(generation) == "" {
		return
	}
	root := s
	for root.rootServer != nil {
		root = root.rootServer
	}
	go root.runControlPublicAccessProxy(siteID, generation)
}

func (s *Server) runControlPublicAccessProxy(siteID int64, generation string) {
	const maxAttempts = 24
	var lastReason string
	for attempt := 0; attempt < maxAttempts; attempt++ {
		state, ok := s.loadControlPublicAccessProxyState(siteID)
		if !ok || state.Generation != generation {
			return
		}
		domains, err := s.controlDomainsForSite(siteID)
		if err != nil {
			state.AccessState = publicAccessProgressAttention
			state.AccessStage = "dns"
			state.AccessMessage = "无法读取站点域名，请重新检查或修改访问域名。"
			if state.Requested {
				state.Status = publicAccessProxyFailed
				state.Error = "无法读取站点域名，橙云尚未开启。"
			}
			s.updateControlPublicAccessProxyState(siteID, state)
			return
		}
		specs := controlDomainSpecs(domains)
		primary := primaryHostFromDomainSpecs(specs)
		if primary == "" || primary != state.PrimaryDomain {
			state.AccessState = publicAccessProgressAttention
			state.AccessStage = "dns"
			state.AccessMessage = "访问域名已变化，请重新应用公网访问配置。"
			if state.Requested {
				state.Status = publicAccessProxyFailed
				state.Error = state.AccessMessage
			}
			s.updateControlPublicAccessProxyState(siteID, state)
			return
		}

		verifyCtx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
		verified := verifyDomainReachable(verifyCtx, primary)
		cancel()
		if verified.OK && state.ProxyApplied {
			state.Status = publicAccessProxyEnabled
			state.Error = ""
			state.AccessState = publicAccessProgressReady
			state.AccessStage = "ready"
			state.AccessMessage = ""
			s.updateControlPublicAccessProxyState(siteID, state)
			return
		}
		if verified.OK {
			if !state.Requested {
				state.Status = publicAccessProxyDisabled
				state.Error = ""
				state.AccessState = publicAccessProgressReady
				state.AccessStage = "ready"
				state.AccessMessage = ""
				s.updateControlPublicAccessProxyState(siteID, state)
				return
			}
			state.Status = publicAccessProxyEnabling
			state.Error = ""
			state.AccessState = publicAccessProgressPending
			state.AccessStage = "proxy"
			state.AccessMessage = "网站已可访问，正在开启 Cloudflare 橙云。"
			if !s.updateControlPublicAccessProxyState(siteID, state) {
				return
			}
			proxy := true
			message, applyErr := s.applyCloudflareDNSForSpecs(context.Background(), specs, &proxy)
			state, ok = s.loadControlPublicAccessProxyState(siteID)
			if !ok || state.Generation != generation || !state.Requested {
				return
			}
			if applyErr != nil {
				state.Status = publicAccessProxyFailed
				state.Error = strings.TrimSpace(message)
				if state.Error == "" {
					state.Error = "Cloudflare 橙云开启失败：" + applyErr.Error()
				}
				state.AccessState = publicAccessProgressAttention
				state.AccessStage = "proxy"
				state.AccessMessage = state.Error
				s.updateControlPublicAccessProxyState(siteID, state)
				return
			}
			state.ProxyApplied = true
			state.Status = publicAccessProxyPending
			state.Error = "橙云已提交，正在等待 Cloudflare 代理生效。"
			state.AccessState = publicAccessProgressPending
			state.AccessStage = "proxy"
			state.AccessMessage = state.Error
			if !s.updateControlPublicAccessProxyState(siteID, state) {
				return
			}
			lastReason = state.Error
		} else {
			if state.ProxyApplied {
				lastReason = "橙云已提交，正在等待 Cloudflare 代理生效。"
				state.AccessStage = "proxy"
			} else {
				dnsCtx, dnsCancel := context.WithTimeout(context.Background(), 5*time.Second)
				dnsInfo := s.detectDomainDNS(dnsCtx, primary)
				dnsCancel()
				if dnsInfo.PointsToServer == "yes" || dnsInfo.PointsToServer == "via_cloudflare" {
					lastReason = "DNS 已生效，正在等待源站 HTTPS 验证通过。"
					state.AccessStage = "https"
				} else {
					lastReason = "正在等待 DNS 生效。"
					state.AccessStage = "dns"
				}
			}
			state.AccessState = publicAccessProgressPending
			state.AccessMessage = lastReason
			if state.Requested {
				state.Status = publicAccessProxyPending
				state.Error = lastReason
			} else {
				state.Status = publicAccessProxyDisabled
				state.Error = ""
			}
			if !s.updateControlPublicAccessProxyState(siteID, state) {
				return
			}
		}

		timer := time.NewTimer(5 * time.Second)
		<-timer.C
	}
	state, ok := s.loadControlPublicAccessProxyState(siteID)
	if !ok || state.Generation != generation {
		return
	}
	if state.ProxyApplied {
		state.AccessStage = "proxy"
		state.AccessMessage = "网站仍可直接访问，但等待 Cloudflare 橙云生效超时；可单独重试开启橙云。"
	} else if lastReason != "" {
		state.AccessMessage = lastReason + " 等待超时，可稍后重试；现有 DNS 与站点配置不会回滚。"
	} else {
		state.AccessStage = "https"
		state.AccessMessage = "等待 HTTPS 验证超时，可稍后重试；现有 DNS 与站点配置不会回滚。"
	}
	state.AccessState = publicAccessProgressAttention
	if state.Requested {
		state.Status = publicAccessProxyFailed
		state.Error = state.AccessMessage
	} else {
		state.Status = publicAccessProxyDisabled
		state.Error = ""
	}
	s.updateControlPublicAccessProxyState(siteID, state)
}

func (s *Server) resumeControlPublicAccessProxyJobs() {
	if s == nil || s.platform == nil {
		return
	}
	sites, err := s.platform.Sites()
	if err != nil {
		return
	}
	for _, site := range sites {
		if site == nil {
			continue
		}
		state, ok := s.loadControlPublicAccessProxyState(site.ID)
		if !ok {
			continue
		}
		if state.AccessState == publicAccessProgressPending ||
			(state.AccessState == "" && state.Requested &&
				(state.Status == publicAccessProxyPending || state.Status == publicAccessProxyEnabling)) {
			s.scheduleControlPublicAccessProxy(site.ID, state.Generation)
		}
	}
}
