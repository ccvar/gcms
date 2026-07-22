package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

// 芯片单测统一用零值 AdminTr：catalog 为空时 T 返回 fallback（也就是中文文案），断言稳定。
func chipTr() *i18n.AdminTr { return &i18n.AdminTr{} }

func chipNow() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.Local) }

func TestBuildDeployChipPending(t *testing.T) {
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site: &platform.Site{ID: 2, CreatedAt: chipNow().AddDate(0, 0, -9)},
		// 有已发布内容也照样待部署：没绑域名、没发过 CF 就是没上线。
		ContentAt:  chipNow().Add(-2 * time.Hour),
		HasContent: true,
	}, chipNow())
	if chip == nil || !chip.Pending {
		t.Fatalf("chip = %+v, want pending", chip)
	}
	if chip.Text != "" {
		t.Fatalf("pending chip text = %q, want empty", chip.Text)
	}
	if !strings.Contains(chip.Title, "绑定域名或完成 Cloudflare 部署后开始计时") {
		t.Fatalf("pending chip title = %q", chip.Title)
	}
}

func TestBuildDeployChipServerDomain(t *testing.T) {
	now := chipNow()
	boundAt := now.AddDate(0, 0, -10)
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site: &platform.Site{ID: 2, CreatedAt: now.AddDate(0, 0, -30)},
		Domains: []*platform.SiteDomain{
			{Host: "alias.test", CreatedAt: now.AddDate(0, 0, -3)},
			{Host: "srv.test", IsPrimary: true, CreatedAt: boundAt},
		},
		ContentAt:  now.AddDate(0, 0, -3),
		HasContent: true,
	}, now)
	if chip == nil || chip.Pending {
		t.Fatalf("chip = %+v, want deployed", chip)
	}
	if chip.Text != "运行 10天 · 更新 3天前" {
		t.Fatalf("chip text = %q", chip.Text)
	}
	wantDate := boundAt.Local().Format("2006-01-02")
	if !strings.Contains(chip.Title, "域名绑定于 "+wantDate) || !strings.Contains(chip.Title, "内容最近对外更新") {
		t.Fatalf("chip title = %q", chip.Title)
	}
}

func TestBuildDeployChipImportedContentCannotPredateLaunch(t *testing.T) {
	now := chipNow()
	createdAt := now.AddDate(0, 0, -1)
	contentAt := now.AddDate(0, 0, -31)
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site:       &platform.Site{ID: 1, IsDefault: true, CreatedAt: createdAt},
		ContentAt:  contentAt,
		HasContent: true,
	}, now)
	if chip == nil || chip.Pending {
		t.Fatalf("chip = %+v, want deployed", chip)
	}
	if chip.Text != "运行 1天 · 更新 1天前" {
		t.Fatalf("chip text = %q, want launch-clamped update", chip.Text)
	}
	if !strings.Contains(chip.Title, contentAt.Local().Format("2006-01-02 15:04")) ||
		!strings.Contains(chip.Title, createdAt.Local().Format("2006-01-02 15:04")) ||
		!strings.Contains(chip.Title, "按上线时间") {
		t.Fatalf("chip title = %q, want original content and effective launch times", chip.Title)
	}
}

func TestBuildDeployChipServerDomainEarliestWithoutPrimary(t *testing.T) {
	now := chipNow()
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site: &platform.Site{ID: 2},
		Domains: []*platform.SiteDomain{
			{Host: "b.test", CreatedAt: now.AddDate(0, 0, -2)},
			{Host: "a.test", CreatedAt: now.AddDate(0, 0, -6)},
		},
	}, now)
	if chip == nil || chip.Pending || !strings.Contains(chip.Text, "运行 6天") {
		t.Fatalf("chip = %+v, want 运行 6天 (earliest domain)", chip)
	}
	if !strings.Contains(chip.Title, "暂无已发布内容") {
		t.Fatalf("chip title = %q, want no-content note", chip.Title)
	}
}

