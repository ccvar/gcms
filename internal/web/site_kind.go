package web

import (
	"encoding/json"
	"strings"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/store"
)

// 站型预设（工厂/外贸站 P1）。
//
// 「站点类型」只在创建站点时做一次性预设，不做持续约束：settings 里的
// site.kind 仅作记录（站点设置页只读展示），之后站点行为完全由常规配置决定。
//   - content（内容站，默认）：行为与现状完全一致，不做任何额外动作。
//   - factory（工厂站）：创建时 a) 启用 product 内容类型（走引擎的 per-site
//     enablement，与后台「扩展」开关同一个 settings 键）；b) 导航自动挂「商品」
//     入口（写 nav_menu，URL 与 menuTargetOptions 里 ext:product 的目标一致）；
//     c) 首页仍用现有主题（工厂主题族是 P2，不动渲染）。
//   - dtc（外贸独立站）：跨境卖家自有品牌官网（DTC 零售感）。预设动作与工厂站
//     完全一致（启用商品 + 导航挂商品 + 演示商品 + brand=text）——差异在主题族
//     （dtc-* 三骨架）与文案气质，站型只做记录与预设，不做持续约束。

const (
	siteKindSettingKey = "site.kind"
	siteKindContent    = "content"
	siteKindFactory    = "factory"
	siteKindDTC        = "dtc"
)

// normalizeSiteKind 把表单值收敛为合法站型；未知值一律按内容站处理。
func normalizeSiteKind(raw string) string {
	switch strings.TrimSpace(raw) {
	case siteKindFactory:
		return siteKindFactory
	case siteKindDTC:
		return siteKindDTC
	}
	return siteKindContent
}

// siteKindOf 读取站点记录的站型（旧站没有该键 → 内容站）。
func siteKindOf(st *store.Store) string {
	return normalizeSiteKind(st.Setting(siteKindSettingKey))
}

// productNavLabels 是「商品」导航项在各内置语种下的文案。
// zh/en 直接取注册表 Names；id/th/vi 与前台 i18n（nav.products）保持一致。
var productNavLabels = map[string]string{
	"id": "Produk",
	"th": "สินค้า",
	"vi": "Sản phẩm",
}

// applySiteKindPreset 在新建站点时按站型做一次性预设（写目标站自己的 settings）。
// mgr 用平台实例的 i18n 管理器取导航默认文案；st 是新站点刚打开的 store。
// seedDemo=true（站点选了「带演示数据」）时，工厂站/独立站额外写入演示商品，
// 让新站开箱就有货架效果；选「空数据」的站点不写（尊重从零开始的语义）。
// 独立站（dtc）预设与工厂站完全一致：启用商品 + 导航挂商品 + 演示商品 + brand=text。
func applySiteKindPreset(st *store.Store, mgr *i18n.Manager, kind string, seedDemo bool) error {
	kind = normalizeSiteKind(kind)
	if err := st.SetSetting(siteKindSettingKey, kind); err != nil {
		return err
	}
	if kind != siteKindFactory && kind != siteKindDTC {
		return nil // 内容站 = 现状，不做任何预设动作
	}

	// a) 启用 product 内容类型（读改写启用集合，与 adminExtToggle 同一存储约定）。
	enabled := parseEnabledTypes(st.Setting(enabledContentTypesKey))
	enabled["product"] = true
	if err := st.SetSetting(enabledContentTypesKey, joinEnabledTypes(enabled)); err != nil {
		return err
	}

	// a2) 演示商品（复用 seedPost/insertSeed 既有种子机制；幂等，见 SeedFactoryDemoProducts）。
	if seedDemo {
		if err := st.SeedFactoryDemoProducts(); err != nil {
			return err
		}
	}

	// a3) 品牌落为文字站名：logo 缺省时前台会回落 gcms「简记」演示图（深色字），
	//     放工厂站既出戏，在 foundry/gunmetal 等深色工厂皮上还会整个隐形；
	//     文字品牌吃主题 --ink，全部工厂皮兼容。
	//     注意写死不加空值守卫：本预设只在建站时跑一次，此刻 settings 里唯一可能的
	//     brand 值是演示种子（seedShowcase）刚写的 "logo"（demoSettingKeys 不清它，
	//     空数据站也残留）——那是种子默认不是用户配置，工厂站必须最终生效 text。
	if err := st.SetSetting("site.brand", "text"); err != nil {
		return err
	}

	// b) 导航自动挂「商品」：
	//    - 站点已有 nav_menu（如带演示数据的种子菜单）→ 把「商品」插到首页之后，其余保留；
	//    - 尚无 nav_menu（空数据站）→ 写出完整默认菜单（首页/商品/分类/关于），
	//      否则 nav_menu 一旦非空，前台就不再回落默认菜单、其余入口会消失。
	//    分类归档取默认路径 /category（archivePath 的缺省；新站点尚无自定义 slug）。
	locales := mgr.ActiveWith(st.Setting("locales"), st.Setting("custom_locales"))
	def := "zh"
	if len(locales) > 0 {
		def = locales[0].Code
	}
	labelsFor := func(key string) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			labels[l.Code] = mgr.Tr(l.Code, def).T(key)
		}
		return labels
	}
	product := contentTypeByKey("product")
	productLabels := map[string]string{}
	for _, l := range locales {
		if v := strings.TrimSpace(product.Names[l.Code]); v != "" {
			productLabels[l.Code] = v
		} else if v := productNavLabels[l.Code]; v != "" {
			productLabels[l.Code] = v
		} else {
			productLabels[l.Code] = product.Name(l.Code)
		}
	}
	productRow := MenuRow{URL: "/" + product.URLPrefix, Labels: productLabels}

	rawNavigation := st.Setting("nav_menu")
	rows := parseMenuRows(rawNavigation)
	if len(rows) == 0 {
		if menuRowsConfigured(rawNavigation) {
			return nil // 用户明确选择空导航，不因启用内容类型而自动恢复默认项。
		}
		rows = []MenuRow{
			{URL: "/", Labels: labelsFor("nav.home")},
			productRow,
			{URL: "/category", Labels: labelsFor("nav.category")},
			{URL: "/about", Labels: labelsFor("nav.about")},
		}
	} else {
		for _, row := range rows {
			if row.URL == productRow.URL {
				return nil // 已有商品入口（幂等）
			}
		}
		at := 0
		if rows[0].URL == "/" {
			at = 1 // 插在首页之后
		}
		rows = append(rows[:at], append([]MenuRow{productRow}, rows[at:]...)...)
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return st.SetSetting("nav_menu", string(b))
}
