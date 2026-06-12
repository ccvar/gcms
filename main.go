// CCVAR 简记 —— 用 Go + SQLite 构建的轻量 CMS。
// 单一二进制：模板与静态资源经 embed 打包，数据存于一个 SQLite 文件。
package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cms.ccvar.com/internal/store"
	"cms.ccvar.com/internal/web"
)

//go:embed templates
var templatesFS embed.FS

//go:embed assets
var assetsFS embed.FS

func main() {
	dbPath := env("CMS_DB", "data/cms.db")
	if dir := filepath.Dir(dbPath); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	// 定时把到点的「定时发布」文章翻为已发布（启动时先处理一次）
	_, _ = st.PublishDue()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			_, _ = st.PublishDue()
		}
	}()

	baseURL := env("BASE_URL", "http://localhost:8080")
	uploadDir := env("UPLOAD_DIR", filepath.Join(filepath.Dir(dbPath), "uploads"))
	srv, err := web.New(st, baseURL, uploadDir, templatesFS, assetsFS)
	if err != nil {
		log.Fatalf("初始化 Web 失败: %v", err)
	}

	addr := env("ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
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

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
