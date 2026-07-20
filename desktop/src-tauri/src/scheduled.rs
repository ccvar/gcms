//! 排期视图：查 gcms 各站点「待定时发布(status=scheduled)」的内容，汇总成一张排期表。
//! 反映的是 gcms 服务端状态（服务端到点自动发），Pilot 只读展示。

use serde::Serialize;
use std::time::Duration;

use crate::discovery;
use crate::keychain;
use crate::pack::Connection;

#[derive(Serialize, Clone)]
pub struct ScheduledItem {
    pub site_slug: String,
    pub site_name: String,
    pub id: i64,
    pub title: String,
    pub lang: String,
    pub published_at: String,
    /// 接口给的是**相对路径**（`/zh/posts/xxx`，见 api.go::apiContentURL），不是完整链接 ——
    /// 前端要配 discovery 里那个站的域名才能拼出真实访问地址。
    pub url: String,
    /// `scheduled`（待发布）| `published`（已发布）。
    /// 前端据此决定「预览」开哪种链接：待发布的公开 URL 打不开、只能走短期草稿链接；
    /// 已发布的就该直接开真实访问链接。
    pub status: String,
    /// 所属集合（接口路径段）：`posts` / `links` / `pages` / 扩展类型前缀（`products` 等）。
    /// 预览链接要按它拼 `/{collection}/{id}/preview-url`；前端也据此给非文章条目打型标。
    pub collection: String,
}

/// 已发布的往回看几天。接口**没有日期过滤**（只能按 updated_at DESC 取最近的），
/// 所以这里是拉回来之后自己截断的：往回一周，够看「昨天发了什么」，也不至于把几年的历史都拽回来。
const PUBLISHED_LOOKBACK_DAYS: i64 = 7;

/// 取某条排期内容的**前台预览链接**（短期有效、真前台模板渲染草稿）——
/// 排期内容未发布，公开 URL 打不开，必须走 preview-url 接口。
pub async fn preview_url(
    conn: &Connection,
    site_slug: &str,
    collection: &str,
    id: i64,
) -> Result<String, String> {
    let key = keychain::get_key(&conn.id)?;
    let disc = discovery::discover(conn).await?;
    let sites = disc
        .get("items")
        .and_then(|i| i.as_array())
        .cloned()
        .unwrap_or_default();
    let api_base = sites
        .iter()
        .find(|s| s.get("slug").and_then(|v| v.as_str()) == Some(site_slug))
        .and_then(|s| s.get("api_base").and_then(|v| v.as_str()))
        .unwrap_or("")
        .to_string();
    if api_base.is_empty() {
        return Err("没有找到该站点的接口地址".into());
    }
    // preview-url 是 /{collection}/{id}/preview-url 的泛化路由：链接/页面/扩展类型同构。
    // collection 只放行路径段安全字符，异常值回退 posts（老调用方没传集合）。
    let coll = collection.trim();
    let coll = if !coll.is_empty()
        && coll
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
    {
        coll
    } else {
        "posts"
    };
    let url = format!(
        "{}/{}/{}/preview-url",
        api_base.trim_end_matches('/'),
        coll,
        id
    );
    // 注意：preview-url 是 POST（生成短期链接），GET 会 404。
    let resp = reqwest::Client::new()
        .post(&url)
        .header("Content-Type", "application/json")
        .body("{}")
        .header("Authorization", format!("Bearer {key}"))
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    let text = resp.text().await.unwrap_or_default();
    if !status.is_success() {
        // 带上服务端错误信息（message 字段），否则只有裸 404 无从定位
        let msg = serde_json::from_str::<serde_json::Value>(&text)
            .ok()
            .and_then(|v| v.get("message").and_then(|m| m.as_str()).map(String::from))
            .unwrap_or_else(|| text.chars().take(120).collect());
        return Err(format!("获取预览链接失败：{status} {msg}"));
    }
    let body: serde_json::Value = serde_json::from_str(&text).map_err(|e| e.to_string())?;
    let u = body
        .get("preview_url")
        .or_else(|| body.get("frontend_preview_url"))
        .and_then(|v| v.as_str())
        .unwrap_or("");
    if u.is_empty() {
        return Err("该内容没有可用的前台预览链接".into());
    }
    Ok(u.to_string())
}

