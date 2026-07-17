package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	cloudflareAccountIDKey    = "cloudflare.account_id"
	cloudflareAPITokenKey     = "cloudflare.api_token"
	cloudflareDeployModeKey   = "cloudflare.deploy_mode"
	cloudflareWorkerNameKey   = "cloudflare.worker_name"
	cloudflarePagesProjectKey = "cloudflare.pages_project_name"
	cloudflareZoneIDKey       = "cloudflare.zone_id"
	cloudflareRoutePatternKey = "cloudflare.route_pattern"
	cloudflareDomainsKey      = "cloudflare.domains"
	cloudflareOriginURLKey    = "cloudflare.origin_url"
	cloudflareHTMLTTLKey      = "cloudflare.html_cache_ttl"
	cloudflareAutoSyncKey     = "cloudflare.auto_sync"
	cloudflareSyncModeKey     = "cloudflare.sync_mode"
	cloudflareSyncTimeKey     = "cloudflare.sync_time"
	cloudflareSyncPendingKey  = "cloudflare.sync_pending"
	cloudflareSyncNextAtKey   = "cloudflare.sync_next_at"
	cloudflareSourceModeKey   = "cloudflare.source_frontend_mode"
	cloudflareAccountNameKey  = "cloudflare.account_name"
	cloudflareZoneNameKey     = "cloudflare.zone_name"

	cloudflareDefaultWorkerName  = "gcms-frontend"
	cloudflareModeWorkerAssets   = "worker_assets"
	cloudflareModePages          = "pages"
	cloudflareDNSStatusManaged   = "managed"
	cloudflareDNSStatusManual    = "manual"
	cloudflareSourceModeRedirect = "redirect"
	cloudflareSourceModeNoindex  = "noindex"
	cloudflareSourceModeNone     = "none"
	cloudflareSyncModeManual     = "manual"
	cloudflareSyncModeRealtime   = "realtime"
	cloudflareSyncModeDaily      = "daily"
	cloudflareDefaultSyncTime    = "03:00"
	cloudflareDefaultHTMLTTL     = 300
	cloudflareAPITimeout         = 4 * time.Minute
	cloudflareStaleAfter         = 3 * time.Minute
	cloudflareHistoryLimit       = 8
)

var cloudflareWorkerNameRE = regexp.MustCompile(`[^a-z0-9-]+`)

type CloudflareConfig struct {
	AccountID        string
	APIToken         string
	DeployMode       string
	WorkerName       string
	PagesProjectName string
	ZoneID           string
	RoutePattern     string
	Domains          []CloudflareDomain
	OriginURL        string
	HTMLCacheTTL     int
	AutoSync         bool
	SyncMode         string
	SyncTime         string
	SyncPending      bool
	SyncNextAt       string
	SourceMode       string
	AccountName      string
	ZoneName         string
	DefaultLang      string
	Locales          []string
}

type CloudflareDomain struct {
	Host              string `json:"host"`
	Primary           bool   `json:"primary,omitempty"`
	RedirectToPrimary bool   `json:"redirect_to_primary,omitempty"`
}

type CloudflareStatus struct {
	Status           string                    `json:"status"`
	Step             string                    `json:"step"`
	Message          string                    `json:"message"`
	DeployMode       string                    `json:"deploy_mode"`
	WorkerName       string                    `json:"worker_name"`
	PagesProjectName string                    `json:"pages_project_name,omitempty"`
	RoutePattern     string                    `json:"route_pattern"`
	PrimaryDomain    string                    `json:"primary_domain,omitempty"`
	Domains          string                    `json:"domains,omitempty"`
	UpdatedAt        string                    `json:"updated_at"`
	LastDeployAt     string                    `json:"last_deploy_at,omitempty"`
	FirstDeployAt    string                    `json:"first_deploy_at,omitempty"`
	FirstDeployEst   bool                      `json:"first_deploy_estimated,omitempty"`
	LastPurgeAt      string                    `json:"last_purge_at,omitempty"`
	DNSStatus        string                    `json:"dns_status,omitempty"`
	DNSMessage       string                    `json:"dns_message,omitempty"`
	Configured       bool                      `json:"configured"`
	TokenSet         bool                      `json:"token_set"`
	AutoSync         bool                      `json:"auto_sync"`
	SyncMode         string                    `json:"sync_mode"`
	SyncTime         string                    `json:"sync_time"`
	SyncPending      bool                      `json:"sync_pending"`
	SyncNextAt       string                    `json:"sync_next_at,omitempty"`
	Published        bool                      `json:"published"`
	CanUnpublish     bool                      `json:"can_unpublish"`
	CanPurge         bool                      `json:"can_purge"`
	Running          bool                      `json:"running"`
	History          []CloudflareStatusHistory `json:"history,omitempty"`
}

type CloudflareStatusHistory struct {
	Action           string `json:"action"`
	Status           string `json:"status"`
	Step             string `json:"step,omitempty"`
	Message          string `json:"message"`
	DeployMode       string `json:"deploy_mode,omitempty"`
	WorkerName       string `json:"worker_name,omitempty"`
	PagesProjectName string `json:"pages_project_name,omitempty"`
	PrimaryDomain    string `json:"primary_domain,omitempty"`
	Domains          string `json:"domains,omitempty"`
	At               string `json:"at"`
}

type CloudflareView struct {
	Config           CloudflareConfig
	Status           *CloudflareStatus
	TokenSet         bool
	Configured       bool
	TokenTemplateURL string
	RouteHost        string
	LikelyZoneName   string
	PrimaryDomain    string
	AliasDomains     string
	AliasDomainInput string
	RedirectAliases  bool
	DomainSummary    string
	TokenFingerprint string
	ProjectName      string
	ProjectDefault   string
	ProjectCustom    bool
}

type cloudflareClientView struct {
	TokenSet         bool   `json:"token_set"`
	Configured       bool   `json:"configured"`
	PrimaryDomain    string `json:"primary_domain"`
	DomainSummary    string `json:"domain_summary"`
	PublicURL        string `json:"public_url"`
	TokenFingerprint string `json:"token_fingerprint"`
	ProjectName      string `json:"project_name"`
	ProjectDefault   string `json:"project_default"`
	ProjectCustom    bool   `json:"project_custom"`
	DeployMode       string `json:"deploy_mode"`
	SourceMode       string `json:"source_mode"`
	AutoSync         bool   `json:"auto_sync"`
	SyncMode         string `json:"sync_mode"`
	SyncTime         string `json:"sync_time"`
	SyncPending      bool   `json:"sync_pending"`
	SyncNextAt       string `json:"sync_next_at,omitempty"`
	CanUnpublish     bool   `json:"can_unpublish"`
	CanPurge         bool   `json:"can_purge"`
	Published        bool   `json:"published"`
	DNSStatus        string `json:"dns_status,omitempty"`
	DNSMessage       string `json:"dns_message,omitempty"`
}

type cloudflareDetectedTarget struct {
	AccountID   string
	AccountName string
	ZoneID      string
	ZoneName    string
}

type cloudflareDNSManageResult struct {
	Status  string
	Message string
}

type cloudflareDNSTarget struct {
	Type    string
	Host    string
	Content string
}

type cloudflareAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cloudflareZone struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Account cloudflareAccount `json:"account"`
}

type cloudflareTokenVerifyResult struct {
	Status string `json:"status"`
}

