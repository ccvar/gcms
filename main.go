// CCVAR 简记 —— 用 Go + SQLite 构建的轻量 CMS。
// 单一二进制：模板与静态资源经 embed 打包，数据存于一个 SQLite 文件。
package main

import (
	"bytes"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
	"cms.ccvar.com/internal/web"
	"golang.org/x/crypto/bcrypt"
)

//go:embed templates
var templatesFS embed.FS

//go:embed assets
var assetsFS embed.FS

func main() {
	dbPath := env("CMS_DB", "data/cms.db")
	systemDBPath := env("SYSTEM_DB", filepath.Join(filepath.Dir(dbPath), "system.db"))
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "pilot-security-status":
			printPilotSecurityStatus(dbPath, systemDBPath)
			return
		case "pilot-set-admin-password":
			if err := setPilotAdminPassword(dbPath, systemDBPath, os.Stdin, os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "修改后台密码失败：%v\n", err)
				os.Exit(1)
			}
			return
		}
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

// bcrypt 只安全定义到 72 字节；在读取阶段就设硬上限，避免悄悄截断两个不同密码。
const pilotAdminPasswordMaxBytes = 72

// setPilotAdminPassword 是给服务器本机运维使用的写入命令。
// 新密码只从 stdin 读取，绝不接受命令行参数，也不会写入输出、日志或配置文件。
func setPilotAdminPassword(dbPath, systemDBPath string, input io.Reader, output io.Writer) error {
	password, err := readPilotAdminPassword(input)
	if err != nil {
		return err
	}
	defer clearBytes(password)

	if strings.TrimSpace(dbPath) == "" {
		return errors.New("未配置站点数据库")
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return errors.New("站点数据库不存在")
		}
		return fmt.Errorf("无法读取站点数据库：%w", err)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("打开站点数据库：%w", err)
	}
	defer st.Close()
	ps, err := platform.Open(systemDBPath)
	if err != nil {
		return fmt.Errorf("打开平台数据库：%w", err)
	}
	defer ps.Close()

	user, _, credentialsErr := ps.GetAdminCredentials()
	if credentialsErr != nil && !errors.Is(credentialsErr, sql.ErrNoRows) {
		return fmt.Errorf("读取平台管理员：%w", credentialsErr)
	}
	if strings.TrimSpace(user) == "" {
		user, _ = st.GetSetting("admin_user")
	}
	if strings.TrimSpace(user) == "" {
		user = "admin"
	}

	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("生成密码凭据：%w", err)
	}
	defer clearBytes(hash)

	// 先注销所有旧会话，再同步平台与旧站点凭据。平台库是多站后台的权威来源，
	// 站点库继续同步，保证旧版与降级读取仍使用同一个密码。
	if err := ps.RevokeAdminSessions(); err != nil {
		return fmt.Errorf("注销平台旧会话：%w", err)
	}
	if err := st.RevokeAdminSessions(); err != nil {
		return fmt.Errorf("注销站点旧会话：%w", err)
	}
	if err := ps.SetAdminPasswordHash(user, string(hash)); err != nil {
		return fmt.Errorf("更新平台管理员密码：%w", err)
	}
	if err := st.SetSetting("admin_user", user); err != nil {
		return fmt.Errorf("同步站点管理员：%w", err)
	}
	if err := st.SetSetting("admin_password_hash", string(hash)); err != nil {
		return fmt.Errorf("同步站点管理员密码：%w", err)
	}

	fmt.Fprintln(output, "PILOT_GCMS_PASSWORD_UPDATED\t1")
	fmt.Fprintf(output, "PILOT_GCMS_ADMIN_USER\t%s\n", pilotStatusField(user))
	return nil
}

func readPilotAdminPassword(input io.Reader) ([]byte, error) {
	if input == nil {
		return nil, errors.New("没有收到新密码")
	}
	password, err := io.ReadAll(io.LimitReader(input, pilotAdminPasswordMaxBytes+1))
	if err != nil {
		return nil, errors.New("读取新密码失败")
	}
	if len(password) > pilotAdminPasswordMaxBytes {
		clearBytes(password)
		return nil, errors.New("新密码过长，最多 72 个英文字符（中文等字符占用更多长度）")
	}
	// 兼容人在终端中输入后按 Enter；由 Pilot 传入时没有行尾，不会改变密码内容。
	password = bytes.TrimSuffix(password, []byte{'\n'})
	password = bytes.TrimSuffix(password, []byte{'\r'})
	if !utf8.Valid(password) {
		clearBytes(password)
		return nil, errors.New("新密码必须是有效文本")
	}
	if bytes.ContainsAny(password, "\x00\r\n") {
		clearBytes(password)
		return nil, errors.New("新密码不能包含换行或空字符")
	}
	length := utf8.RuneCount(password)
	if length < 8 {
		clearBytes(password)
		return nil, errors.New("新密码至少需要 8 个字符")
	}
	if bytes.Equal(password, []byte(store.DefaultAdminPassword)) {
		clearBytes(password)
		return nil, errors.New("新密码不能继续使用默认密码")
	}
	return password, nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
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
