// CCVAR 简记 —— 用 Go + SQLite 构建的轻量 CMS。
// 单一二进制：模板与静态资源经 embed 打包，数据存于一个 SQLite 文件。
package main

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
	"cms.ccvar.com/internal/web"
)

//go:embed templates
var templatesFS embed.FS

//go:embed assets
var assetsFS embed.FS

func main() {
	dbPath := env("CMS_DB", "data/cms.db")
	systemDBPath := env("SYSTEM_DB", filepath.Join(filepath.Dir(dbPath), "system.db"))
	if len(os.Args) == 2 && os.Args[1] == "pilot-security-status" {
		printPilotSecurityStatus(dbPath, systemDBPath)
		return
	}
	if dir := filepath.Dir(dbPath); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	baseURL := env("BASE_URL", "http://localhost:8080")
	uploadDir := env("UPLOAD_DIR", filepath.Join(filepath.Dir(dbPath), "uploads"))
	ps, err := platform.Open(systemDBPath)
	if err != nil {
		log.Fatalf("打开平台数据库失败: %v", err)
	}
	defer ps.Close()
	adminUser, _ := st.GetSetting("admin_user")
	adminHash, _ := st.GetSetting("admin_password_hash")
	siteName, _ := st.GetSetting("site.name")
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug:                        "main",
		Name:                        siteName,
		DBPath:                      dbPath,
		UploadDir:                   uploadDir,
		AdminUser:                   adminUser,
		AdminPasswordHash:           adminHash,
		ManagementAutomationEnabled: true,
	}); err != nil {
		log.Fatalf("初始化平台默认站点失败: %v", err)
	}
	srv, err := web.NewWithPlatform(st, ps, baseURL, uploadDir, templatesFS, assetsFS)
	if err != nil {
		log.Fatalf("初始化 Web 失败: %v", err)
	}

	// 定时把到点的「定时发布」文章翻为已发布（启动时先处理一次）。
	// 走 Server 方法而非裸 st.PublishDue()：翻发布同时触发发布钩子（sitemap 缓存失效、Telegram 推送）。
	srv.RunScheduledPublish()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			srv.RunScheduledPublish()
		}
	}()

	addr := env("ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("CCVAR 简记 已启动 → http://localhost%s  （后台 /admin）", addr)
	// 首次启动（播种了演示数据）→ 在控制台醒目打印默认账号密码
	if st.Seeded {
		user := "admin"
		if u, _ := st.GetSetting("admin_user"); u != "" {
			user = u
		}
		fmt.Fprint(os.Stderr, "\n"+
			"  ┌─────────────────────────────────────────────┐\n"+
			"  │  首次启动已创建演示数据                       │\n"+
			"  │  后台地址：/admin                            │\n"+
			fmt.Sprintf("  │  默认用户名：%-31s│\n", user)+
			fmt.Sprintf("  │  默认密码：  %-31s│\n", store.DefaultAdminPassword)+
			"  │  请登录后尽快在「设置 → 安全」修改密码        │\n"+
			"  └─────────────────────────────────────────────┘\n\n")
	}
	log.Fatal(httpSrv.ListenAndServe())
}

// printPilotSecurityStatus 是给服务器本机运维探针使用的只读命令。
// 它只输出状态和用户名，绝不输出密码哈希；公网 HTTP 路由不会暴露该信息。
func printPilotSecurityStatus(dbPath, systemDBPath string) {
	user, hash := readPilotAdminCredentials(systemDBPath, dbPath)
	status := "unknown"
	if hash != "" {
		status = "changed"
		if store.IsDefaultAdminPasswordHash(hash) {
			status = "default"
		}
	}
	fmt.Printf("PILOT_GCMS_PASSWORD_STATUS\t%s\n", status)
	fmt.Printf("PILOT_GCMS_ADMIN_USER\t%s\n", pilotStatusField(user))
}

func readPilotAdminCredentials(systemDBPath, dbPath string) (string, string) {
	if user, hash, ok := queryPilotAdminCredentials(systemDBPath,
		`SELECT username,password_hash FROM platform_admins ORDER BY id ASC LIMIT 1`); ok && hash != "" {
		return user, hash
	}
	return queryPilotSiteCredentials(dbPath)
}

func queryPilotSiteCredentials(path string) (string, string) {
	if strings.TrimSpace(path) == "" {
		return "", ""
	}
	if _, err := os.Stat(path); err != nil {
		return "", ""
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", filepath.ToSlash(path)))
	if err != nil {
		return "", ""
	}
	defer db.Close()
	var user, hash sql.NullString
	err = db.QueryRow(`SELECT
		(SELECT value FROM settings WHERE key='admin_user'),
		(SELECT value FROM settings WHERE key='admin_password_hash')`).Scan(&user, &hash)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(user.String), strings.TrimSpace(hash.String)
}

func queryPilotAdminCredentials(path, query string) (string, string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", "", false
	}
	if _, err := os.Stat(path); err != nil {
		return "", "", false
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", filepath.ToSlash(path)))
	if err != nil {
		return "", "", false
	}
	defer db.Close()
	var user, hash string
	if err := db.QueryRow(query).Scan(&user, &hash); err != nil {
		return "", "", false
	}
	return strings.TrimSpace(user), strings.TrimSpace(hash), true
}

func pilotStatusField(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