func TestBuildDeployChipCFFirstDeployFromHistory(t *testing.T) {
	now := chipNow()
	first := now.AddDate(0, 0, -30)
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site:        &platform.Site{ID: 3},
		CFPublished: true,
		CFStatus: &CloudflareStatus{
			Status:       "success",
			LastDeployAt: now.AddDate(0, 0, -2).UTC().Format(time.RFC3339),
			Published:    true,
			History: []CloudflareStatusHistory{ // 新的在前
				{Action: "deploy", Status: "success", At: now.AddDate(0, 0, -2).UTC().Format(time.RFC3339)},
				{Action: "deploy", Status: "failed", At: now.AddDate(0, 0, -31).UTC().Format(time.RFC3339)},
				{Action: "deploy", Status: "success", At: first.UTC().Format(time.RFC3339)},
			},
		},
		ContentAt:  now,
		HasContent: true,
	}, now)
	if chip == nil || chip.Pending {
		t.Fatalf("chip = %+v, want deployed", chip)
	}
	if chip.Text != "运行 30天 · 更新 今天" {
		t.Fatalf("chip text = %q", chip.Text)
	}
	if !strings.Contains(chip.Title, "首次发布于 "+first.Local().Format("2006-01-02")) {
		t.Fatalf("chip title = %q, want first-publish口径", chip.Title)
	}
}

func TestBuildDeployChipCFHistoryRotatedUsesOldestSurviving(t *testing.T) {
	now := chipNow()
	history := make([]CloudflareStatusHistory, cloudflareHistoryLimit) // 装满：首发可能已被挤掉
	for i := range history {
		history[i] = CloudflareStatusHistory{Action: "deploy", Status: "success", At: now.AddDate(0, 0, -(i + 2)).UTC().Format(time.RFC3339)}
	}
	// 老状态档（没有持久化 first_deploy_at）+ 历史已滚满：
	// 取现存最旧一条成功记录当下界（比「最近一次发布」诚实得多），悬停注明可能更久。
	oldest := now.AddDate(0, 0, -(cloudflareHistoryLimit + 1))
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site:        &platform.Site{ID: 3},
		CFPublished: true,
		CFStatus: &CloudflareStatus{
			Status:       "success",
			LastDeployAt: now.AddDate(0, 0, -2).UTC().Format(time.RFC3339),
			Published:    true,
			History:      history,
		},
	}, now)
	if chip == nil || !strings.Contains(chip.Text, fmt.Sprintf("运行 %d天", cloudflareHistoryLimit+1)) {
		t.Fatalf("chip = %+v, want 运行 %d天 (oldest surviving)", chip, cloudflareHistoryLimit+1)
	}
	if !strings.Contains(chip.Title, "最早可查的发布记录于 "+oldest.Local().Format("2006-01-02")) || !strings.Contains(chip.Title, "实际运行时间可能更长") {
		t.Fatalf("chip title = %q, want floor口径", chip.Title)
	}
}

func TestBuildDeployChipCFPersistedFirstDeploy(t *testing.T) {
	now := chipNow()
	first := now.AddDate(0, 0, -40)
	// 持久化的 first_deploy_at 优先于历史推算：历史滚满也不影响口径。
	history := make([]CloudflareStatusHistory, cloudflareHistoryLimit)
	for i := range history {
		history[i] = CloudflareStatusHistory{Action: "deploy", Status: "success", At: now.AddDate(0, 0, -(i + 1)).UTC().Format(time.RFC3339)}
	}
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site:        &platform.Site{ID: 3},
		CFPublished: true,
		CFStatus: &CloudflareStatus{
			Status:        "success",
			FirstDeployAt: first.UTC().Format(time.RFC3339),
			LastDeployAt:  now.AddDate(0, 0, -1).UTC().Format(time.RFC3339),
			Published:     true,
			History:       history,
		},
	}, now)
	if chip == nil || !strings.Contains(chip.Text, "运行 40天") {
		t.Fatalf("chip = %+v, want 运行 40天 (persisted first deploy)", chip)
	}
	if !strings.Contains(chip.Title, "首次发布于 "+first.Local().Format("2006-01-02")) {
		t.Fatalf("chip title = %q, want first-publish口径", chip.Title)
	}
	// estimated 标记 → 下界措辞。
	chip = buildDeployChip(chipTr(), deployChipInput{
		Site:        &platform.Site{ID: 3},
		CFPublished: true,
		CFStatus: &CloudflareStatus{
			Status:         "success",
			FirstDeployAt:  first.UTC().Format(time.RFC3339),
			FirstDeployEst: true,
			Published:      true,
		},
	}, now)
	if chip == nil || !strings.Contains(chip.Text, "运行 40天") || !strings.Contains(chip.Title, "最早可查的发布记录于") {
		t.Fatalf("chip = %+v title = %q, want floor口径 via estimated flag", chip, chip.Title)
	}
}

