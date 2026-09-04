package web

// Content mutations enqueue canonical URLs in SQLite. A minute worker batches
// and delivers them, preserving notifications across restarts and failures.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/store"
)

const (
	indexNowKeySetting         = "indexnow.key"
	indexNowEnabledSetting     = "indexnow.enabled"
	indexNowLastSuccessSetting = "indexnow.last_success_at"
	indexNowLastStatusSetting  = "indexnow.last_status"
	indexNowLastErrorSetting   = "indexnow.last_error"
	indexNowKeyLen             = 32
	indexNowBatchSize          = 500
	indexNowDebounce           = 5 * time.Minute
)

var (
	indexNowEndpoint   = "https://api.indexnow.org/indexnow"
	indexNowHTTPClient = &http.Client{Timeout: 15 * time.Second}
)

type indexNowBatchPayload struct {
	Host        string   `json:"host"`
	Key         string   `json:"key"`
	KeyLocation string   `json:"keyLocation,omitempty"`
	URLList     []string `json:"urlList"`
}

func generateIndexNowKey() (string, error) {
	var b [indexNowKeyLen / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Server) indexNowKey() (string, error) {
	s.indexNowMu.Lock()
	defer s.indexNowMu.Unlock()
	if key := strings.TrimSpace(s.store.Setting(indexNowKeySetting)); key != "" {
		return key, nil
	}
	key, err := generateIndexNowKey()
	if err != nil {
		return "", err
	}
	if err := s.store.SetSetting(indexNowKeySetting, key); err != nil {
		return "", err
	}
	return key, nil
}

// Empty means enabled for compatibility with sites that already used the old
// always-on integration. Administrators can explicitly store "0" to opt out.
func (s *Server) indexNowEnabled() bool {
	return strings.TrimSpace(s.store.Setting(indexNowEnabledSetting)) != "0"
}

func buildIndexNowURL(endpoint, pageURL, key string) string {
	q := url.Values{}
	q.Set("url", pageURL)
	q.Set("key", key)
	return endpoint + "?" + q.Encode()
}

func indexNowPostSupported(s *Server, p *store.Post) bool {
	if s == nil || p == nil {
		return false
	}
	switch p.Type {
	case "post", "page", "link":
		return true
	default:
		return s.contentTypeActive(p.Type)
	}
}

func samePublicContentURL(a, b *store.Post) bool {
	return a != nil && b != nil && a.Type == b.Type && a.Lang == b.Lang && a.Slug == b.Slug
}

func (s *Server) indexNowContentURLs(r *http.Request, p *store.Post) []string {
	if p == nil || !indexNowPostSupported(s, p) {
		return nil
	}
	base := s.publicBaseURL(r)
	if isLocalBaseURL(base) {
		return nil
	}
	paths := []string{s.apiContentURL(p), "/" + p.Lang + "/"}
	switch p.Type {
	case "post":
		paths = append(paths, "/"+p.Lang+s.archiveConfig(p.Lang, "post").Path)
	case "link":
		paths = append(paths, "/"+p.Lang+"/links")
	case "page":
	default:
		if ct := s.lookupType(p.Type); ct != nil {
			paths = append(paths, "/"+p.Lang+"/"+strings.Trim(ct.URLPrefix, "/"))
		}
	}
	if p.CategoryID.Valid {
		if c, _ := s.store.GetCategoryByID(p.CategoryID.Int64); c != nil {
			categoryPath := ""
			switch p.Type {
			case "post":
				categoryPath = "/category/" + c.Slug
			case "link":
				categoryPath = "/links/cat/" + c.Slug
			default:
				if ct := s.lookupType(p.Type); ct != nil && ct.HasCategory {
					categoryPath = "/" + strings.Trim(ct.URLPrefix, "/") + "/cat/" + c.Slug
				}
			}
			if categoryPath != "" {
				paths = append(paths, "/"+p.Lang+categoryPath)
			}
		}
	}
	seen := map[string]bool{}
	urls := make([]string, 0, len(paths))
	for _, path := range paths {
		u := absWithBase(base, path)
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

func (s *Server) enqueueIndexNowURLs(urls []string, reason string, delay time.Duration) {
	if !s.indexNowEnabled() {
		return
	}
	if len(urls) > 0 {
		// Generate before Cloudflare's delayed static export snapshots the site,
		// so the ownership file and the changed pages ship in the same release.
		if _, err := s.indexNowKey(); err != nil {
			log.Printf("indexnow: 生成校验密钥失败: %v", err)
			return
		}
	}
	when := time.Now().Add(delay)
	for _, pageURL := range urls {
		if err := s.store.EnqueueIndexNow(pageURL, reason, when); err != nil {
			log.Printf("indexnow: URL 入队失败 %s: %v", pageURL, err)
		}
	}
}

// fireContentChangeHooks handles additions, updates, redirects and removals.
func (s *Server) fireContentChangeHooks(r *http.Request, before, after *store.Post) {
	beforePublic := before != nil && before.Status == "published" && indexNowPostSupported(s, before)
	afterPublic := after != nil && after.Status == "published" && indexNowPostSupported(s, after)
	if !beforePublic && !afterPublic {
		return
	}
	s.invalidateSitemapCache()
	if afterPublic {
		s.fireTelegramPush(r, after)
	}
	if beforePublic && (!afterPublic || !samePublicContentURL(before, after)) {
		oldURLs := s.indexNowContentURLs(r, before)
		if len(oldURLs) > 0 {
			s.enqueueIndexNowURLs(oldURLs[:1], "remove", indexNowDebounce)
		}
		if len(oldURLs) > 1 {
			s.enqueueIndexNowURLs(oldURLs[1:], "update", indexNowDebounce)
		}
	}
	if afterPublic {
		reason := "update"
		if !beforePublic {
			reason = "publish"
		}
		s.enqueueIndexNowURLs(s.indexNowContentURLs(r, after), reason, indexNowDebounce)
	}
}

func (s *Server) firePublishHooks(r *http.Request, p *store.Post) {
	s.fireContentChangeHooks(r, nil, p)
}

func (s *Server) invalidateSitemapCache() {
	s.cacheMu.Lock()
	for k := range s.endpoints {
		if strings.HasPrefix(k, "sitemap:") {
			delete(s.endpoints, k)
		}
	}
	s.cacheMu.Unlock()
}

func (s *Server) submitIndexNowBatch(items []*store.IndexNowQueueItem, key string) (int, string, time.Duration) {
	if len(items) == 0 {
		return http.StatusOK, "", 0
	}
	u, err := url.Parse(items[0].URL)
	if err != nil || u.Hostname() == "" {
		return http.StatusUnprocessableEntity, "无效 URL", 24 * time.Hour
	}
	urls := make([]string, 0, len(items))
	for _, item := range items {
		parsed, parseErr := url.Parse(item.URL)
		if parseErr != nil || !strings.EqualFold(parsed.Host, u.Host) {
			return http.StatusUnprocessableEntity, "批次中包含不同域名或无效 URL", 24 * time.Hour
		}
		urls = append(urls, item.URL)
	}
	payload := indexNowBatchPayload{
		Host:        u.Host,
		Key:         key,
		KeyLocation: u.Scheme + "://" + u.Host + "/" + key + ".txt",
		URLList:     urls,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err.Error(), 10 * time.Minute
	}
	request, err := http.NewRequest(http.MethodPost, indexNowEndpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error(), 10 * time.Minute
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := indexNowHTTPClient.Do(request)
	if err != nil {
		return 0, err.Error(), 10 * time.Minute
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	message := strings.TrimSpace(string(responseBody))
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		return resp.StatusCode, message, 0
	}
	retryAfter := 10 * time.Minute
	if resp.StatusCode == http.StatusTooManyRequests {
		if seconds, parseErr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); parseErr == nil && seconds > 0 {
			retryAfter = time.Duration(seconds) * time.Second
		}
	} else if resp.StatusCode == http.StatusForbidden {
		retryAfter = 6 * time.Hour
	} else if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		retryAfter = 24 * time.Hour
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return resp.StatusCode, message, retryAfter
}

func (s *Server) runIndexNowForSite() {
	if !s.indexNowEnabled() || !s.indexNowSendMu.TryLock() {
		return
	}
	defer s.indexNowSendMu.Unlock()
	items, err := s.store.DueIndexNow(time.Now(), indexNowBatchSize)
	if err != nil || len(items) == 0 {
		if err != nil {
			log.Printf("indexnow: 读取队列失败: %v", err)
		}
		return
	}
	key, err := s.indexNowKey()
	if err != nil {
		log.Printf("indexnow: 读取/生成 key 失败: %v", err)
		return
	}
	// A site can briefly contain old and new hosts during a domain migration.
	// IndexNow requires every POST batch to contain one host, so partition first.
	groups := map[string][]*store.IndexNowQueueItem{}
	var order []string
	for _, item := range items {
		groupKey := "invalid:" + item.URL
		if parsed, parseErr := url.Parse(item.URL); parseErr == nil && parsed.Host != "" {
			groupKey = strings.ToLower(parsed.Host)
		}
		if _, ok := groups[groupKey]; !ok {
			order = append(order, groupKey)
		}
		groups[groupKey] = append(groups[groupKey], item)
	}
	lastStatus := 0
	lastError := ""
	anySuccess := false
	for _, groupKey := range order {
		status, success, errText := s.runIndexNowBatch(groups[groupKey], key)
		if success {
			anySuccess = true
			if lastError == "" {
				lastStatus = status
			}
			continue
		}
		lastStatus = status
		lastError = errText
	}
	_ = s.store.SetSetting(indexNowLastStatusSetting, strconv.Itoa(lastStatus))
	if anySuccess {
		_ = s.store.SetSetting(indexNowLastSuccessSetting, time.Now().UTC().Format(time.RFC3339))
	}
	if lastError == "" {
		_ = s.store.SetSetting(indexNowLastErrorSetting, "")
	} else {
		_ = s.store.SetSetting(indexNowLastErrorSetting, time.Now().Format("2006-01-02 15:04:05")+" "+lastError)
	}
}

func (s *Server) runIndexNowBatch(items []*store.IndexNowQueueItem, key string) (int, bool, string) {
	status, message, retryAfter := s.submitIndexNowBatch(items, key)
	urls := make([]string, 0, len(items))
	for _, item := range items {
		urls = append(urls, item.URL)
	}
	success := status == http.StatusOK || status == http.StatusAccepted
	historyMessage := message
	if success {
		historyMessage = ""
	}
	_ = s.store.RecordIndexNowSubmissions(items, status, success, historyMessage)
	if success {
		_ = s.store.DeleteIndexNow(urls)
		return status, true, ""
	}
	if retryAfter <= 0 {
		retryAfter = 10 * time.Minute
	}
	_ = s.store.RetryIndexNow(urls, time.Now().Add(retryAfter), message)
	errText := fmt.Sprintf("HTTP %d", status)
	if status == 0 {
		errText = "网络错误"
	}
	if message != "" {
		errText += "：" + message
	}
	log.Printf("indexnow: 批量提交 %d 条 URL 失败: %s", len(items), errText)
	return status, false, errText
}

func (s *Server) RunIndexNow() {
	runtimes := s.platformRuntimesSnapshot()
	if len(runtimes) == 0 {
		s.runIndexNowForSite()
		return
	}
	for _, rt := range runtimes {
		if rt != nil && rt.server != nil {
			rt.server.runIndexNowForSite()
		}
	}
}

type sitemapIndexNowURLSet struct {
	URLs []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

// The sitemap remains the single source of truth for manual initial sync.
func (s *Server) indexNowSitemapURLs(r *http.Request) ([]string, error) {
	if r == nil {
		return nil, fmt.Errorf("缺少站点请求上下文")
	}
	req := r.Clone(r.Context())
	req.Method = http.MethodGet
	req.URL.Path = "/sitemap.xml"
	req.URL.RawQuery = ""
	rec := httptest.NewRecorder()
	s.sitemap(rec, req)
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("生成 sitemap 返回 HTTP %d", rec.Code)
	}
	var set sitemapIndexNowURLSet
	if err := xml.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	urls := make([]string, 0, len(set.URLs))
	for _, entry := range set.URLs {
		u := strings.TrimSpace(entry.Loc)
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls, nil
}

func (s *Server) serveIndexNowKeyFile(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	p := r.URL.Path
	if !strings.HasSuffix(p, ".txt") || strings.Count(p, "/") != 1 {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(p, "/"), ".txt")
	if len(name) != indexNowKeyLen {
		return false
	}
	key := strings.TrimSpace(s.store.Setting(indexNowKeySetting))
	if key == "" || name != key {
		return false
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(key))
	return true
}
