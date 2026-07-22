package web

// 站点管理卡片左下角的「托管 · 运行/更新」芯片。
//
// 口径（与产品确认）：
//   - 待部署：站点既没绑定任何域名，也从未成功发布到 Cloudflare → 芯片显示「待部署」，
//     悬停提示"绑定域名或完成 Cloudflare 部署后开始计时"；托管方式图标（服务器/CF）照旧。
//   - 已部署：显示「运行 N 天 · 更新 M」。
//     N=对外可访问至今的天数：在「Cloudflare 首次成功发布」与「域名绑定」两个锚点里取
//     **最早**的一个（都是"站点开始对外服务"的起点，谁早算谁——最近一次部署不是运行起点）。
//     CF 首发时间持久化在状态档 first_deploy_at（写档时自动回填，见 writeCloudflareStatusFile）；
//     升级前的老档从现存历史推：历史未滚满即真首发，滚满取现存最旧一条当下界并在悬停注明。
//     两个锚点都没有再回落站点创建时间。
//     M=前台可感知的最近更新：全部已发布内容（含扩展类型）max(published_at, updated_at)
//     与首次对外服务时间取较新者，显示为「今天 / x 天前」。站点导入旧内容后刚上线时，
//     “更新”不会早于“运行”起点，避免出现「运行 1 天 · 更新 31 天前」的矛盾口径。
//   - 悬停 title 给出精确日期与口径说明。
//
// 渲染拆成 Parts（标签淡、数值实）：模板/JS 恢复用 SteadyHTML，纯文本场景用 Text。

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/platform"
)

// DeployChipPart 芯片的一段：「运行」是标签（淡色），「13 天」是数值（实色）。
type DeployChipPart struct {
	Label string
	Value string
}

// DeployChip 单张站点卡的芯片预计算结果（模板直接渲染，前端不再自算相对时间）。
// 三态：Pending=待部署；否则 Parts/Text 非空=已部署；两者皆空=没有可展示口径（隐藏芯片，
// 只出现在极端形态：单站模式合成的默认站且库里没有任何已发布内容）。
type DeployChip struct {
	Pending    bool
	Parts      []DeployChipPart // 「运行|13 天」「更新|今天」；无已发布内容时只有前半段
	Text       string           // Parts 的纯文本形态（data-steady 兜底与测试断言用）
	SteadyHTML string           // Parts 的 span 标记（已转义）；JS 轮询结束后 innerHTML 恢复
	Title      string           // 悬停：精确日期 + 口径；待部署时为开始计时提示
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
	cfAt, cfKind := cloudflareFirstDeployTime(in.CFStatus)
	// 默认站没有"绑定域名"一说（走当前默认入口），天然算已部署。
	deployed := in.CFPublished || !domainAt.IsZero() || !cfAt.IsZero() || in.Site.IsDefault
	if !deployed {
		return &DeployChip{
			Pending: true,
			Title:   admin.T("admin.sites.chip_pending_hint", "绑定域名或完成 Cloudflare 部署后开始计时"),
		}
	}
	deployAt, sinceLine := deployChipSince(admin, in, domainAt, cfAt, cfKind)
	var parts []DeployChipPart
	var lines []string
	if !deployAt.IsZero() {
		days := chipDaysBetween(deployAt, now)
		parts = append(parts, DeployChipPart{
			Label: admin.T("admin.sites.chip_live_label", "运行"),
			Value: fmt.Sprintf(admin.T("admin.sites.chip_live_val", "%d天"), days),
		})
		lines = append(lines, sinceLine)
	}
	if in.HasContent && !in.ContentAt.IsZero() {
		contentAt := in.ContentAt
		contentBeforeLaunch := !deployAt.IsZero() && contentAt.Before(deployAt)
		if contentBeforeLaunch {
			// 导入内容的原始时间可能早于站点首次上线；首次上线本身就是一次对外更新。
			contentAt = deployAt
		}
		parts = append(parts, DeployChipPart{
			Label: admin.T("admin.sites.chip_updated_label", "更新"),
			Value: chipRelDays(admin, contentAt, now),
		})
		if contentBeforeLaunch {
			lines = append(lines, fmt.Sprintf(
				admin.T("admin.sites.chip_title_updated_on_launch", "内容最近改动：%s；首次上线晚于该内容，“更新”按上线时间 %s 计"),
				in.ContentAt.Local().Format("2006-01-02 15:04"),
				contentAt.Local().Format("2006-01-02 15:04"),
			))
		} else {
			lines = append(lines, fmt.Sprintf(admin.T("admin.sites.chip_title_updated", "内容最近对外更新：%s（全部已发布内容的最新改动，含扩展类型）"), contentAt.Local().Format("2006-01-02 15:04")))
		}
	} else {
		lines = append(lines, admin.T("admin.sites.chip_title_no_content", "暂无已发布内容"))
	}
	return &DeployChip{
		Parts:      parts,
		Text:       chipPartsText(parts),
		SteadyHTML: chipPartsHTML(parts),
		Title:      strings.Join(lines, "\n"),
	}
}

// chipPartsText 「运行 13 天 · 更新 今天」——Parts 的纯文本形态。
func chipPartsText(parts []DeployChipPart) string {
	segs := make([]string, 0, len(parts))
	for _, p := range parts {
		segs = append(segs, strings.TrimSpace(p.Label+" "+p.Value))
	}
	return strings.Join(segs, " · ")
}