func TestBuildDeployChipEarliestAnchorWins(t *testing.T) {
	now := chipNow()
	// CF 首发 5天前，但域名 12天前就绑了 → 运行天数按更早的域名锚点计。
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site:        &platform.Site{ID: 3},
		Domains:     []*platform.SiteDomain{{Host: "old.test", IsPrimary: true, CreatedAt: now.AddDate(0, 0, -12)}},
		CFPublished: true,
		CFStatus: &CloudflareStatus{
			Status:        "success",
			FirstDeployAt: now.AddDate(0, 0, -5).UTC().Format(time.RFC3339),
			Published:     true,
		},
	}, now)
	if chip == nil || !strings.Contains(chip.Text, "运行 12天") || !strings.Contains(chip.Title, "域名绑定于") {
		t.Fatalf("chip = %+v, want earliest (domain) anchor", chip)
	}
	// 反过来：CF 首发更早 → CF 锚点赢，哪怕当前是服务器形态。
	chip = buildDeployChip(chipTr(), deployChipInput{
		Site:    &platform.Site{ID: 3},
		Domains: []*platform.SiteDomain{{Host: "new.test", IsPrimary: true, CreatedAt: now.AddDate(0, 0, -3)}},
		CFStatus: &CloudflareStatus{
			Status:        "idle",
			FirstDeployAt: now.AddDate(0, 0, -20).UTC().Format(time.RFC3339),
		},
	}, now)
	if chip == nil || !strings.Contains(chip.Text, "运行 20天") || !strings.Contains(chip.Title, "首次发布于") {
		t.Fatalf("chip = %+v, want earliest (CF) anchor", chip)
	}
}

