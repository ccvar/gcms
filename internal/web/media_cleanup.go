package web

// 孤儿上传文件清理（平台设置 → 存储清理）：扫描各站点内容与站点设置里的
// uploads/ 引用，未被引用的文件先移入该站 uploads/.trash 隔离，
// 进入隔离超过 7 天后再真正删除。

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/store"
)

const (
	mediaTrashDirName = ".trash"           // uploads 下的隔离目录
	mediaTrashMaxAge  = 7 * 24 * time.Hour // 隔离期：超过后真删
)

// mediaOrphan 一个未被引用的上传文件。
type mediaOrphan struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// MediaCleanupSite 存储清理页表格里的一行站点。
type MediaCleanupSite struct {
	ID     int64
	Name   string
	Slug   string
	Domain string // 主域名；未绑定时为空
}

// mediaCleanupTarget 一次清理要处理的站点（store + uploads 目录）。
type mediaCleanupTarget struct {
	ID   int64
	Name string
	Slug string
	st   *store.Store
	dir  string
}

// mediaCleanupSiteReport 单站扫描 / 清理结果（JSON 输出；Count 在清理时表示移动数）。
type mediaCleanupSiteReport struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

// isUploadNameChar 判断字符是否可能出现在上传文件名里（与 validUploadFilename 的字符集一致）。
func isUploadNameChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == '.':
		return true
	}
	return false
}

// scanUploadRefs 在一段文本里查找 "uploads/<文件名>" 引用，把文件名（basename）记入 refs。
// 上传文件名全站唯一，因此只按 basename 匹配即可，不必关心引用写的是相对还是绝对路径。
func scanUploadRefs(text string, refs map[string]bool) {
	const marker = "uploads/"
	for i := 0; ; {
		idx := strings.Index(text[i:], marker)
		if idx < 0 {
			return
		}
		start := i + idx + len(marker)
		end := start
		for end < len(text) && isUploadNameChar(text[end]) {
			end++
		}
		// 去掉句尾等场景粘上的多余点号（合法文件名不会以点开头或结尾）。
		if name := strings.Trim(text[start:end], "."); name != "" {
			refs[name] = true
		}
		i = start
	}
}

// collectReferencedUploads 收集一个站点被引用的 uploads 文件名集合。
// 扫描来源：所有 posts 行的全部文本字段（所有语种、所有类型、含草稿，正文 / 封面 / extra JSON 等），
// 以及 settings 表全部 value（Logo、favicon、分享图、hero、导航等都存在其中）。
func collectReferencedUploads(st *store.Store) (map[string]bool, error) {
	refs := map[string]bool{}
	texts, err := st.AllPostReferenceTexts()
	if err != nil {
		return nil, fmt.Errorf("扫描内容引用失败: %w", err)
	}
	for _, t := range texts {
		scanUploadRefs(t, refs)
	}
	values, err := st.AllSettingValues()
	if err != nil {
		return nil, fmt.Errorf("扫描设置引用失败: %w", err)
	}
	for _, v := range values {
		scanUploadRefs(v, refs)
	}
	return refs, nil
}

// listOrphanUploads 扫描站点 uploads 目录，列出未被引用的文件（跳过子目录、.trash 与其他隐藏文件）。
func listOrphanUploads(st *store.Store, uploadDir string) ([]mediaOrphan, error) {
	refs, err := collectReferencedUploads(st)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return nil, fmt.Errorf("读取上传目录失败: %w", err)
	}
	var orphans []mediaOrphan
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || refs[name] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		orphans = append(orphans, mediaOrphan{Name: name, Size: info.Size()})
	}
	return orphans, nil
}

// trashOrphanUploads 把孤儿文件移入 uploads/.trash 隔离（时间戳前缀防重名），
// 并把 mtime 重置为当下，作为隔离入库时间供 purge 判断。返回移动数与总字节。
func trashOrphanUploads(uploadDir string, orphans []mediaOrphan) (int, int64, error) {
	trashDir := filepath.Join(uploadDir, mediaTrashDirName)
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		return 0, 0, fmt.Errorf("创建回收站目录失败: %w", err)
	}
	now := time.Now()
	prefix := now.Format("20060102-150405")
	var moved int
	var total int64
	for _, o := range orphans {
		dst := filepath.Join(trashDir, prefix+"-"+o.Name)
		if err := os.Rename(filepath.Join(uploadDir, o.Name), dst); err != nil {
			continue // 单个失败不阻塞其余文件
		}
		_ = os.Chtimes(dst, now, now)
		moved++
		total += o.Size
	}
	return moved, total, nil
}

// purgeExpiredTrash 真删 .trash 里进入隔离超过 maxAge 的文件（按 mtime 判断）。
func purgeExpiredTrash(uploadDir string, maxAge time.Duration) {
	trashDir := filepath.Join(uploadDir, mediaTrashDirName)
	entries, err := os.ReadDir(trashDir)
	if err != nil {
		return
	}
	deadline := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(deadline) {
			continue
		}
		_ = os.Remove(filepath.Join(trashDir, e.Name()))
	}
}

