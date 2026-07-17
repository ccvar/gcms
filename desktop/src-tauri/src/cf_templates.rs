//! 站点模板库：把做好的 CF 项目存成模板（沉淀），之后引用模板起新项目。
//! 模板 = <data_dir>/templates/<slug>/ 目录 + pilot-template.json（名称/描述/时间）。
//! 存/取都过滤掉密钥、依赖、构建产物，只留源码。

use serde::Serialize;
use std::fs;
use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

#[derive(Serialize, Default, Debug)]
pub struct Template {
    pub slug: String,
    pub name: String,
    pub desc: String,
    /// 用途分类（落地页/内容/作品集/…），前端按它做筛选。只有随附模板有：用户存模板时不问分类，
    /// 前端把没分类的一律归到「自建」。
    pub category: String,
    /// 站里有几个页面（.html 个数）。模板的单位是「一个站」不是「一个页」，前端据此挂「N 页」标。
    pub pages: usize,
    pub created_at: u64,
    /// 随附的起始模板：不可删除，前端也据此挂「内置」标。
    pub builtin: bool,
}

/// 一档随附模板。
pub struct Builtin {
    pub slug: &'static str,
    pub name: &'static str,
    pub desc: &'static str,
    pub category: &'static str,
    /// (文件名, 内容)。**第一个必须是 index.html** —— 缩略图和站点入口都认它。
    pub pages: &'static [(&'static str, &'static str)],
}

// ---- 随附起始模板 ----
//
// 为什么内置：模板库的机器（存/预览/引用）早就通了，但**冷启动是空的** —— 只有已经做出好站的人
// 才吃得到「沉淀」。而 AI 建站最不可控的就是设计：从白纸开始，每次都要现场发明一套设计系统，
// 方差极大。给它一个**设计好的起点**去改，比任何提示词都管用。
//
// 硬性形态：**单文件、内联 CSS、零外部资源**。三个原因，缺一不可：
// 1) 缩略图是把 index.html 原文塞进 iframe srcdoc（lib.rs::template_index_html），相对路径的
//    <link>/<img> 会解析到 app 的 origin → 404 → 缩略图全白；内联才有真图。
// 2) SKIP 会滤掉 node_modules/dist/build，带构建的模板存下来 = 拷回去跑不起来；单文件完美往返。
// 3) 离线可用（不依赖 CDN/Google Fonts）。
//
// 分类是**用途**不是风格：前五档是同一种页（落地页）的五种长相，风格已经写在 desc 里了；
// 真正找不到的是「我要做的是博客/作品集/商品页」——所以后加的这批按页型铺开。
//
// **模板的单位是「一个站」，不是「一个页」**：站型本来就多页的（博客=列表+文章、电商=列表+详情+
// 购物车、作品集=网格+详情、企业=首页+关于+联系），就得把页配齐——只给一个页的作品集，格子点进去
// 没有落点，那不是起点，是半成品。落地页/活动报名**本来就是一页式**，配多页反而是错的。
macro_rules! page {
    ($slug:literal, $file:literal) => {
        ($file, include_str!(concat!("builtin/", $slug, "/", $file)))
    };
}
pub const BUILTIN: &[Builtin] = &[
    Builtin { slug: "minimal", name: "极简留白", desc: "纯白底 · 大留白 · 一个低饱和强调色", category: "落地页",
        pages: &[page!("minimal", "index.html")] },
    Builtin { slug: "editorial", name: "杂志编辑", desc: "米白底 · 衬线大标题 · 窄栏长文", category: "落地页",
        pages: &[page!("editorial", "index.html")] },
    Builtin { slug: "dark-tech", name: "深色科技", desc: "近黑底 · 霓虹点缀 · 等宽字点缀", category: "落地页",
        pages: &[page!("dark-tech", "index.html")] },
    Builtin { slug: "warm-craft", name: "暖色手作", desc: "奶油底 · 陶土强调 · 圆润温暖", category: "落地页",
        pages: &[page!("warm-craft", "index.html")] },
    Builtin { slug: "saas", name: "企业 SaaS", desc: "浅底 · 卡片层次 · 标准价格分区", category: "落地页",
        pages: &[page!("saas", "index.html")] },
    Builtin { slug: "blog", name: "博客", desc: "冷灰纸底 · 梅子紫 · 文章列表 + 长文排版", category: "内容",
        pages: &[page!("blog", "index.html"), page!("blog", "article.html")] },
    Builtin { slug: "portfolio", name: "作品集", desc: "画廊灰墙 · 苔橄榄 · 作品网格 + 作品详情", category: "作品集",
        pages: &[page!("portfolio", "index.html"), page!("portfolio", "work.html")] },
    Builtin { slug: "shop", name: "电商店铺", desc: "琥珀金 · 商品列表 / 详情 / 购物车", category: "电商",
        pages: &[page!("shop", "index.html"), page!("shop", "product.html"), page!("shop", "cart.html")] },
    Builtin { slug: "corp", name: "企业官网", desc: "冷钢灰 · 石油蓝 · 首页 / 关于 / 联系", category: "企业",
        pages: &[page!("corp", "index.html"), page!("corp", "about.html"), page!("corp", "contact.html")] },
    Builtin { slug: "event", name: "活动报名", desc: "时间地点 · 日程表 · 讲者与票档", category: "活动",
        pages: &[page!("event", "index.html")] },
    // 外贸工厂：**正文英文**——受众是海外采购商，不是国内客户。这是它和「企业官网」那档的分水岭。
    // 主心骨是「优势」和「产品目录」，不是普通企业官网的公司简介。
    Builtin { slug: "factory", name: "外贸工厂", desc: "深松绿 · 英文站 · 优势/产品/询盘 · OEM 出口制造商", category: "外贸",
        pages: &[page!("factory", "index.html"), page!("factory", "products.html"),
                 page!("factory", "product.html"), page!("factory", "contact.html")] },
];

