//! 冻结的发现契约：GET {api_base}/sites →
//! {"items":[{id,slug,name,capabilities,api_base}],"all_sites":bool}
//! 单站包（gcms_）没有 /sites，返回一个用 .env base 合成的单站列表。

use reqwest::Method;
use serde_json::{json, Value};

use crate::keychain;
use crate::pack::Connection;

pub async fn discover(conn: &Connection) -> Result<Value, String> {
    discover_with_refresh(conn, false).await
}

pub async fn discover_with_refresh(
    conn: &Connection,
    refresh_stats: bool,
) -> Result<Value, String> {
    let key = keychain::get_key(&conn.id)?;
    if conn.key_kind == "gcms_" {
        // 单站连接：没有发现端点，合成一个条目保证 UI 统一。
        return Ok(json!({
            "items": [{
                "id": 0,
                "slug": "default",
                "name": conn.name,
                "capabilities": Value::Null,
                "api_base": conn.api_base,
            }],
            "all_sites": false,
            "synthetic": true,
        }));
    }
    let url = if refresh_stats {
        format!("{}/sites?refresh_stats=1", conn.api_base)
    } else {
        format!("{}/sites", conn.api_base)
    };
    let resp = reqwest::Client::new()
        .get(&url)
        .header("Authorization", format!("Bearer {key}"))
        // 主动刷新会并发读取多个站点的 Google 摘要，允许比普通发现更长的窗口。
        .timeout(std::time::Duration::from_secs(if refresh_stats {
            90
        } else {
            15
        }))
        .send()
        .await
        .map_err(|e| format!("请求发现接口失败: {e}"))?;
    let status = resp.status();
    let body: Value = resp
        .json()
        .await
        .map_err(|e| format!("发现接口响应不是 JSON: {e}"))?;
    if !status.is_success() {
        let msg = body
            .get("error")
            .and_then(|e| e.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("未知错误");
        return Err(format!("发现接口 {status}: {msg}"));
    }
    Ok(body)
}

fn integration_url(conn: &Connection, site_id: Option<i64>) -> Result<String, String> {
    if conn.key_kind == "gcms_" {
        return Err(
            "当前是单站技能包，暂不支持平台级接入配置；请导入 GCMS 平台助手技能包。".into(),
        );
    }
    Ok(match site_id {
        Some(id) if id > 0 => format!("{}/control/sites/{id}/integrations", conn.api_base),
        Some(_) => return Err("站点 ID 无效。".into()),
        None => format!("{}/control/integrations", conn.api_base),
    })
}

fn google_provision_url(conn: &Connection, site_id: i64, service: &str) -> Result<String, String> {
    if conn.key_kind == "gcms_" {
        return Err(
            "当前是单站技能包，暂不支持平台级 Google 接入；请导入 GCMS 平台助手技能包。".into(),
        );
    }
    if site_id <= 0 {
        return Err("站点 ID 无效。".into());
    }
    let service_path = match service {
        "analytics" => "analytics",
        "search_console" => "search-console",
        _ => return Err("未知的 Google 接入类型。".into()),
    };
    Ok(format!(
        "{}/control/sites/{site_id}/integrations/{service_path}/enable",
        conn.api_base
    ))
}

fn deployment_url(conn: &Connection, site_id: i64) -> Result<String, String> {
    if conn.key_kind == "gcms_" {
        return Err(
            "当前是单站技能包，暂不支持平台站点部署管理；请导入 GCMS 平台助手技能包。".into(),
        );
    }
    if site_id <= 0 {
        return Err("站点 ID 无效。".into());
    }
    Ok(format!(
        "{}/control/sites/{site_id}/deployment",
        conn.api_base
    ))
}

fn themes_url(conn: &Connection) -> Result<String, String> {
    if conn.key_kind == "gcms_" {
        return Err("当前是单站技能包，暂不支持平台主题管理；请导入 GCMS 平台助手技能包。".into());
    }
    Ok(format!("{}/control/themes", conn.api_base))
}

fn site_theme_url(conn: &Connection, site_id: i64) -> Result<String, String> {
    if conn.key_kind == "gcms_" {
        return Err(
            "当前是单站技能包，暂不支持平台站点主题管理；请导入 GCMS 平台助手技能包。".into(),
        );
    }
    if site_id <= 0 {
        return Err("站点 ID 无效。".into());
    }
    Ok(format!("{}/control/sites/{site_id}/theme", conn.api_base))
}

fn normalized_theme_id(value: &str) -> Result<String, String> {
    let normalized = value.trim();
    if normalized.is_empty()
        || normalized.len() > 128
        || !normalized
            .chars()
            .all(|ch| ch.is_ascii_alphanumeric() || matches!(ch, '-' | '_' | '.'))
    {
        return Err("主题 ID 无效，请重新选择主题。".into());
    }
    Ok(normalized.to_string())
}

fn valid_request_id(value: &str) -> bool {
    (8..=128).contains(&value.len())
        && value
            .chars()
            .all(|ch| ch.is_ascii_alphanumeric() || matches!(ch, '-' | '_' | '.' | ':'))
}

fn response_error(status: reqwest::StatusCode, body: &Value) -> String {
    let message = response_message(body);
    format!("GCMS 接入配置接口 {status}: {message}")
}

fn response_message(body: &Value) -> &str {
    body.get("message")
        .and_then(Value::as_str)
        .or_else(|| {
            body.get("error")
                .and_then(|value| value.get("message"))
                .and_then(Value::as_str)
        })
        .or_else(|| body.get("error").and_then(Value::as_str))
        .unwrap_or("未知错误")
}

async fn theme_response(response: reqwest::Response, operation: &str) -> Result<Value, String> {
    let status = response.status();
    let body: Value = response
        .json()
        .await
        .map_err(|e| format!("GCMS {operation}响应不是 JSON: {e}"))?;
    if !status.is_success() {
        return Err(format!(
            "GCMS {operation}接口 {status}: {}",
            response_message(&body)
        ));
    }
    Ok(body)
}

fn theme_mutation_payload(
    theme_id: Option<&str>,
    rollback: bool,
    expected_current_theme: Option<&str>,
) -> Result<Value, String> {
    let mut payload = json!({
        "action": if rollback { "rollback" } else { "apply" },
        "rollback": rollback,
    });
    if rollback {
        if let Some(value) = theme_id.map(str::trim).filter(|value| !value.is_empty()) {
            payload["theme_id"] = Value::String(normalized_theme_id(value)?);
        }
    } else {
        let target = theme_id.ok_or_else(|| "请选择要应用的主题。".to_string())?;
        payload["theme_id"] = Value::String(normalized_theme_id(target)?);
    }
    if let Some(expected) = expected_current_theme
        .map(str::trim)
        .filter(|value| !value.is_empty())
    {
        payload["expected_current_theme"] = Value::String(normalized_theme_id(expected)?);
    }
    Ok(payload)
}

async fn integration_request(
    conn: &Connection,
    method: Method,
    site_id: Option<i64>,
    payload: Option<&Value>,
) -> Result<Value, String> {
    let key = keychain::get_key(&conn.id)?;
    let url = integration_url(conn, site_id)?;
    let client = reqwest::Client::new();
    let timeout = if method == Method::PUT { 45 } else { 20 };
    let mut request = client
        .request(method, &url)
        .header("Authorization", format!("Bearer {key}"))
        // Cloudflare 授权保存会在 GCMS 内验证 Token、账号和 Zone，服务端自身
        // 最长允许 30 秒；PUT 留足网络与序列化余量，GET 仍保持快速失败。
        .timeout(std::time::Duration::from_secs(timeout));
    if let Some(value) = payload {
        request = request.json(value);
    }
    let response = request
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 接入配置失败: {e}"))?;
    let status = response.status();
    let body: Value = response
        .json()
        .await
        .map_err(|e| format!("GCMS 接入配置响应不是 JSON: {e}"))?;
    if !status.is_success() {
        return Err(response_error(status, &body));
    }
    Ok(body)
}

pub async fn integrations(conn: &Connection, site_id: Option<i64>) -> Result<Value, String> {
    integration_request(conn, Method::GET, site_id, None).await
}

pub async fn save_integrations(
    conn: &Connection,
    site_id: Option<i64>,
    payload: &Value,
) -> Result<Value, String> {
    integration_request(conn, Method::PUT, site_id, Some(payload)).await
}

pub async fn provision_site_google(
    conn: &Connection,
    site_id: i64,
    service: &str,
    payload: &Value,
) -> Result<Value, String> {
    let key = keychain::get_key(&conn.id)?;
    let url = google_provision_url(conn, site_id, service)?;
    let response = reqwest::Client::new()
        .post(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("Accept", "application/json")
        .json(payload)
        // GA may create a property and retry until Google exposes it.
        .timeout(std::time::Duration::from_secs(90))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS Google 接入失败: {e}"))?;
    let status = response.status();
    let body: Value = response
        .json()
        .await
        .map_err(|e| format!("GCMS Google 接入响应不是 JSON: {e}"))?;
    if !status.is_success() {
        return Err(response_error(status, &body));
    }
    Ok(body)
}

pub async fn site_google_analytics_options(
    conn: &Connection,
    site_id: i64,
    account_id: &str,
) -> Result<Value, String> {
    let key = keychain::get_key(&conn.id)?;
    let base = google_provision_url(conn, site_id, "analytics")?;
    let mut url = reqwest::Url::parse(base.trim_end_matches("/enable"))
        .map_err(|e| format!("GCMS Analytics 属性接口地址无效: {e}"))?;
    url.path_segments_mut()
        .map_err(|_| "GCMS Analytics 属性接口地址无效。".to_string())?
        .push("options");
    url.query_pairs_mut()
        .append_pair("account", account_id.trim());
    let response = reqwest::Client::new()
        .get(url)
        .header("Authorization", format!("Bearer {key}"))
        .header("Accept", "application/json")
        .timeout(std::time::Duration::from_secs(45))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS Analytics 属性失败: {e}"))?;
    let status = response.status();
    let body: Value = response
        .json()
        .await
        .map_err(|e| format!("GCMS Analytics 属性响应不是 JSON: {e}"))?;
    if !status.is_success() {
        return Err(response_error(status, &body));
    }
    Ok(body)
}

pub async fn site_deployment(conn: &Connection, site_id: i64) -> Result<Value, String> {
    let key = keychain::get_key(&conn.id)?;
    let url = deployment_url(conn, site_id)?;
    let response = reqwest::Client::new()
        .get(&url)
        .header("Authorization", format!("Bearer {key}"))
        .timeout(std::time::Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 站点部署接口失败: {e}"))?;
    let status = response.status();
    let body: Value = response
        .json()
        .await
        .map_err(|e| format!("GCMS 站点部署响应不是 JSON: {e}"))?;
    if !status.is_success() {
        return Err(format!(
            "GCMS 站点部署接口 {status}: {}",
            response_message(&body)
        ));
    }
    Ok(body)
}

pub async fn themes(conn: &Connection) -> Result<Value, String> {
    let url = themes_url(conn)?;
    let key = keychain::get_key(&conn.id)?;
    let response = reqwest::Client::new()
        .get(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("Accept", "application/json")
        .timeout(std::time::Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 主题目录失败: {e}"))?;
    theme_response(response, "主题目录").await
}

pub async fn site_theme(conn: &Connection, site_id: i64) -> Result<Value, String> {
    let url = site_theme_url(conn, site_id)?;
    let key = keychain::get_key(&conn.id)?;
    let response = reqwest::Client::new()
        .get(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("Accept", "application/json")
        .timeout(std::time::Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 站点主题失败: {e}"))?;
    theme_response(response, "站点主题").await
}

/// 申请指定候选主题的短时预览地址。GCMS 会把主题 ID 绑定进签名票据，
/// Pilot 只接收可打开的短时 URL，不读取后台登录态。
pub async fn site_theme_preview_url(
    conn: &Connection,
    site_id: i64,
    theme_id: &str,
) -> Result<Value, String> {
    if site_id <= 0 {
        return Err("站点 ID 无效。".into());
    }
    let target = normalized_theme_id(theme_id)?;
    // 同时完成单站技能包拦截；候选主题预览仍复用 GCMS 的站点预览签发入口。
    let _ = site_theme_url(conn, site_id)?;
    let key = keychain::get_key(&conn.id)?;
    let url = format!("{}/control/sites/{site_id}/preview-url", conn.api_base);
    let response = reqwest::Client::new()
        .post(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("Accept", "application/json")
        .json(&json!({"theme_id": target}))
        .timeout(std::time::Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 候选主题预览失败: {e}"))?;
    theme_response(response, "候选主题预览").await
}

pub async fn plan_site_theme(
    conn: &Connection,
    site_id: i64,
    theme_id: Option<&str>,
    rollback: bool,
    expected_current_theme: Option<&str>,
) -> Result<Value, String> {
    let url = format!("{}?dry_run=1", site_theme_url(conn, site_id)?);
    let payload = theme_mutation_payload(theme_id, rollback, expected_current_theme)?;
    let key = keychain::get_key(&conn.id)?;
    let response = reqwest::Client::new()
        .put(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("Accept", "application/json")
        .json(&payload)
        .timeout(std::time::Duration::from_secs(30))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 主题变更预检查失败: {e}"))?;
    theme_response(response, "主题变更预检查").await
}

pub async fn apply_site_theme(
    conn: &Connection,
    site_id: i64,
    theme_id: Option<&str>,
    rollback: bool,
    request_id: &str,
    expected_current_theme: Option<&str>,
    unlock_token: Option<&str>,
) -> Result<Value, String> {
    let idempotency_key = request_id.trim();
    if !valid_request_id(idempotency_key) {
        return Err("主题变更请求标识无效，请重试。".into());
    }
    let url = site_theme_url(conn, site_id)?;
    let payload = theme_mutation_payload(theme_id, rollback, expected_current_theme)?;
    let key = keychain::get_key(&conn.id)?;
    let mut request = reqwest::Client::new()
        .put(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("Accept", "application/json")
        .header("X-GCMS-Control-Confirm", "themes.apply")
        .header("Idempotency-Key", idempotency_key);
    if let Some(token) = unlock_token.filter(|token| !token.trim().is_empty()) {
        request = request.header("X-GCMS-Control-Unlock", token);
    }
    let response = request
        .json(&payload)
        .timeout(std::time::Duration::from_secs(45))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 应用站点主题失败: {e}"))?;
    theme_response(response, "应用站点主题").await
}

/// 向 GCMS 平台申请短时整站预览地址。这里仅使用已经导入 Pilot 的平台 API
/// 密钥，不读取或保存 GCMS 后台密码，也不依赖 SSH 连接。
pub async fn site_preview_url(conn: &Connection, site_id: i64) -> Result<Value, String> {
    if conn.key_kind == "gcms_" {
        return Err("当前是单站技能包，不能创建平台私有预览；请导入 GCMS 平台助手技能包。".into());
    }
    if site_id <= 0 {
        return Err("站点 ID 无效。".into());
    }
    let key = keychain::get_key(&conn.id)?;
    let url = format!("{}/control/sites/{site_id}/preview-url", conn.api_base);
    let response = reqwest::Client::new()
        .post(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("Accept", "application/json")
        .timeout(std::time::Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 私有预览接口失败: {e}"))?;
    let status = response.status();
    let body: Value = response
        .json()
        .await
        .map_err(|e| format!("GCMS 私有预览响应不是 JSON: {e}"))?;
    if !status.is_success() {
        return Err(format!(
            "GCMS 私有预览接口 {status}: {}",
            response_message(&body)
        ));
    }
    Ok(body)
}

pub async fn save_site_deployment(
    conn: &Connection,
    site_id: i64,
    payload: &Value,
    request_id: &str,
) -> Result<Value, String> {
    let idempotency_key = request_id.trim();
    if !valid_request_id(idempotency_key) {
        return Err("站点部署请求标识无效，请重试。".into());
    }
    let key = keychain::get_key(&conn.id)?;
    let url = deployment_url(conn, site_id)?;
    let action = payload
        .get("action")
        .and_then(Value::as_str)
        .unwrap_or("save")
        .trim()
        .to_ascii_lowercase();
    let timeout = if matches!(action.as_str(), "deploy" | "unpublish" | "purge") {
        90
    } else {
        30
    };
    let response = reqwest::Client::new()
        .put(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("X-GCMS-Control-Confirm", "deployment.apply")
        .header("Idempotency-Key", idempotency_key)
        .json(payload)
        .timeout(std::time::Duration::from_secs(timeout))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 站点部署接口失败: {e}"))?;
    let status = response.status();
    let body: Value = response
        .json()
        .await
        .map_err(|e| format!("GCMS 站点部署响应不是 JSON: {e}"))?;
    if !status.is_success() {
        return Err(format!(
            "GCMS 站点部署接口 {status}: {}",
            response_message(&body)
        ));
    }
    Ok(body)
}

pub async fn set_site_status(
    conn: &Connection,
    site_id: i64,
    status: &str,
    request_id: &str,
) -> Result<Value, String> {
    if conn.key_kind == "gcms_" {
        return Err("当前是单站技能包，不能切换平台站点状态；请导入 GCMS 平台助手技能包。".into());
    }
    if site_id <= 0 {
        return Err("站点 ID 无效。".into());
    }
    let normalized = status.trim().to_ascii_lowercase();
    if normalized != "enabled" && normalized != "disabled" {
        return Err("站点状态只能是 enabled 或 disabled。".into());
    }
    let idempotency_key = request_id.trim();
    if !valid_request_id(idempotency_key) {
        return Err("站点状态请求标识无效，请重试。".into());
    }

    let key = keychain::get_key(&conn.id)?;
    let url = format!("{}/control/sites/{site_id}", conn.api_base);
    let response = reqwest::Client::new()
        .patch(&url)
        .header("Authorization", format!("Bearer {key}"))
        .header("X-GCMS-Control-Confirm", "sites.update")
        .header("Idempotency-Key", idempotency_key)
        .json(&json!({"status": normalized}))
        .timeout(std::time::Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| format!("请求 GCMS 站点状态接口失败: {e}"))?;
    let response_status = response.status();
    let body: Value = response
        .json()
        .await
        .map_err(|e| format!("GCMS 站点状态响应不是 JSON: {e}"))?;
    if !response_status.is_success() {
        return Err(format!(
            "GCMS 站点状态接口 {response_status}: {}",
            response_message(&body)
        ));
    }
    Ok(body)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn response_message_reads_nested_platform_error() {
        let body =
            json!({"error": {"code": "default_site_protected", "message": "默认站点不能关闭。"}});
        assert_eq!(response_message(&body), "默认站点不能关闭。");
    }

    #[test]
    fn theme_mutation_payload_normalizes_apply_and_optimistic_guard() {
        let payload =
            theme_mutation_payload(Some(" magazine "), false, Some(" editorial ")).unwrap();
        assert_eq!(payload["action"], "apply");
        assert_eq!(payload["rollback"], false);
        assert_eq!(payload["theme_id"], "magazine");
        assert_eq!(payload["expected_current_theme"], "editorial");
    }

    #[test]
    fn theme_mutation_payload_allows_rollback_without_target() {
        let payload = theme_mutation_payload(None, true, None).unwrap();
        assert_eq!(payload["action"], "rollback");
        assert_eq!(payload["rollback"], true);
        assert!(payload.get("theme_id").is_none());
    }

    #[test]
    fn theme_identifiers_reject_empty_or_unsafe_values() {
        assert!(normalized_theme_id("").is_err());
        assert!(normalized_theme_id("../editorial").is_err());
        assert!(normalized_theme_id("field-ledger").is_ok());
        assert!(theme_mutation_payload(None, false, None).is_err());
    }
}