type cloudflareAPIResponse struct {
	Success bool            `json:"success"`
	Errors  []cloudflareErr `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cloudflareAPIError struct {
	Status int
	Errors []cloudflareErr
	Raw    string
}

func (e cloudflareAPIError) Error() string {
	if strings.TrimSpace(e.Raw) != "" {
		return fmt.Sprintf("Cloudflare 返回 HTTP %d：%s", e.Status, e.Raw)
	}
	return fmt.Sprintf("Cloudflare 返回错误：%s", cloudflareErrorMessage(e.Errors, e.Status))
}

type cloudflareErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cloudflareRoute struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

type cloudflareDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied *bool  `json:"proxied"`
}

type cloudflarePagesProject struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ProjectName      string `json:"project_name"`
	Subdomain        string `json:"subdomain"`
	ProductionBranch string `json:"production_branch"`
}

type cloudflarePagesUploadToken struct {
	JWT string `json:"jwt"`
}

type cloudflarePagesDeployment struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type cloudflareAssetManifestEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

type cloudflareAssetsUploadSession struct {
	JWT     string     `json:"jwt"`
	Buckets [][]string `json:"buckets"`
}

type cloudflareAssetsUploadResult struct {
	JWT string `json:"jwt"`
}

type cloudflareWorkerVersionResult struct {
	ID string `json:"id"`
}

func cloudflareStatusPath() string {
	return filepath.Join(upgradeRoot(), "run", "cloudflare-deploy.json")
}

func cloudflareStatusPathForRuntime(rt *SiteRuntime) string {
	if rt == nil || rt.Site == nil || rt.Site.IsDefault {
		return cloudflareStatusPath()
	}
	if dbPath := strings.TrimSpace(rt.Site.DBPath); dbPath != "" {
		return filepath.Join(filepath.Dir(dbPath), "run", "cloudflare-deploy.json")
	}
	if uploadDir := strings.TrimSpace(rt.UploadDir); uploadDir != "" {
		return filepath.Join(filepath.Dir(uploadDir), "run", "cloudflare-deploy.json")
	}
	return cloudflareStatusPath()
}

func cloudflareStatusHistory(st *CloudflareStatus) []CloudflareStatusHistory {
	if st == nil || len(st.History) == 0 {
		return nil
	}
	history := append([]CloudflareStatusHistory(nil), st.History...)
	if len(history) > cloudflareHistoryLimit {
		history = history[:cloudflareHistoryLimit]
	}
	return history
}

func withCloudflareHistory(st CloudflareStatus, action string) CloudflareStatus {
	prev := readCloudflareStatus()
	st.History = appendCloudflareHistory(cloudflareStatusHistory(prev), cloudflareHistoryEntry(st, action))
	return st
}

func appendCloudflareHistory(history []CloudflareStatusHistory, entry CloudflareStatusHistory) []CloudflareStatusHistory {
	if strings.TrimSpace(entry.At) == "" {
		entry.At = time.Now().UTC().Format(time.RFC3339)
	}
	out := make([]CloudflareStatusHistory, 0, min(len(history)+1, cloudflareHistoryLimit))
	out = append(out, entry)
	for _, item := range history {
		if len(out) >= cloudflareHistoryLimit {
			break
		}
		out = append(out, item)
	}
	return out
}

func cloudflareHistoryEntry(st CloudflareStatus, action string) CloudflareStatusHistory {
	action = strings.TrimSpace(action)
	if action == "" {
		action = st.Step
	}
	at := st.UpdatedAt
	if action == "deploy" && strings.TrimSpace(st.LastDeployAt) != "" {
		at = st.LastDeployAt
	}
	if action == "purge" && strings.TrimSpace(st.LastPurgeAt) != "" {
		at = st.LastPurgeAt
	}
	if strings.TrimSpace(at) == "" {
		at = time.Now().UTC().Format(time.RFC3339)
	}
	return CloudflareStatusHistory{
		Action:           action,
		Status:           st.Status,
		Step:             st.Step,
		Message:          st.Message,
		DeployMode:       st.DeployMode,
		WorkerName:       st.WorkerName,
		PagesProjectName: st.PagesProjectName,
		PrimaryDomain:    st.PrimaryDomain,
		Domains:          st.Domains,
		At:               at,
	}
}

func (cfg CloudflareConfig) tokenSet() bool {
	return strings.TrimSpace(cfg.APIToken) != ""
}

func (cfg CloudflareConfig) configured() bool {
	if !cfg.tokenSet() || strings.TrimSpace(cfg.primaryHost()) == "" {
		return false
	}
	if cfg.usingPages() {
		return strings.TrimSpace(cfg.PagesProjectName) != ""
	}
	return strings.TrimSpace(cfg.WorkerName) != ""
}

func (cfg CloudflareConfig) validateDeploy() error {
	if !cfg.tokenSet() {
		return errors.New("请先粘贴 Cloudflare API Token。")
	}
	if cfg.usingPages() {
		if strings.TrimSpace(cfg.PagesProjectName) == "" {
			return errors.New("请填写 Cloudflare Pages 项目名称。")
		}
	} else if strings.TrimSpace(cfg.WorkerName) == "" {
		return errors.New("请填写 Worker 名称。")
	}
	if strings.TrimSpace(cfg.primaryHost()) == "" {
		return errors.New("请填写前台访问域名，例如 example.com 或 www.example.com。")
	}
	if cfg.RoutePattern != "" && strings.Contains(cfg.RoutePattern, "://") {
		return errors.New("前台访问域名请填写 example.com 或 www.example.com 这种格式，不要带 http:// 或 https://。")
	}
	return nil
}

func (cfg CloudflareConfig) usingPages() bool {
	return normalizeCloudflareDeployMode(cfg.DeployMode) == cloudflareModePages
}

func (cfg CloudflareConfig) publicDomains() []CloudflareDomain {
	domains := normalizeCloudflareDomains(cfg.Domains)
	if len(domains) > 0 {
		return domains
	}
	if host := cloudflareRouteHost(cfg.RoutePattern); host != "" {
		return []CloudflareDomain{{Host: host, Primary: true}}
	}
	return nil
}

func (cfg CloudflareConfig) primaryHost() string {
	for _, d := range cfg.publicDomains() {
		if d.Primary {
			return d.Host
		}
	}
	domains := cfg.publicDomains()
	if len(domains) > 0 {
		return domains[0].Host
	}
	return cloudflareRouteHost(cfg.RoutePattern)
}

func (cfg CloudflareConfig) routePatterns() []string {
	domains := cfg.publicDomains()
	patterns := make([]string, 0, len(domains))
	seen := map[string]bool{}
	for _, domain := range domains {
		if domain.Host == "" {
			continue
		}
		pattern := normalizeCloudflareRoutePattern(domain.Host)
		if pattern == "" || seen[pattern] {
			continue
		}
		seen[pattern] = true
		patterns = append(patterns, pattern)
	}
	return patterns
}

func (cfg CloudflareConfig) redirectHosts() []string {
	primary := cfg.primaryHost()
	out := []string{}
	for _, domain := range cfg.publicDomains() {
		if domain.Host == "" || sameCloudflareDNSName(domain.Host, primary) || !domain.RedirectToPrimary {
			continue
		}
		out = append(out, domain.Host)
	}
	sort.Strings(out)
	return out
}

func (cfg CloudflareConfig) publicDomainSummary() string {
	domains := cfg.publicDomains()
	if len(domains) == 0 {
		return ""
	}
	hosts := make([]string, 0, len(domains))
	for _, domain := range domains {
		if domain.Host == "" {
			continue
		}
		if domain.Primary {
			hosts = append([]string{domain.Host}, hosts...)
		} else {
			hosts = append(hosts, domain.Host)
		}
	}
	return strings.Join(hosts, " / ")
}

func normalizeCloudflareDomains(domains []CloudflareDomain) []CloudflareDomain {
	out := make([]CloudflareDomain, 0, len(domains))
	seen := map[string]bool{}
	hasPrimary := false
	for _, domain := range domains {
		host := normalizeCloudflareDomainHost(domain.Host)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		domain.Host = host
		if domain.Primary && !hasPrimary {
			hasPrimary = true
		} else {
			domain.Primary = false
		}
		out = append(out, domain)
	}
	if len(out) > 0 && !hasPrimary {
		out[0].Primary = true
	}
	return out
}

func cloudflareStatusFailed(cfg CloudflareConfig, step, msg string) CloudflareStatus {
	return cloudflareStatusFailedFromPrevious(readCloudflareStatus(), cfg, step, msg)
}

func cloudflareStatusFailedFromPrevious(prev *CloudflareStatus, cfg CloudflareConfig, step, msg string) CloudflareStatus {
	if strings.TrimSpace(msg) == "" {
		msg = "Cloudflare 部署失败。"
	}
	if prev == nil {
		prev = readCloudflareStatus()
	}
	failedStep := strings.TrimSpace(step)
	if failedStep == "" || failedStep == "failed" {
		failedStep = strings.TrimSpace(prev.Step)
		if failedStep == "" || failedStep == "done" {
			failedStep = "queued"
		}
	}
	st := CloudflareStatus{
		Status:           "failed",
		Step:             failedStep,
		Message:          msg,
		DeployMode:       normalizeCloudflareDeployMode(cfg.DeployMode),
		WorkerName:       cfg.WorkerName,
		PagesProjectName: cfg.PagesProjectName,
		RoutePattern:     cfg.RoutePattern,
		PrimaryDomain:    cfg.primaryHost(),
		Domains:          cfg.publicDomainSummary(),
		Configured:       cfg.configured(),
		TokenSet:         cfg.tokenSet(),
		AutoSync:         cfg.AutoSync,
		DNSStatus:        prev.DNSStatus,
		DNSMessage:       prev.DNSMessage,
		Published:        cloudflareStatusPublished(prev),
		LastDeployAt:     prev.LastDeployAt,
		LastPurgeAt:      prev.LastPurgeAt,
	}
	st.History = appendCloudflareHistory(cloudflareStatusHistory(prev), cloudflareHistoryEntry(st, "failed"))
	return st
}

func normalizeCloudflareDeployMode(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case cloudflareModePages:
		return cloudflareModePages
	default:
		// 默认走 Worker Assets：一个 Worker 即可承载静态站，入口控制和缓存策略更统一。
		return cloudflareModeWorkerAssets
	}
}

func normalizeCloudflareDNSStatus(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case cloudflareDNSStatusManaged:
		return cloudflareDNSStatusManaged
	case cloudflareDNSStatusManual:
		return cloudflareDNSStatusManual
	default:
		return ""
	}
}

func normalizeCloudflareSourceMode(v string) string {
	switch strings.TrimSpace(v) {
	case cloudflareSourceModeNoindex:
		return cloudflareSourceModeNoindex
	case cloudflareSourceModeNone:
		return cloudflareSourceModeNone
	default:
		return cloudflareSourceModeRedirect
	}
}

func normalizeCloudflareSyncMode(v string) string {
	switch strings.TrimSpace(v) {
	case cloudflareSyncModeManual:
		return cloudflareSyncModeManual
	case cloudflareSyncModeDaily:
		return cloudflareSyncModeDaily
	default:
		return cloudflareSyncModeRealtime
	}
}

func normalizeCloudflareSyncTime(v string) string {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) < 2 {
		return cloudflareDefaultSyncTime
	}
	hour, errH := strconv.Atoi(parts[0])
	minute, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return cloudflareDefaultSyncTime
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func cloudflareNextDailySyncAt(now time.Time, syncTime string) time.Time {
	syncTime = normalizeCloudflareSyncTime(syncTime)
	parts := strings.Split(syncTime, ":")
	hour, _ := strconv.Atoi(parts[0])
	minute, _ := strconv.Atoi(parts[1])
	localNow := now.In(time.Local)
	next := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, time.Local)
	if !next.After(localNow) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func formHasValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func normalizeCloudflareWorkerName(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "_", "-")
	v = cloudflareWorkerNameRE.ReplaceAllString(v, "-")
	v = strings.Trim(v, "-")
	if v == "" {
		return cloudflareDefaultWorkerName
	}
	if len(v) > 63 {
		v = strings.Trim(v[:63], "-")
	}
	if v == "" {
		return cloudflareDefaultWorkerName
	}
	return v
}

func normalizeCloudflarePagesProjectName(v string) string {
	return normalizeCloudflareWorkerName(v)
}

func cloudflareDefaultProjectNameForHost(host string) string {
	host = normalizeCloudflareDomainHost(host)
	if host == "" {
		return cloudflareDefaultWorkerName
	}
	return normalizeCloudflareWorkerName("gcms-" + strings.ReplaceAll(host, ".", "-"))
}

func normalizeCloudflareOrigin(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return v
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func normalizeCloudflareRoutePattern(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.Contains(v, "://") {
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			path := strings.TrimSpace(u.EscapedPath())
			if path == "" || path == "/" {
				return u.Host + "/*"
			}
			return u.Host + strings.TrimRight(path, "/") + "/*"
		}
	}
	if strings.Contains(v, " ") {
		v = strings.Fields(v)[0]
	}
	if strings.HasSuffix(v, "/*") || strings.Contains(v, "*") {
		return v
	}
	if strings.Contains(v, "/") {
		return strings.TrimRight(v, "/") + "/*"
	}
	return v + "/*"
}

func normalizeCloudflareDomainHost(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.Contains(v, "://") {
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			v = u.Host
		}
	}
	if strings.Contains(v, " ") {
		v = strings.Fields(v)[0]
	}
	v = strings.TrimPrefix(strings.TrimSpace(v), "*.")
	v = strings.TrimSuffix(v, "/*")
	if i := strings.Index(v, "/"); i >= 0 {
		v = v[:i]
	}
	v = strings.Trim(strings.ToLower(v), ".")
	if i := strings.LastIndex(v, ":"); i > -1 {
		v = v[:i]
	}
	if cloudflareLocalHost(v) {
		return ""
	}
	return v
}

func cloudflareDomainsFromForm(primary string, aliases []string, redirectAliases bool) []CloudflareDomain {
	out := []CloudflareDomain{}
	if host := normalizeCloudflareDomainHost(primary); host != "" {
		out = append(out, CloudflareDomain{Host: host, Primary: true})
	}
	for _, raw := range aliases {
		for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == '\n' || r == '\r' || r == ',' || r == ';'
		}) {
			host := normalizeCloudflareDomainHost(item)
			if host == "" {
				continue
			}
			out = append(out, CloudflareDomain{Host: host, RedirectToPrimary: redirectAliases})
		}
	}
	return normalizeCloudflareDomains(out)
}

func encodeCloudflareDomains(domains []CloudflareDomain) string {
	domains = normalizeCloudflareDomains(domains)
	if len(domains) == 0 {
		return ""
	}
	data, err := json.Marshal(domains)
	if err != nil {
		return ""
	}
	return string(data)
}

func decodeCloudflareDomains(raw string) []CloudflareDomain {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var domains []CloudflareDomain
	if err := json.Unmarshal([]byte(raw), &domains); err != nil {
		return nil
	}
	return normalizeCloudflareDomains(domains)
}

func (s *Server) defaultCloudflareRoutePattern() string {
	return cloudflareRoutePatternFromBaseURL(s.baseURL)
}

func cloudflareRoutePatternFromBaseURL(base string) string {
	base = strings.TrimSpace(base)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if cloudflareLocalHost(host) {
		return ""
	}
	return u.Host + "/*"
}

func (s *Server) defaultCloudflareOriginURL() string {
	return cloudflareOriginFromBaseURL(s.baseURL)
}

func cloudflareOriginFromBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if cloudflareLocalHost(host) {
		return ""
	}
	return base
}

func cloudflareLocalHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	return host == "localhost" ||
		host == "127.0.0.1" ||
		host == "::1" ||
		strings.HasSuffix(host, ".local")
}

func (s *Server) cloudflareConfig() CloudflareConfig {
	ttl, err := strconv.Atoi(strings.TrimSpace(s.store.Setting(cloudflareHTMLTTLKey)))
	if err != nil || ttl < 0 {
		ttl = cloudflareDefaultHTMLTTL
	}
	if ttl > 86400 {
		ttl = 86400
	}
	worker := normalizeCloudflareWorkerName(s.store.Setting(cloudflareWorkerNameKey))
	pagesProject := normalizeCloudflarePagesProjectName(s.store.Setting(cloudflarePagesProjectKey))
	origin := normalizeCloudflareOrigin(s.store.Setting(cloudflareOriginURLKey))
	if origin == "" {
		origin = s.defaultCloudflareOriginURL()
	}
	route := normalizeCloudflareRoutePattern(s.store.Setting(cloudflareRoutePatternKey))
	domains := decodeCloudflareDomains(s.store.Setting(cloudflareDomainsKey))
	if len(domains) == 0 && route != "" {
		domains = []CloudflareDomain{{Host: cloudflareRouteHost(route), Primary: true}}
	}
	if len(domains) > 0 {
		route = normalizeCloudflareRoutePattern(domains[0].Host)
		for _, d := range domains {
			if d.Primary {
				route = normalizeCloudflareRoutePattern(d.Host)
				break
			}
		}
	}
	autoSyncRaw := strings.TrimSpace(s.store.Setting(cloudflareAutoSyncKey))
	syncMode := normalizeCloudflareSyncMode(s.store.Setting(cloudflareSyncModeKey))
	return CloudflareConfig{
		AccountID:        strings.TrimSpace(s.store.Setting(cloudflareAccountIDKey)),
		APIToken:         strings.TrimSpace(s.store.Setting(cloudflareAPITokenKey)),
		DeployMode:       normalizeCloudflareDeployMode(s.store.Setting(cloudflareDeployModeKey)),
		WorkerName:       worker,
		PagesProjectName: pagesProject,
		ZoneID:           strings.TrimSpace(s.store.Setting(cloudflareZoneIDKey)),
		RoutePattern:     route,
		Domains:          domains,
		OriginURL:        origin,
		HTMLCacheTTL:     ttl,
		AutoSync:         autoSyncRaw != "0" && syncMode != cloudflareSyncModeManual,
		SyncMode:         syncMode,
		SyncTime:         normalizeCloudflareSyncTime(s.store.Setting(cloudflareSyncTimeKey)),
		SyncPending:      s.store.Setting(cloudflareSyncPendingKey) == "1",
		SyncNextAt:       strings.TrimSpace(s.store.Setting(cloudflareSyncNextAtKey)),
		SourceMode:       normalizeCloudflareSourceMode(s.store.Setting(cloudflareSourceModeKey)),
		AccountName:      strings.TrimSpace(s.store.Setting(cloudflareAccountNameKey)),
		ZoneName:         strings.TrimSpace(s.store.Setting(cloudflareZoneNameKey)),
	}
}

func (s *Server) cloudflareConfigForRequest(r *http.Request) CloudflareConfig {
	cfg := s.cloudflareConfig()
	s.applyCloudflareRequestDefaults(r, &cfg)
	return cfg
}

func (s *Server) cloudflareStaticRuntimeConfig(cfg CloudflareConfig) CloudflareConfig {
	locales := s.locales()
	cfg.Locales = cfg.Locales[:0]
	for _, loc := range locales {
		code := strings.TrimSpace(loc.Code)
		if code != "" {
			cfg.Locales = append(cfg.Locales, code)
		}
	}
	if len(cfg.Locales) == 0 {
		cfg.Locales = []string{"zh"}
	}
	cfg.DefaultLang = strings.TrimSpace(s.defaultLang())
	if cfg.DefaultLang == "" {
		cfg.DefaultLang = cfg.Locales[0]
	}
	return cfg
}

func (s *Server) cloudflareViewForRequest(r *http.Request) *CloudflareView {
	view := s.cloudflareView()
	s.applyCloudflareRequestDefaults(r, &view.Config)
	view.Status.DeployMode = view.Config.DeployMode
	view.Status.WorkerName = view.Config.WorkerName
	view.Status.PagesProjectName = view.Config.PagesProjectName
	view.Status.RoutePattern = view.Config.RoutePattern
	view.Status.PrimaryDomain = view.Config.primaryHost()
	view.Status.Domains = view.Config.publicDomainSummary()
	view.Status.Configured = view.Config.configured()
	view.Status.AutoSync = view.Config.AutoSync
	view.Status.SyncMode = view.Config.SyncMode
	view.Status.SyncTime = view.Config.SyncTime
	view.Status.SyncPending = view.Config.SyncPending
	view.Status.SyncNextAt = view.Config.SyncNextAt
	applyCloudflareLegacyDeploymentStatus(view.Config, view.Status)
	view.Status.Published = cloudflareStatusPublished(view.Status)
	view.Status.CanUnpublish = cloudflareCanUnpublish(view.Config, view.Status)
	view.Status.CanPurge = cloudflareCanPurge(view.Config, view.Status)
	view.Configured = view.Status.Configured
	view.decorate()
	return view
}

func (s *Server) applyCloudflareRequestDefaults(r *http.Request, cfg *CloudflareConfig) {
	if cfg == nil || r == nil {
		return
	}
	base := s.publicBaseURL(r)
	if strings.TrimSpace(cfg.OriginURL) == "" {
		cfg.OriginURL = cloudflareOriginFromBaseURL(base)
	}
}

func readCloudflareStatus() *CloudflareStatus {
	return readCloudflareStatusFile(cloudflareStatusPath())
}

func (s *Server) cloudflareStatusPath() string {
	if s != nil && strings.TrimSpace(s.cloudflareStatusFile) != "" {
		return strings.TrimSpace(s.cloudflareStatusFile)
	}
	return cloudflareStatusPath()
}

func (s *Server) readCloudflareStatus() *CloudflareStatus {
	return readCloudflareStatusFile(s.cloudflareStatusPath())
}

func (s *Server) writeCloudflareStatus(st CloudflareStatus) {
	writeCloudflareStatusFile(s.cloudflareStatusPath(), st)
}

func (s *Server) withCloudflareHistory(st CloudflareStatus, action string) CloudflareStatus {
	prev := s.readCloudflareStatus()
	st.History = appendCloudflareHistory(cloudflareStatusHistory(prev), cloudflareHistoryEntry(st, action))
	return st
}

func (s *Server) cloudflareStatusFailed(cfg CloudflareConfig, step, msg string) CloudflareStatus {
	return cloudflareStatusFailedFromPrevious(s.readCloudflareStatus(), cfg, step, msg)
}

func readCloudflareStatusFile(path string) *CloudflareStatus {
	st := &CloudflareStatus{
		Status:  "idle",
		Message: "暂无 Cloudflare 部署任务",
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = cloudflareStatusPath()
	}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, st)
		if st.Status == "" {
			st.Status = "idle"
		}
		if st.Message == "" {
			st.Message = "暂无 Cloudflare 部署任务"
		}
	}
	st.DeployMode = normalizeCloudflareDeployMode(st.DeployMode)
	st.DNSStatus = normalizeCloudflareDNSStatus(st.DNSStatus)
	st.History = cloudflareStatusHistory(st)
	if st.Status == "running" && cloudflareStatusStale(st) {
		st.Status = "failed"
		st.Step = "timeout"
		st.Message = "上一次部署任务长时间没有更新，可能已被中断。请检查 Token、前台域名和 Cloudflare 权限后重新部署。"
		writeCloudflareStatusFile(path, *st)
	}
	st.Running = st.Status == "running"
	return st
}

func cloudflareStatusStale(st *CloudflareStatus) bool {
	if st == nil || st.Status != "running" || strings.TrimSpace(st.UpdatedAt) == "" {
		return false
	}
	updatedAt, err := time.Parse(time.RFC3339, st.UpdatedAt)
	if err != nil {
		return false
	}
	return time.Since(updatedAt) > cloudflareStaleAfter
}

func writeCloudflareStatus(st CloudflareStatus) {
	writeCloudflareStatusFile(cloudflareStatusPath(), st)
}

func writeCloudflareStatusFile(path string, st CloudflareStatus) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = cloudflareStatusPath()
	}
	var prev *CloudflareStatus
	if st.History == nil || strings.TrimSpace(st.FirstDeployAt) == "" {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			var p CloudflareStatus
			if err := json.Unmarshal(data, &p); err == nil {
				prev = &p
			}
		}
	}
	if st.History == nil && prev != nil {
		st.History = cloudflareStatusHistory(prev)
	}
	// first_deploy_at 只进不退：部署历史是 8 条环形，首发记录早晚会被挤掉，所以首发
	// 时间必须单独落档。写档一律带上旧档的值；旧档没有（升级前的老档）就地回填，站点卡
	// 「运行 N 天」的悬停口径按 estimated 标记措辞（精确首发 / 只是下界）。
	if strings.TrimSpace(st.FirstDeployAt) == "" && prev != nil {
		st.FirstDeployAt = strings.TrimSpace(prev.FirstDeployAt)
		st.FirstDeployEst = prev.FirstDeployEst
	}
	if strings.TrimSpace(st.FirstDeployAt) == "" {
		// 锚点取最早：旧档**未旋转**历史里最旧的成功部署（本次写档 append 可能刚把它
		// 挤出环形，必须在旋转前的 prev 里找）、本次历史里最旧的成功部署、以及新旧
		// last_deploy_at（History 字段比 last_deploy_at 晚一天引入，存在只有后者的老档）。
		type fdCand struct {
			at      time.Time
			raw     string
			success bool
		}
		var best *fdCand
		consider := func(raw string, success bool) {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return
			}
			ts, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return
			}
			if best == nil || ts.Before(best.at) {
				best = &fdCand{at: ts, raw: raw, success: success}
			}
		}
		var prevHist []CloudflareStatusHistory
		prevLast := ""
		if prev != nil {
			prevHist = cloudflareStatusHistory(prev)
			prevLast = strings.TrimSpace(prev.LastDeployAt)
		}
		consider(oldestCloudflareDeploySuccess(prevHist), true)
		consider(oldestCloudflareDeploySuccess(st.History), true)
		consider(prevLast, false)
		consider(st.LastDeployAt, false)
		if best != nil {
			st.FirstDeployAt = best.raw
			if !best.success {
				// 只有 last_deploy_at 一类痕迹：只能证明「至少从那时起」，是下界。
				st.FirstDeployEst = true
			} else {
				// 锚点是成功历史条目：只有当旧档历史已滚满**且**此前确实成功发布过
				// （旋转可能吃掉过更早的成功记录）才是下界；旧档历史未滚满、或此前
				// 从未成功发布（连 last_deploy_at 都没有）时，这就是真首发。
				prevFull := len(prevHist) >= cloudflareHistoryLimit
				prevEverDeployed := prevLast != "" || oldestCloudflareDeploySuccess(prevHist) != ""
				st.FirstDeployEst = prevFull && prevEverDeployed
			}
		}
	}
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	st.Running = st.Status == "running"
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.WriteFile(path, append(data, '\n'), 0o644)
	}
}

// oldestCloudflareDeploySuccess 现存历史（新的在前）里最旧一条成功部署的时间戳；没有返回 ""。
func oldestCloudflareDeploySuccess(history []CloudflareStatusHistory) string {
	for i := len(history) - 1; i >= 0; i-- {
		h := history[i]
		if h.Action != "deploy" || h.Status != "success" {
			continue
		}
		if at := strings.TrimSpace(h.At); at != "" {
			return at
		}
	}
	return ""
}

func (s *Server) cloudflareView() *CloudflareView {
	cfg := s.cloudflareConfig()
	st := s.readCloudflareStatus()
	st.DeployMode = cfg.DeployMode
	st.WorkerName = cfg.WorkerName
	st.PagesProjectName = cfg.PagesProjectName
	st.RoutePattern = cfg.RoutePattern
	st.PrimaryDomain = cfg.primaryHost()
	st.Domains = cfg.publicDomainSummary()
	st.Configured = cfg.configured()
	st.TokenSet = cfg.tokenSet()
	st.AutoSync = cfg.AutoSync
	st.SyncMode = cfg.SyncMode
	st.SyncTime = cfg.SyncTime
	st.SyncPending = cfg.SyncPending
	st.SyncNextAt = cfg.SyncNextAt
	st.DNSStatus = normalizeCloudflareDNSStatus(st.DNSStatus)
	applyCloudflareLegacyDeploymentStatus(cfg, st)
	st.Published = cloudflareStatusPublished(st)
	st.CanUnpublish = cloudflareCanUnpublish(cfg, st)
	st.CanPurge = cloudflareCanPurge(cfg, st)
	view := &CloudflareView{
		Config:     cfg,
		Status:     st,
		TokenSet:   cfg.tokenSet(),
		Configured: cfg.configured(),
	}
	view.decorate()
	view.TokenTemplateURL = cloudflareAPITokenTemplateURL(view.ProjectName)
	return view
}

func (s *Server) cloudflareClientViewForRequest(r *http.Request) cloudflareClientView {
	return cloudflareClientViewFromView(s.cloudflareViewForRequest(r))
}

func (s *Server) cloudflareJSONState(r *http.Request) (*CloudflareStatus, cloudflareClientView) {
	view := s.cloudflareViewForRequest(r)
	return view.Status, cloudflareClientViewFromView(view)
}

func (s *Server) cloudflareJSONPayload(r *http.Request, ok bool, message string) map[string]any {
	status, view := s.cloudflareJSONState(r)
	return map[string]any{"ok": ok, "message": message, "status": status, "view": view}
}

func (s *Server) cloudflareStatusWithDNSCheck(ctx context.Context) *CloudflareStatus {
	view := s.cloudflareView()
	st := view.Status
	if !cloudflareShouldCheckDNS(view.Config, st) {
		return st
	}
	checkCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	result, err := detectCloudflareDNSStatus(checkCtx, view.Config)
	if err != nil {
		if normalizeCloudflareDNSStatus(st.DNSStatus) == cloudflareDNSStatusManaged {
			return st
		}
		result = cloudflareDNSManualResult("暂时无法自动检测前台域名 DNS；请确认 Token 包含 Zone DNS Read 权限，或发布一次最新内容。")
	}
	if result.Status == "" {
		return st
	}
	prevMessage := st.Message
	if result.Status == cloudflareDNSStatusManaged && strings.TrimSpace(st.LastDeployAt) == "" && strings.Contains(st.Message, "旧版本") {
		st.Message = "Cloudflare 配置已存在；已自动检测到前台域名 DNS 已接管。发布一次最新内容后，会补齐部署时间和部署记录。"
	}
	if normalizeCloudflareDNSStatus(st.DNSStatus) == result.Status && strings.TrimSpace(st.DNSMessage) == strings.TrimSpace(result.Message) && st.Message == prevMessage {
		return st
	}
	st.DNSStatus = result.Status
	st.DNSMessage = result.Message
	s.writeCloudflareStatus(*st)
	return st
}

func cloudflareShouldCheckDNS(cfg CloudflareConfig, st *CloudflareStatus) bool {
	if st == nil || st.Running || !cloudflareStatusPublished(st) {
		return false
	}
	return cfg.configured() && cfg.tokenSet() && strings.TrimSpace(cfg.ZoneID) != ""
}

func cloudflareClientViewFromView(view *CloudflareView) cloudflareClientView {
	if view == nil {
		return cloudflareClientView{}
	}
	cfg := view.Config
	st := view.Status
	published := cloudflareStatusPublished(st)
	publicURL := ""
	if published && view.PrimaryDomain != "" {
		publicURL = "https://" + view.PrimaryDomain
	}
	return cloudflareClientView{
		TokenSet:         view.TokenSet,
		Configured:       view.Configured,
		PrimaryDomain:    view.PrimaryDomain,
		DomainSummary:    view.DomainSummary,
		PublicURL:        publicURL,
		TokenFingerprint: view.TokenFingerprint,
		ProjectName:      view.ProjectName,
		ProjectDefault:   view.ProjectDefault,
		ProjectCustom:    view.ProjectCustom,
		DeployMode:       normalizeCloudflareDeployMode(cfg.DeployMode),
		SourceMode:       normalizeCloudflareSourceMode(cfg.SourceMode),
		AutoSync:         cfg.AutoSync,
		SyncMode:         normalizeCloudflareSyncMode(cfg.SyncMode),
		SyncTime:         normalizeCloudflareSyncTime(cfg.SyncTime),
		SyncPending:      cfg.SyncPending,
		SyncNextAt:       cfg.SyncNextAt,
		CanUnpublish:     cloudflareCanUnpublish(cfg, st),
		CanPurge:         cloudflareCanPurge(cfg, st),
		Published:        published,
		DNSStatus:        normalizeCloudflareDNSStatus(st.DNSStatus),
		DNSMessage:       st.DNSMessage,
	}
}

func cloudflareStatusPublished(st *CloudflareStatus) bool {
	if st == nil {
		return false
	}
	if st.Published {
		return true
	}
	return st.Status == "success" &&
		strings.TrimSpace(st.LastDeployAt) != "" &&
		!strings.Contains(st.Message, "取消")
}

func applyCloudflareLegacyDeploymentStatus(cfg CloudflareConfig, st *CloudflareStatus) {
	if st == nil || st.Running || st.Published || cloudflareStatusPublished(st) {
		return
	}
	if st.Status == "failed" || strings.TrimSpace(st.LastDeployAt) != "" {
		return
	}
	if strings.Contains(st.Message, "取消") {
		return
	}
	if !cfg.configured() || strings.TrimSpace(cfg.AccountID) == "" || strings.TrimSpace(cfg.ZoneID) == "" || strings.TrimSpace(cfg.primaryHost()) == "" {
		return
	}
	st.Published = true
	if st.Status == "" || st.Status == "idle" {
		st.Status = "success"
	}
	msg := strings.TrimSpace(st.Message)
	if msg == "" || msg == "暂无 Cloudflare 部署任务" {
		st.Message = "Cloudflare 配置已存在；旧版本没有记录部署时间。发布一次最新内容后，会自动补齐部署记录并接管前台域名 DNS。"
	}
	if normalizeCloudflareDNSStatus(st.DNSStatus) == "" {
		st.DNSStatus = cloudflareDNSStatusManual
		if strings.TrimSpace(st.DNSMessage) == "" {
			st.DNSMessage = "旧版本未记录 DNS 接管状态；发布最新内容后会自动接管前台域名 DNS。"
		}
	}
}

func cloudflareCanUnpublish(cfg CloudflareConfig, st *CloudflareStatus) bool {
	return cfg.configured() && cloudflareStatusPublished(st) && (st == nil || !st.Running)
}

func cloudflareCanPurge(cfg CloudflareConfig, st *CloudflareStatus) bool {
	return cfg.tokenSet() && strings.TrimSpace(cfg.ZoneID) != "" && cloudflareStatusPublished(st) && (st == nil || !st.Running)
}

func (view *CloudflareView) decorate() {
	if view == nil {
		return
	}
	view.RouteHost = cloudflareRouteHost(view.Config.RoutePattern)
	view.LikelyZoneName = cloudflareLikelyZoneName(view.RouteHost)
	view.PrimaryDomain = view.Config.primaryHost()
	view.DomainSummary = view.Config.publicDomainSummary()
	view.ProjectDefault = cloudflareDefaultProjectNameForHost(view.PrimaryDomain)
	view.ProjectName = view.Config.WorkerName
	if view.Config.usingPages() {
		view.ProjectName = view.Config.PagesProjectName
	}
	if strings.TrimSpace(view.ProjectName) == "" {
		view.ProjectName = view.ProjectDefault
	}
	if !view.TokenSet && view.PrimaryDomain == "" {
		view.ProjectDefault = ""
		view.ProjectName = ""
	}
	view.ProjectCustom = view.ProjectName != "" && view.ProjectName != cloudflareDefaultWorkerName && view.ProjectName != view.ProjectDefault
	aliases := []string{}
	for _, domain := range view.Config.publicDomains() {
		if domain.Primary {
			continue
		}
		if domain.RedirectToPrimary {
			view.RedirectAliases = true
		}
		view.AliasDomainInput = strings.TrimSpace(view.AliasDomainInput + "\n" + domain.Host)
		if domain.RedirectToPrimary {
			aliases = append(aliases, domain.Host+" -> "+view.PrimaryDomain)
		} else {
			aliases = append(aliases, domain.Host)
		}
	}
	view.AliasDomains = strings.Join(aliases, "\n")
	view.TokenFingerprint = cloudflareTokenFingerprint(view.Config.APIToken)
}

func cloudflareTokenFingerprint(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 10 {
		return "****" + token
	}
	return token[:4] + "_****" + token[len(token)-6:]
}

const cloudflareDefaultAPITokenName = "GCMS Cloudflare Deploy"

func cloudflareAPITokenTemplateURL(tokenName string) string {
	permissions := []map[string]string{
		{"key": "workers_scripts", "type": "edit"},
		{"key": "workers_routes", "type": "edit"},
		{"key": "page", "type": "edit"},
		{"key": "dns", "type": "edit"},
		{"key": "cache", "type": "purge"},
		{"key": "zone", "type": "read"},
		{"key": "account_settings", "type": "read"},
	}
	raw, _ := json.Marshal(permissions)
	u := url.URL{Scheme: "https", Host: "dash.cloudflare.com", Path: "/profile/api-tokens"}
	q := u.Query()
	tokenName = strings.TrimSpace(tokenName)
	if tokenName == "" {
		tokenName = cloudflareDefaultAPITokenName
	}
	q.Set("name", tokenName)
	q.Set("accountId", "*")
	q.Set("zoneId", "all")
	q.Set("permissionGroupKeys", string(raw))
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) saveCloudflareConfigFromRequest(r *http.Request) (CloudflareConfig, error) {
	cfg := s.cloudflareConfigForRequest(r)
	if err := r.ParseForm(); err != nil {
		return cfg, err
	}
	prevAccountID := cfg.AccountID
	prevZoneID := cfg.ZoneID
	if _, ok := r.Form["account_id"]; ok {
		cfg.AccountID = strings.TrimSpace(r.FormValue("account_id"))
	} else {
		cfg.AccountID = ""
	}
	if _, ok := r.Form["zone_id"]; ok {
		cfg.ZoneID = strings.TrimSpace(r.FormValue("zone_id"))
	} else {
		cfg.ZoneID = ""
	}
	if cfg.AccountID == "" || cfg.AccountID != prevAccountID {
		cfg.AccountName = ""
	}
	if cfg.ZoneID == "" || cfg.ZoneID != prevZoneID {
		cfg.ZoneName = ""
	}
	submittedToken := strings.TrimSpace(r.FormValue("api_token"))
	tokenToVerify := submittedToken
	if tokenToVerify == "" && r.FormValue("verify_token") == "1" {
		tokenToVerify = strings.TrimSpace(cfg.APIToken)
	}
	if tokenToVerify != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := verifyCloudflareAPIToken(ctx, tokenToVerify); err != nil {
			return cfg, err
		}
	}
	if submittedToken != "" {
		cfg.APIToken = submittedToken
	}
	if _, ok := r.Form["deploy_mode"]; ok {
		cfg.DeployMode = normalizeCloudflareDeployMode(r.FormValue("deploy_mode"))
	}
	cfg.WorkerName = normalizeCloudflareWorkerName(r.FormValue("worker_name"))
	cfg.PagesProjectName = normalizeCloudflarePagesProjectName(r.FormValue("pages_project_name"))
	if _, ok := r.Form["primary_domain"]; ok {
		cfg.Domains = cloudflareDomainsFromForm(r.FormValue("primary_domain"), r.Form["alias_domains"], r.FormValue("redirect_aliases") == "1")
		cfg.RoutePattern = ""
		for _, domain := range cfg.Domains {
			if domain.Primary {
				cfg.RoutePattern = normalizeCloudflareRoutePattern(domain.Host)
				break
			}
		}
	} else if raw := strings.TrimSpace(r.FormValue("route_pattern")); raw != "" || r.FormValue("deploy") != "1" {
		host := normalizeCloudflareDomainHost(raw)
		cfg.RoutePattern = normalizeCloudflareRoutePattern(raw)
		if host != "" {
			cfg.Domains = normalizeCloudflareDomains([]CloudflareDomain{{Host: host, Primary: true}})
		} else {
			cfg.Domains = nil
		}
	}
	if _, ok := r.Form["project_custom"]; ok && r.FormValue("project_custom") != "1" {
		project := cloudflareDefaultProjectNameForHost(cfg.primaryHost())
		cfg.WorkerName = project
		cfg.PagesProjectName = project
	}
	if _, ok := r.Form["source_frontend_mode"]; ok {
		cfg.SourceMode = normalizeCloudflareSourceMode(r.FormValue("source_frontend_mode"))
	}
	if raw := strings.TrimSpace(r.FormValue("origin_url")); raw != "" || r.FormValue("deploy") != "1" {
		cfg.OriginURL = normalizeCloudflareOrigin(raw)
	}
	if values, ok := r.Form["auto_sync"]; ok {
		cfg.AutoSync = formHasValue(values, "1")
	}
	if _, ok := r.Form["sync_mode"]; ok {
		cfg.SyncMode = normalizeCloudflareSyncMode(r.FormValue("sync_mode"))
	}
	if cfg.SyncMode == cloudflareSyncModeManual {
		cfg.AutoSync = false
	}
	if _, ok := r.Form["sync_time"]; ok {
		cfg.SyncTime = normalizeCloudflareSyncTime(r.FormValue("sync_time"))
	}
	if _, ok := r.Form["html_cache_ttl"]; ok {
		ttl, err := strconv.Atoi(strings.TrimSpace(r.FormValue("html_cache_ttl")))
		if err != nil {
			return cfg, errors.New("HTML 缓存时间必须是数字。")
		}
		if ttl < 0 || ttl > 86400 {
			return cfg, errors.New("HTML 缓存时间需要在 0 到 86400 秒之间。")
		}
		cfg.HTMLCacheTTL = ttl
	}
	if cfg.HTMLCacheTTL <= 0 {
		cfg.HTMLCacheTTL = cloudflareDefaultHTMLTTL
	}

	settings := map[string]string{
		cloudflareAccountIDKey:    cfg.AccountID,
		cloudflareDeployModeKey:   cfg.DeployMode,
		cloudflareWorkerNameKey:   cfg.WorkerName,
		cloudflarePagesProjectKey: cfg.PagesProjectName,
		cloudflareZoneIDKey:       cfg.ZoneID,
		cloudflareAccountNameKey:  cfg.AccountName,
		cloudflareZoneNameKey:     cfg.ZoneName,
		cloudflareRoutePatternKey: cfg.RoutePattern,
		cloudflareDomainsKey:      encodeCloudflareDomains(cfg.Domains),
		cloudflareOriginURLKey:    cfg.OriginURL,
		cloudflareHTMLTTLKey:      strconv.Itoa(cfg.HTMLCacheTTL),
		cloudflareAutoSyncKey:     boolSetting(cfg.AutoSync),
		cloudflareSyncModeKey:     normalizeCloudflareSyncMode(cfg.SyncMode),
		cloudflareSyncTimeKey:     normalizeCloudflareSyncTime(cfg.SyncTime),
		cloudflareSourceModeKey:   normalizeCloudflareSourceMode(cfg.SourceMode),
	}
	if strings.TrimSpace(r.FormValue("api_token")) != "" {
		settings[cloudflareAPITokenKey] = cfg.APIToken
	}
	for k, v := range settings {
		if err := s.store.SetSetting(k, v); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func boolSetting(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func (s *Server) adminSaveCloudflare(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cfg, err := s.saveCloudflareConfigFromRequest(r)
	if err != nil {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, s.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if r.FormValue("deploy") == "1" {
		if err := cfg.validateDeploy(); err != nil {
			if jsonReq {
				writeJSON(w, http.StatusBadRequest, s.cloudflareJSONPayload(r, false, err.Error()))
				return
			}
			s.showSettings(w, r, "cloudflare", "", err.Error())
			return
		}
		if err := s.queueCloudflareDeploy(cfg); err != nil {
			if jsonReq {
				writeJSON(w, http.StatusConflict, s.cloudflareJSONPayload(r, false, err.Error()))
				return
			}
			s.showSettings(w, r, "cloudflare", "", err.Error())
			return
		}
		if jsonReq {
			writeJSON(w, http.StatusAccepted, s.cloudflareJSONPayload(r, true, "Cloudflare 配置已保存，部署任务已启动。"))
			return
		}
		s.redirectSettings(w, r, "cloudflare", "Cloudflare 配置已保存，部署任务已启动。")
		return
	}
	if jsonReq {
		writeJSON(w, http.StatusOK, s.cloudflareJSONPayload(r, true, "Cloudflare 部署配置已保存。"))
		return
	}
	s.redirectSettings(w, r, "cloudflare", "Cloudflare 部署配置已保存。")
}

func (s *Server) adminSaveCloudflareSync(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, s.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	cfg := s.cloudflareConfigForRequest(r)
	cfg.SyncMode = normalizeCloudflareSyncMode(r.FormValue("sync_mode"))
	cfg.AutoSync = cfg.SyncMode != cloudflareSyncModeManual
	cfg.SyncTime = normalizeCloudflareSyncTime(r.FormValue("sync_time"))
	settings := map[string]string{
		cloudflareAutoSyncKey: boolSetting(cfg.AutoSync),
		cloudflareSyncModeKey: cfg.SyncMode,
		cloudflareSyncTimeKey: cfg.SyncTime,
	}
	if cfg.SyncMode == cloudflareSyncModeManual {
		settings[cloudflareSyncNextAtKey] = ""
	}
	for k, v := range settings {
		if err := s.store.SetSetting(k, v); err != nil {
			if jsonReq {
				writeJSON(w, http.StatusInternalServerError, s.cloudflareJSONPayload(r, false, err.Error()))
				return
			}
			s.showSettings(w, r, "cloudflare", "", err.Error())
			return
		}
	}
	if cfg.SyncMode == cloudflareSyncModeManual {
		s.stopCloudflareTimer()
	}
	if cfg.SyncPending && cfg.configured() && cfg.SyncMode == cloudflareSyncModeDaily {
		s.armCloudflareDailySync(cfg, "已有内容变化，将按每天同步时间发布静态站。")
	}
	if cfg.SyncPending && cfg.configured() && cfg.SyncMode == cloudflareSyncModeRealtime {
		s.scheduleCloudflareSync("已有内容变化，Cloudflare 静态站将自动重新发布。")
	}
	if jsonReq {
		writeJSON(w, http.StatusOK, s.cloudflareJSONPayload(r, true, "内容同步规则已保存。"))
		return
	}
	s.redirectSettings(w, r, "cloudflare", "内容同步规则已保存。")
}

func (s *Server) queueCloudflareDeploy(cfg CloudflareConfig) error {
	st := s.readCloudflareStatus()
	if st.Running {
		return errors.New("已有 Cloudflare 部署任务正在运行。")
	}
	published := cloudflareStatusPublished(st)
	s.writeCloudflareStatus(CloudflareStatus{
		Status:           "running",
		Step:             "queued",
		Message:          "部署任务已启动，正在连接 Cloudflare。",
		DeployMode:       cfg.DeployMode,
		WorkerName:       cfg.WorkerName,
		PagesProjectName: cfg.PagesProjectName,
		RoutePattern:     cfg.RoutePattern,
		PrimaryDomain:    cfg.primaryHost(),
		Domains:          cfg.publicDomainSummary(),
		Configured:       cfg.configured(),
		TokenSet:         cfg.tokenSet(),
		AutoSync:         cfg.AutoSync,
		DNSStatus:        st.DNSStatus,
		DNSMessage:       st.DNSMessage,
		Published:        published,
	})
	go func() {
		defer func() {
			if v := recover(); v != nil {
				s.writeCloudflareStatus(s.cloudflareStatusFailed(cfg, "failed", fmt.Sprintf("Cloudflare 部署任务异常中断：%v", v)))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), cloudflareAPITimeout)
		defer cancel()
		if err := s.deployCloudflare(ctx, cfg); err != nil {
			s.writeCloudflareStatus(s.cloudflareStatusFailed(cfg, "failed", err.Error()))
		}
	}()
	return nil
}

func (s *Server) queueCloudflareUnpublish(cfg CloudflareConfig) error {
	st := s.readCloudflareStatus()
	if st.Running {
		return errors.New("已有 Cloudflare 部署任务正在运行。")
	}
	published := cloudflareStatusPublished(st)
	s.writeCloudflareStatus(CloudflareStatus{
		Status:           "running",
		Step:             "route",
		Message:          "正在取消 Cloudflare 公开入口绑定。",
		DeployMode:       cfg.DeployMode,
		WorkerName:       cfg.WorkerName,
		PagesProjectName: cfg.PagesProjectName,
		RoutePattern:     cfg.RoutePattern,
		PrimaryDomain:    cfg.primaryHost(),
		Domains:          cfg.publicDomainSummary(),
		Configured:       cfg.configured(),
		TokenSet:         cfg.tokenSet(),
		AutoSync:         cfg.AutoSync,
		DNSStatus:        st.DNSStatus,
		DNSMessage:       st.DNSMessage,
		Published:        published,
	})
	go func() {
		defer func() {
			if v := recover(); v != nil {
				s.writeCloudflareStatus(s.cloudflareStatusFailed(cfg, "failed", fmt.Sprintf("Cloudflare 取消部署异常中断：%v", v)))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), cloudflareAPITimeout)
		defer cancel()
		if err := s.unpublishCloudflare(ctx, cfg); err != nil {
			s.writeCloudflareStatus(s.cloudflareStatusFailed(cfg, "failed", err.Error()))
		}
	}()
	return nil
}

func (s *Server) prepareCloudflareAPIConfig(ctx context.Context, cfg CloudflareConfig) (CloudflareConfig, error) {
	if strings.TrimSpace(cfg.APIToken) != "" {
		cfg.APIToken = strings.TrimSpace(cfg.APIToken)
	} else {
		return cfg, errors.New("请先粘贴 Cloudflare API Token。")
	}
	var detectErr error
	if strings.TrimSpace(cfg.AccountID) == "" || (strings.TrimSpace(cfg.ZoneID) == "" && strings.TrimSpace(cfg.RoutePattern) != "") {
		target, err := discoverCloudflareTarget(ctx, cfg.APIToken, cfg.RoutePattern)
		if err != nil {
			detectErr = err
		}
		if cfg.AccountID == "" && target.AccountID != "" {
			cfg.AccountID = target.AccountID
			cfg.AccountName = target.AccountName
		}
		if cfg.ZoneID == "" && target.ZoneID != "" {
			cfg.ZoneID = target.ZoneID
			cfg.ZoneName = target.ZoneName
			if cfg.AccountID == "" {
				cfg.AccountID = target.AccountID
				cfg.AccountName = target.AccountName
			}
		}
		_ = s.saveCloudflareDetectedTarget(cfg)
	}
	if strings.TrimSpace(cfg.AccountID) == "" {
		return cfg, errors.New("无法自动识别 Cloudflare Account ID。请确认 Token 权限包含 Account Settings Read，或在高级设置里手动填写 Account ID。")
	}
	if strings.TrimSpace(cfg.RoutePattern) != "" && strings.TrimSpace(cfg.ZoneID) == "" {
		return cfg, cloudflareZoneDetectError(cfg.RoutePattern, detectErr)
	}
	return cfg, nil
}

func cloudflareZoneDetectError(routePattern string, detectErr error) error {
	host := cloudflareRouteHost(routePattern)
	zoneHint := cloudflareLikelyZoneName(host)
	var b strings.Builder
	if host != "" {
		fmt.Fprintf(&b, "无法自动识别 %s 对应的 Cloudflare Zone ID。", host)
	} else {
		b.WriteString("无法自动识别前台访问域名对应的 Cloudflare Zone ID。")
	}
	if zoneHint != "" && zoneHint != host {
		fmt.Fprintf(&b, " 这个路由通常属于 %s 这个 Zone；创建 Token 时请在域名范围里选择它。", zoneHint)
	}
	b.WriteString(" 请确认 Token 权限包含 Zone Read，或在高级设置里手动填写 Zone ID。")
	if detectErr != nil {
		fmt.Fprintf(&b, " Cloudflare 返回：%s", detectErr.Error())
	}
	return errors.New(b.String())
}

func (s *Server) saveCloudflareDetectedTarget(cfg CloudflareConfig) error {
	settings := map[string]string{
		cloudflareAccountIDKey:   strings.TrimSpace(cfg.AccountID),
		cloudflareAccountNameKey: strings.TrimSpace(cfg.AccountName),
		cloudflareZoneIDKey:      strings.TrimSpace(cfg.ZoneID),
		cloudflareZoneNameKey:    strings.TrimSpace(cfg.ZoneName),
	}
	for k, v := range settings {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if err := s.store.SetSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

func discoverCloudflareTarget(ctx context.Context, token, routePattern string) (cloudflareDetectedTarget, error) {
	var target cloudflareDetectedTarget
	var zoneErr error
	zoneLookupOK := false
	host := cloudflareRouteHost(routePattern)
	zones, err := listCloudflareZones(ctx, token)
	if err == nil {
		zoneLookupOK = true
		if zone := matchCloudflareZone(host, zones); zone.ID != "" {
			target.ZoneID = zone.ID
			target.ZoneName = zone.Name
			target.AccountID = zone.Account.ID
			target.AccountName = zone.Account.Name
			return target, nil
		}
		if host == "" && len(zones) == 1 {
			zone := zones[0]
			target.ZoneID = zone.ID
			target.ZoneName = zone.Name
			target.AccountID = zone.Account.ID
			target.AccountName = zone.Account.Name
			return target, nil
		}
	} else {
		zoneErr = err
	}
	for _, name := range cloudflareZoneNameCandidates(host) {
		zones, err := listCloudflareZonesByName(ctx, token, name)
		if err != nil {
			if zoneErr == nil {
				zoneErr = err
			}
			continue
		}
		zoneLookupOK = true
		if zone := matchCloudflareZone(host, zones); zone.ID != "" {
			target.ZoneID = zone.ID
			target.ZoneName = zone.Name
			target.AccountID = zone.Account.ID
			target.AccountName = zone.Account.Name
			return target, nil
		}
	}
	accounts, err := listCloudflareAccounts(ctx, token)
	if err != nil {
		if zoneErr != nil {
			return target, zoneErr
		}
		return target, nil
	}
	if len(accounts) == 1 {
		target.AccountID = accounts[0].ID
		target.AccountName = accounts[0].Name
	}
	if host != "" && zoneErr == nil {
		if !zoneLookupOK {
			return target, errors.New("Cloudflare 没有返回可读取的 Zone 数据。")
		}
		return target, cloudflareNoMatchingZoneError(host)
	}
	return target, zoneErr
}

func cloudflareNoMatchingZoneError(host string) error {
	host = strings.TrimSpace(host)
	zoneHint := cloudflareLikelyZoneName(host)
	if zoneHint != "" && zoneHint != host {
		return fmt.Errorf("Cloudflare 当前 Token 看不到 %s 这个 Zone；%s 通常属于它。请在创建 Token 时把 Zone Resources 选到 %s，或在高级设置里手动填写 Zone ID。", zoneHint, host, zoneHint)
	}
	if host != "" {
		return fmt.Errorf("Cloudflare 当前 Token 看不到 %s 对应的 Zone。请确认这个域名已接入 Cloudflare，并且 Token 的 Zone Resources 包含它。", host)
	}
	return errors.New("Cloudflare 当前 Token 看不到可用的 Zone。")
}

func listCloudflareZones(ctx context.Context, token string) ([]cloudflareZone, error) {
	return listCloudflareZonesWithPath(ctx, token, "/zones?per_page=50")
}

func listCloudflareZonesByName(ctx context.Context, token, name string) ([]cloudflareZone, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	return listCloudflareZonesWithPath(ctx, token, "/zones?per_page=50&name="+url.QueryEscape(name))
}

func listCloudflareZonesWithPath(ctx context.Context, token, path string) ([]cloudflareZone, error) {
	result, err := cloudflareAPIRequest(ctx, token, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	var zones []cloudflareZone
	if len(result) > 0 {
		if err := json.Unmarshal(result, &zones); err != nil {
			return nil, err
		}
	}
	return zones, nil
}

func cloudflareZoneNameCandidates(host string) []string {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return nil
	}
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return []string{host}
	}
	out := make([]string, 0, len(parts)-1)
	seen := map[string]bool{}
	for i := 0; i <= len(parts)-2; i++ {
		name := strings.Join(parts[i:], ".")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func cloudflareLikelyZoneName(host string) string {
	candidates := cloudflareZoneNameCandidates(host)
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return candidates[1]
}

func listCloudflareAccounts(ctx context.Context, token string) ([]cloudflareAccount, error) {
	result, err := cloudflareAPIRequest(ctx, token, http.MethodGet, "/accounts?per_page=50", nil, "")
	if err != nil {
		return nil, err
	}
	var accounts []cloudflareAccount
	if len(result) > 0 {
		if err := json.Unmarshal(result, &accounts); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}

func cloudflareRouteHost(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ""
	}
	if strings.Contains(pattern, "://") {
		if u, err := url.Parse(pattern); err == nil {
			pattern = u.Host
		}
	}
	if i := strings.Index(pattern, "/"); i >= 0 {
		pattern = pattern[:i]
	}
	pattern = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(pattern)), "*.")
	if i := strings.LastIndex(pattern, ":"); i > -1 {
		pattern = pattern[:i]
	}
	return pattern
}

func matchCloudflareZone(host string, zones []cloudflareZone) cloudflareZone {
	host = strings.TrimSpace(strings.ToLower(host))
	var best cloudflareZone
	for _, zone := range zones {
		name := strings.ToLower(strings.TrimSpace(zone.Name))
		if name == "" {
			continue
		}
		if host == name || strings.HasSuffix(host, "."+name) || (host == "" && best.ID == "") {
			if len(name) > len(best.Name) {
				best = zone
			}
		}
	}
	return best
}

func (s *Server) adminCloudflareStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cloudflareStatusWithDNSCheck(r.Context()))
}

func (s *Server) adminStartCloudflareDeploy(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cfg := s.cloudflareConfigForRequest(r)
	if err := cfg.validateDeploy(); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, s.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if err := s.queueCloudflareDeploy(cfg); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusConflict, s.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if jsonReq {
		writeJSON(w, http.StatusAccepted, s.cloudflareJSONPayload(r, true, "Cloudflare 部署任务已启动。"))
		return
	}
	s.redirectSettings(w, r, "cloudflare", "Cloudflare 部署任务已启动，请稍后刷新状态。")
}

// adminPlatformSiteCloudflareDeploy 在平台「站点管理」页为指定站点触发一次 Cloudflare 重新发布，
// 在该站点自己的运行时（rt.server）上下文里执行，从而使用该站点的配置、内容与部署状态文件。
func (s *Server) adminPlatformSiteCloudflareDeploy(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		if jsonReq {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "非平台模式，无法按站点部署。"})
			return
		}
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "无效的站点编号。"})
			return
		}
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	rt, ok := s.runtimePool().runtimeByID(id)
	if !ok || rt == nil || rt.server == nil {
		if jsonReq {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "站点不存在或未就绪。"})
			return
		}
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	cfg := rt.server.cloudflareConfig()
	if err := cfg.validateDeploy(); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, rt.server.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	if err := rt.server.queueCloudflareDeploy(cfg); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusConflict, rt.server.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	if jsonReq {
		writeJSON(w, http.StatusAccepted, rt.server.cloudflareJSONPayload(r, true, "Cloudflare 部署任务已启动。"))
		return
	}
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

// adminPlatformSiteCloudflareStatus 供「站点管理」卡片轮询指定站点的 Cloudflare 部署状态，
// 用于把徽标从「部署中」切换回最近部署时间。只读，无副作用。
func (s *Server) adminPlatformSiteCloudflareStatus(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false})
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false})
		return
	}
	rt, ok := s.runtimePool().runtimeByID(id)
	if !ok || rt == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false})
		return
	}
	status, lastDeployAt := "", ""
	if st := readCloudflareStatusFile(cloudflareStatusPathForRuntime(rt)); st != nil {
		status = strings.TrimSpace(st.Status)
		lastDeployAt = strings.TrimSpace(st.LastDeployAt)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"status":       status,
		"running":      status == "running",
		"lastDeployAt": lastDeployAt,
	})
}

func (s *Server) adminStartCloudflareUnpublish(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cfg := s.cloudflareConfigForRequest(r)
	if err := cfg.validateDeploy(); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, s.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if err := s.queueCloudflareUnpublish(cfg); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusConflict, s.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if jsonReq {
		writeJSON(w, http.StatusAccepted, s.cloudflareJSONPayload(r, true, "正在取消 Cloudflare 公开部署。"))
		return
	}
	s.redirectSettings(w, r, "cloudflare", "正在取消 Cloudflare 公开部署。")
}

func (s *Server) adminCloudflarePurge(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cfg := s.cloudflareConfig()
	if !cfg.tokenSet() {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, s.cloudflareJSONPayload(r, false, "清除缓存需要先粘贴 Cloudflare API Token。"))
			return
		}
		s.showSettings(w, r, "cloudflare", "", "清除缓存需要先粘贴 Cloudflare API Token。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.purgeCloudflareCache(ctx, cfg, "手动清除 Cloudflare 缓存完成。"); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, s.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if jsonReq {
		writeJSON(w, http.StatusOK, s.cloudflareJSONPayload(r, true, "Cloudflare 缓存已清除。"))
		return
	}
	s.redirectSettings(w, r, "cloudflare", "Cloudflare 缓存已清除。")
}

func (s *Server) adminCloudflareReset(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := s.clearCloudflareBinding(); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusInternalServerError, s.cloudflareJSONPayload(r, false, err.Error()))
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	s.writeCloudflareStatus(CloudflareStatus{Status: "idle", Step: "", Message: "Cloudflare 绑定已清空。", History: []CloudflareStatusHistory{}})
	if jsonReq {
		writeJSON(w, http.StatusOK, s.cloudflareJSONPayload(r, true, "Cloudflare 绑定已清空。"))
		return
	}
	s.redirectSettings(w, r, "cloudflare", "Cloudflare 绑定已清空。")
}

func cloudflareBindingSettingKeys() []string {
	return []string{
		cloudflareAPITokenKey,
		cloudflareDeployModeKey,
		cloudflareWorkerNameKey,
		cloudflarePagesProjectKey,
		cloudflareRoutePatternKey,
		cloudflareDomainsKey,
		cloudflareSourceModeKey,
		cloudflareAccountIDKey,
		cloudflareAccountNameKey,
		cloudflareZoneIDKey,
		cloudflareZoneNameKey,
		cloudflareAutoSyncKey,
		cloudflareSyncModeKey,
		cloudflareSyncTimeKey,
		cloudflareSyncPendingKey,
		cloudflareSyncNextAtKey,
	}
}

func (s *Server) clearCloudflareBinding() error {
	s.stopCloudflareTimer()
	for _, key := range cloudflareBindingSettingKeys() {
		if err := s.store.SetSetting(key, ""); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deployCloudflare(ctx context.Context, cfg CloudflareConfig) error {
	initialStatus := s.readCloudflareStatus()
	wasPublished := cloudflareStatusPublished(initialStatus)
	lastDeployAt := initialStatus.LastDeployAt
	setStep := func(step, msg string) {
		s.writeCloudflareStatus(CloudflareStatus{
			Status:           "running",
			Step:             step,
			Message:          msg,
			DeployMode:       cfg.DeployMode,
			WorkerName:       cfg.WorkerName,
			PagesProjectName: cfg.PagesProjectName,
			RoutePattern:     cfg.RoutePattern,
			PrimaryDomain:    cfg.primaryHost(),
			Domains:          cfg.publicDomainSummary(),
			Configured:       cfg.configured(),
			TokenSet:         cfg.tokenSet(),
			AutoSync:         cfg.AutoSync,
			DNSStatus:        initialStatus.DNSStatus,
			DNSMessage:       initialStatus.DNSMessage,
			Published:        wasPublished,
			LastDeployAt:     lastDeployAt,
		})
	}
	lastPurgeAt := ""
	setStep("detect", "正在识别 Cloudflare 账号和域名。")
	var err error
	cfg, err = s.prepareCloudflareAPIConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("获取 Cloudflare 授权失败：%w", err)
	}
	cfg = s.cloudflareStaticRuntimeConfig(cfg)
	setStep("export", "正在导出前台静态站。")
	exported, err := s.exportStaticSite(ctx, cfg)
	if err != nil {
		return fmt.Errorf("导出静态站失败：%w", err)
	}
	defer os.RemoveAll(exported.Dir)
	if cfg.usingPages() {
		setStep("assets", fmt.Sprintf("正在上传 %d 个静态文件到 Cloudflare Pages。", exported.Count))
		dnsResult, err := deployCloudflarePagesStaticSite(ctx, cfg, exported, setStep)
		if err != nil {
			return err
		}
		if cfg.ZoneID != "" {
			setStep("purge", "正在清理 Cloudflare 缓存。")
			if err := purgeCloudflareEverything(ctx, cfg); err != nil {
				return fmt.Errorf("清理 Cloudflare 缓存失败：%w", err)
			}
			lastPurgeAt = time.Now().UTC().Format(time.RFC3339)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		s.clearCloudflareSyncPending()
		s.writeCloudflareStatus(s.withCloudflareHistory(CloudflareStatus{
			Status:           "success",
			Step:             "done",
			Message:          fmt.Sprintf("Cloudflare Pages 静态站已部署：%d 个文件已上传，项目 %s 已发布。", exported.Count, cfg.PagesProjectName),
			DeployMode:       cfg.DeployMode,
			WorkerName:       cfg.WorkerName,
			PagesProjectName: cfg.PagesProjectName,
			RoutePattern:     cfg.RoutePattern,
			PrimaryDomain:    cfg.primaryHost(),
			Domains:          cfg.publicDomainSummary(),
			UpdatedAt:        now,
			LastDeployAt:     now,
			LastPurgeAt:      lastPurgeAt,
			Configured:       cfg.configured(),
			TokenSet:         cfg.tokenSet(),
			AutoSync:         cfg.AutoSync,
			DNSStatus:        dnsResult.Status,
			DNSMessage:       dnsResult.Message,
			Published:        true,
		}, "deploy"))
		return nil
	}

	setStep("assets", fmt.Sprintf("正在上传 %d 个静态文件到 Cloudflare Worker Assets。", exported.Count))
	assetsJWT, err := uploadCloudflareStaticAssets(ctx, cfg, exported)
	if err != nil {
		return fmt.Errorf("上传静态资源失败：%w", err)
	}
	setStep("worker", "正在发布静态站 Worker。")
	if err := uploadCloudflareWorker(ctx, cfg, assetsJWT); err != nil {
		return fmt.Errorf("上传 Worker 失败：%w", err)
	}
	dnsResult := cloudflareDNSManualResult("静态站已发布，但域名 DNS 尚未确认接管。")
	if cfg.ZoneID != "" && len(cfg.routePatterns()) > 0 {
		setStep("route", "正在绑定 Worker 路由。")
		if err := ensureCloudflareRoutes(ctx, cfg); err != nil {
			return fmt.Errorf("绑定 Worker 路由失败：%w", err)
		}
		setStep("dns", "正在将前台域名接管到 Cloudflare 托管入口。")
		dnsResult, err = ensureCloudflareDNSRecords(ctx, cfg)
		if err != nil {
			return fmt.Errorf("绑定 DNS 失败：%w", err)
		}
		setStep("purge", "正在清理 Cloudflare 缓存。")
		if err := purgeCloudflareEverything(ctx, cfg); err != nil {
			return fmt.Errorf("清理 Cloudflare 缓存失败：%w", err)
		}
		lastPurgeAt = time.Now().UTC().Format(time.RFC3339)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if dnsResult.Status == "" {
		dnsResult = cloudflareDNSManualResult("静态站已发布，但域名 DNS 尚未确认接管。")
	}
	s.clearCloudflareSyncPending()
	s.writeCloudflareStatus(s.withCloudflareHistory(CloudflareStatus{
		Status:           "success",
		Step:             "done",
		Message:          fmt.Sprintf("Cloudflare 静态站已部署：%d 个文件已上传，前台由 Worker Assets 托管。", exported.Count),
		DeployMode:       cfg.DeployMode,
		WorkerName:       cfg.WorkerName,
		PagesProjectName: cfg.PagesProjectName,
		RoutePattern:     cfg.RoutePattern,
		PrimaryDomain:    cfg.primaryHost(),
		Domains:          cfg.publicDomainSummary(),
		UpdatedAt:        now,
		LastDeployAt:     now,
		LastPurgeAt:      lastPurgeAt,
		Configured:       cfg.configured(),
		TokenSet:         cfg.tokenSet(),
		AutoSync:         cfg.AutoSync,
		DNSStatus:        dnsResult.Status,
		DNSMessage:       dnsResult.Message,
		Published:        true,
	}, "deploy"))
	return nil
}

func (s *Server) scheduleCloudflareSync(reason string) {
	cfg := s.cloudflareConfig()
	if !cfg.configured() {
		return
	}
	if normalizeCloudflareSyncMode(cfg.SyncMode) == cloudflareSyncModeManual || !cfg.AutoSync {
		_ = s.store.SetSetting(cloudflareSyncPendingKey, "1")
		_ = s.store.SetSetting(cloudflareSyncNextAtKey, "")
		s.stopCloudflareTimer()
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "内容已更新，Cloudflare 静态站将自动重新发布。"
	}
	if normalizeCloudflareSyncMode(cfg.SyncMode) == cloudflareSyncModeDaily {
		s.queueCloudflareDailySync(cfg, reason)
		return
	}
	next := time.Now().Add(25 * time.Second)
	_ = s.store.SetSetting(cloudflareSyncPendingKey, "1")
	_ = s.store.SetSetting(cloudflareSyncNextAtKey, next.UTC().Format(time.RFC3339))
	s.cloudflareMu.Lock()
	if s.cloudflareTimer != nil {
		s.cloudflareTimer.Stop()
	}
	msg := reason
	s.cloudflareTimer = time.AfterFunc(25*time.Second, func() {
		prev := s.readCloudflareStatus()
		s.writeCloudflareStatus(CloudflareStatus{
			Status:           "running",
			Step:             "queued",
			Message:          msg,
			DeployMode:       cfg.DeployMode,
			WorkerName:       cfg.WorkerName,
			PagesProjectName: cfg.PagesProjectName,
			RoutePattern:     cfg.RoutePattern,
			PrimaryDomain:    cfg.primaryHost(),
			Domains:          cfg.publicDomainSummary(),
			Configured:       cfg.configured(),
			TokenSet:         cfg.tokenSet(),
			AutoSync:         cfg.AutoSync,
			SyncMode:         cfg.SyncMode,
			SyncTime:         cfg.SyncTime,
			SyncPending:      true,
			SyncNextAt:       next.UTC().Format(time.RFC3339),
			Published:        cloudflareStatusPublished(prev),
			LastDeployAt:     prev.LastDeployAt,
		})
		ctx, cancel := context.WithTimeout(context.Background(), cloudflareAPITimeout)
		defer cancel()
		if err := s.deployCloudflare(ctx, cfg); err != nil {
			s.writeCloudflareStatus(s.cloudflareStatusFailed(cfg, "failed", err.Error()))
		}
	})
	s.cloudflareMu.Unlock()
}

func (s *Server) stopCloudflareTimer() {
	s.cloudflareMu.Lock()
	if s.cloudflareTimer != nil {
		s.cloudflareTimer.Stop()
		s.cloudflareTimer = nil
	}
	s.cloudflareMu.Unlock()
}

func (s *Server) queueCloudflareDailySync(cfg CloudflareConfig, reason string) {
	next := cloudflareNextDailySyncAt(time.Now(), cfg.SyncTime)
	_ = s.store.SetSetting(cloudflareSyncPendingKey, "1")
	_ = s.store.SetSetting(cloudflareSyncNextAtKey, next.UTC().Format(time.RFC3339))
	s.armCloudflareDailySyncAt(cfg, reason, next)
}

func (s *Server) armCloudflareDailySync(cfg CloudflareConfig, reason string) {
	next := cloudflareNextDailySyncAt(time.Now(), cfg.SyncTime)
	_ = s.store.SetSetting(cloudflareSyncNextAtKey, next.UTC().Format(time.RFC3339))
	s.armCloudflareDailySyncAt(cfg, reason, next)
}

func (s *Server) armCloudflareDailySyncAt(cfg CloudflareConfig, reason string, next time.Time) {
	if strings.TrimSpace(reason) == "" {
		reason = "有内容变化，正在按每天同步规则重新发布 Cloudflare 静态站。"
	}
	delay := time.Until(next)
	if delay < time.Second {
		delay = time.Second
	}
	s.cloudflareMu.Lock()
	if s.cloudflareTimer != nil {
		s.cloudflareTimer.Stop()
	}
	msg := reason
	s.cloudflareTimer = time.AfterFunc(delay, func() {
		s.runCloudflareDailySync(msg)
	})
	s.cloudflareMu.Unlock()
}

func (s *Server) runCloudflareDailySync(reason string) {
	cfg := s.cloudflareConfig()
	if !cfg.AutoSync || !cfg.configured() || normalizeCloudflareSyncMode(cfg.SyncMode) != cloudflareSyncModeDaily || !cfg.SyncPending {
		return
	}
	prev := s.readCloudflareStatus()
	if prev.Running {
		s.queueCloudflareDailySync(cfg, reason)
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "有内容变化，正在按每天同步规则重新发布 Cloudflare 静态站。"
	}
	s.writeCloudflareStatus(CloudflareStatus{
		Status:           "running",
		Step:             "queued",
		Message:          reason,
		DeployMode:       cfg.DeployMode,
		WorkerName:       cfg.WorkerName,
		PagesProjectName: cfg.PagesProjectName,
		RoutePattern:     cfg.RoutePattern,
		PrimaryDomain:    cfg.primaryHost(),
		Domains:          cfg.publicDomainSummary(),
		Configured:       cfg.configured(),
		TokenSet:         cfg.tokenSet(),
		AutoSync:         cfg.AutoSync,
		SyncMode:         cfg.SyncMode,
		SyncTime:         cfg.SyncTime,
		SyncPending:      true,
		SyncNextAt:       cfg.SyncNextAt,
		DNSStatus:        prev.DNSStatus,
		DNSMessage:       prev.DNSMessage,
		Published:        cloudflareStatusPublished(prev),
		LastDeployAt:     prev.LastDeployAt,
	})
	ctx, cancel := context.WithTimeout(context.Background(), cloudflareAPITimeout)
	defer cancel()
	if err := s.deployCloudflare(ctx, cfg); err != nil {
		s.writeCloudflareStatus(s.cloudflareStatusFailed(cfg, "failed", err.Error()))
		s.queueCloudflareDailySync(cfg, reason)
	}
}

func (s *Server) clearCloudflareSyncPending() {
	_ = s.store.SetSetting(cloudflareSyncPendingKey, "")
	_ = s.store.SetSetting(cloudflareSyncNextAtKey, "")
}

func (s *Server) resumeCloudflareSync() {
	cfg := s.cloudflareConfig()
	if cfg.AutoSync && cfg.configured() && cfg.SyncPending && normalizeCloudflareSyncMode(cfg.SyncMode) == cloudflareSyncModeDaily {
		s.armCloudflareDailySync(cfg, "已有内容变化，将按每天同步时间发布静态站。")
	}
}

func (s *Server) purgeCloudflareCache(ctx context.Context, cfg CloudflareConfig, message string) error {
	prev := s.readCloudflareStatus()
	var err error
	cfg, err = s.prepareCloudflareAPIConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("获取 Cloudflare 授权失败：%w", err)
	}
	if strings.TrimSpace(cfg.ZoneID) == "" {
		return errors.New("清理 Cloudflare 缓存需要先识别或填写 Zone ID。")
	}
	if err := purgeCloudflareEverything(ctx, cfg); err != nil {
		return fmt.Errorf("清理 Cloudflare 缓存失败：%w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.writeCloudflareStatus(s.withCloudflareHistory(CloudflareStatus{
		Status:           "success",
		Step:             "purge",
		Message:          message,
		DeployMode:       cfg.DeployMode,
		WorkerName:       cfg.WorkerName,
		PagesProjectName: cfg.PagesProjectName,
		RoutePattern:     cfg.RoutePattern,
		PrimaryDomain:    cfg.primaryHost(),
		Domains:          cfg.publicDomainSummary(),
		LastDeployAt:     prev.LastDeployAt,
		LastPurgeAt:      now,
		Configured:       cfg.configured(),
		TokenSet:         cfg.tokenSet(),
		AutoSync:         cfg.AutoSync,
		DNSStatus:        prev.DNSStatus,
		DNSMessage:       prev.DNSMessage,
		Published:        cloudflareStatusPublished(prev),
	}, "purge"))
	return nil
}

func (s *Server) unpublishCloudflare(ctx context.Context, cfg CloudflareConfig) error {
	var err error
	cfg, err = s.prepareCloudflareAPIConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("获取 Cloudflare 授权失败：%w", err)
	}
	if cfg.usingPages() {
		if err := deleteCloudflarePagesDomains(ctx, cfg); err != nil {
			return fmt.Errorf("解绑 Pages 自定义域名失败：%w", err)
		}
	} else {
		if err := deleteCloudflareRoutes(ctx, cfg); err != nil {
			return fmt.Errorf("解绑 Worker 路由失败：%w", err)
		}
	}
	lastPurgeAt := ""
	if strings.TrimSpace(cfg.ZoneID) != "" {
		if err := purgeCloudflareEverything(ctx, cfg); err != nil {
			return fmt.Errorf("清理 Cloudflare 缓存失败：%w", err)
		}
		lastPurgeAt = time.Now().UTC().Format(time.RFC3339)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.writeCloudflareStatus(s.withCloudflareHistory(CloudflareStatus{
		Status:           "success",
		Step:             "done",
		Message:          "Cloudflare 公开入口已取消；项目和静态资源仍保留在 Cloudflare，DNS 未被删除。",
		DeployMode:       cfg.DeployMode,
		WorkerName:       cfg.WorkerName,
		PagesProjectName: cfg.PagesProjectName,
		RoutePattern:     cfg.RoutePattern,
		PrimaryDomain:    cfg.primaryHost(),
		Domains:          cfg.publicDomainSummary(),
		UpdatedAt:        now,
		LastPurgeAt:      lastPurgeAt,
		Configured:       cfg.configured(),
		TokenSet:         cfg.tokenSet(),
		AutoSync:         cfg.AutoSync,
		Published:        false,
	}, "unpublish"))
	return nil
}

func deployCloudflarePagesStaticSite(ctx context.Context, cfg CloudflareConfig, exported *staticExportResult, setStep func(string, string)) (cloudflareDNSManageResult, error) {
	setStep("worker", "正在准备 Cloudflare Pages 项目。")
	project, err := ensureCloudflarePagesProject(ctx, cfg)
	if err != nil {
		return cloudflareDNSManageResult{}, fmt.Errorf("准备 Cloudflare Pages 项目失败：%w", err)
	}
	setStep("assets", fmt.Sprintf("正在上传 %d 个静态文件到 Pages。", exported.Count))
	manifest, err := uploadCloudflarePagesAssets(ctx, cfg, exported)
	if err != nil {
		return cloudflareDNSManageResult{}, fmt.Errorf("上传 Pages 静态资源失败：%w", err)
	}
	setStep("worker", "正在发布 Cloudflare Pages 静态站。")
	if _, err := createCloudflarePagesDeployment(ctx, cfg, manifest); err != nil {
		return cloudflareDNSManageResult{}, fmt.Errorf("发布 Cloudflare Pages 失败：%w", err)
	}
	if cfg.ZoneID != "" && len(cfg.routePatterns()) > 0 {
		setStep("route", "正在绑定 Pages 自定义域名。")
		if err := ensureCloudflarePagesDomains(ctx, cfg); err != nil {
			return cloudflareDNSManageResult{}, fmt.Errorf("绑定 Pages 自定义域名失败：%w", err)
		}
		setStep("dns", "正在将前台域名接管到 Pages 托管入口。")
		target := strings.TrimSpace(project.Subdomain)
		if target == "" {
			target = cfg.PagesProjectName + ".pages.dev"
		}
		if err := ensureCloudflarePagesDNSRecords(ctx, cfg, target); err != nil {
			return cloudflareDNSManageResult{}, fmt.Errorf("绑定 Pages DNS 失败：%w", err)
		}
		return cloudflareDNSManagedResult("前台域名 DNS 已切换为 Cloudflare Pages 托管入口。"), nil
	}
	return cloudflareDNSManualResult("静态站已发布，但域名 DNS 尚未确认接管。"), nil
}

func ensureCloudflarePagesProject(ctx context.Context, cfg CloudflareConfig) (cloudflarePagesProject, error) {
	project, err := getCloudflarePagesProject(ctx, cfg)
	if err == nil {
		if project.ProjectName == "" {
			project.ProjectName = cfg.PagesProjectName
		}
		if project.Name == "" {
			project.Name = project.ProjectName
		}
		return project, nil
	}
	if !cloudflareHasErrorCode(err, 8000007) && !cloudflareErrorContains(err, "not found") {
		return cloudflarePagesProject{}, cloudflareStagePermissionError("pages", err)
	}
	return createCloudflarePagesProject(ctx, cfg)
}

func getCloudflarePagesProject(ctx context.Context, cfg CloudflareConfig) (cloudflarePagesProject, error) {
	path := fmt.Sprintf("/accounts/%s/pages/projects/%s", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.PagesProjectName))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodGet, path, nil, "")
	if err != nil {
		return cloudflarePagesProject{}, err
	}
	var project cloudflarePagesProject
	if len(result) > 0 {
		if err := json.Unmarshal(result, &project); err != nil {
			return cloudflarePagesProject{}, err
		}
	}
	return project, nil
}

func createCloudflarePagesProject(ctx context.Context, cfg CloudflareConfig) (cloudflarePagesProject, error) {
	body, _ := json.Marshal(map[string]any{
		"name":              cfg.PagesProjectName,
		"production_branch": "main",
	})
	path := fmt.Sprintf("/accounts/%s/pages/projects", url.PathEscape(cfg.AccountID))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, bytes.NewReader(body), "application/json")
	if err != nil {
		return cloudflarePagesProject{}, cloudflareStagePermissionError("pages", err)
	}
	var project cloudflarePagesProject
	if len(result) > 0 {
		if err := json.Unmarshal(result, &project); err != nil {
			return cloudflarePagesProject{}, err
		}
	}
	if project.ProjectName == "" {
		project.ProjectName = cfg.PagesProjectName
	}
	if project.Name == "" {
		project.Name = project.ProjectName
	}
	return project, nil
}

func uploadCloudflarePagesAssets(ctx context.Context, cfg CloudflareConfig, exported *staticExportResult) (map[string]string, error) {
	jwt, err := cloudflarePagesUploadJWT(ctx, cfg)
	if err != nil {
		return nil, err
	}
	hashes := sortedStaticFileHashes(exported)
	missing, err := cloudflarePagesMissingHashes(ctx, jwt, hashes)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		if err := uploadCloudflarePagesMissingFiles(ctx, jwt, exported, missing); err != nil {
			return nil, err
		}
	}
	if err := upsertCloudflarePagesHashes(ctx, jwt, hashes); err != nil {
		return nil, err
	}
	manifest := map[string]string{}
	for _, p := range sortedStaticFilePaths(exported.Files) {
		f := exported.Files[p]
		manifest[f.Path] = f.Hash
	}
	return manifest, nil
}

func sortedStaticFileHashes(exported *staticExportResult) []string {
	seen := map[string]bool{}
	hashes := make([]string, 0, len(exported.ByHash))
	for _, file := range exported.Files {
		if file.Hash == "" || seen[file.Hash] {
			continue
		}
		seen[file.Hash] = true
		hashes = append(hashes, file.Hash)
	}
	sort.Strings(hashes)
	return hashes
}

func cloudflarePagesUploadJWT(ctx context.Context, cfg CloudflareConfig) (string, error) {
	path := fmt.Sprintf("/accounts/%s/pages/projects/%s/upload-token", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.PagesProjectName))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodGet, path, nil, "")
	if err != nil {
		return "", cloudflareStagePermissionError("pages", err)
	}
	var token cloudflarePagesUploadToken
	if len(result) > 0 {
		if err := json.Unmarshal(result, &token); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(token.JWT) == "" {
		return "", errors.New("Cloudflare Pages 没有返回静态资源上传令牌。")
	}
	return token.JWT, nil
}

func cloudflarePagesMissingHashes(ctx context.Context, jwt string, hashes []string) ([]string, error) {
	body, _ := json.Marshal(map[string][]string{"hashes": hashes})
	result, err := cloudflareAPIRequest(ctx, jwt, http.MethodPost, "/pages/assets/check-missing", bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, cloudflareStagePermissionError("pages_assets", err)
	}
	var missing []string
	if len(result) > 0 {
		if err := json.Unmarshal(result, &missing); err != nil {
			return nil, err
		}
	}
	return missing, nil
}

func uploadCloudflarePagesMissingFiles(ctx context.Context, jwt string, exported *staticExportResult, hashes []string) error {
	const batchSize = 50
	for start := 0; start < len(hashes); start += batchSize {
		end := start + batchSize
		if end > len(hashes) {
			end = len(hashes)
		}
		if err := uploadCloudflarePagesFileBatch(ctx, jwt, exported, hashes[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func uploadCloudflarePagesFileBatch(ctx context.Context, jwt string, exported *staticExportResult, hashes []string) error {
	type uploadFile struct {
		Key      string            `json:"key"`
		Value    string            `json:"value"`
		Metadata map[string]string `json:"metadata"`
		Base64   bool              `json:"base64"`
	}
	payload := make([]uploadFile, 0, len(hashes))
	for _, hash := range hashes {
		diskPath := exported.ByHash[hash]
		if diskPath == "" {
			return fmt.Errorf("Pages 上传清单缺少文件 hash：%s", hash)
		}
		data, err := os.ReadFile(diskPath)
		if err != nil {
			return err
		}
		contentType := exportedContentType(exported, hash)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		payload = append(payload, uploadFile{
			Key:      hash,
			Value:    base64.StdEncoding.EncodeToString(data),
			Metadata: map[string]string{"contentType": contentType},
			Base64:   true,
		})
	}
	body, _ := json.Marshal(payload)
	_, err := cloudflareAPIRequest(ctx, jwt, http.MethodPost, "/pages/assets/upload", bytes.NewReader(body), "application/json")
	if err != nil {
		return cloudflareStagePermissionError("pages_assets", err)
	}
	return nil
}

func upsertCloudflarePagesHashes(ctx context.Context, jwt string, hashes []string) error {
	body, _ := json.Marshal(map[string][]string{"hashes": hashes})
	_, err := cloudflareAPIRequest(ctx, jwt, http.MethodPost, "/pages/assets/upsert-hashes", bytes.NewReader(body), "application/json")
	if err != nil {
		return cloudflareStagePermissionError("pages_assets", err)
	}
	return nil
}

func createCloudflarePagesDeployment(ctx context.Context, cfg CloudflareConfig, manifest map[string]string) (cloudflarePagesDeployment, error) {
	manifestJSON, _ := json.Marshal(manifest)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := writeMultipartField(mw, "manifest", "", "application/json", manifestJSON); err != nil {
		return cloudflarePagesDeployment{}, err
	}
	if err := writeMultipartField(mw, "branch", "", "text/plain; charset=utf-8", []byte("main")); err != nil {
		return cloudflarePagesDeployment{}, err
	}
	if err := writeMultipartField(mw, "commit_message", "", "text/plain; charset=utf-8", []byte("Deploy gcms static site")); err != nil {
		return cloudflarePagesDeployment{}, err
	}
	if err := mw.Close(); err != nil {
		return cloudflarePagesDeployment{}, err
	}
	path := fmt.Sprintf("/accounts/%s/pages/projects/%s/deployments", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.PagesProjectName))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, &body, mw.FormDataContentType())
	if err != nil {
		return cloudflarePagesDeployment{}, cloudflareStagePermissionError("pages", err)
	}
	var deployment cloudflarePagesDeployment
	if len(result) > 0 {
		if err := json.Unmarshal(result, &deployment); err != nil {
			return cloudflarePagesDeployment{}, err
		}
	}
	return deployment, nil
}

func ensureCloudflarePagesDomains(ctx context.Context, cfg CloudflareConfig) error {
	for _, domain := range cfg.publicDomains() {
		if domain.Host == "" {
			continue
		}
		next := cfg
		next.RoutePattern = normalizeCloudflareRoutePattern(domain.Host)
		if err := ensureCloudflarePagesDomain(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

func ensureCloudflarePagesDomain(ctx context.Context, cfg CloudflareConfig) error {
	host := cloudflareRouteHost(cfg.RoutePattern)
	if host == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"name": host})
	path := fmt.Sprintf("/accounts/%s/pages/projects/%s/domains", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.PagesProjectName))
	_, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, bytes.NewReader(body), "application/json")
	if err == nil || cloudflareErrorContains(err, "already") || cloudflareErrorContains(err, "exists") {
		return nil
	}
	return cloudflareStagePermissionError("pages", err)
}

func deleteCloudflarePagesDomains(ctx context.Context, cfg CloudflareConfig) error {
	for _, domain := range cfg.publicDomains() {
		if domain.Host == "" {
			continue
		}
		if err := deleteCloudflarePagesDomain(ctx, cfg, domain.Host); err != nil {
			return err
		}
	}
	return nil
}

func deleteCloudflarePagesDomain(ctx context.Context, cfg CloudflareConfig, host string) error {
	host = normalizeCloudflareDomainHost(host)
	if host == "" {
		return nil
	}
	path := fmt.Sprintf("/accounts/%s/pages/projects/%s/domains/%s", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.PagesProjectName), url.PathEscape(host))
	_, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodDelete, path, nil, "")
	if err == nil || cloudflareHasErrorCode(err, 8000007) || cloudflareErrorContains(err, "not found") {
		return nil
	}
	return cloudflareStagePermissionError("pages", err)
}

func ensureCloudflarePagesDNSRecords(ctx context.Context, cfg CloudflareConfig, target string) error {
	for _, domain := range cfg.publicDomains() {
		if domain.Host == "" {
			continue
		}
		next := cfg
		next.RoutePattern = normalizeCloudflareRoutePattern(domain.Host)
		if err := ensureCloudflarePagesDNSRecord(ctx, next, target); err != nil {
			return err
		}
	}
	return nil
}

func ensureCloudflarePagesDNSRecord(ctx context.Context, cfg CloudflareConfig, target string) error {
	host := cloudflareRouteHost(cfg.RoutePattern)
	target = strings.TrimSpace(strings.TrimSuffix(target, "."))
	if strings.TrimSpace(cfg.ZoneID) == "" || host == "" || target == "" {
		return nil
	}
	records, err := listCloudflareDNSRecords(ctx, cfg, host)
	if err != nil {
		return cloudflareStagePermissionError("dns", err)
	}
	cnameID := ""
	for _, rec := range records {
		if !sameCloudflareDNSName(rec.Name, host) || !cloudflareDNSRouteRecord(rec.Type) {
			continue
		}
		if strings.EqualFold(rec.Type, "CNAME") {
			cnameID = rec.ID
			continue
		}
		if err := deleteCloudflareDNSRecord(ctx, cfg, rec.ID); err != nil {
			return err
		}
	}
	if cnameID != "" {
		return putCloudflareDNSRecord(ctx, cfg, cnameID, "CNAME", host, target, true)
	}
	return createCloudflareDNSRecord(ctx, cfg, "CNAME", host, target, true)
}

func uploadCloudflareStaticAssets(ctx context.Context, cfg CloudflareConfig, exported *staticExportResult) (string, error) {
	manifest := map[string]cloudflareAssetManifestEntry{}
	for _, p := range sortedStaticFilePaths(exported.Files) {
		f := exported.Files[p]
		manifest[f.Path] = cloudflareAssetManifestEntry{Hash: f.Hash, Size: f.Size}
	}
	body, _ := json.Marshal(map[string]any{"manifest": manifest})
	path := fmt.Sprintf("/accounts/%s/workers/scripts/%s/assets-upload-session", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.WorkerName))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, bytes.NewReader(body), "application/json")
	if err != nil {
		return "", cloudflareStagePermissionError("assets", err)
	}
	var session cloudflareAssetsUploadSession
	if err := json.Unmarshal(result, &session); err != nil {
		return "", err
	}
	if strings.TrimSpace(session.JWT) == "" {
		return "", errors.New("Cloudflare 没有返回静态资源上传令牌。")
	}
	completionJWT := session.JWT
	for _, bucket := range session.Buckets {
		if len(bucket) == 0 {
			continue
		}
		jwt, err := uploadCloudflareAssetBucket(ctx, cfg, session.JWT, exported, bucket)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(jwt) != "" {
			completionJWT = jwt
		}
	}
	if strings.TrimSpace(completionJWT) == "" {
		return "", errors.New("Cloudflare 没有返回静态资源完成令牌。")
	}
	return completionJWT, nil
}

func uploadCloudflareAssetBucket(ctx context.Context, cfg CloudflareConfig, uploadJWT string, exported *staticExportResult, hashes []string) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, hash := range hashes {
		diskPath := exported.ByHash[hash]
		if diskPath == "" {
			return "", fmt.Errorf("静态资源上传清单缺少文件 hash：%s", hash)
		}
		data, err := os.ReadFile(diskPath)
		if err != nil {
			return "", err
		}
		contentType := exportedContentType(exported, hash)
		if contentType == "" {
			contentType = "application/null"
		}
		if err := writeMultipartField(mw, hash, hash, contentType, []byte(base64.StdEncoding.EncodeToString(data))); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	path := fmt.Sprintf("/accounts/%s/workers/assets/upload?base64=true", url.PathEscape(cfg.AccountID))
	result, err := cloudflareAPIRequest(ctx, uploadJWT, http.MethodPost, path, &body, mw.FormDataContentType())
	if err != nil {
		return "", cloudflareStagePermissionError("assets", err)
	}
	var uploaded cloudflareAssetsUploadResult
	if err := json.Unmarshal(result, &uploaded); err != nil {
		return "", err
	}
	return uploaded.JWT, nil
}

func exportedContentType(exported *staticExportResult, hash string) string {
	for _, file := range exported.Files {
		if file.Hash == hash {
			return file.ContentType
		}
	}
	return ""
}

func uploadCloudflareWorker(ctx context.Context, cfg CloudflareConfig, assetsJWT string) error {
	versionID, err := uploadCloudflareWorkerVersion(ctx, cfg, assetsJWT)
	if err != nil {
		// Cloudflare may require the script to exist before accepting immutable
		// versions. Seed it once through the script upload API, then publish the
		// real Worker Assets version and deployment.
		if seedErr := uploadCloudflareWorkerScript(ctx, cfg); seedErr != nil {
			return seedErr
		}
		versionID, err = uploadCloudflareWorkerVersion(ctx, cfg, assetsJWT)
		if err != nil {
			return cloudflareStagePermissionError("worker", err)
		}
	}
	if err := deployCloudflareWorkerVersion(ctx, cfg, versionID); err != nil {
		return cloudflareStagePermissionError("worker", err)
	}
	return nil
}

func cloudflareWorkerUploadMetadata(assetsJWT string) []byte {
	metadata := map[string]any{
		"main_module":        "worker.js",
		"compatibility_date": "2025-01-01",
		"bindings": []map[string]string{
			{"type": "assets", "name": "ASSETS"},
		},
		"assets": map[string]any{
			"jwt": assetsJWT,
			"config": map[string]any{
				"html_handling":      "auto-trailing-slash",
				"not_found_handling": "404-page",
			},
		},
	}
	metadataJSON, _ := json.Marshal(metadata)
	return metadataJSON
}

func cloudflareWorkerSeedMetadata() []byte {
	metadataJSON, _ := json.Marshal(map[string]any{
		"main_module":        "worker.js",
		"compatibility_date": "2025-01-01",
	})
	return metadataJSON
}

func newCloudflareWorkerUploadBody(assetsJWT string, cfg CloudflareConfig) (*bytes.Buffer, string, error) {
	return newCloudflareWorkerMultipartBody(cloudflareWorkerUploadMetadata(assetsJWT), []byte(cloudflareWorkerScriptForConfig(cfg)))
}

func newCloudflareWorkerSeedBody(cfg CloudflareConfig) (*bytes.Buffer, string, error) {
	return newCloudflareWorkerMultipartBody(cloudflareWorkerSeedMetadata(), []byte(cloudflareWorkerScriptForConfig(cfg)))
}

func newCloudflareWorkerMultipartBody(metadata []byte, script []byte) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := writeMultipartField(mw, "metadata", "", "application/json", metadata); err != nil {
		return nil, "", err
	}
	if err := writeMultipartField(mw, "worker.js", "worker.js", "application/javascript+module", script); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &body, mw.FormDataContentType(), nil
}

func uploadCloudflareWorkerVersion(ctx context.Context, cfg CloudflareConfig, assetsJWT string) (string, error) {
	body, contentType, err := newCloudflareWorkerUploadBody(assetsJWT, cfg)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/accounts/%s/workers/scripts/%s/versions", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.WorkerName))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, body, contentType)
	if err != nil {
		return "", err
	}
	var version cloudflareWorkerVersionResult
	if err := json.Unmarshal(result, &version); err != nil {
		return "", err
	}
	if strings.TrimSpace(version.ID) == "" {
		return "", errors.New("Cloudflare 没有返回 Worker Version ID。")
	}
	return version.ID, nil
}

func deployCloudflareWorkerVersion(ctx context.Context, cfg CloudflareConfig, versionID string) error {
	body, _ := json.Marshal(map[string]any{
		"strategy": "percentage",
		"versions": []map[string]any{
			{
				"version_id": versionID,
				"percentage": 100,
			},
		},
		"annotations": map[string]string{
			"workers/message": "Deploy gcms static site",
		},
	})
	path := fmt.Sprintf("/accounts/%s/workers/scripts/%s/deployments", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.WorkerName))
	_, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, bytes.NewReader(body), "application/json")
	return err
}

func uploadCloudflareWorkerScript(ctx context.Context, cfg CloudflareConfig) error {
	body, contentType, err := newCloudflareWorkerSeedBody(cfg)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/accounts/%s/workers/scripts/%s", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.WorkerName))
	_, err = cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPut, path, body, contentType)
	if err != nil {
		return cloudflareStagePermissionError("worker", err)
	}
	return nil
}

func writeMultipartField(mw *multipart.Writer, name, filename, contentType string, data []byte) error {
	h := make(textproto.MIMEHeader)
	if filename != "" {
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, name, filename))
	} else {
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, name))
	}
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func ensureCloudflareRoutes(ctx context.Context, cfg CloudflareConfig) error {
	for _, pattern := range cfg.routePatterns() {
		next := cfg
		next.RoutePattern = pattern
		if err := ensureCloudflareRoute(ctx, next); err != nil {
			return err
		}
	}
	return nil
}

func ensureCloudflareRoute(ctx context.Context, cfg CloudflareConfig) error {
	routes, err := listCloudflareRoutes(ctx, cfg)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"pattern": cfg.RoutePattern, "script": cfg.WorkerName})
	for _, rt := range routes {
		if rt.Pattern == cfg.RoutePattern {
			path := fmt.Sprintf("/zones/%s/workers/routes/%s", url.PathEscape(cfg.ZoneID), url.PathEscape(rt.ID))
			_, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPut, path, bytes.NewReader(body), "application/json")
			if err != nil {
				return cloudflareStagePermissionError("route", err)
			}
			return nil
		}
	}
	path := fmt.Sprintf("/zones/%s/workers/routes", url.PathEscape(cfg.ZoneID))
	_, err = cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, bytes.NewReader(body), "application/json")
	if err != nil {
		return cloudflareStagePermissionError("route", err)
	}
	return nil
}

func deleteCloudflareRoutes(ctx context.Context, cfg CloudflareConfig) error {
	routes, err := listCloudflareRoutes(ctx, cfg)
	if err != nil {
		return cloudflareStagePermissionError("route", err)
	}
	patterns := map[string]bool{}
	for _, pattern := range cfg.routePatterns() {
		patterns[pattern] = true
	}
	for _, rt := range routes {
		if !patterns[rt.Pattern] {
			continue
		}
		if strings.TrimSpace(rt.Script) != "" && rt.Script != cfg.WorkerName {
			continue
		}
		path := fmt.Sprintf("/zones/%s/workers/routes/%s", url.PathEscape(cfg.ZoneID), url.PathEscape(rt.ID))
		if _, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodDelete, path, nil, ""); err != nil {
			return cloudflareStagePermissionError("route", err)
		}
	}
	return nil
}

func listCloudflareRoutes(ctx context.Context, cfg CloudflareConfig) ([]cloudflareRoute, error) {
	path := fmt.Sprintf("/zones/%s/workers/routes", url.PathEscape(cfg.ZoneID))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	var routes []cloudflareRoute
	if len(result) > 0 {
		_ = json.Unmarshal(result, &routes)
	}
	return routes, nil
}

func ensureCloudflareDNSRecords(ctx context.Context, cfg CloudflareConfig) (cloudflareDNSManageResult, error) {
	for _, domain := range cfg.publicDomains() {
		if domain.Host == "" {
			continue
		}
		next := cfg
		next.RoutePattern = normalizeCloudflareRoutePattern(domain.Host)
		if err := ensureCloudflareDNSRecord(ctx, next, domain); err != nil {
			return cloudflareDNSManageResult{}, err
		}
	}
	return cloudflareDNSManagedResult("前台域名 DNS 已切换为 Cloudflare Worker Assets 托管入口。"), nil
}

func detectCloudflareDNSStatus(ctx context.Context, cfg CloudflareConfig) (cloudflareDNSManageResult, error) {
	if strings.TrimSpace(cfg.ZoneID) == "" || !cfg.tokenSet() {
		return cloudflareDNSManualResult("暂无可检测的 Cloudflare DNS 授权。"), nil
	}
	modeName := "Cloudflare Worker Assets"
	targetFor := func(domain CloudflareDomain) cloudflareDNSTarget {
		return cloudflareWorkerDNSTarget(cfg, domain)
	}
	if cfg.usingPages() {
		modeName = "Cloudflare Pages"
		pagesTarget := cloudflarePagesDefaultSubdomain(cfg)
		if strings.TrimSpace(cfg.AccountID) != "" && strings.TrimSpace(cfg.PagesProjectName) != "" {
			if project, err := getCloudflarePagesProject(ctx, cfg); err == nil && strings.TrimSpace(project.Subdomain) != "" {
				pagesTarget = strings.TrimSpace(project.Subdomain)
			}
		}
		targetFor = func(domain CloudflareDomain) cloudflareDNSTarget {
			return cloudflarePagesDNSTarget(cfg, domain, pagesTarget)
		}
	}
	checked := 0
	for _, domain := range cfg.publicDomains() {
		if domain.Host == "" {
			continue
		}
		target := targetFor(domain)
		if target.Host == "" || target.Type == "" || target.Content == "" {
			continue
		}
		records, err := listCloudflareDNSRecords(ctx, cfg, target.Host)
		if err != nil {
			return cloudflareDNSManageResult{}, cloudflareStagePermissionError("dns", err)
		}
		checked++
		if !cloudflareDNSRecordsMatchTarget(records, target) {
			return cloudflareDNSManualResult(fmt.Sprintf("%s 的 DNS 尚未指向 %s 托管入口。", target.Host, modeName)), nil
		}
	}
	if checked == 0 {
		return cloudflareDNSManualResult("暂无可检测的前台域名。"), nil
	}
	return cloudflareDNSManagedResult(fmt.Sprintf("前台域名 DNS 已接管到 %s 托管入口。", modeName)), nil
}

func ensureCloudflareDNSRecord(ctx context.Context, cfg CloudflareConfig, domain CloudflareDomain) error {
	host := cloudflareRouteHost(cfg.RoutePattern)
	if strings.TrimSpace(cfg.ZoneID) == "" || host == "" {
		return nil
	}
	if domain.Host == "" {
		domain.Host = host
	}
	target := cloudflareWorkerDNSTarget(cfg, domain)
	if target.Host == "" || target.Type == "" || target.Content == "" {
		return nil
	}
	records, err := listCloudflareDNSRecords(ctx, cfg, host)
	if err != nil {
		return cloudflareStagePermissionError("dns", err)
	}
	keepID := ""
	for _, rec := range records {
		if !sameCloudflareDNSName(rec.Name, host) || !cloudflareDNSRouteRecord(rec.Type) {
			continue
		}
		if strings.EqualFold(rec.Type, target.Type) && keepID == "" {
			keepID = rec.ID
			continue
		}
		if err := deleteCloudflareDNSRecord(ctx, cfg, rec.ID); err != nil {
			return err
		}
	}
	if keepID != "" {
		return putCloudflareDNSRecord(ctx, cfg, keepID, target.Type, target.Host, target.Content, true)
	}
	return createCloudflareDNSRecord(ctx, cfg, target.Type, target.Host, target.Content, true)
}

func cloudflareWorkerDNSTarget(cfg CloudflareConfig, domain CloudflareDomain) cloudflareDNSTarget {
	host := normalizeCloudflareDomainHost(domain.Host)
	if host == "" {
		host = cloudflareRouteHost(cfg.RoutePattern)
	}
	if host == "" {
		return cloudflareDNSTarget{}
	}
	primary := cfg.primaryHost()
	if domain.RedirectToPrimary && primary != "" && !sameCloudflareDNSName(host, primary) {
		return cloudflareDNSTarget{Type: "CNAME", Host: host, Content: primary}
	}
	return cloudflareDNSTarget{Type: "AAAA", Host: host, Content: "100::"}
}

func cloudflarePagesDefaultSubdomain(cfg CloudflareConfig) string {
	project := strings.TrimSpace(cfg.PagesProjectName)
	if project == "" {
		project = cloudflareDefaultProjectNameForHost(cfg.primaryHost())
	}
	if project == "" {
		return ""
	}
	return project + ".pages.dev"
}

func cloudflarePagesDNSTarget(cfg CloudflareConfig, domain CloudflareDomain, pagesTarget string) cloudflareDNSTarget {
	host := normalizeCloudflareDomainHost(domain.Host)
	if host == "" {
		host = cloudflareRouteHost(cfg.RoutePattern)
	}
	pagesTarget = strings.TrimSpace(strings.TrimSuffix(pagesTarget, "."))
	if host == "" || pagesTarget == "" {
		return cloudflareDNSTarget{}
	}
	return cloudflareDNSTarget{Type: "CNAME", Host: host, Content: pagesTarget}
}

func cloudflareDNSRecordsMatchTarget(records []cloudflareDNSRecord, target cloudflareDNSTarget) bool {
	found := false
	for _, rec := range records {
		if !sameCloudflareDNSName(rec.Name, target.Host) || !cloudflareDNSRouteRecord(rec.Type) {
			continue
		}
		if !cloudflareDNSRecordMatchesTarget(rec, target) {
			return false
		}
		found = true
	}
	return found
}

func cloudflareDNSRecordMatchesTarget(rec cloudflareDNSRecord, target cloudflareDNSTarget) bool {
	if !strings.EqualFold(rec.Type, target.Type) {
		return false
	}
	if rec.Proxied == nil || !*rec.Proxied {
		return false
	}
	content := strings.TrimSpace(strings.TrimSuffix(rec.Content, "."))
	targetContent := strings.TrimSpace(strings.TrimSuffix(target.Content, "."))
	if strings.EqualFold(target.Type, "CNAME") {
		return sameCloudflareDNSName(content, targetContent)
	}
	return strings.EqualFold(content, targetContent)
}

func cloudflareDNSManagedResult(message string) cloudflareDNSManageResult {
	if strings.TrimSpace(message) == "" {
		message = "前台域名 DNS 已切换为 Cloudflare 托管入口。"
	}
	return cloudflareDNSManageResult{Status: cloudflareDNSStatusManaged, Message: message}
}

func cloudflareDNSManualResult(message string) cloudflareDNSManageResult {
	if strings.TrimSpace(message) == "" {
		message = "静态站已发布，但域名 DNS 尚未确认接管。"
	}
	return cloudflareDNSManageResult{Status: cloudflareDNSStatusManual, Message: message}
}

func createCloudflareDNSRecord(ctx context.Context, cfg CloudflareConfig, recordType, host, content string, proxied bool) error {
	body, _ := json.Marshal(map[string]any{
		"type":    strings.ToUpper(strings.TrimSpace(recordType)),
		"name":    host,
		"content": content,
		"ttl":     1,
		"proxied": proxied,
	})
	path := fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(cfg.ZoneID))
	if _, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, bytes.NewReader(body), "application/json"); err != nil {
		return cloudflareStagePermissionError("dns", err)
	}
	return nil
}

func putCloudflareDNSRecord(ctx context.Context, cfg CloudflareConfig, recordID, recordType, host, content string, proxied bool) error {
	body, _ := json.Marshal(map[string]any{
		"type":    strings.ToUpper(strings.TrimSpace(recordType)),
		"name":    host,
		"content": content,
		"ttl":     1,
		"proxied": proxied,
	})
	path := fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(cfg.ZoneID), url.PathEscape(recordID))
	if _, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPut, path, bytes.NewReader(body), "application/json"); err != nil {
		return cloudflareStagePermissionError("dns", err)
	}
	return nil
}

func deleteCloudflareDNSRecord(ctx context.Context, cfg CloudflareConfig, recordID string) error {
	path := fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(cfg.ZoneID), url.PathEscape(recordID))
	if _, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodDelete, path, nil, ""); err != nil {
		return cloudflareStagePermissionError("dns", err)
	}
	return nil
}

func listCloudflareDNSRecords(ctx context.Context, cfg CloudflareConfig, host string) ([]cloudflareDNSRecord, error) {
	path := fmt.Sprintf("/zones/%s/dns_records?per_page=100&name=%s", url.PathEscape(cfg.ZoneID), url.QueryEscape(host))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	var records []cloudflareDNSRecord
	if len(result) > 0 {
		if err := json.Unmarshal(result, &records); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func cloudflareDNSRouteRecord(recordType string) bool {
	switch strings.ToUpper(strings.TrimSpace(recordType)) {
	case "A", "AAAA", "CNAME":
		return true
	default:
		return false
	}
}

func sameCloudflareDNSName(a, b string) bool {
	return strings.Trim(strings.ToLower(strings.TrimSpace(a)), ".") == strings.Trim(strings.ToLower(strings.TrimSpace(b)), ".")
}

func purgeCloudflareEverything(ctx context.Context, cfg CloudflareConfig) error {
	if strings.TrimSpace(cfg.ZoneID) == "" {
		return nil
	}
	body := strings.NewReader(`{"purge_everything":true}`)
	path := fmt.Sprintf("/zones/%s/purge_cache", url.PathEscape(cfg.ZoneID))
	_, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, body, "application/json")
	if err != nil {
		return cloudflareStagePermissionError("purge", err)
	}
	return nil
}

var verifyCloudflareAPIToken = verifyCloudflareAPITokenLive

func verifyCloudflareAPITokenLive(ctx context.Context, token string) error {
	result, err := cloudflareAPIRequest(ctx, token, http.MethodGet, "/user/tokens/verify", nil, "")
	if err != nil {
		return fmt.Errorf("Cloudflare Token 验证失败：请确认 Token 已复制完整、未被删除，并且仍处于 active 状态。原始错误：%w", err)
	}
	var verified cloudflareTokenVerifyResult
	if len(result) > 0 {
		if err := json.Unmarshal(result, &verified); err != nil {
			return fmt.Errorf("Cloudflare Token 验证结果无法解析：%w", err)
		}
	}
	if !strings.EqualFold(strings.TrimSpace(verified.Status), "active") {
		return fmt.Errorf("Cloudflare Token 验证失败：当前状态为 %q，请重新获取 active 状态的授权 Token。", strings.TrimSpace(verified.Status))
	}
	return nil
}

func cloudflareAPIRequest(ctx context.Context, token, method, path string, body io.Reader, contentType string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, "https://api.cloudflare.com/client/v4"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var envelope cloudflareAPIResponse
	if err := json.Unmarshal(data, &envelope); err != nil {
		if resp.StatusCode >= 400 {
			return nil, cloudflareAPIError{Status: resp.StatusCode, Raw: strings.TrimSpace(string(data))}
		}
		return data, nil
	}
	if resp.StatusCode >= 400 || !envelope.Success {
		return nil, cloudflareAPIError{Status: resp.StatusCode, Errors: envelope.Errors}
	}
	return envelope.Result, nil
}

func cloudflareStagePermissionError(stage string, err error) error {
	if !cloudflareHasErrorCode(err, 10000) {
		return err
	}
	switch stage {
	case "assets":
		return fmt.Errorf("Cloudflare 拒绝上传静态资源：请重新创建 Token，并确认摘要里包含 Account 级的 Workers Scripts Edit 权限；Account Resources 必须包含当前账号。如果手动填过 Account ID，也请确认它属于这个账号。原始错误：%w", err)
	case "pages", "pages_assets":
		return fmt.Errorf("Cloudflare 拒绝发布 Pages：请重新创建 Token，并确认摘要里包含 Account 级的 Cloudflare Pages Edit 权限；Account Resources 必须包含当前账号。原始错误：%w", err)
	case "worker":
		return fmt.Errorf("Cloudflare 拒绝上传 Worker：请重新创建 Token，并确认摘要里包含 Account 级的 Workers Scripts Edit 权限；Account Resources 必须包含当前账号。如果手动填过 Account ID，也请确认它属于这个账号。原始错误：%w", err)
	case "dns":
		return fmt.Errorf("Cloudflare 拒绝绑定 DNS：请重新创建 Token，并确认摘要里包含 Zone 级的 DNS Edit 和 Zone Read 权限；Zone Resources 必须包含当前前台域名。原始错误：%w", err)
	case "route":
		return fmt.Errorf("Cloudflare 拒绝绑定路由：请重新创建 Token，并确认摘要里包含 Zone 级的 Workers Routes Edit 和 Zone Read 权限；Zone Resources 必须包含当前域名。原始错误：%w", err)
	case "purge":
		return fmt.Errorf("Cloudflare 拒绝清理缓存：请重新创建 Token，并确认摘要里包含 Zone 级的 Cache Purge 权限；Zone Resources 必须包含当前域名。原始错误：%w", err)
	default:
		return err
	}
}

func cloudflareHasErrorCode(err error, code int) bool {
	var apiErr cloudflareAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, e := range apiErr.Errors {
		if e.Code == code {
			return true
		}
	}
	return false
}

func cloudflareErrorContains(err error, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if err == nil || needle == "" {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), needle) {
		return true
	}
	var apiErr cloudflareAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, e := range apiErr.Errors {
		if strings.Contains(strings.ToLower(e.Message), needle) {
			return true
		}
	}
	return false
}

func cloudflareErrorMessage(errs []cloudflareErr, status int) string {
	if len(errs) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Message == "" {
			continue
		}
		if e.Code != 0 {
			parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
		} else {
			parts = append(parts, e.Message)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return strings.Join(parts, "；")
}

func cloudflareWorkerScript() string {
	return cloudflareWorkerScriptForConfig(CloudflareConfig{})
}

func cloudflareWorkerDefaultLang(cfg CloudflareConfig) string {
	def := strings.TrimSpace(cfg.DefaultLang)
	if def == "" && len(cfg.Locales) > 0 {
		def = strings.TrimSpace(cfg.Locales[0])
	}
	if def == "" {
		return "zh"
	}
	return def
}

func cloudflareWorkerLocales(cfg CloudflareConfig) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, code := range cfg.Locales {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		key := strings.ToLower(code)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, code)
	}
	def := cloudflareWorkerDefaultLang(cfg)
	if !seen[strings.ToLower(def)] {
		out = append([]string{def}, out...)
	}
	if len(out) == 0 {
		out = []string{"zh"}
	}
	return out
}

func cloudflareWorkerScriptForConfig(cfg CloudflareConfig) string {
	primaryJSON, _ := json.Marshal(cfg.primaryHost())
	redirectsJSON, _ := json.Marshal(cfg.redirectHosts())
	publicHosts := make([]string, 0, len(cfg.publicDomains()))
	for _, domain := range cfg.publicDomains() {
		if domain.Host != "" {
			publicHosts = append(publicHosts, domain.Host)
		}
	}
	publicHostsJSON, _ := json.Marshal(publicHosts)
	defaultLangJSON, _ := json.Marshal(cloudflareWorkerDefaultLang(cfg))
	localesJSON, _ := json.Marshal(cloudflareWorkerLocales(cfg))
	return fmt.Sprintf(`const BLOCKED_PREFIXES = ["/admin", "/api/admin", "/preview"];
const RESERVED_PREFIXES = ["/assets", "/uploads", "/api"];
const RESERVED_PATHS = new Set(["/robots.txt", "/sitemap.xml", "/rss.xml", "/favicon.ico", "/_redirects", "/_worker.js"]);
const PRIMARY_HOST = %s;
const REDIRECT_HOSTS = new Set(%s);
const PUBLIC_HOSTS = new Set(%s);
const DEFAULT_LANG = %s;
const LOCALES = %s;

function blocked(pathname) {
  return BLOCKED_PREFIXES.some((prefix) => pathname === prefix || pathname.startsWith(prefix + "/"));
}

function reserved(pathname) {
  return RESERVED_PATHS.has(pathname) || RESERVED_PREFIXES.some((prefix) => pathname === prefix || pathname.startsWith(prefix + "/"));
}

function normalizeLang(value) {
  return String(value || "").trim().toLowerCase().replace(/_/g, "-");
}

function localeForSegment(segment) {
  const normalized = normalizeLang(segment);
  return LOCALES.find((code) => normalizeLang(code) === normalized) || "";
}

function parseAcceptLanguage(header) {
  return String(header || "")
    .split(",")
    .map((part, index) => {
      const bits = part.trim().split(";");
      const value = normalizeLang(bits[0]);
      let q = 1;
      for (const bit of bits.slice(1)) {
        const pieces = bit.trim().split("=");
        if (pieces[0] === "q") {
          const parsed = Number.parseFloat(pieces[1]);
          if (!Number.isNaN(parsed)) q = Math.max(0, Math.min(1, parsed));
        }
      }
      return { value, q, index };
    })
    .filter((pref) => pref.value && pref.q > 0)
    .sort((a, b) => (b.q === a.q ? a.index - b.index : b.q - a.q));
}

function preferredLanguage(header) {
  for (const pref of parseAcceptLanguage(header)) {
    if (pref.value === "*") {
      return DEFAULT_LANG;
    }
    const exact = LOCALES.find((code) => normalizeLang(code) === pref.value);
    if (exact) {
      return exact;
    }
    const primary = pref.value.split("-")[0];
    const primaryMatch = LOCALES.find((code) => {
      const normalized = normalizeLang(code);
      return normalized === primary || normalized.startsWith(primary + "-");
    });
    if (primaryMatch) {
      return primaryMatch;
    }
  }
  return DEFAULT_LANG;
}

function makeRedirect(url, status, varyLanguage) {
  const headers = { Location: url.toString() };
  if (varyLanguage) {
    headers.Vary = "Accept-Language";
    headers["Cache-Control"] = "private, no-store";
  }
  return new Response(null, { status, headers });
}

function redirectTarget(url) {
  const host = url.hostname.toLowerCase();
  if (!PRIMARY_HOST || host === PRIMARY_HOST) {
    return null;
  }
  if (!REDIRECT_HOSTS.has(host) && PUBLIC_HOSTS.has(host)) {
    return null;
  }
  const next = new URL(url.toString());
  next.hostname = PRIMARY_HOST;
  return next;
}

function localeRedirect(url, request) {
  const pathname = url.pathname || "/";
  if (reserved(pathname)) {
    return null;
  }
  const head = pathname.replace(/^\/+/, "").split("/")[0];
  if (localeForSegment(head)) {
    return null;
  }
  const next = new URL(url.toString());
  if (pathname === "/" || pathname === "") {
    next.pathname = "/" + preferredLanguage(request.headers.get("Accept-Language")) + "/";
    return { url: next, varyLanguage: true };
  }
  next.pathname = "/" + DEFAULT_LANG + (pathname.startsWith("/") ? pathname : "/" + pathname);
  return { url: next, varyLanguage: false };
}

function remap(url) {
  const next = new URL(url.toString());
  const pathname = next.pathname.replace(/\/+$/, "") || "/";
  const page = next.searchParams.get("page");
  const cat = next.searchParams.get("cat");
  const hasSearch = next.searchParams.has("q");

  if (hasSearch && /\/search\/?$/.test(pathname)) {
    next.search = "";
    return next;
  }
  if (cat && /^\/[^/]+\/links\/?$/.test(pathname)) {
    const safeCat = encodeURIComponent(cat).replace(/%%2F/gi, "");
    next.pathname = pathname.replace(/\/+$/, "") + "/cat/" + safeCat + (page && page !== "1" ? "/page/" + page : "") + "/";
    next.search = "";
    return next;
  }
  if (page && page !== "1") {
    next.pathname = pathname.replace(/\/+$/, "") + "/page/" + page + "/";
    next.search = "";
    return next;
  }
  if (/\/page\/[1-9][0-9]*$/.test(pathname) || /^\/[^/]+\/links\/cat\/[^/]+$/.test(pathname)) {
    next.pathname = pathname + "/";
    next.search = "";
    return next;
  }
  return url;
}

export default {
  async fetch(request, env, ctx) {
    const incoming = new URL(request.url);
    const redirectURL = redirectTarget(incoming);
    if (redirectURL) {
      return makeRedirect(redirectURL, 301, false);
    }
    if (blocked(incoming.pathname)) {
      return new Response("Not found", { status: 404 });
    }
    const langRedirect = localeRedirect(incoming, request);
    if (langRedirect) {
      return makeRedirect(langRedirect.url, 302, langRedirect.varyLanguage);
    }
    if (request.method !== "GET" && request.method !== "HEAD") {
      return new Response("Method Not Allowed", { status: 405 });
    }
    if (!env.ASSETS || typeof env.ASSETS.fetch !== "function") {
      return new Response("GCMS static assets binding is missing. Please redeploy this site from the GCMS Cloudflare deployment page.", { status: 503 });
    }

    const assetURL = remap(incoming);
    const assetRequest = assetURL === incoming ? request : new Request(assetURL.toString(), request);
    const response = await env.ASSETS.fetch(assetRequest);
    const out = new Response(response.body, response);
    out.headers.set("X-GCMS-Edge", "cloudflare-static");
    return out;
  },
};
`, string(primaryJSON), string(redirectsJSON), string(publicHostsJSON), string(defaultLangJSON), string(localesJSON))
}