// mediaCleanupTargets 遍历平台全部站点，经运行时池取各站 store 与 uploads 目录（不重复开库）。
func (s *Server) mediaCleanupTargets() ([]mediaCleanupTarget, error) {
	sites, err := s.platform.Sites()
	if err != nil {
		return nil, err
	}
	pool := s.runtimePool()
	if pool == nil {
		return nil, fmt.Errorf("站点运行时池未初始化")
	}
	var out []mediaCleanupTarget
	for _, site := range sites {
		if site == nil {
			continue
		}
		rt, ok := pool.runtimeByID(site.ID)
		if !ok || rt == nil || rt.Store == nil || strings.TrimSpace(rt.UploadDir) == "" {
			continue
		}
		out = append(out, mediaCleanupTarget{ID: site.ID, Name: site.Name, Slug: site.Slug, st: rt.Store, dir: rt.UploadDir})
	}
	return out, nil
}

// adminMediaCleanupPage 平台设置 → 存储清理页：列出全部站点，扫描 / 清理由前端 AJAX 完成。
func (s *Server) adminMediaCleanupPage(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
		return
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "存储清理")
	s.platformAuthed(v, sess)
	targets, err := s.mediaCleanupTargets()
	if err != nil {
		s.serverError(w, err)
		return
	}
	// 域名回退链：自绑主域名（SiteDomains 里 enabled 的 primary）→ 站点管理卡片同源的
	// 公开地址（platformAuthed 已经通过 populatePlatformSites 填好 PlatformOfficialHosts，
	// 覆盖 Cloudflare 部署域名等）；都没有才在页面上显示「未绑定域名」。
	primaryHosts := map[int64]string{}
	if domains, err := s.platform.SiteDomains(); err == nil {
		for _, d := range domains {
			if d != nil && d.Enabled && d.IsPrimary {
				primaryHosts[d.SiteID] = d.Host
			}
		}
	}
	for _, t := range targets {
		domain := primaryHosts[t.ID]
		if domain == "" {
			domain = v.PlatformOfficialHosts[t.ID]
		}
		v.MediaCleanupSites = append(v.MediaCleanupSites, MediaCleanupSite{ID: t.ID, Name: t.Name, Slug: t.Slug, Domain: domain})
	}
	s.rnd.Admin(w, "media_cleanup", http.StatusOK, v)
}

// adminMediaCleanupScan 全站扫描（只报告不动文件）：逐站列出未引用文件数与大小，
// 顺带真删各站隔离超过 7 天的回收站文件。返回 {ok,count,bytes,sites}。
func (s *Server) adminMediaCleanupScan(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "platform_only", "message": "仅平台控制台可用。"})
		return
	}
	targets, err := s.mediaCleanupTargets()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "scan_failed", "message": err.Error()})
		return
	}
	reports := make([]mediaCleanupSiteReport, 0, len(targets))
	var count int
	var total int64
	for _, t := range targets {
		purgeExpiredTrash(t.dir, mediaTrashMaxAge)
		rep := mediaCleanupSiteReport{ID: t.ID, Name: t.Name, Slug: t.Slug}
		orphans, err := listOrphanUploads(t.st, t.dir)
		if err != nil {
			rep.Error = err.Error() // 单站失败不阻塞其余站点
		}
		for _, o := range orphans {
			rep.Count++
			rep.Bytes += o.Size
		}
		count += rep.Count
		total += rep.Bytes
		reports = append(reports, rep)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count, "bytes": total, "sites": reports})
}

// adminMediaCleanupClean 把未引用文件移入各站回收站；表单 site=<站点 ID> 时只清理该站。
// 顺带真删隔离超过 7 天的回收站文件。返回 {ok,moved,bytes,sites}。
func (s *Server) adminMediaCleanupClean(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "platform_only", "message": "仅平台控制台可用。"})
		return
	}
	targets, err := s.mediaCleanupTargets()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "scan_failed", "message": err.Error()})
		return
	}
	if raw := strings.TrimSpace(r.FormValue("site")); raw != "" {
		siteID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_site", "message": "无效的站点 ID。"})
			return
		}
		var picked []mediaCleanupTarget
		for _, t := range targets {
			if t.ID == siteID {
				picked = append(picked, t)
			}
		}
		if len(picked) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "site_not_found", "message": "站点不存在或未启用上传目录。"})
			return
		}
		targets = picked
	}
	reports := make([]mediaCleanupSiteReport, 0, len(targets))
	var moved int
	var total int64
	for _, t := range targets {
		purgeExpiredTrash(t.dir, mediaTrashMaxAge)
		rep := mediaCleanupSiteReport{ID: t.ID, Name: t.Name, Slug: t.Slug}
		orphans, err := listOrphanUploads(t.st, t.dir)
		if err != nil {
			rep.Error = err.Error()
		} else if n, b, err := trashOrphanUploads(t.dir, orphans); err != nil {
			rep.Error = err.Error()
		} else {
			rep.Count = n
			rep.Bytes = b
		}
		moved += rep.Count
		total += rep.Bytes
		reports = append(reports, rep)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "moved": moved, "bytes": total, "sites": reports})
}