func TestWriteCloudflareStatusFilePersistsFirstDeploy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloudflare-deploy.json")
	now := time.Now()
	first := now.AddDate(0, 0, -9).UTC().Format(time.RFC3339)
	last := now.AddDate(0, 0, -1).UTC().Format(time.RFC3339)
	// 首写：历史未滚满 → 最旧成功记录落档为精确首发。
	writeCloudflareStatusFile(path, CloudflareStatus{
		Status: "success", LastDeployAt: last, Published: true,
		History: []CloudflareStatusHistory{
			{Action: "deploy", Status: "success", At: last},
			{Action: "deploy", Status: "success", At: first},
		},
	})
	st := readCloudflareStatusFile(path)
	if st.FirstDeployAt != first || st.FirstDeployEst {
		t.Fatalf("first write: first_deploy_at = %q est=%v, want %q est=false", st.FirstDeployAt, st.FirstDeployEst, first)
	}
	// 后续写档不带首发 → 从旧档只进不退地带上，历史再怎么滚都不丢。
	full := make([]CloudflareStatusHistory, cloudflareHistoryLimit)
	for i := range full {
		full[i] = CloudflareStatusHistory{Action: "deploy", Status: "success", At: now.AddDate(0, 0, -i).UTC().Format(time.RFC3339)}
	}
	writeCloudflareStatusFile(path, CloudflareStatus{Status: "success", LastDeployAt: last, Published: true, History: full})
	if st = readCloudflareStatusFile(path); st.FirstDeployAt != first || st.FirstDeployEst {
		t.Fatalf("carry-forward: first_deploy_at = %q est=%v, want %q est=false", st.FirstDeployAt, st.FirstDeployEst, first)
	}
	// 升级迁移①：老档（无首发字段）历史已滚满，升级后第一笔写档恰好又把最旧一条挤出环形
	// （比如清缓存追加 purge 条目）——回填必须用**旋转前**旧档里的最旧成功记录，不能用挤完的。
	rawWrite := func(path string, st CloudflareStatus) {
		data, err := json.Marshal(st)
		if err != nil {
			t.Fatalf("marshal old-format status: %v", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write old-format status: %v", err)
		}
	}
	path2 := filepath.Join(t.TempDir(), "cloudflare-deploy.json")
	rawWrite(path2, CloudflareStatus{Status: "success", LastDeployAt: last, Published: true, History: full})
	oldestUnrotated := full[len(full)-1].At
	rotated := appendCloudflareHistory(full, CloudflareStatusHistory{Action: "purge", Status: "success", At: time.Now().UTC().Format(time.RFC3339)})
	writeCloudflareStatusFile(path2, CloudflareStatus{Status: "success", LastDeployAt: last, Published: true, History: rotated})
	if st = readCloudflareStatusFile(path2); st.FirstDeployAt != oldestUnrotated || !st.FirstDeployEst {
		t.Fatalf("rotated backfill: first_deploy_at = %q est=%v, want %q est=true", st.FirstDeployAt, st.FirstDeployEst, oldestUnrotated)
	}

	// 升级迁移②：只有 last_deploy_at、没有 history 的更老档（History 字段晚一天引入）——
	// 旧的 last_deploy_at 比本次成功部署早，必须当下界收进来，不能把「今天」当成精确首发。
	path3 := filepath.Join(t.TempDir(), "cloudflare-deploy.json")
	ancient := now.AddDate(0, 0, -29).UTC().Format(time.RFC3339)
	rawWrite(path3, CloudflareStatus{Status: "success", LastDeployAt: ancient, Published: true})
	nowTS := now.UTC().Format(time.RFC3339)
	writeCloudflareStatusFile(path3, CloudflareStatus{
		Status: "success", LastDeployAt: nowTS, Published: true,
		History: []CloudflareStatusHistory{{Action: "deploy", Status: "success", At: nowTS}},
	})
	if st = readCloudflareStatusFile(path3); st.FirstDeployAt != ancient || !st.FirstDeployEst {
		t.Fatalf("last-deploy-only backfill: first_deploy_at = %q est=%v, want %q est=true", st.FirstDeployAt, st.FirstDeployEst, ancient)
	}

	// 真首发不误标：连败 7 次（历史 7 条 failed、从没成功过）后第 8 次成功——
	// 历史长度到顶但没有任何更早成功可丢，est 必须是 false、口径就是精确首发。
	path4 := filepath.Join(t.TempDir(), "cloudflare-deploy.json")
	fails := make([]CloudflareStatusHistory, cloudflareHistoryLimit-1)
	for i := range fails {
		fails[i] = CloudflareStatusHistory{Action: "deploy", Status: "failed", At: now.AddDate(0, 0, -(i + 1)).UTC().Format(time.RFC3339)}
	}
	writeCloudflareStatusFile(path4, CloudflareStatus{Status: "failed", History: fails})
	if st = readCloudflareStatusFile(path4); st.FirstDeployAt != "" {
		t.Fatalf("all-failed history must not backfill first_deploy_at, got %q", st.FirstDeployAt)
	}
	writeCloudflareStatusFile(path4, CloudflareStatus{
		Status: "success", LastDeployAt: nowTS, Published: true,
		History: append([]CloudflareStatusHistory{{Action: "deploy", Status: "success", At: nowTS}}, fails...),
	})
	if st = readCloudflareStatusFile(path4); st.FirstDeployAt != nowTS || st.FirstDeployEst {
		t.Fatalf("first-ever success: first_deploy_at = %q est=%v, want %q est=false", st.FirstDeployAt, st.FirstDeployEst, nowTS)
	}
}

