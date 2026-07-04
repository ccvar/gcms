package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubCaddy 往临时 PATH 目录放一个可控的假 caddy（记录调用、可指定 validate 失败），
// 并让 systemctl 不存在（reload 走 caddy reload 分支）。返回调用日志文件路径。
func stubCaddy(t *testing.T, failValidateOn string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := `#!/bin/sh
echo "$@" >> "` + log + `"
if [ "$1" = "validate" ] && [ -n "` + failValidateOn + `" ]; then
  case "$3" in *` + failValidateOn + `*) exit 1;; esac
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "caddy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin) // 只留 stub：systemctl 不存在 → reload 用 caddy reload
	return log
}

func caddyTestEnv(t *testing.T) (confDir string) {
	t.Helper()
	confDir = t.TempDir()
	main := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(main, []byte("import "+confDir+"/*.caddy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CADDY_CONF_DIR", confDir)
	t.Setenv("CADDYFILE", main)
	return confDir
}

// TestApplyCaddyDirect 直写模式：保存后生成 gcms-<域名>.caddy、清理孤儿、绝不碰 gcms.caddy、触发重载。
func TestApplyCaddyDirect(t *testing.T) {
	log := stubCaddy(t, "")
	confDir := caddyTestEnv(t)
	// 预置：一个孤儿站点文件 + 安装文件 gcms.caddy（必须原样保留）
	if err := os.WriteFile(filepath.Join(confDir, "gcms-old.example.caddy"), []byte("old {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "gcms.caddy"), []byte("# install file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, _, ps, _, blogSite := setupPlatformAutomation(t)
	if err := ps.AddSiteDomain(blogSite.ID, "https", "www.bgvar.com", false, true); err != nil {
		t.Fatal(err)
	}

	msg, handled := srv.applyCaddyDirect()
	if !handled {
		t.Fatalf("direct write should handle when conf dir writable")
	}
	if !strings.Contains(msg, "已写入 1 个") || !strings.Contains(msg, "并重载生效") {
		t.Fatalf("msg = %q", msg)
	}

	// 站点文件：主域名命名 + 内容含别名 301 + reverse_proxy
	b, err := os.ReadFile(filepath.Join(confDir, "gcms-blog.test.caddy"))
	if err != nil {
		t.Fatalf("site file not written: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "redir https://blog.test{uri} permanent") || !strings.Contains(content, "reverse_proxy") {
		t.Fatalf("site file content unexpected: %s", content)
	}
	// 孤儿清理 + 安装文件保留
	if _, err := os.Stat(filepath.Join(confDir, "gcms-old.example.caddy")); !os.IsNotExist(err) {
		t.Fatalf("orphan gcms-old.example.caddy not removed")
	}
	if _, err := os.Stat(filepath.Join(confDir, "gcms.caddy")); err != nil {
		t.Fatalf("install file gcms.caddy must be untouched: %v", err)
	}
	// 调用序列：validate（临时文件+主文件）+ reload
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "validate") || !strings.Contains(string(calls), "reload") {
		t.Fatalf("caddy calls unexpected: %s", calls)
	}
}

// TestApplyCaddyDirectRollback 主 Caddyfile 校验失败 → 整组回滚（新文件消失、孤儿复原）。
func TestApplyCaddyDirectRollback(t *testing.T) {
	stubCaddy(t, "Caddyfile") // 仅主 Caddyfile validate 失败
	confDir := caddyTestEnv(t)
	if err := os.WriteFile(filepath.Join(confDir, "gcms-keep.example.caddy"), []byte("keep {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, _, _, _, _ := setupPlatformAutomation(t)
	msg, handled := srv.applyCaddyDirect()
	if !handled || !strings.Contains(msg, "已整组回滚") {
		t.Fatalf("expected rollback message, got handled=%v msg=%q", handled, msg)
	}
	// 新站点文件不应留下；原有文件应复原
	if _, err := os.Stat(filepath.Join(confDir, "gcms-blog.test.caddy")); !os.IsNotExist(err) {
		t.Fatalf("new site file should be rolled back")
	}
	if b, err := os.ReadFile(filepath.Join(confDir, "gcms-keep.example.caddy")); err != nil || string(b) != "keep {}\n" {
		t.Fatalf("pre-existing group file not restored: %v %q", err, b)
	}
}

// TestApplyCaddyDirectUnwritable conf 目录不可写 → handled=false，交给脚本/手动路径。
func TestApplyCaddyDirectUnwritable(t *testing.T) {
	stubCaddy(t, "")
	confDir := caddyTestEnv(t)
	if err := os.Chmod(confDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(confDir, 0o755) })

	srv, _, _, _, _ := setupPlatformAutomation(t)
	if _, handled := srv.applyCaddyDirect(); handled {
		t.Fatalf("unwritable dir must not be handled by direct mode")
	}
}