pub fn is_builtin(slug: &str) -> bool {
    BUILTIN.iter().any(|b| b.slug == slug)
}

/// 把随附模板写进 <data_dir>/templates/<slug>/（覆写以随版本刷新）。
/// created_at 固定 0 → 排在用户自己沉淀的模板后面。
pub fn ensure_builtin(data_dir: &Path) -> std::io::Result<()> {
    let dir = data_dir.join("templates");
    prune_stale_builtin(&dir);
    for b in BUILTIN {
        let d = dir.join(b.slug);
        fs::create_dir_all(&d)?;
        for (file, html) in b.pages {
            fs::write(d.join(file), html)?;
        }
        let manifest = serde_json::json!({
            "name": b.name, "desc": b.desc, "category": b.category, "created_at": 0, "builtin": true
        });
        let bytes = serde_json::to_vec_pretty(&manifest)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;
        fs::write(d.join("pilot-template.json"), bytes)?;
    }
    Ok(())
}

/// 清掉「**曾经**是随附模板、现在已经不在 BUILTIN 里」的目录。
///
/// 为什么非有不可：blog-index 和 article 合并成 blog 之后，老目录还躺在已装用户的数据目录里，
/// 而 `is_builtin()` 已经不认它们了 —— 它们会**冒充成用户自建的模板**留在库里（还带着删除键、
/// 落进「自建」筛选），像见了鬼。ensure_builtin 只写不删，这个洞得自己堵。
///
/// 安全边界：**只删清单里写着 `builtin: true` 的**。`save()` 从来不写这个键，所以用户自己
/// 沉淀的模板一个都碰不到。
fn prune_stale_builtin(templates_dir: &Path) {
    let Ok(rd) = fs::read_dir(templates_dir) else { return };
    for e in rd.flatten() {
        let p = e.path();
        if !p.is_dir() {
            continue;
        }
        let slug = e.file_name().to_string_lossy().into_owned();
        if is_builtin(&slug) {
            continue; // 还在册，留着
        }
        let was_builtin = fs::read(p.join("pilot-template.json"))
            .ok()
            .and_then(|r| serde_json::from_slice::<serde_json::Value>(&r).ok())
            .and_then(|j| j.get("builtin").and_then(|x| x.as_bool()))
            .unwrap_or(false);
        if was_builtin {
            let _ = fs::remove_dir_all(&p);
        }
    }
}