// chipPartsHTML Parts 的 span 标记：标签淡、数值实（样式见 admin.css .site-cf-status）。
// 放进 data-steady-html 属性供 JS 在轮询结束后 innerHTML 恢复，所以逐段转义。
func chipPartsHTML(parts []DeployChipPart) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString(`<i class="chip-dot" aria-hidden="true">·</i>`)
		}
		b.WriteString(`<span class="chip-t">`)
		b.WriteString(template.HTMLEscapeString(p.Label))
		b.WriteString(`</span><b class="chip-n">`)
		b.WriteString(template.HTMLEscapeString(p.Value))
		b.WriteString(`</b>`)
	}
	return b.String()
}

// deployChipSince 选定「运行 N 天」的起点与悬停口径行：CF 首发与域名绑定两个锚点取最早，
// 都没有再回落站点创建时间。
func deployChipSince(admin *i18n.AdminTr, in deployChipInput, domainAt, cfAt time.Time, cfKind cfDeployTimeKind) (time.Time, string) {
	dateOf := func(t time.Time) string { return t.Local().Format("2006-01-02") }
	type anchor struct {
		at   time.Time
		line string
	}
	var anchors []anchor
	if !cfAt.IsZero() {
		var line string
		switch cfKind {
		case cfDeployTimeExact:
			line = fmt.Sprintf(admin.T("admin.sites.chip_title_cf_first", "首次发布于 %s（Cloudflare）"), dateOf(cfAt))
		case cfDeployTimeFloor:
			line = fmt.Sprintf(admin.T("admin.sites.chip_title_cf_floor", "最早可查的发布记录于 %s（更早的部署历史已滚动清理，实际运行时间可能更长）"), dateOf(cfAt))
		default:
			line = fmt.Sprintf(admin.T("admin.sites.chip_title_cf_last", "最近发布于 %s（Cloudflare 未记录首次发布，运行天数按最近一次发布计）"), dateOf(cfAt))
		}
		anchors = append(anchors, anchor{cfAt, line})
	}
	if !domainAt.IsZero() {
		anchors = append(anchors, anchor{domainAt, fmt.Sprintf(admin.T("admin.sites.chip_title_domain", "域名绑定于 %s（运行天数按域名绑定时间计）"), dateOf(domainAt))})
	}
	if len(anchors) > 0 {
		best := anchors[0]
		for _, a := range anchors[1:] {
			if a.at.Before(best.at) {
				best = a
			}
		}
		return best.at, best.line
	}
	if !in.Site.CreatedAt.IsZero() {
		return in.Site.CreatedAt, fmt.Sprintf(admin.T("admin.sites.chip_title_site_created", "站点创建于 %s（未记录部署时间，运行天数按创建时间计）"), dateOf(in.Site.CreatedAt))
	}
	return time.Time{}, ""
}

// deployChipDomainTime 域名口径的部署起点：全部已启用域名里**最早**绑定的一条——
// 站点从第一个域名生效起就对外服务了，主域名后来换绑不该把「运行天数」清零。
func deployChipDomainTime(domains []*platform.SiteDomain) time.Time {
	var earliest time.Time
	for _, d := range domains {
		if d == nil || d.CreatedAt.IsZero() {
			continue
		}
		if earliest.IsZero() || d.CreatedAt.Before(earliest) {
			earliest = d.CreatedAt
		}
	}
	return earliest
}

// cfDeployTimeKind CF 发布时间的口径档位（决定悬停措辞）。
type cfDeployTimeKind int

const (
	cfDeployTimeNone  cfDeployTimeKind = iota
	cfDeployTimeExact                  // 真首发：持久化 first_deploy_at 或历史未滚满推出
	cfDeployTimeFloor                  // 下界：历史已滚满，取现存最旧成功记录
	cfDeployTimeLast                   // 只有最近一次发布时间
)

// cloudflareFirstDeployTime 从站点的 Cloudflare 部署状态取「首次成功发布」时间。
// 优先读持久化的 first_deploy_at（estimated 标记决定档位）；升级前的老状态档从现存
// 历史推：未滚满即真首发，滚满取现存最旧一条当下界；连历史都没有退回 LastDeployAt。
func cloudflareFirstDeployTime(st *CloudflareStatus) (time.Time, cfDeployTimeKind) {
	if st == nil {
		return time.Time{}, cfDeployTimeNone
	}
	if ts, ok := parseChipTime(st.FirstDeployAt); ok {
		if st.FirstDeployEst {
			return ts, cfDeployTimeFloor
		}
		return ts, cfDeployTimeExact
	}
	if at := oldestCloudflareDeploySuccess(st.History); at != "" {
		if ts, ok := parseChipTime(at); ok {
			if len(st.History) < cloudflareHistoryLimit {
				return ts, cfDeployTimeExact
			}
			return ts, cfDeployTimeFloor
		}
	}
	if ts, ok := parseChipTime(st.LastDeployAt); ok {
		return ts, cfDeployTimeLast
	}
	return time.Time{}, cfDeployTimeNone
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
	return fmt.Sprintf(admin.T("admin.sites.chip_days_ago", "%d天前"), d)
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
