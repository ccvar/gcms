package web

// 站点管理卡片左下角的「托管 · 运行/更新」芯片。
//
// 口径（与产品确认）：
//   - 待部署：站点既没绑定任何域名，也从未成功发布到 Cloudflare → 芯片显示「待部署」，
//     悬停提示"绑定域名或完成 Cloudflare 部署后开始计时"；托管方式图标（服务器/CF）照旧。
//   - 已部署：显示「运行 N 天 · 更新 M」。
//     N=部署至今天数：CF 站取首次成功发布时间（部署历史环形保存，装满后首发可能已丢，
//     退回最近一次发布并在悬停注明口径；都没有再回落域名绑定/站点创建时间）；
//     服务器直连站取主域名绑定时间（没有回落站点创建时间）。
//     M=前台可感知的最近内容更新：全部已发布内容（含扩展类型）max(published_at, updated_at)，
//     显示为「今天 / x 天前」。
//   - 悬停 title 给出两个精确日期与口径说明。

import (
	"fmt"
	"strings"
	"time"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/platform"
)

// DeployChip 单张站点卡的芯片预计算结果（模板直接渲染，前端不再自算相对时间）。
// 三态：Pending=待部署；否则 Text 非空=已部署；两者皆空=没有可展示口径（隐藏芯片，
// 只出现在极端形态：单站模式合成的默认站且库里没有任何已发布内容）。
type DeployChip struct {
	Pending bool
	Text    string // 「运行 N 天 · 更新 M」；无已发布内容时只有前半段
	Title   string // 悬停：精确日期 + 口径；待部署时为开始计时提示
}

// deployChipInput 计算一张卡片芯片所需的全部原料（由列表页一次性备好，不在这里查库）。
type deployChipInput struct {
	Site        *platform.Site
	Domains     []*platform.SiteDomain // 该站已启用域名（与卡片 PlatformDomains 同源）
	CFPublished bool                   // 当前处于 Cloudflare 已发布态（卡片走 CF 形态）
	CFStatus    *CloudflareStatus      // 该站 cloudflare-deploy.json 内容；可为 nil
	ContentAt   time.Time              // 全部已发布内容 max(published_at, updated_at)
	HasContent  bool                   // false=库里没有任何已对外生效的内容
}

// buildDeployChip 按上述口径把原料折算成芯片文字与悬停说明。
func buildDeployChip(admin *i18n.AdminTr, in deployChipInput, now time.Time) *DeployChip {
	if in.Site == nil || admin == nil {
		return nil
	}
	domainAt := deployChipDomainTime(in.Domains)
	cfAt, cfExact := cloudflareFirstDeployTime(in.CFStatus)
	// 默认站没有"绑定域名"一说（走当前默认入口），天然算已部署。
	deployed := in.CFPublished || !domainAt.IsZero() || !cfAt.IsZero() || in.Site.IsDefault
	if !deployed {
		return &DeployChip{
			Pending: true,
			Title:   admin.T("admin.sites.chip_pending_hint", "绑定域名或完成 Cloudflare 部署后开始计时"),
		}
	}
	deployAt, sinceLine := deployChipSince(admin, in, domainAt, cfAt, cfExact)
	var parts, lines []string
	if !deployAt.IsZero() {
		days := chipDaysBetween(deployAt, now)
		parts = append(parts, fmt.Sprintf(admin.T("admin.sites.chip_live_days", "运行 %d 天"), days))
		lines = append(lines, sinceLine)
	}
	if in.HasContent && !in.ContentAt.IsZero() {
		parts = append(parts, fmt.Sprintf(admin.T("admin.sites.chip_updated", "更新 %s"), chipRelDays(admin, in.ContentAt, now)))
		lines = append(lines, fmt.Sprintf(admin.T("admin.sites.chip_title_updated", "内容最近对外更新：%s（全部已发布内容的最新改动，含扩展类型）"), in.ContentAt.Local().Format("2006-01-02 15:04")))
	} else {
		lines = append(lines, admin.T("admin.sites.chip_title_no_content", "暂无已发布内容"))
	}
	return &DeployChip{Text: strings.Join(parts, " · "), Title: strings.Join(lines, "\n")}
}

