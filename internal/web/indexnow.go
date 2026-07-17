package web

// indexnow.go 发布钩子：posts/pages/links 发布或已发布内容更新时——
//  a) 立即失效 sitemap 端点缓存（保证 lastmod 实时；内容写路径的 clearGeneratedCaches
//     会整体清空，这里再显式兜底一次）；
//  b) 异步（goroutine，不阻塞响应，失败仅日志）把该内容 URL 提交到 IndexNow
//     （https://api.indexnow.org/indexnow），让 Bing 等引擎实时抓取。
// IndexNow key 首次用时生成（crypto/rand 32 hex）存 settings；校验要求站点在
// GET /{key}.txt 原样返回 key 文本（见 serveIndexNowKeyFile，withLocale 前置匹配）。

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cms.ccvar.com/internal/store"
)

const (
	indexNowKeySetting = "indexnow.key"
	indexNowEndpoint   = "https://api.indexnow.org/indexnow"
	indexNowKeyLen     = 32 // hex 字符数（16 字节）
)

// indexNowHTTPClient 提交用小超时客户端（异步 goroutine 内使用）。
var indexNowHTTPClient = &http.Client{Timeout: 10 * time.Second}

// generateIndexNowKey 生成 32 位小写 hex key（crypto/rand 16 字节）。
func generateIndexNowKey() (string, error) {
	var b [indexNowKeyLen / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// indexNowKey 读取本站 IndexNow key；没有则首次生成并写入 settings（互斥防并发双写）。
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

// buildIndexNowURL 纯函数：组装单条提交的 GET URL（?url=<页面>&key=<key>，含转义）。
func buildIndexNowURL(endpoint, pageURL, key string) string {
	q := url.Values{}
	q.Set("url", pageURL)
	q.Set("key", key)
	return endpoint + "?" + q.Encode()
}

// firePublishHooks 发布钩子入口：内容发布或已发布内容更新后调用（admin 与自动化 API 共用）。
// 类型白名单 = post/page/link + 本站已启用的扩展类型（如商品）——工厂站发布商品同样要
// 失效 sitemap 缓存、提交 IndexNow、触发 Telegram 推送；未启用类型不触发（防脏数据）。
// 仅处理 status=published；本地/未配置域名的站点跳过 IndexNow 提交
// （URL 对搜索引擎无意义，也保证测试不打真网）。
func (s *Server) firePublishHooks(r *http.Request, p *store.Post) {
	if p == nil || p.Status != "published" {
		return
	}
	switch p.Type {
	case "post", "page", "link":
	default:
		if !s.contentTypeActive(p.Type) {
			return
		}
	}
	s.invalidateSitemapCache()
	// Telegram 频道自动推送（posts + 已启用扩展类型；台账去重，异步发送，绝不阻塞发布）。
	s.fireTelegramPush(r, p)
	base := s.publicBaseURL(r)
	if isLocalBaseURL(base) {
		return
	}
	pageURL := absWithBase(base, s.apiContentURL(p))
	key, err := s.indexNowKey()
	if err != nil {
		log.Printf("indexnow: 读取/生成 key 失败: %v", err)
		return
	}
	go submitIndexNow(pageURL, key)
}

// invalidateSitemapCache 只清 sitemap 端点缓存（key 前缀 "sitemap:"），发布后 lastmod 实时。
func (s *Server) invalidateSitemapCache() {
	s.cacheMu.Lock()
	for k := range s.endpoints {
		if strings.HasPrefix(k, "sitemap:") {
			delete(s.endpoints, k)
		}
	}
	s.cacheMu.Unlock()
}

// submitIndexNow 实际提交（goroutine 内执行）：失败只记日志，绝不影响发布主流程。
func submitIndexNow(pageURL, key string) {
	resp, err := indexNowHTTPClient.Get(buildIndexNowURL(indexNowEndpoint, pageURL, key))
	if err != nil {
		log.Printf("indexnow: 提交 %s 失败: %v", pageURL, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("indexnow: 提交 %s 返回 HTTP %d", pageURL, resp.StatusCode)
	}
}

// serveIndexNowKeyFile GET /{key}.txt：IndexNow 校验要求站点根路径原样返回 key 文本。
// 只匹配「已生成」的 key（读路径绝不触发生成），避免任意 .txt 探测造成写库。
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
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(key))
	return true
}