/// 拷贝时跳过的目录/文件：依赖、构建产物、版本控制、任何 .env*。
const SKIP: &[&str] = &[
    "node_modules",
    ".wrangler",
    ".git",
    "dist",
    "build",
    ".svelte-kit",
    ".vercel",
    ".next",
    ".DS_Store",
    "pilot-template.json",
];

fn should_skip(name: &str) -> bool {
    SKIP.contains(&name) || name.starts_with(".env")
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

fn slugify(s: &str) -> String {
    let slug: String = s
        .trim()
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c.to_ascii_lowercase() } else { '-' })
        .collect();
    slug.trim_matches('-').to_string()
}

fn safe_slug(slug: &str) -> bool {
    !slug.is_empty()
        && slug.len() <= 100
        && slug.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
}

/// 目录里是否有可拷贝内容（顶层存在非跳过项）。空项目/只有 node_modules 时为 false。
fn has_copyable_content(src: &Path) -> bool {
    std::fs::read_dir(src)
        .map(|rd| {
            rd.flatten()
                .any(|e| !should_skip(&e.file_name().to_string_lossy()))
        })
        .unwrap_or(false)
}

fn copy_dir_filtered(src: &Path, dst: &Path) -> std::io::Result<()> {
    fs::create_dir_all(dst)?;
    for e in fs::read_dir(src)? {
        let e = e?;
        let name = e.file_name();
        let ns = name.to_string_lossy();
        if should_skip(&ns) {
            continue;
        }
        let sp = e.path();
        let dp = dst.join(&name);
        if sp.is_dir() {
            copy_dir_filtered(&sp, &dp)?;
        } else {
            fs::copy(&sp, &dp)?;
        }
    }
    Ok(())
}

/// 把 src 项目目录存成模板。
pub fn save(templates_dir: &Path, name: &str, desc: &str, src: &Path) -> Result<Template, String> {
    let slug = slugify(name);
    if slug.is_empty() {
        return Err("模板名不能为空".into());
    }
    if !src.is_dir() || !has_copyable_content(src) {
        return Err("项目还是空的，先在对话里让 AI 建点东西再存模板".into());
    }
    let dst = templates_dir.join(&slug);
    if dst.exists() {
        return Err(if is_builtin(&slug) {
            format!("「{slug}」和随附的起始模板重名了，换个名字。")
        } else {
            format!("模板「{slug}」已存在，换个名字或先删除旧的。")
        });
    }
    fs::create_dir_all(templates_dir).map_err(|e| e.to_string())?;
    copy_dir_filtered(src, &dst).map_err(|e| format!("拷贝项目失败: {e}"))?;
    let created = now();
    let manifest = serde_json::json!({ "name": name, "desc": desc, "created_at": created });
    fs::write(
        dst.join("pilot-template.json"),
        serde_json::to_vec_pretty(&manifest).map_err(|e| e.to_string())?,
    )
    .map_err(|e| format!("写模板清单失败: {e}"))?;
    let pages = count_pages(&dst);
    Ok(Template {
        slug,
        name: name.trim().into(),
        desc: desc.trim().into(),
        category: String::new(), // 存模板时不问分类；前端把空分类归到「自建」
        pages,
        created_at: created,
        builtin: false, // 用户自己存的，永远不是随附模板（重名在上面就挡掉了）
    })
}

/// 站里有几个页面。只数顶层的 .html —— 用户存的模板可能有 assets/ 之类的子目录，
/// 递归去数会把一堆无关的东西也算成「页」。
fn count_pages(dir: &Path) -> usize {
    fs::read_dir(dir)
        .map(|rd| {
            rd.flatten()
                .filter(|e| e.path().extension().and_then(|x| x.to_str()) == Some("html"))
                .count()
        })
        .unwrap_or(0)
}

