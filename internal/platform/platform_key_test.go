package platform

import (
	"path/filepath"
	"testing"
	"time"
)

// openPlatformStore 打开一个临时平台库。
func openPlatformStore(t *testing.T) *Store {
	t.Helper()
	ps, err := Open(filepath.Join(t.TempDir(), "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// mkSite 造一个 enabled + 开启自动化的站点。
func mkSite(t *testing.T, ps *Store, slug string) *Site {
	t.Helper()
	site, err := ps.CreateSite(slug, slug+" name", filepath.Join(t.TempDir(), slug+".db"), filepath.Join(t.TempDir(), slug), true)
	if err != nil {
		t.Fatalf("create site %s: %v", slug, err)
	}
	return site
}

func idsOf(sites []*Site) map[int64]bool {
	m := map[int64]bool{}
	for _, s := range sites {
		m[s.ID] = true
	}
	return m
}

func TestPlatformKeyAllowlistMembership(t *testing.T) {
	ps := openPlatformStore(t)
	a, b, c := mkSite(t, ps, "alpha"), mkSite(t, ps, "beta"), mkSite(t, ps, "gamma")

	token := "gcmsp_allow123"
	id, err := ps.CreatePlatformKey("bot", token, "gcmsp_allow12", KeyMembershipAllowlist,
		"posts:read,posts:write", []int64{a.ID, b.ID}, time.Time{})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	key, ok, err := ps.GetPlatformKeyByToken(token)
	if err != nil || !ok {
		t.Fatalf("get by token: ok=%v err=%v", ok, err)
	}
	if key.ID != id || key.MembershipMode != KeyMembershipAllowlist {
		t.Fatalf("key mismatch: %#v", key)
	}
	if len(key.AllowedSiteIDs) != 2 {
		t.Fatalf("allowlist not hydrated: %#v", key.AllowedSiteIDs)
	}
	if !key.CanManageSite(a.ID) || !key.CanManageSite(b.ID) || key.CanManageSite(c.ID) {
		t.Fatalf("CanManageSite wrong for allowlist key")
	}

	manageable, err := ps.ManageableSites(key)
	if err != nil {
		t.Fatalf("manageable: %v", err)
	}
	got := idsOf(manageable)
	if len(got) != 2 || !got[a.ID] || !got[b.ID] {
		t.Fatalf("manageable = %v, want alpha+beta", got)
	}

	// 发现即可调用：gamma 不在成员范围 → PlatformKeyCanAccessSite 为假。
	if okC, _ := ps.PlatformKeyCanAccessSite(key, c.ID); okC {
		t.Fatalf("gamma should be forbidden for allowlist key")
	}
	if okA, err := ps.PlatformKeyCanAccessSite(key, a.ID); err != nil || !okA {
		t.Fatalf("alpha should be allowed: ok=%v err=%v", okA, err)
	}
}

func TestPlatformKeyAllMembershipCoversFutureSites(t *testing.T) {
	ps := openPlatformStore(t)
	a := mkSite(t, ps, "alpha")

	token := "gcmsp_all123"
	if _, err := ps.CreatePlatformKey("all-bot", token, "gcmsp_all123", KeyMembershipAll, "posts:read", nil, time.Time{}); err != nil {
		t.Fatalf("create all key: %v", err)
	}
	key, ok, err := ps.GetPlatformKeyByToken(token)
	if err != nil || !ok {
		t.Fatalf("get all key: ok=%v err=%v", ok, err)
	}
	if len(key.AllowedSiteIDs) != 0 {
		t.Fatalf("all-mode key should have zero join rows, got %#v", key.AllowedSiteIDs)
	}

	before, _ := ps.ManageableSites(key)
	if len(before) != 1 || before[0].ID != a.ID {
		t.Fatalf("before = %v, want alpha only", idsOf(before))
	}

	// 后加站点：all 模式无需重新下发即自动覆盖。
	b := mkSite(t, ps, "beta")
	after, _ := ps.ManageableSites(key)
	if got := idsOf(after); len(got) != 2 || !got[a.ID] || !got[b.ID] {
		t.Fatalf("after adding beta, manageable = %v, want alpha+beta", got)
	}
	if !key.CanManageSite(b.ID) {
		t.Fatalf("all-mode key should manage newly-added site")
	}
}

func TestPlatformKeyLiveAuthzReactsToSiteToggles(t *testing.T) {
	ps := openPlatformStore(t)
	a := mkSite(t, ps, "alpha")

	token := "gcmsp_live123"
	if _, err := ps.CreatePlatformKey("bot", token, "gcmsp_live123", KeyMembershipAll, "posts:read", nil, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	key, _, _ := ps.GetPlatformKeyByToken(token)

	if okA, _ := ps.PlatformKeyCanAccessSite(key, a.ID); !okA {
		t.Fatalf("alpha should start accessible")
	}
	// 关闭该站自动化 → 立即失效（红队要求：实时读平台库，非缓存 rt.Site）。
	if err := ps.SetSiteAutomation(a.ID, false); err != nil {
		t.Fatalf("disable automation: %v", err)
	}
	if okA, _ := ps.PlatformKeyCanAccessSite(key, a.ID); okA {
		t.Fatalf("alpha must be forbidden right after automation off")
	}
	if m, _ := ps.ManageableSites(key); len(m) != 0 {
		t.Fatalf("manageable should be empty after automation off, got %v", idsOf(m))
	}
	// 重新开启 → 恢复。
	_ = ps.SetSiteAutomation(a.ID, true)
	if okA, _ := ps.PlatformKeyCanAccessSite(key, a.ID); !okA {
		t.Fatalf("alpha should be accessible again")
	}
	// 停用站点（status）→ 失效。
	if err := ps.SetSiteStatus(a.ID, "disabled"); err != nil {
		t.Fatalf("disable status: %v", err)
	}
	if okA, _ := ps.PlatformKeyCanAccessSite(key, a.ID); okA {
		t.Fatalf("disabled site must be forbidden")
	}
}

func TestPlatformKeyArchiveCascadesAllowlist(t *testing.T) {
	ps := openPlatformStore(t)
	a, b := mkSite(t, ps, "alpha"), mkSite(t, ps, "beta")

	token := "gcmsp_arch123"
	id, err := ps.CreatePlatformKey("bot", token, "gcmsp_arch123", KeyMembershipAllowlist, "posts:read", []int64{a.ID, b.ID}, time.Time{})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	// 归档 alpha：ON DELETE CASCADE 应删除其白名单行（fail-closed）。归档前须先停用。
	if err := ps.SetSiteStatus(a.ID, "disabled"); err != nil {
		t.Fatalf("disable alpha: %v", err)
	}
	if _, err := ps.ArchiveSite(a.ID, filepath.Join(t.TempDir(), "alpha-archive.db")); err != nil {
		t.Fatalf("archive alpha: %v", err)
	}
	key, ok, err := ps.GetPlatformKey(id)
	if err != nil || !ok {
		t.Fatalf("get key: ok=%v err=%v", ok, err)
	}
	if len(key.AllowedSiteIDs) != 1 || key.AllowedSiteIDs[0] != b.ID {
		t.Fatalf("after archive, allowlist = %v, want beta only", key.AllowedSiteIDs)
	}
	if key.CanManageSite(a.ID) {
		t.Fatalf("archived alpha must be dropped from allowlist")
	}
}

func TestPlatformKeyRevokeAndExpiry(t *testing.T) {
	ps := openPlatformStore(t)
	mkSite(t, ps, "alpha")

	// 已过期。
	expiredTok := "gcmsp_exp123"
	if _, err := ps.CreatePlatformKey("exp", expiredTok, "gcmsp_exp123", KeyMembershipAll, "posts:read", nil, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, ok, _ := ps.GetPlatformKeyByToken(expiredTok); ok {
		t.Fatalf("expired key must not authenticate")
	}

	// 吊销。
	revTok := "gcmsp_rev123"
	id, err := ps.CreatePlatformKey("rev", revTok, "gcmsp_rev123", KeyMembershipAll, "posts:read", nil, time.Time{})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if _, ok, _ := ps.GetPlatformKeyByToken(revTok); !ok {
		t.Fatalf("key should authenticate before revoke")
	}
	if err := ps.RevokePlatformKey(id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok, _ := ps.GetPlatformKeyByToken(revTok); ok {
		t.Fatalf("revoked key must not authenticate")
	}
	// 后台仍可按 id 取到（含吊销时间）。
	key, ok, _ := ps.GetPlatformKey(id)
	if !ok || key.Active() {
		t.Fatalf("revoked key should be inactive but fetchable by id")
	}
}

func TestPlatformKeyUpdate(t *testing.T) {
	ps := openPlatformStore(t)
	a, b, c := mkSite(t, ps, "alpha"), mkSite(t, ps, "beta"), mkSite(t, ps, "gamma")

	token := "gcmsp_upd123"
	id, err := ps.CreatePlatformKey("bot", token, "gcmsp_upd123", KeyMembershipAllowlist, "posts:read", []int64{a.ID}, time.Time{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := ps.UpdatePlatformKey(id, "renamed", KeyMembershipAllowlist, "posts:read,posts:write", []int64{b.ID, c.ID}, time.Time{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	key, _, _ := ps.GetPlatformKey(id)
	if key.Name != "renamed" || key.Scopes != "posts:read,posts:write" {
		t.Fatalf("update fields wrong: %#v", key)
	}
	if got := key.AllowedSiteIDs; len(got) != 2 || key.CanManageSite(a.ID) || !key.CanManageSite(b.ID) || !key.CanManageSite(c.ID) {
		t.Fatalf("allowlist not replaced: %v", got)
	}
	// 切到 all 模式 → 白名单行清空但可管全部。
	if err := ps.UpdatePlatformKey(id, "renamed", KeyMembershipAll, "posts:read", nil, time.Time{}); err != nil {
		t.Fatalf("update to all: %v", err)
	}
	key, _, _ = ps.GetPlatformKey(id)
	if key.MembershipMode != KeyMembershipAll || len(key.AllowedSiteIDs) != 0 {
		t.Fatalf("switch to all failed: %#v", key)
	}
	if !key.CanManageSite(a.ID) {
		t.Fatalf("all mode should manage alpha")
	}
}

func TestRotatePlatformKeyToken(t *testing.T) {
	ps := openPlatformStore(t)
	mkSite(t, ps, "alpha")
	oldTok := "gcmsp_rotateold12345"
	id, err := ps.CreatePlatformKey("bot", oldTok, oldTok[:13], KeyMembershipAll, "posts:read", nil, time.Time{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok, _ := ps.GetPlatformKeyByToken(oldTok); !ok {
		t.Fatalf("old token should authenticate before rotate")
	}
	newTok := "gcmsp_rotatenew12345"
	if err := ps.RotatePlatformKeyToken(id, newTok, newTok[:13]); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, ok, _ := ps.GetPlatformKeyByToken(oldTok); ok {
		t.Fatalf("old token must stop working after rotate")
	}
	if _, ok, _ := ps.GetPlatformKeyByToken(newTok); !ok {
		t.Fatalf("new token must authenticate after rotate")
	}
	// 吊销后不能再换。
	if err := ps.RevokePlatformKey(id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := ps.RotatePlatformKeyToken(id, "gcmsp_shouldfail1234", "gcmsp_shouldf"); err == nil {
		t.Fatalf("rotate on revoked key should fail")
	}
}

func TestPlatformAutomationLog(t *testing.T) {
	ps := openPlatformStore(t)
	a := mkSite(t, ps, "alpha")
	id, err := ps.CreatePlatformKey("bot", "gcmsp_log123", "gcmsp_log123", KeyMembershipAll, "posts:write", nil, time.Time{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := ps.CreatePlatformAutomationLog(id, a.ID, "create", "posts", 7, "创建文章"); err != nil {
		t.Fatalf("log: %v", err)
	}
	// keyID<=0 → 以 NULL 存（不违反 FK）。
	if err := ps.CreatePlatformAutomationLog(0, a.ID, "update", "posts", 8, "无主动作"); err != nil {
		t.Fatalf("log keyID=0: %v", err)
	}
	logs, err := ps.ListPlatformAutomationLogs(50)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("want 2 logs, got %d", len(logs))
	}

	// 删除密钥后，历史保留、key_id 置空（ON DELETE SET NULL）。
	if err := ps.DeletePlatformKey(id); err != nil {
		t.Fatalf("delete key: %v", err)
	}
	logs, _ = ps.ListPlatformAutomationLogs(50)
	if len(logs) != 2 {
		t.Fatalf("logs should survive key deletion, got %d", len(logs))
	}
	for _, l := range logs {
		if l.KeyID != 0 {
			t.Fatalf("key_id should be NULL(0) after key deletion, got %d", l.KeyID)
		}
	}
}