func TestBuildDeployChipCFWithoutRecordFallsBack(t *testing.T) {
	now := chipNow()
	// CF 已发布但状态文件里没有任何发布时间：先回落域名绑定时间。
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site:        &platform.Site{ID: 3, CreatedAt: now.AddDate(0, 0, -20)},
		Domains:     []*platform.SiteDomain{{Host: "cf.test", IsPrimary: true, CreatedAt: now.AddDate(0, 0, -7)}},
		CFPublished: true,
		CFStatus:    &CloudflareStatus{Status: "success", Published: true},
	}, now)
	if chip == nil || !strings.Contains(chip.Text, "运行 7天") || !strings.Contains(chip.Title, "域名绑定于") {
		t.Fatalf("chip = %+v, want domain fallback", chip)
	}
	// 连域名也没有：回落站点创建时间并注明。
	chip = buildDeployChip(chipTr(), deployChipInput{
		Site:        &platform.Site{ID: 3, CreatedAt: now.AddDate(0, 0, -20)},
		CFPublished: true,
		CFStatus:    &CloudflareStatus{Status: "success", Published: true},
	}, now)
	if chip == nil || !strings.Contains(chip.Text, "运行 20天") || !strings.Contains(chip.Title, "站点创建于") {
		t.Fatalf("chip = %+v, want site-created fallback", chip)
	}
}

func TestBuildDeployChipCFUnpublishedButDeployedOnce(t *testing.T) {
	now := chipNow()
	// 曾成功发布过 CF、现已下线且没绑域名：口径上算已部署，不回「待部署」。
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site:     &platform.Site{ID: 4},
		CFStatus: &CloudflareStatus{Status: "idle", LastDeployAt: now.AddDate(0, 0, -4).UTC().Format(time.RFC3339)},
	}, now)
	if chip == nil || chip.Pending || !strings.Contains(chip.Text, "运行 4天") {
		t.Fatalf("chip = %+v, want deployed via past CF deploy", chip)
	}
}

func TestBuildDeployChipDefaultSiteUsesCreatedAt(t *testing.T) {
	now := chipNow()
	chip := buildDeployChip(chipTr(), deployChipInput{
		Site:       &platform.Site{ID: 1, IsDefault: true, CreatedAt: now.AddDate(0, 0, -15)},
		ContentAt:  now.AddDate(0, 0, -1),
		HasContent: true,
	}, now)
	if chip == nil || chip.Pending {
		t.Fatalf("default site chip = %+v, want deployed", chip)
	}
	if chip.Text != "运行 15天 · 更新 1天前" {
		t.Fatalf("chip text = %q", chip.Text)
	}
	if !strings.Contains(chip.Title, "站点创建于") {
		t.Fatalf("chip title = %q", chip.Title)
	}
}

func TestChipDayFormatting(t *testing.T) {
	now := chipNow()
	if got := chipRelDays(chipTr(), now.Add(-2*time.Hour), now); got != "今天" {
		t.Fatalf("same-day rel = %q, want 今天", got)
	}
	// 跨零点即 1天前：昨晚 23:00 → 今天中午。
	yesterdayNight := time.Date(2026, 7, 16, 23, 0, 0, 0, time.Local)
	if got := chipRelDays(chipTr(), yesterdayNight, now); got != "1天前" {
		t.Fatalf("cross-midnight rel = %q, want 1天前", got)
	}
	if got := chipDaysBetween(now.Add(-30*time.Minute), now); got != 1 {
		t.Fatalf("same-day live days = %d, want 1", got)
	}
	if got := chipDaysBetween(now.AddDate(0, 0, -12), now); got != 12 {
		t.Fatalf("12-day live days = %d, want 12", got)
	}
}