pub fn list(templates_dir: &Path) -> Vec<Template> {
    let Ok(rd) = fs::read_dir(templates_dir) else { return vec![] };
    let mut v: Vec<Template> = rd
        .flatten()
        .filter(|e| e.path().is_dir())
        .map(|e| {
            let slug = e.file_name().to_string_lossy().into_owned();
            let mf = fs::read(e.path().join("pilot-template.json"))
                .ok()
                .and_then(|r| serde_json::from_slice::<serde_json::Value>(&r).ok());
            let (name, desc, category, created) = mf
                .map(|j| {
                    (
                        j.get("name").and_then(|x| x.as_str()).unwrap_or(&slug).to_string(),
                        j.get("desc").and_then(|x| x.as_str()).unwrap_or("").to_string(),
                        j.get("category").and_then(|x| x.as_str()).unwrap_or("").to_string(),
                        j.get("created_at").and_then(|x| x.as_u64()).unwrap_or(0),
                    )
                })
                .unwrap_or((slug.clone(), String::new(), String::new(), 0));
            let builtin = is_builtin(&slug);
            let pages = count_pages(&e.path());
            Template { slug, name, desc, category, pages, created_at: created, builtin }
        })
        .collect();
    // 用户自己沉淀的在前（created_at 新→旧），随附模板 created_at=0 自然沉底。
    v.sort_by(|a, b| b.created_at.cmp(&a.created_at));
    v
}

/// 模板的显示名（预览窗标题用）。读不到就退回 slug —— 标题差一点无所谓，别为它让预览失败。
pub fn display_name(templates_dir: &Path, slug: &str) -> String {
    fs::read(templates_dir.join(slug).join("pilot-template.json"))
        .ok()
        .and_then(|r| serde_json::from_slice::<serde_json::Value>(&r).ok())
        .and_then(|j| j.get("name").and_then(|x| x.as_str()).map(str::to_string))
        .filter(|s| !s.trim().is_empty())
        .unwrap_or_else(|| slug.to_string())
}

pub fn delete(templates_dir: &Path, slug: &str) -> Result<(), String> {
    if !safe_slug(slug) {
        return Err("非法模板名".into());
    }
    // 随附模板不给删：删了下次启动 ensure_builtin 又会写回来，白费事还显得像 bug。
    if is_builtin(slug) {
        return Err("这是随附的起始模板，不能删除".into());
    }
    fs::remove_dir_all(templates_dir.join(slug)).map_err(|e| format!("删除模板失败: {e}"))
}

