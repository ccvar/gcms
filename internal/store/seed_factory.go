package store

// 工厂/外贸站演示商品种子（P1 跟进）。
//
// 新建「工厂站」且选择带演示数据时调用：写入商品分类（kind=product，中英配对）
// 与 6 组中英配对的演示商品（共 12 行，每语种货架 6 条），全部已发布、带
// 规格 repeater（型号/材质/尺寸/起订量/认证）、挂分类、封面用工业风 SVG 占位。
// 演示商品刻意不填价格——外贸常态是「面议」，price 是自由文本字段（可填
// 「US$ 12.5/pc」等），照抄演示建站的老板不会把裸数字挂上前台
// （assets/covers/factory-*.svg，中性几何 + 品类图形，不再复用 gcms 后台截图式插图）。
// 另写入「工厂实力」演示数字（settings factory.stats + factory.stats::en，按语种），
// 新演示工厂站首页开箱即 4 张数字卡。
// 与 seedIfEmpty 的机制一致（seedPost + insertSeed），仅数据不同；幂等：站内已有
// product 内容则不再写入。内容站不会走到这里。

// factoryDemoCat 是一组中英配对的商品分类。
type factoryDemoCat struct {
	Slug, Group, NameZh, DescZh, NameEn, DescEn string
}

var factoryDemoCats = []factoryDemoCat{
	{
		Slug: "mechanical-parts", Group: "pcat-mechanical",
		NameZh: "机械配件", DescZh: "CNC 加工件、轴承与传动件，按图纸定制。",
		NameEn: "Mechanical Parts", DescEn: "CNC machined parts, bearings and transmission components, custom per drawing.",
	},
	{
		Slug: "led-lighting", Group: "pcat-led",
		NameZh: "LED 照明", DescZh: "工业与户外 LED 灯具，出口欧美主流认证齐全。",
		NameEn: "LED Lighting", DescEn: "Industrial and outdoor LED fixtures with mainstream export certifications.",
	},
	{
		Slug: "textiles", Group: "pcat-textiles",
		NameZh: "纺织面料", DescZh: "功能面料与家纺制品，支持来样定织定染。",
		NameEn: "Textiles & Fabrics", DescEn: "Functional fabrics and home textiles, OEM weaving and dyeing supported.",
	},
}