// TestAdminSitesDeployChipsRendering 起真实平台服务：三种形态（待部署 / 服务器已部署 / CF 已部署）
// 各渲染出正确的芯片文字、悬停口径与 JS 挂点。
func TestAdminSitesDeployChipsRendering(t *testing.T) {
	dir := t.TempDir()
	newSiteStore := func(sub string) (string, *store.Store) {
		// 每站独立子目录：cloudflare-deploy.json 按 dir(DBPath)/run 定位，混在同目录会互相串。
		base := filepath.Join(dir, sub)
		if err := os.MkdirAll(base, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
		dbPath := filepath.Join(base, "site.db")
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("open %s store: %v", sub, err)
		}
		return dbPath, st
	}

	// 种子内容的时间戳是写死的历史日期，会让「更新 M」随跑测日期漂移；
	// 给每个要断言 M 的站现挂一篇刚发布的内容，让 M 恒等于「今天」。
	freshPost := func(st *store.Store, name string) {
		if _, err := st.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "chip-fresh", Title: "Chip Fresh", Status: "published"}); err != nil {
			t.Fatalf("create fresh post for %s: %v", name, err)
		}
	}

	defaultDB, defaultStore := newSiteStore("main")
	t.Cleanup(func() { _ = defaultStore.Close() })
	freshPost(defaultStore, "main")

	pendDB, pendStore := newSiteStore("pend")
	_ = pendStore.Close()

	srvDB, srvStore := newSiteStore("srv")
	freshPost(srvStore, "srv")
	// 一篇定时发布：卡片 meta 行应出现「1条待发」。
	if _, err := srvStore.CreatePost(&store.Post{Type: "post", Lang: "zh", Slug: "chip-sched", Title: "Chip Sched", Status: "scheduled", PublishedAt: time.Now().AddDate(0, 0, 3)}); err != nil {
		t.Fatalf("create scheduled post for srv: %v", err)
	}
	_ = srvStore.Close()

	cfDB, cfStore := newSiteStore("cf")
	if err := cfStore.SetSetting(cloudflareDomainsKey, encodeCloudflareDomains([]CloudflareDomain{{Host: "cfsite.test", Primary: true}})); err != nil {
		t.Fatalf("set cf domains: %v", err)
	}
	freshPost(cfStore, "cf")
	_ = cfStore.Close()
	now := time.Now()
	firstDeploy := now.AddDate(0, 0, -5)
	lastDeploy := now.AddDate(0, 0, -1)
	writeCloudflareStatusFile(filepath.Join(filepath.Dir(cfDB), "run", "cloudflare-deploy.json"), CloudflareStatus{
		Status:       "success",
		Message:      "Cloudflare 静态站已部署",
		LastDeployAt: lastDeploy.UTC().Format(time.RFC3339),
		Published:    true,
		History: []CloudflareStatusHistory{
			{Action: "deploy", Status: "success", At: lastDeploy.UTC().Format(time.RFC3339)},
			{Action: "deploy", Status: "success", At: firstDeploy.UTC().Format(time.RFC3339)},
		},
	})

	ps, err := platform.Open(filepath.Join(dir, "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	hash := nonDefaultTestPasswordHash(t)
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug: "main", Name: "Chip Default", DBPath: defaultDB,
		UploadDir: filepath.Join(dir, "main", "uploads"),
		AdminUser: "admin", AdminPasswordHash: hash,
		ManagementAutomationEnabled: true,
	}); err != nil {
		t.Fatalf("bootstrap default site: %v", err)
	}
	pendSite, err := ps.CreateSite("pend", "Pending Site", pendDB, filepath.Join(dir, "pend", "uploads"), true)
	if err != nil {
		t.Fatalf("create pend site: %v", err)
	}
	srvSite, err := ps.CreateSite("srv", "Server Site", srvDB, filepath.Join(dir, "srv", "uploads"), true)
	if err != nil {
		t.Fatalf("create srv site: %v", err)
	}
	if err := ps.AddSiteDomain(srvSite.ID, "https", "srv.test", true, true); err != nil {
		t.Fatalf("bind srv domain: %v", err)
	}
	cfSite, err := ps.CreateSite("cf", "CF Site", cfDB, filepath.Join(dir, "cf", "uploads"), true)
	if err != nil {
		t.Fatalf("create cf site: %v", err)
	}

	srv, err := NewWithPlatform(defaultStore, ps, "https://platform.test", filepath.Join(dir, "main", "uploads"), os.DirFS("../.."), os.DirFS("../.."))
	if err != nil {
		t.Fatalf("new platform server: %v", err)
	}
	h := srv.Handler()

	login := httptest.NewRecorder()
	loginForm := url.Values{"username": {"admin"}, "password": {nonDefaultTestPassword}}
	loginReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/login", strings.NewReader(loginForm.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(login, loginReq)
	var loginCookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == cookieName {
			loginCookie = c
			break
		}
	}
	if loginCookie == nil {
		t.Fatalf("login did not set session cookie, status=%d", login.Code)
	}
	// 把会话的「当前后台」切到默认站：卡片应以 is-current 边框标识（文字徽章已拿掉）。
	defSite, err := ps.DefaultSite()
	if err != nil {
		t.Fatalf("default site: %v", err)
	}
	// 平台模式的会话在 platform_sessions（不是站点库的 admin_sessions）。
	if err := ps.SetAdminSessionSite(loginCookie.Value, defSite.ID); err != nil {
		t.Fatalf("set session site: %v", err)
	}
	page := httptest.NewRecorder()
	pageReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	pageReq.AddCookie(loginCookie)
	h.ServeHTTP(page, pageReq)
	if page.Code != http.StatusOK {
		t.Fatalf("sites page status = %d, body = %s", page.Code, page.Body.String())
	}
	body := page.Body.String()

	card := func(siteID int64) string {
		marker := fmt.Sprintf(`id="site-card-%d"`, siteID)
		i := strings.Index(body, marker)
		if i < 0 {
			t.Fatalf("site card %d not found", siteID)
		}
		rest := body[i+len(marker):]
		if j := strings.Index(rest, `id="site-card-`); j >= 0 {
			rest = rest[:j]
		}
		return rest
	}

	// 待部署：无域名、未发过 CF —— 即便种子内容已发布，也不算上线。
	pendCard := card(pendSite.ID)
	if !strings.Contains(pendCard, "待部署") || !strings.Contains(pendCard, "绑定域名或完成 Cloudflare 部署后开始计时") {
		t.Fatalf("pend card missing 待部署 chip: %s", pendCard)
	}
	if !strings.Contains(pendCard, `site-cf-status is-pending`) {
		t.Fatalf("pend card missing pending chip class: %s", pendCard)
	}
	if strings.Contains(pendCard, "data-server-suffix") || strings.Contains(pendCard, "data-cf-poll") {
		t.Fatalf("pend card should not render live-days chip: %s", pendCard)
	}

	// 服务器已部署：N=主域名绑定时间（今天绑的 → 运行 1天），M=种子内容今天生效。
	srvCard := card(srvSite.ID)
	if !strings.Contains(srvCard, "运行 1天 · 更新 今天") || !strings.Contains(srvCard, "data-server-suffix") {
		t.Fatalf("srv card missing server chip: %s", srvCard)
	}
	if !strings.Contains(srvCard, "域名绑定于") {
		t.Fatalf("srv card missing domain口径 title: %s", srvCard)
	}
	if !strings.Contains(srvCard, "1条待发") {
		t.Fatalf("srv card missing scheduled badge: %s", srvCard)
	}
	if strings.Contains(srvCard, "is-current") {
		t.Fatalf("srv card should not be marked current: %s", srvCard)
	}

	// CF 已部署：N=首次成功发布（5天前，历史未滚满取最旧 deploy/success）。
	cfCard := card(cfSite.ID)
	if !strings.Contains(cfCard, "运行 5天 · 更新 今天") || !strings.Contains(cfCard, "data-cf-poll") {
		t.Fatalf("cf card missing cf chip: %s", cfCard)
	}
	if !strings.Contains(cfCard, "首次发布于") || !strings.Contains(cfCard, `data-steady="运行 5天 · 更新 今天"`) {
		t.Fatalf("cf card missing first-publish口径/steady attr: %s", cfCard)
	}
	if !strings.Contains(cfCard, "https://cfsite.test/") {
		t.Fatalf("cf card missing official url (CF form not active): %s", cfCard)
	}

	// 默认站：不能绑域名但天然已部署，按站点创建时间计。
	defCard := card(defSite.ID)
	if !strings.Contains(defCard, "运行 1天 · 更新 今天") || !strings.Contains(defCard, "站点创建于") {
		t.Fatalf("default card missing site-created chip: %s", defCard)
	}
	// 登录会话落在默认站 → 默认站卡片是「当前后台」，用 is-current 边框标识（文字徽章已拿掉）。
	if !strings.Contains(defCard, "is-current") {
		t.Fatalf("default card missing is-current class: %s", defCard)
	}
	if strings.Contains(body, "当前后台") {
		t.Fatalf("当前后台 text badge should be gone from sites page")
	}
}
