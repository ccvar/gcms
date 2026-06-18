package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"cms.ccvar.com/internal/store"
)

type staticExportResult struct {
	Dir    string
	Files  map[string]staticExportFile
	ByHash map[string]string
	Count  int
	Bytes  int64
}

type staticExportFile struct {
	Path        string
	DiskPath    string
	Hash        string
	Size        int64
	ContentType string
}

type staticSearchEntry struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Excerpt  string `json:"excerpt,omitempty"`
	URL      string `json:"url"`
	Category string `json:"category,omitempty"`
	Date     string `json:"date,omitempty"`
}

func (s *Server) exportStaticSite(ctx context.Context, cfg CloudflareConfig) (*staticExportResult, error) {
	host := cloudflareRouteHost(cfg.RoutePattern)
	if host == "" {
		return nil, errors.New("请先填写前台访问域名。")
	}
	if s.assetsFS == nil {
		return nil, errors.New("静态资源文件系统未初始化。")
	}
	dir, err := os.MkdirTemp("", "gcms-static-*")
	if err != nil {
		return nil, err
	}
	result := &staticExportResult{
		Dir:    dir,
		Files:  map[string]staticExportFile{},
		ByHash: map[string]string{},
	}
	baseURL := "https://" + host
	handler := s.Handler()

	render := func(requestTarget, outputPath string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.exportRenderedPath(ctx, handler, host, baseURL, requestTarget, outputPath, result)
	}
	writeJSON := func(outputPath string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		return s.exportBytes(outputPath, data, "application/json; charset=utf-8", result)
	}

	locales := s.locales()
	if len(locales) == 0 {
		return nil, errors.New("没有可用语种，无法导出静态站。")
	}
	defLang := s.defaultLang()
	if err := render("/"+defLang+"/", "/index.html"); err != nil {
		return nil, err
	}

	for _, loc := range locales {
		lang := loc.Code
		prefix := "/" + lang

		if err := s.exportHomePages(render, lang, prefix); err != nil {
			return nil, err
		}
		if err := s.exportPostPages(render, lang, prefix); err != nil {
			return nil, err
		}
		if err := s.exportCategoryPages(render, lang, prefix); err != nil {
			return nil, err
		}
		if err := s.exportLinkPages(render, lang, prefix); err != nil {
			return nil, err
		}
		if err := s.exportPagePages(render, lang, prefix); err != nil {
			return nil, err
		}
		if err := render(prefix+"/api-docs", prefix+"/api-docs/index.html"); err != nil {
			return nil, err
		}
		if err := render(prefix+"/search", prefix+"/search/index.html"); err != nil {
			return nil, err
		}
		index, err := s.staticSearchIndex(lang)
		if err != nil {
			return nil, err
		}
		if err := writeJSON(prefix+"/search-index.json", index); err != nil {
			return nil, err
		}
		if err := render(prefix+"/rss.xml", prefix+"/rss.xml"); err != nil {
			return nil, err
		}
	}

	if err := render("/sitemap.xml", "/sitemap.xml"); err != nil {
		return nil, err
	}
	if err := render("/robots.txt", "/robots.txt"); err != nil {
		return nil, err
	}
	if err := render("/favicon.ico", "/favicon.ico"); err != nil {
		return nil, err
	}
	if redirects := cloudflarePagesRedirectsFile(cfg); redirects != "" {
		if err := s.exportBytes("/_redirects", []byte(redirects), "text/plain; charset=utf-8", result); err != nil {
			return nil, err
		}
	}
	if err := s.exportAssets(result); err != nil {
		return nil, err
	}
	if err := s.exportUploads(result); err != nil {
		return nil, err
	}
	return result, nil
}