/// 把模板拷进目标项目目录（引用模板建站）。
pub fn instantiate(templates_dir: &Path, slug: &str, dest_project_dir: &Path) -> Result<(), String> {
    if !safe_slug(slug) {
        return Err("非法模板名".into());
    }
    let src = templates_dir.join(slug);
    if !src.is_dir() {
        return Err("模板不存在".into());
    }
    // 目标项目已存在且非空 → 拒绝，别覆盖用户已有的项目。
    if dest_project_dir.is_dir()
        && std::fs::read_dir(dest_project_dir).map(|mut r| r.next().is_some()).unwrap_or(false)
    {
        return Err("这个项目名已存在且非空，换个名字（不会覆盖你已有的项目）".into());
    }
    copy_dir_filtered(&src, dest_project_dir).map_err(|e| format!("拷贝模板失败: {e}"))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 从模板 HTML 的 `:root` 里抠出某个颜色令牌。
    /// （`--accent:` 带冒号，所以不会误命中 `--accent-soft:`。）
    fn token_rgb(html: &str, var: &str) -> Option<(f64, f64, f64)> {
        let key = format!("{var}:");
        let i = html.find(&key)? + key.len();
        // 模板里全是中文注释：**不能**按字节数硬切（`&html[i..i+40]` 会切进汉字中间直接 panic）。
        // find/get 给的都是合法边界，越界也只是 None。
        let rest = html.get(i..)?;
        let h = rest.find('#')?;
        if h > 40 {
            return None; // 离令牌太远，那个 # 不是它的值
        }
        let hex = rest.get(h + 1..h + 7)?;
        let c = |a, b| u8::from_str_radix(hex.get(a..b)?, 16).ok().map(|v| v as f64 / 255.0);
        Some((c(0, 2)?, c(2, 4)?, c(4, 6)?))
    }

    /// HSL 的色相（0-360）与饱和度（0-100）。
    fn hue_sat(html: &str, var: &str) -> Option<(f64, f64)> {
        let (r, g, b) = token_rgb(html, var)?;
        let (max, min) = (r.max(g).max(b), r.min(g).min(b));
        let (l, d) = ((max + min) / 2.0, max - min);
        if d < 1e-9 {
            return Some((0.0, 0.0)); // 纯灰：不占色相
        }
        let s = if l > 0.5 { d / (2.0 - max - min) } else { d / (max + min) };
        let h = if max == r {
            ((g - b) / d).rem_euclid(6.0)
        } else if max == g {
            (b - r) / d + 2.0
        } else {
            (r - g) / d + 4.0
        };
        Some(((h * 60.0).rem_euclid(360.0), s * 100.0))
    }

    /// 底色是深的吗（WCAG 相对亮度 < 0.25）。深底与浅底的模板天然不会看混。
    fn bg_is_dark(html: &str) -> bool {
        let Some((r, g, b)) = token_rgb(html, "--bg") else { return false };
        let f = |v: f64| if v <= 0.03928 { v / 12.92 } else { ((v + 0.055) / 1.055).powf(2.4) };
        0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b) < 0.25
    }

    /// 下架的随附模板要**自动清掉**，但**绝不能碰用户自己存的**。
    ///
    /// 真实场景：blog-index / article 合并成 blog 之后，老目录还躺在已装用户的数据目录里。
    /// ensure_builtin 只写不删 → 老目录留着，而 is_builtin() 已经不认它们了 →
    /// 它们会**冒充成用户自建模板**（带删除键、落进「自建」筛选）。
    /// 这条测试同时钉死另一头：删除只认清单里的 `builtin: true`，用户的模板碰都不许碰。
    #[test]
    fn stale_builtin_pruned_user_templates_untouched() {
        let base = std::env::temp_dir().join(format!("tmplp-{}", uuid::Uuid::new_v4()));
        let tdir = base.join("templates");
        fs::create_dir_all(&tdir).unwrap();

        // 上个版本种下的随附模板，这版下架了
        let old = tdir.join("blog-index");
        fs::create_dir_all(&old).unwrap();
        fs::write(old.join("index.html"), "<h1>老的随附模板</h1>").unwrap();
        // r#""# 而不是 br#""#：字节串字面量里不许有非 ASCII（中文会直接编译不过）
        fs::write(old.join("pilot-template.json"), r#"{"name":"博客首页","builtin":true}"#).unwrap();

        // 用户自己沉淀的（save() 从不写 builtin 键）—— 名字同样不在 BUILTIN 里
        let mine = tdir.join("my-shop");
        fs::create_dir_all(&mine).unwrap();
        fs::write(mine.join("index.html"), "<h1>我自己的站</h1>").unwrap();
        fs::write(mine.join("pilot-template.json"), r#"{"name":"我的店","created_at":123}"#).unwrap();

        ensure_builtin(&base).unwrap();

        assert!(!old.exists(), "下架的随附模板该被清掉，否则会冒充成用户模板赖在库里");
        assert!(mine.join("index.html").exists(), "★ 用户自己存的模板一个字节都不许碰");
        let ls = list(&tdir);
        assert!(ls.iter().any(|t| t.slug == "my-shop" && !t.builtin), "用户模板还得在，且不是 builtin");
        assert!(!ls.iter().any(|t| t.slug == "blog-index"), "老档不该还在列表里");
        assert!(ls.iter().any(|t| t.slug == "blog" && t.pages == 2), "新的合并档该在，且是两页");

        fs::remove_dir_all(&base).ok();
    }

    /// ★ 配色的机器裁判：随附模板之间强调色不许撞色相。
    ///
    /// 为什么要这条：这批模板是多个 agent **各写各的**，谁也看不见别人选了什么色 —— 结果
    /// article/shop 撞到 **0.1°**、portfolio/event 撞到 3.7°，两次都是**事后人眼**才发现的。
    /// 模板库的价值就是缩略图一眼能分辨，撞色相直接砍掉这个价值。有了这条，下次加第 12 档
    /// 撞了就直接红，不用等谁盯着看。
    #[test]
    fn builtin_accents_dont_collide() {
        const MIN_GAP: f64 = 25.0;
        /// 饱和度低于此即「近乎中性灰」，不主张色相（minimal 的 #4C6663 s15 正是）。
        const GREY_SAT: f64 = 25.0;
        /// 已知遗留，**最早那 5 档就带着的**：editorial 与 warm-craft 同为浅底暖红，只差 5.3°。
        /// 没顺手改，是因为 warm-craft 的介绍里明写「陶土强调」——改色就得连文案一起改；
        /// 且两者版式差得远（衬线杂志长文 vs 圆润手作），缩略图不至于认错。真要动，动 editorial。
        const GRANDFATHERED: &[(&str, &str)] = &[("editorial", "warm-craft")];

        let v: Vec<_> = BUILTIN
            .iter()
            .map(|b| {
                let html = b.pages[0].1; // 以 index.html 为准（同站各页令牌值必须一致）
                let (h, s) = hue_sat(html, "--accent")
                    .unwrap_or_else(|| panic!("{}: :root 里读不到 --accent", b.slug));
                (b.slug, h, s, bg_is_dark(html))
            })
            .collect();

        for (i, &(a, ha, sa, da)) in v.iter().enumerate() {
            for &(b, hb, sb, db) in &v[i + 1..] {
                if sa < GREY_SAT || sb < GREY_SAT {
                    continue; // 近灰不占色相
                }
                if da != db {
                    continue; // 一深底一浅底，本来就不会看混
                }
                if GRANDFATHERED.contains(&(a, b)) || GRANDFATHERED.contains(&(b, a)) {
                    continue;
                }
                let d = (ha - hb).abs().min(360.0 - (ha - hb).abs());
                assert!(
                    d >= MIN_GAP,
                    "「{a}」和「{b}」强调色撞车：色相只差 {d:.1}°（{ha:.0}° vs {hb:.0}°）。\
                     模板库靠缩略图一眼分辨，撞色相就等于少了一档 —— 挑个空的色相带。"
                );
            }
        }
    }

    #[test]
    fn save_list_use_delete_roundtrip() {
        let base = std::env::temp_dir().join(format!("tmpl-{}", uuid::Uuid::new_v4()));
        let tdir = base.join("templates");
        let src = base.join("proj");
        fs::create_dir_all(src.join("node_modules")).unwrap();
        fs::create_dir_all(src.join("src")).unwrap();
        fs::write(src.join("index.html"), "<h1>hi</h1>").unwrap();
        fs::write(src.join(".env"), "SECRET=x").unwrap();
        fs::write(src.join("node_modules").join("big.js"), "junk").unwrap();
        fs::write(src.join("src").join("app.js"), "code").unwrap();

        let t = save(&tdir, "Coffee Landing", "深色咖啡落地页", &src).unwrap();
        assert_eq!(t.slug, "coffee-landing");
        // 密钥与依赖被过滤
        assert!(!tdir.join("coffee-landing").join(".env").exists());
        assert!(!tdir.join("coffee-landing").join("node_modules").exists());
        assert!(tdir.join("coffee-landing").join("index.html").exists());
        assert!(tdir.join("coffee-landing").join("src").join("app.js").exists());

        let ls = list(&tdir);
        assert_eq!(ls.len(), 1);
        assert_eq!(ls[0].name, "Coffee Landing");

        // 引用建站
        let dest = base.join("newproj");
        instantiate(&tdir, "coffee-landing", &dest).unwrap();
        assert!(dest.join("index.html").exists());
        assert!(!dest.join("pilot-template.json").exists()); // 模板清单不进新项目

        delete(&tdir, "coffee-landing").unwrap();
        assert!(list(&tdir).is_empty());
        // 路径穿越被拒
        assert!(delete(&tdir, "../evil").is_err());
        fs::remove_dir_all(&base).ok();
    }

    /// 随附模板：种得进去、列得出来、删不掉、重名存不了。
    #[test]
    fn builtin_seed_list_and_protect() {
        let base = std::env::temp_dir().join(format!("tmplb-{}", uuid::Uuid::new_v4()));
        ensure_builtin(&base).unwrap();
        let tdir = base.join("templates");

        let ls = list(&tdir);
        assert_eq!(ls.len(), BUILTIN.len(), "每一档都该种进去");
        assert!(ls.iter().all(|t| t.builtin), "随附模板必须标 builtin，前端据此挂标/藏删除键");
        // 分类是前端筛选的唯一依据：漏一个，它就掉进「其他」那档里，看着像 bug
        assert!(ls.iter().all(|t| !t.category.is_empty()), "随附模板都得有分类: {ls:?}");
        // 光有风格档（全是落地页）等于没分类可筛
        assert!(
            ls.iter().map(|t| t.category.as_str()).collect::<std::collections::HashSet<_>>().len() >= 3,
            "分类要真的铺开页型，不然筛选没意义"
        );

        // **每一页**都得是能直接渲染的自包含单文件（缩略图靠读 index.html 原文塞 srcdoc）
        for b in BUILTIN {
            assert_eq!(b.pages[0].0, "index.html", "{}: 第一页必须是 index.html（缩略图与站点入口都认它）", b.slug);
            for (file, _) in b.pages {
                let html = fs::read_to_string(tdir.join(b.slug).join(file)).unwrap_or_default();
                let at = format!("{}/{file}", b.slug);
                assert!(html.contains("<style"), "{at}: CSS 必须内联，否则缩略图是白的");
                assert!(html.contains("--accent"), "{at}: 必须有设计变量");
                // 外链资源 = 缩略图 404 + 离线打不开。srcdoc 里相对路径会解析到 app 的 origin。
                assert!(
                    !html.contains("<link rel=\"stylesheet\"") && !html.contains("<script src="),
                    "{at}: 不许外链 CSS/JS"
                );
                assert!(!html.contains("//fonts.googleapis.com"), "{at}: 不许外链字体");
                assert!(!html.contains("<img src=\"http"), "{at}: 不许外链图片");
            }
        }

        // 页数：站型该有几页就几页（模板的单位是「一个站」不是「一个页」）
        let by = |s: &str| ls.iter().find(|t| t.slug == s).unwrap();
        assert_eq!(by("blog").pages, 2, "博客 = 列表 + 文章");
        assert_eq!(by("shop").pages, 3, "电商 = 列表 + 详情 + 购物车");
        assert_eq!(by("corp").pages, 3, "企业 = 首页 + 关于 + 联系");
        assert_eq!(by("minimal").pages, 1, "落地页本来就是一页式");

        // 多页模板的页间跳转必须是真的：主导航指向的文件得**真的在磁盘上**
        for b in BUILTIN.iter().filter(|b| b.pages.len() > 1) {
            for (file, html) in b.pages {
                let links: Vec<&str> = html
                    .match_indices("href=\"")
                    .filter_map(|(i, _)| {
                        let rest = &html[i + 6..];
                        rest.find('"').map(|e| &rest[..e])
                    })
                    .filter(|h| h.ends_with(".html"))
                    .collect();
                for l in &links {
                    let target = l.split('#').next().unwrap_or(l);
                    assert!(
                        b.pages.iter().any(|(f, _)| f == &target),
                        "{}/{file} 链到了 `{l}`，但这个站里没有这个页 —— 死链",
                        b.slug
                    );
                }
            }
        }

        // 预览窗标题用的是显示名，不是 slug
        assert_eq!(display_name(&tdir, "saas"), "企业 SaaS");
        assert_eq!(display_name(&tdir, "不存在"), "不存在", "读不到就退回 slug，别让标题弄挂预览");

        // 删不掉
        assert!(delete(&tdir, "minimal").is_err(), "随附模板不该能删");
        assert!(tdir.join("minimal").exists());

        // 重名存不了（给的是「换个名字」而不是「先删除旧的」——因为删不掉）
        let src = base.join("proj");
        fs::create_dir_all(&src).unwrap();
        fs::write(src.join("index.html"), "<h1>x</h1>").unwrap();
        let e = save(&tdir, "minimal", "", &src).unwrap_err();
        assert!(e.contains("换个名字"), "重名提示要说人话: {e}");

        // 覆写幂等：再种一次不炸、数量不变
        ensure_builtin(&base).unwrap();
        assert_eq!(list(&tdir).len(), BUILTIN.len());

        fs::remove_dir_all(&base).ok();
    }
}