// deployChipSince 选定「运行 N 天」的起点与悬停口径行。
// CF 形态优先部署记录，服务器形态优先域名绑定；各自兜底到对方来源，最后回落站点创建时间。
func deployChipSince(admin *i18n.AdminTr, in deployChipInput, domainAt, cfAt time.Time, cfExact bool) (time.Time, string) {
	dateOf := func(t time.Time) string { return t.Local().Format("2006-01-02") }
	cfLine := func() (time.Time, string) {
		if cfExact {
			return cfAt, fmt.Sprintf(admin.T("admin.sites.chip_title_cf_first", "首次发布于 %s（Cloudflare）"), dateOf(cfAt))
		}
		return cfAt, fmt.Sprintf(admin.T("admin.sites.chip_title_cf_last", "最近发布于 %s（Cloudflare 未记录首次发布，运行天数按最近一次发布计）"), dateOf(cfAt))
	}
	domainLine := func() (time.Time, string) {
		return domainAt, fmt.Sprintf(admin.T("admin.sites.chip_title_domain", "域名绑定于 %s（运行天数按域名绑定时间计）"), dateOf(domainAt))
	}
	if in.CFPublished {
		switch {
		case !cfAt.IsZero():
			return cfLine()
		case !domainAt.IsZero():
			return domainLine()
		}
	} else {
		switch {
		case !domainAt.IsZero():
			return domainLine()
		case !cfAt.IsZero(): // 发布过 CF 但当前已下线且没绑域名：仍按发布时间计
			return cfLine()
		}
	}
	if !in.Site.CreatedAt.IsZero() {
		return in.Site.CreatedAt, fmt.Sprintf(admin.T("admin.sites.chip_title_site_created", "站点创建于 %s（未记录部署时间，运行天数按创建时间计）"), dateOf(in.Site.CreatedAt))
	}
	return time.Time{}, ""
}

// deployChipDomainTime 域名口径的部署起点：主域名的绑定时间；没标主域名时取最早绑定的一条。
func deployChipDomainTime(domains []*platform.SiteDomain) time.Time {
	var earliest time.Time
	for _, d := range domains {
		if d == nil || d.CreatedAt.IsZero() {
			continue
		}
		if d.IsPrimary {
			return d.CreatedAt
		}
		if earliest.IsZero() || d.CreatedAt.Before(earliest) {
			earliest = d.CreatedAt
		}
	}
	return earliest
}

// cloudflareFirstDeployTime 从站点的 Cloudflare 部署状态推「首次成功发布」时间。
// History 新的在前、环形裁剪到 cloudflareHistoryLimit 条：未装满说明没丢过记录，
// 最旧一条 deploy/success 即首发（exact=true）；装满则首发可能已被挤掉，
// 退回 LastDeployAt（exact=false，悬停口径注明按最近一次发布计）。
func cloudflareFirstDeployTime(st *CloudflareStatus) (t time.Time, exact bool) {
	if st == nil {
		return time.Time{}, false
	}
	if len(st.History) < cloudflareHistoryLimit {
		for i := len(st.History) - 1; i >= 0; i-- {
			h := st.History[i]
			if h.Action != "deploy" || h.Status != "success" {
				continue
			}
			if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(h.At)); err == nil {
				return ts, true
			}
		}
	}
	if v := strings.TrimSpace(st.LastDeployAt); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			return ts, false
		}
	}
	return time.Time{}, false
}

// chipCalendarDays 本地时区的自然日差（跨零点即 1 天，与「今天/昨天」直觉一致）。
func chipCalendarDays(from, now time.Time) int {
	fy, fm, fd := from.Local().Date()
	ny, nm, nd := now.Local().Date()
	a := time.Date(fy, fm, fd, 0, 0, 0, 0, time.Local)
	b := time.Date(ny, nm, nd, 0, 0, 0, 0, time.Local)
	return int(b.Sub(a).Hours() / 24)
}

// chipDaysBetween 「运行 N 天」的 N：自然日差，最少 1（部署当天即「运行 1 天」）。
func chipDaysBetween(from, now time.Time) int {
	if d := chipCalendarDays(from, now); d > 1 {
		return d
	}
	return 1
}

// chipRelDays 「更新 M」的 M：今天 / x 天前。
func chipRelDays(admin *i18n.AdminTr, t, now time.Time) string {
	d := chipCalendarDays(t, now)
	if d <= 0 {
		return admin.T("admin.sites.chip_today", "今天")
	}
	return fmt.Sprintf(admin.T("admin.sites.chip_days_ago", "%d 天前"), d)
}

// parseChipTime RFC3339 → time；空串/坏值一律当没有。
func parseChipTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