func cloudflarePagesRedirectsFile(cfg CloudflareConfig) string {
	primary := cfg.primaryHost()
	if primary == "" {
		return ""
	}
	lines := []string{}
	for _, host := range cfg.redirectHosts() {
		if host == "" || sameCloudflareDNSName(host, primary) {
			continue
		}
		lines = append(lines, fmt.Sprintf("https://%s/* https://%s/:splat 301", host, primary))
	}
	if len(lines) == 0 {
		return ""
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func (s *Server) exportHomePages(render func(string, string) error, lang, prefix string) error {
	postsPerPage := s.intSetting(homePostsPerPageKey, defaultHomePostsPerPage, minHomePostsPerPage, maxHomePostsPerPage)
	total, err := s.store.CountPublished(lang)
	if err != nil {
		return err
	}
	pages := maxInt(1, ceilDiv(total, postsPerPage))
	if err := render(prefix+"/", prefix+"/index.html"); err != nil {
		return err
	}
	for pageNum := 2; pageNum <= pages; pageNum++ {
		if err := render(fmt.Sprintf("%s/?page=%d", prefix, pageNum), fmt.Sprintf("%s/page/%d/index.html", prefix, pageNum)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) exportPostPages(render func(string, string) error, lang, prefix string) error {
	posts, err := s.store.AllPublished(lang)
	if err != nil {
		return err
	}
	for _, p := range posts {
		if err := render(prefix+"/posts/"+url.PathEscape(p.Slug), prefix+"/posts/"+p.Slug+"/index.html"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) exportCategoryPages(render func(string, string) error, lang, prefix string) error {
	const size = 8
	all := s.archiveConfig(lang, "post")
	total, err := s.store.CountPublished(lang)
	if err != nil {
		return err
	}
	if err := exportPagedArchive(render, prefix, all.Path, total, size); err != nil {
		return err
	}
	cats, err := s.store.ListCategories(lang, "post")
	if err != nil {
		return err
	}
	for _, c := range cats {
		total, err := s.store.CountByCategory(c.ID)
		if err != nil {
			return err
		}
		if err := exportPagedArchive(render, prefix, "/category/"+c.Slug, total, size); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) exportLinkPages(render func(string, string) error, lang, prefix string) error {
	const size = 12
	all := s.archiveConfig(lang, "link")
	total, err := s.store.CountLinks(lang, 0)
	if err != nil {
		return err
	}
	if err := exportPagedArchive(render, prefix, all.Path, total, size); err != nil {
		return err
	}
	cats, err := s.store.ListCategories(lang, "link")
	if err != nil {
		return err
	}
	for _, c := range cats {
		total, err := s.store.CountLinks(lang, c.ID)
		if err != nil {
			return err
		}
		pages := maxInt(1, ceilDiv(total, size))
		request := prefix + "/links?cat=" + url.QueryEscape(c.Slug)
		output := prefix + "/links/cat/" + c.Slug + "/index.html"
		if err := render(request, output); err != nil {
			return err
		}
		for pageNum := 2; pageNum <= pages; pageNum++ {
			request := fmt.Sprintf("%s/links?cat=%s&page=%d", prefix, url.QueryEscape(c.Slug), pageNum)
			output := fmt.Sprintf("%s/links/cat/%s/page/%d/index.html", prefix, c.Slug, pageNum)
			if err := render(request, output); err != nil {
				return err
			}
		}
	}
	links, err := s.store.AllLinksAllLangs()
	if err != nil {
		return err
	}
	for _, p := range links {
		if p.Lang != lang {
			continue
		}
		if err := render(prefix+"/links/"+url.PathEscape(p.Slug), prefix+"/links/"+p.Slug+"/index.html"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) exportPagePages(render func(string, string) error, lang, prefix string) error {
	pages, err := s.store.AllPagesAllLangs()
	if err != nil {
		return err
	}
	for _, p := range pages {
		if p.Lang != lang {
			continue
		}
		if err := render(prefix+"/"+url.PathEscape(p.Slug), prefix+"/"+p.Slug+"/index.html"); err != nil {
			return err
		}
	}
	return nil
}

func exportPagedArchive(render func(string, string) error, prefix, archivePath string, total, size int) error {
	pages := maxInt(1, ceilDiv(total, size))
	outputBase := strings.TrimRight(prefix+archivePath, "/")
	if outputBase == "" {
		outputBase = "/"
	}
	if err := render(outputBase, outputBase+"/index.html"); err != nil {
		return err
	}
	for pageNum := 2; pageNum <= pages; pageNum++ {
		request := fmt.Sprintf("%s?page=%d", outputBase, pageNum)
		output := fmt.Sprintf("%s/page/%d/index.html", outputBase, pageNum)
		if err := render(request, output); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) staticSearchIndex(lang string) ([]staticSearchEntry, error) {
	var out []staticSearchEntry
	posts, err := s.store.AllPublished(lang)
	if err != nil {
		return nil, err
	}
	for _, p := range posts {
		out = append(out, staticSearchEntry{
			Type:     "post",
			Title:    p.Title,
			Excerpt:  p.Excerpt,
			URL:      "/" + lang + "/posts/" + p.Slug,
			Category: categoryName(p.Category),
			Date:     p.PublishedAt.Format("2006-01-02"),
		})
	}
	pages, err := s.store.AllPagesAllLangs()
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		if p.Lang != lang {
			continue
		}
		out = append(out, staticSearchEntry{
			Type:    "page",
			Title:   p.Title,
			Excerpt: p.Excerpt,
			URL:     "/" + lang + "/" + p.Slug,
			Date:    p.PublishedAt.Format("2006-01-02"),
		})
	}
	links, err := s.store.AllLinksAllLangs()
	if err != nil {
		return nil, err
	}
	for _, p := range links {
		if p.Lang != lang {
			continue
		}
		out = append(out, staticSearchEntry{
			Type:     "link",
			Title:    p.Title,
			Excerpt:  p.Excerpt,
			URL:      "/" + lang + "/links/" + p.Slug,
			Category: categoryName(p.Category),
			Date:     p.PublishedAt.Format("2006-01-02"),
		})
	}
	return out, nil
}

func categoryName(c *store.Category) string {
	if c == nil {
		return ""
	}
	return c.Name
}

func (s *Server) exportRenderedPath(ctx context.Context, handler http.Handler, host, baseURL, requestTarget, outputPath string, result *staticExportResult) error {
	u, err := url.Parse(requestTarget)
	if err != nil {
		return err
	}
	if u.Path == "" {
		u.Path = "/"
	}
	req := httptest.NewRequest(http.MethodGet, "https://"+host+u.RequestURI(), nil)
	req = req.WithContext(withPublicBase(ctx, baseURL))
	req.Host = host
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", host)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("静态导出 %s 失败：HTTP %d", requestTarget, resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(outputPath))
	}
	return s.exportBytes(outputPath, body, contentType, result)
}

func (s *Server) exportAssets(result *staticExportResult) error {
	return fs.WalkDir(s.assetsFS, "assets", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(s.assetsFS, name)
		if err != nil {
			return err
		}
		return s.exportBytes("/"+name, data, mime.TypeByExtension(filepath.Ext(name)), result)
	})
}

func (s *Server) exportUploads(result *staticExportResult) error {
	if strings.TrimSpace(s.uploadDir) == "" {
		return nil
	}
	info, err := os.Stat(s.uploadDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(s.uploadDir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil
		}
		rel, err := filepath.Rel(s.uploadDir, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !validUploadFilename(path.Base(rel)) {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return s.exportBytes("/uploads/"+rel, data, mime.TypeByExtension(filepath.Ext(rel)), result)
	})
}

func (s *Server) exportBytes(outputPath string, data []byte, contentType string, result *staticExportResult) error {
	clean := staticAssetPath(outputPath)
	if clean == "" {
		return errors.New("静态导出路径为空。")
	}
	disk := filepath.Join(result.Dir, strings.TrimPrefix(clean, "/"))
	if err := os.MkdirAll(filepath.Dir(disk), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(disk, data, 0o644); err != nil {
		return err
	}
	hash := cloudflareAssetHash(clean, data)
	info := staticExportFile{
		Path:        clean,
		DiskPath:    disk,
		Hash:        hash,
		Size:        int64(len(data)),
		ContentType: contentType,
	}
	result.Files[clean] = info
	if _, ok := result.ByHash[hash]; !ok {
		result.ByHash[hash] = disk
	}
	result.Count = len(result.Files)
	result.Bytes = 0
	for _, f := range result.Files {
		result.Bytes += f.Size
	}
	return nil
}

func staticAssetPath(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "/") {
		v = "/" + v
	}
	v = path.Clean(v)
	if v == "." {
		return ""
	}
	if strings.HasSuffix(v, "/.") {
		v = strings.TrimSuffix(v, ".")
	}
	return v
}

func cloudflareAssetHash(assetPath string, data []byte) string {
	ext := path.Ext(assetPath)
	sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(data) + ext))
	return hex.EncodeToString(sum[:])[:32]
}

func sortedStaticFilePaths(files map[string]staticExportFile) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