// SeedFactoryDemoProducts 为工厂站写入演示商品（分类 + 中英配对商品，已发布）。
// 幂等：已有 product 内容则什么都不做。
func (s *Store) SeedFactoryDemoProducts() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type='product'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	// ---- 商品分类（中英各一套，按 trans_group 配对）----
	catID := map[string]map[string]int64{"zh": {}, "en": {}}
	for i, c := range factoryDemoCats {
		for _, l := range []struct{ lang, name, desc string }{
			{"zh", c.NameZh, c.DescZh},
			{"en", c.NameEn, c.DescEn},
		} {
			res, err := s.db.Exec(`INSERT INTO categories(slug,name,description,position,lang,trans_group,kind) VALUES(?,?,?,?,?,?,?)`,
				c.Slug, l.name, l.desc, i, l.lang, c.Group, "product")
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			catID[l.lang][c.Slug] = id
		}
	}

	// ---- 演示商品（6 组中英配对；规格 repeater：型号/材质/尺寸/起订量/认证）----
	products := []seedPost{
		{
			Type: "product", Slug: "cnc-machined-parts", Lang: "zh", Group: "p-demo-cnc",
			Title: "精密 CNC 铝合金加工件", Cat: "mechanical-parts", Date: "2026-06-18",
			Cover:   "/assets/covers/factory-cnc.svg",
			Excerpt: "6061-T6 铝合金五轴加工，公差 ±0.01mm，支持来图来样定制与小批量试产。",
			Extra:   `{"specs":[{"k":"型号","v":"GC-CNC-6061"},{"k":"材质","v":"6061-T6 铝合金"},{"k":"尺寸","v":"按图纸定制"},{"k":"起订量","v":"500 件"},{"k":"认证","v":"ISO 9001:2015"}]}`,
			Content: md(
				"## 产品说明",
				"",
				"面向汽车、通讯与自动化设备行业的精密结构件，五轴 CNC 一次装夹成型，关键尺寸公差控制在 **±0.01mm**。",
				"",
				"- 支持阳极氧化、喷砂、镭雕等表面处理",
				"- 免费 DFM 评审，72 小时出首样",
				"- 月产能 20 万件，交期 15–20 天",
			),
		},
		{
			Type: "product", Slug: "cnc-machined-parts", Lang: "en", Group: "p-demo-cnc",
			Title: "Precision CNC Machined Aluminum Parts", Cat: "mechanical-parts", Date: "2026-06-18",
			Cover:   "/assets/covers/factory-cnc.svg",
			Excerpt: "5-axis machined 6061-T6 aluminum parts with ±0.01mm tolerance; custom per drawing, low-volume runs welcome.",
			Extra:   `{"specs":[{"k":"Model","v":"GC-CNC-6061"},{"k":"Material","v":"6061-T6 aluminum"},{"k":"Size","v":"Custom per drawing"},{"k":"MOQ","v":"500 pcs"},{"k":"Certification","v":"ISO 9001:2015"}]}`,
			Content: md(
				"## Overview",
				"",
				"Precision structural parts for automotive, telecom and automation equipment, machined in one 5-axis setup with key tolerances held at **±0.01mm**.",
				"",
				"- Anodizing, sandblasting and laser engraving available",
				"- Free DFM review, first article in 72 hours",
				"- 200k pcs monthly capacity, 15–20 day lead time",
			),
		},
		{
			Type: "product", Slug: "ball-bearing-6204", Lang: "zh", Group: "p-demo-bearing",
			Title: "不锈钢深沟球轴承 6204-2RS", Cat: "mechanical-parts", Date: "2026-06-16",
			Cover:   "/assets/covers/factory-bearing.svg",
			Excerpt: "440C 不锈钢双面密封深沟球轴承，防锈耐腐蚀，适配食品机械与户外设备。",
			Extra:   `{"specs":[{"k":"型号","v":"6204-2RS"},{"k":"材质","v":"440C 不锈钢"},{"k":"尺寸","v":"20×47×14 mm"},{"k":"起订量","v":"2000 套"},{"k":"认证","v":"ISO 9001"}]}`,
			Content: md(
				"## 产品说明",
				"",
				"双面橡胶密封结构，出厂注入食品级润滑脂，盐雾测试 96 小时无锈蚀。",
				"",
				"- 噪音等级 Z2V2，转速上限 14000 rpm",
				"- 可按客户要求更换润滑脂与游隙组别",
			),
		},
		{
			Type: "product", Slug: "ball-bearing-6204", Lang: "en", Group: "p-demo-bearing",
			Title: "Stainless Steel Deep Groove Ball Bearing 6204-2RS", Cat: "mechanical-parts", Date: "2026-06-16",
			Cover:   "/assets/covers/factory-bearing.svg",
			Excerpt: "440C stainless steel bearing with double rubber seals; rust-proof for food machinery and outdoor equipment.",
			Extra:   `{"specs":[{"k":"Model","v":"6204-2RS"},{"k":"Material","v":"440C stainless steel"},{"k":"Size","v":"20×47×14 mm"},{"k":"MOQ","v":"2000 sets"},{"k":"Certification","v":"ISO 9001"}]}`,
			Content: md(
				"## Overview",
				"",
				"Double rubber-sealed construction filled with food-grade grease, passing a 96-hour salt spray test without corrosion.",
				"",
				"- Z2V2 noise grade, max speed 14,000 rpm",
				"- Custom grease and clearance groups on request",
			),
		},
		{
			Type: "product", Slug: "led-high-bay-150w", Lang: "zh", Group: "p-demo-highbay",
			Title: "150W LED 工矿灯", Cat: "led-lighting", Date: "2026-06-14",
			Cover:   "/assets/covers/factory-highbay.svg",
			Excerpt: "160lm/W 高光效工矿灯，压铸铝散热，适用于厂房、仓库与体育馆高顶照明。",
			Extra:   `{"specs":[{"k":"型号","v":"GC-HB-150"},{"k":"材质","v":"压铸铝 + 钢化玻璃"},{"k":"尺寸","v":"Φ300×450 mm"},{"k":"起订量","v":"100 台"},{"k":"认证","v":"CE · RoHS"}]}`,
			Content: md(
				"## 产品说明",
				"",
				"采用一体压铸铝壳体与 120° 配光透镜，光效 **160lm/W**，5 万小时光衰小于 10%。",
				"",
				"- 支持 1-10V 调光与微波感应（选配）",
				"- 质保 5 年，配件全球直发",
			),
		},
		{
			Type: "product", Slug: "led-high-bay-150w", Lang: "en", Group: "p-demo-highbay",
			Title: "150W LED High Bay Light", Cat: "led-lighting", Date: "2026-06-14",
			Cover:   "/assets/covers/factory-highbay.svg",
			Excerpt: "160lm/W high bay with die-cast aluminum housing for factories, warehouses and stadium ceilings.",
			Extra:   `{"specs":[{"k":"Model","v":"GC-HB-150"},{"k":"Material","v":"Die-cast aluminum + tempered glass"},{"k":"Size","v":"Φ300×450 mm"},{"k":"MOQ","v":"100 units"},{"k":"Certification","v":"CE · RoHS"}]}`,
			Content: md(
				"## Overview",
				"",
				"One-piece die-cast aluminum housing with 120° optics delivers **160lm/W**, keeping lumen depreciation under 10% at 50,000 hours.",
				"",
				"- Optional 1-10V dimming and microwave motion sensor",
				"- 5-year warranty with worldwide spare-part shipping",
			),
		},
		{
			Type: "product", Slug: "solar-street-light-30w", Lang: "zh", Group: "p-demo-solar",
			Title: "30W 一体化太阳能路灯", Cat: "led-lighting", Date: "2026-06-12",
			Cover:   "/assets/covers/factory-solar.svg",
			Excerpt: "光伏板、电池与灯体一体化设计，免布线安装，阴雨天续航 3 晚以上。",
			Extra:   `{"specs":[{"k":"型号","v":"GC-SSL-30"},{"k":"材质","v":"压铸铝"},{"k":"尺寸","v":"780×320×55 mm"},{"k":"起订量","v":"50 台"},{"k":"认证","v":"CE · IP65"}]}`,
			Content: md(
				"## 产品说明",
				"",
				"单晶硅光伏板 + 磷酸铁锂电池一体化封装，人体感应三档亮度，整灯防护 **IP65**。",
				"",
				"- 免开挖、免布线，一人即可完成安装",
				"- 适用于乡村道路、园区与庭院照明",
			),
		},
		{
			Type: "product", Slug: "solar-street-light-30w", Lang: "en", Group: "p-demo-solar",
			Title: "30W All-in-One Solar Street Light", Cat: "led-lighting", Date: "2026-06-12",
			Cover:   "/assets/covers/factory-solar.svg",
			Excerpt: "Integrated solar panel, battery and fixture with wiring-free installation; runs 3+ rainy nights per charge.",
			Extra:   `{"specs":[{"k":"Model","v":"GC-SSL-30"},{"k":"Material","v":"Die-cast aluminum"},{"k":"Size","v":"780×320×55 mm"},{"k":"MOQ","v":"50 units"},{"k":"Certification","v":"CE · IP65"}]}`,
			Content: md(
				"## Overview",
				"",
				"Monocrystalline panel and LiFePO4 battery sealed in one **IP65** housing, with a 3-level PIR motion dimming profile.",
				"",
				"- No trenching or wiring — one worker installs it in minutes",
				"- Ideal for rural roads, parks and courtyards",
			),
		},
		{
			Type: "product", Slug: "polyester-pongee-300t", Lang: "zh", Group: "p-demo-pongee",
			Title: "300T 涤纶春亚纺面料", Cat: "textiles", Date: "2026-06-10",
			Cover:   "/assets/covers/factory-fabric.svg",
			Excerpt: "300T 高密春亚纺，轻薄防泼水，适用于羽绒服、风衣与户外装备面料。",
			Extra:   `{"specs":[{"k":"型号","v":"GC-PG-300T"},{"k":"材质","v":"100% 涤纶"},{"k":"尺寸","v":"幅宽 150 cm"},{"k":"起订量","v":"3000 米"},{"k":"认证","v":"OEKO-TEX Standard 100"}]}`,
			Content: md(
				"## 产品说明",
				"",
				"高密平纹组织手感柔软，成品克重 58g/m²，PA/PU 涂层与压光、轧花工艺可选。",
				"",
				"- 现货 60+ 色，支持来样定染",
				"- 防泼水等级 4-5 级（AATCC 22）",
			),
		},
		{
			Type: "product", Slug: "polyester-pongee-300t", Lang: "en", Group: "p-demo-pongee",
			Title: "300T Polyester Pongee Fabric", Cat: "textiles", Date: "2026-06-10",
			Cover:   "/assets/covers/factory-fabric.svg",
			Excerpt: "High-density 300T pongee, lightweight and water-repellent for down jackets, windbreakers and outdoor gear.",
			Extra:   `{"specs":[{"k":"Model","v":"GC-PG-300T"},{"k":"Material","v":"100% polyester"},{"k":"Size","v":"Width 150 cm"},{"k":"MOQ","v":"3000 m"},{"k":"Certification","v":"OEKO-TEX Standard 100"}]}`,
			Content: md(
				"## Overview",
				"",
				"Soft-hand high-density plain weave at 58g/m², with optional PA/PU coating, calendering and embossing.",
				"",
				"- 60+ colors in stock, custom dyeing to sample",
				"- Water repellency rated 4-5 (AATCC 22)",
			),
		},
		{
			Type: "product", Slug: "hotel-waffle-towels", Lang: "zh", Group: "p-demo-towel",
			Title: "纯棉华夫格酒店毛巾", Cat: "textiles", Date: "2026-06-08",
			Cover:   "/assets/covers/factory-towel.svg",
			Excerpt: "100% 长绒棉华夫格织造，轻量速干，可绣 LOGO，面向酒店与 SPA 采购。",
			Extra:   `{"specs":[{"k":"型号","v":"GC-TW-500"},{"k":"材质","v":"100% 长绒棉"},{"k":"尺寸","v":"70×140 cm"},{"k":"起订量","v":"1000 条"},{"k":"认证","v":"OEKO-TEX Standard 100"}]}`,
			Content: md(
				"## 产品说明",
				"",
				"华夫格组织比传统毛圈减重 30%，干燥速度提升一倍，50 次工业洗涤不变形。",
				"",
				"- 支持提花、绣花与专属吊牌包装",
				"- 常规 15 天交货，旺季请提前排单",
			),
		},
		{
			Type: "product", Slug: "hotel-waffle-towels", Lang: "en", Group: "p-demo-towel",
			Title: "Cotton Waffle Hotel Towels", Cat: "textiles", Date: "2026-06-08",
			Cover:   "/assets/covers/factory-towel.svg",
			Excerpt: "100% long-staple cotton waffle weave, lightweight and quick-drying; custom logo embroidery for hotels and spas.",
			Extra:   `{"specs":[{"k":"Model","v":"GC-TW-500"},{"k":"Material","v":"100% long-staple cotton"},{"k":"Size","v":"70×140 cm"},{"k":"MOQ","v":"1000 pcs"},{"k":"Certification","v":"OEKO-TEX Standard 100"}]}`,
			Content: md(
				"## Overview",
				"",
				"The waffle weave cuts 30% of the weight of terry towels and dries twice as fast, holding shape through 50 industrial washes.",
				"",
				"- Jacquard, embroidery and private-label packaging available",
				"- 15-day standard lead time; book early in peak season",
			),
		},
	}
	for _, p := range products {
		if err := s.insertSeed(p, catID[p.Lang]); err != nil {
			return err
		}
	}

	// ---- 「工厂实力」演示数字（settings factory.stats；英文走 ::en 按语种覆盖，
	//      与前台 fillFactoryHome 的 localizedSetting 读法配套）----
	if err := s.SetSetting("factory.stats",
		`[{"num":"2008","label":"工厂成立"},{"num":"120+","label":"产线员工"},{"num":"50,000㎡","label":"自有厂房"},{"num":"60+","label":"出口国家"}]`); err != nil {
		return err
	}
	if err := s.SetSetting("factory.stats::en",
		`[{"num":"2008","label":"Founded"},{"num":"120+","label":"Skilled Workers"},{"num":"50,000㎡","label":"Factory Area"},{"num":"60+","label":"Export Countries"}]`); err != nil {
		return err
	}
	// ---- 工厂图集「车间与设备」演示（settings factory.gallery，全局不分语种）：
	//      3 张图纸风车间 SVG（与商品封面同一绘制语言；未配置该键前台不渲染图集区块）。
	//      FAQ / 应用行业刻意不写演示值——让新站直接吃 i18n 五语种默认，顺带演示回落机制。
	if err := s.SetSetting("factory.gallery",
		`["/assets/covers/factory-workshop.svg","/assets/covers/factory-line.svg","/assets/covers/factory-warehouse.svg"]`); err != nil {
		return err
	}
	return nil
}
