//! Cloudflare API 轻客户端：验证 API Token + 拉取账号/域名列表。
//! token 只在验证/连接时经过 Rust，存进钥匙串后运行时以 `CLOUDFLARE_API_TOKEN` 注入 wrangler，
//! 绝不落盘、绝不进 WebView。

use serde::Serialize;
use serde_json::Value;
use std::time::Duration;

const API: &str = "https://api.cloudflare.com/client/v4";

#[derive(Serialize, Default, Clone)]
pub struct CfAccount {
    pub id: String,
    pub name: String,
}

#[derive(Serialize, Default, Clone)]
pub struct CfZone {
    pub id: String,
    pub name: String,
}

#[derive(Serialize, Default)]
pub struct CfVerify {
    /// 期望 "active"。
    pub token_status: String,
    pub accounts: Vec<CfAccount>,
    pub zones: Vec<CfZone>,
}

async fn cf_get(token: &str, path: &str) -> Result<Value, String> {
    let resp = reqwest::Client::new()
        .get(format!("{API}{path}"))
        .header("Authorization", format!("Bearer {token}"))
        .header("Content-Type", "application/json")
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| format!("请求 Cloudflare 失败: {e}"))?;
    let status = resp.status();
    let body: Value = resp
        .json()
        .await
        .map_err(|e| format!("Cloudflare 响应不是 JSON: {e}"))?;
    if !status.is_success() || !body.get("success").and_then(Value::as_bool).unwrap_or(false) {
        let msg = body
            .get("errors")
            .and_then(|e| e.as_array())
            .and_then(|a| a.first())
            .and_then(|e| e.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("未知错误（请确认 Token 权限）");
        return Err(format!("Cloudflare {status}: {msg}"));
    }
    Ok(body)
}

fn parse_id_name(body: &Value) -> Vec<(String, String)> {
    body.get("result")
        .and_then(Value::as_array)
        .map(|arr| {
            arr.iter()
                .map(|x| {
                    (
                        x.get("id").and_then(Value::as_str).unwrap_or_default().to_string(),
                        x.get("name").and_then(Value::as_str).unwrap_or_default().to_string(),
                    )
                })
                .collect()
        })
        .unwrap_or_default()
}

/// 验证 token 并拉取可用账号 + 域名（zone）。域名拿不到（无 Zone 权限）时静默留空。
pub async fn verify_token(token: &str) -> Result<CfVerify, String> {
    let token = token.trim();
    if token.is_empty() {
        return Err("请粘贴 Cloudflare API Token".into());
    }
    // 1) 验证 token 本身
    let v = cf_get(token, "/user/tokens/verify").await?;
    let token_status = v
        .get("result")
        .and_then(|r| r.get("status"))
        .and_then(Value::as_str)
        .unwrap_or("")
        .to_string();
    if token_status != "active" {
        return Err(format!("Token 状态异常：{token_status}（需要 active）"));
    }
    // 2) 账号（部署 Pages/Workers/D1 都要账号）
    let accounts = parse_id_name(&cf_get(token, "/accounts?per_page=50").await?)
        .into_iter()
        .map(|(id, name)| CfAccount { id, name })
        .collect();
    // 3) 域名（可选权限，失败/空都容忍——先连上，绑定自定义域名是后面的事）
    let zones = match cf_get(token, "/zones?per_page=50").await {
        Ok(z) => parse_id_name(&z)
            .into_iter()
            .map(|(id, name)| CfZone { id, name })
            .collect(),
        Err(_) => Vec::new(),
    };
    Ok(CfVerify {
        token_status,
        accounts,
        zones,
    })
}
