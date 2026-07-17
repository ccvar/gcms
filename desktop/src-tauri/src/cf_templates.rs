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
    pub created_at: u64,
    /// 随附的起始模板：不可删除，前端也据此挂「内置」标。
    pub builtin: bool,
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
pub const BUILTIN: &[(&str, &str, &str, &str)] = &[
    ("minimal", "极简留白", "纯白底 · 大留白 · 一个低饱和强调色", include_str!("builtin/minimal.html")),
    ("editorial", "杂志编辑", "米白底 · 衬线大标题 · 窄栏长文", include_str!("builtin/editorial.html")),
    ("dark-tech", "深色科技", "近黑底 · 霓虹点缀 · 等宽字点缀", include_str!("builtin/dark-tech.html")),
    ("warm-craft", "暖色手作", "奶油底 · 陶土强调 · 圆润温暖", include_str!("builtin/warm-craft.html")),
    ("saas", "企业 SaaS", "浅底 · 卡片层次 · 标准价格分区", include_str!("builtin/saas.html")),
];

pub fn is_builtin(slug: &str) -> bool {
    BUILTIN.iter().any(|(s, ..)| *s == slug)
}

/// 把随附模板写进 <data_dir>/templates/<slug>/（覆写以随版本刷新）。
/// created_at 固定 0 → 排在用户自己沉淀的模板后面。
pub fn ensure_builtin(data_dir: &Path) -> std::io::Result<()> {
    let dir = data_dir.join("templates");
    for (slug, name, desc, html) in BUILTIN {
        let d = dir.join(slug);
        fs::create_dir_all(&d)?;
        fs::write(d.join("index.html"), html)?;
        let manifest =
            serde_json::json!({ "name": name, "desc": desc, "created_at": 0, "builtin": true });
        let bytes = serde_json::to_vec_pretty(&manifest)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;
        fs::write(d.join("pilot-template.json"), bytes)?;
    }
    Ok(())
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
    Ok(Template {
        slug,
        name: name.trim().into(),
        desc: desc.trim().into(),
        created_at: created,
        builtin: false, // 用户自己存的，永远不是随附模板（重名在上面就挡掉了）
    })
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
            let (name, desc, created) = mf
                .map(|j| {
                    (
                        j.get("name").and_then(|x| x.as_str()).unwrap_or(&slug).to_string(),
                        j.get("desc").and_then(|x| x.as_str()).unwrap_or("").to_string(),
                        j.get("created_at").and_then(|x| x.as_u64()).unwrap_or(0),
                    )
                })
                .unwrap_or((slug.clone(), String::new(), 0));
            let builtin = is_builtin(&slug);
            Template { slug, name, desc, created_at: created, builtin }
        })
        .collect();
    // 用户自己沉淀的在前（created_at 新→旧），随附模板 created_at=0 自然沉底。
    v.sort_by(|a, b| b.created_at.cmp(&a.created_at));
    v
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
        assert_eq!(ls.len(), BUILTIN.len(), "五档风格都该种进去");
        assert!(ls.iter().all(|t| t.builtin), "随附模板必须标 builtin，前端据此挂标/藏删除键");

        // 每个都得是能直接渲染的单文件（缩略图靠读 index.html 原文）
        for (slug, ..) in BUILTIN {
            let idx = tdir.join(slug).join("index.html");
            let html = fs::read_to_string(&idx).unwrap_or_default();
            assert!(html.contains("<style"), "{slug}: 必须内联 CSS，否则缩略图是白的");
            assert!(html.contains("--accent"), "{slug}: 必须有设计变量");
        }

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