pub async fn list_scheduled(conn: &Connection) -> Result<Vec<ScheduledItem>, String> {
    let key = keychain::get_key(&conn.id)?;
    let disc = discovery::discover(conn).await?;
    let sites = disc
        .get("items")
        .and_then(|i| i.as_array())
        .cloned()
        .unwrap_or_default();

    let client = reqwest::Client::new();
    // 每个站要拉两次（待发布 + 最近已发布）。
    // ★ 改成并发：原注释写「站点通常个位数，顺序拉取即可」——这个假设过期了，实测用户有 21 个站，
    //   顺序 42 个请求（每个超时 15s）会把排期视图拖到肉眼可见地卡。
    //   单站失败（没有 posts:read 等）照旧跳过，不影响其它站。
    let mut tasks = Vec::new();
    for s in &sites {
        let api_base = s
            .get("api_base")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        if api_base.is_empty() {
            continue;
        }
        let slug = s
            .get("slug")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        let name = s
            .get("name")
            .and_then(|v| v.as_str())
            .unwrap_or(&slug)
            .to_string();
        let (c, k) = (client.clone(), key.clone());
        tasks.push(tokio::spawn(async move {
            // ★ 排期不只有文章：链接/页面/扩展类型（商品等）一样能定时发布——先探 /types
            // 拿本站启用的集合清单，再逐集合拉「待发布 + 最近已发布」，全部并发。
            let colls = site_collections(&c, &api_base, &k).await;
            let mut subs = Vec::new();
            for coll in colls {
                for status in ["scheduled", "published"] {
                    let (c2, k2, a2, s2, n2, co2) = (
                        c.clone(),
                        k.clone(),
                        api_base.clone(),
                        slug.clone(),
                        name.clone(),
                        coll.clone(),
                    );
                    subs.push(tokio::spawn(async move {
                        fetch_site(&c2, &a2, &k2, &s2, &n2, &co2, status)
                            .await
                            .unwrap_or_default()
                    }));
                }
            }
            let mut v = Vec::new();
            for s in subs {
                if let Ok(x) = s.await {
                    v.extend(x);
                }
            }
            v
        }));
    }
    let mut out: Vec<ScheduledItem> = Vec::new();
    for t in tasks {
        if let Ok(v) = t.await {
            out.extend(v);
        }
    }
    // 已发布的只留最近一周。apiTime 恒为 UTC RFC3339（api.go::apiTime → `t.UTC().Format(RFC3339)`），
    // 同格式同时区 → 直接比字符串是安全的，不用解析。
    let cutoff = (chrono::Utc::now() - chrono::Duration::days(PUBLISHED_LOOKBACK_DAYS))
        .format("%Y-%m-%dT%H:%M:%SZ")
        .to_string();
    out.retain(|i| i.status != "published" || i.published_at >= cutoff);
    // 按发布时间升序（最近要发的在前；已发布的排在更前面，因为时间更早）。
    out.sort_by(|a, b| a.published_at.cmp(&b.published_at));
    Ok(out)
}

/// 服务端的硬上限。**`limit > 100` 会被静默改成 20**（store.go::ListContentForAutomation：
/// `if limit <= 0 || limit > 100 { limit = 20 }`）—— 这里原来写 200，于是每站**实际只拿到 20 条**，
/// 任何一个站排超过 20 条，多的就无声无息地不在排期视图里。要 100 就得写 100。
const API_MAX_LIMIT: u32 = 100;

/// 站点启用的内容集合：内置三件套 + `/types` 报告的扩展类型（collection=url_prefix）。
/// `/types` 拿不到（老服务端 / Key 无该权限）就退回内置集合——links/pages 接口早于扩展
/// 机制存在，对任何服务端都安全；单个集合无权限时那次列表请求自己失败、被上层跳过。
async fn site_collections(client: &reqwest::Client, api_base: &str, key: &str) -> Vec<String> {
    let mut out = vec![
        "posts".to_string(),
        "links".to_string(),
        "pages".to_string(),
    ];
    let url = format!("{}/types", api_base.trim_end_matches('/'));
    let resp = client
        .get(&url)
        .header("Authorization", format!("Bearer {key}"))
        .timeout(Duration::from_secs(10))
        .send()
        .await;
    if let Ok(r) = resp {
        if r.status().is_success() {
            if let Ok(v) = r.json::<serde_json::Value>().await {
                for t in v
                    .get("types")
                    .and_then(|x| x.as_array())
                    .cloned()
                    .unwrap_or_default()
                {
                    // 默认只列已启用；带 enabled=false 的（?all=1 才会出现）防御性跳过。
                    if t.get("enabled").and_then(|e| e.as_bool()) == Some(false) {
                        continue;
                    }
                    let c = t
                        .get("collection")
                        .or_else(|| t.get("url_prefix"))
                        .and_then(|x| x.as_str())
                        .unwrap_or("")
                        .trim()
                        .to_string();
                    if !c.is_empty() && !out.contains(&c) {
                        out.push(c);
                    }
                }
            }
        }
    }
    out
}

async fn fetch_site(
    client: &reqwest::Client,
    api_base: &str,
    key: &str,
    slug: &str,
    name: &str,
    collection: &str,
    status: &str,
) -> Result<Vec<ScheduledItem>, String> {
    let url = format!(
        "{}/{collection}?status={status}&lang=all&limit={API_MAX_LIMIT}",
        api_base.trim_end_matches('/')
    );
    let resp = client
        .get(&url)
        .header("Authorization", format!("Bearer {key}"))
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if !resp.status().is_success() {
        return Err(format!("{} {}", slug, resp.status()));
    }
    let body: serde_json::Value = resp.json().await.map_err(|e| e.to_string())?;
    let items = body
        .get("items")
        .and_then(|i| i.as_array())
        .cloned()
        .unwrap_or_default();
    let mut out = Vec::new();
    for it in items {
        let published_at = it
            .get("published_at")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        out.push(ScheduledItem {
            site_slug: slug.to_string(),
            site_name: name.to_string(),
            id: it.get("id").and_then(|v| v.as_i64()).unwrap_or(0),
            title: it
                .get("title")
                .and_then(|v| v.as_str())
                .unwrap_or("(无标题)")
                .to_string(),
            lang: it
                .get("lang")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string(),
            published_at,
            url: it
                .get("url")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string(),
            // 以接口回的 status 为准，而不是我们请求的那个：万一服务端把口径改了，
            // 照抄请求参数就会把「其实已发布」的标成待发布，前端跟着开错链接。
            status: it
                .get("status")
                .and_then(|v| v.as_str())
                .unwrap_or(status)
                .to_string(),
            collection: collection.to_string(),
        });
    }
    Ok(out)
}
