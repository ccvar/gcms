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
    pub url: String,
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
    let mut out: Vec<ScheduledItem> = Vec::new();
    // 站点通常个位数，顺序拉取即可；单站失败（无 posts:read 等）跳过不影响其它站。
    for s in &sites {
        let api_base = s.get("api_base").and_then(|v| v.as_str()).unwrap_or("");
        if api_base.is_empty() {
            continue;
        }
        let slug = s.get("slug").and_then(|v| v.as_str()).unwrap_or("").to_string();
        let name = s.get("name").and_then(|v| v.as_str()).unwrap_or(&slug).to_string();
        if let Ok(items) = fetch_site(&client, api_base, &key, &slug, &name).await {
            out.extend(items);
        }
    }
    // 按发布时间升序（最近要发的在前）。
    out.sort_by(|a, b| a.published_at.cmp(&b.published_at));
    Ok(out)
}

async fn fetch_site(
    client: &reqwest::Client,
    api_base: &str,
    key: &str,
    slug: &str,
    name: &str,
) -> Result<Vec<ScheduledItem>, String> {
    let url = format!("{}/posts?status=scheduled&lang=all&limit=200", api_base.trim_end_matches('/'));
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
    let items = body.get("items").and_then(|i| i.as_array()).cloned().unwrap_or_default();
    let mut out = Vec::new();
    for it in items {
        let published_at = it.get("published_at").and_then(|v| v.as_str()).unwrap_or("").to_string();
        out.push(ScheduledItem {
            site_slug: slug.to_string(),
            site_name: name.to_string(),
            id: it.get("id").and_then(|v| v.as_i64()).unwrap_or(0),
            title: it.get("title").and_then(|v| v.as_str()).unwrap_or("(无标题)").to_string(),
            lang: it.get("lang").and_then(|v| v.as_str()).unwrap_or("").to_string(),
            published_at,
            url: it.get("url").and_then(|v| v.as_str()).unwrap_or("").to_string(),
        });
    }
    Ok(out)
}
