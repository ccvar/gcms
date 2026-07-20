//! Cloudflare API 轻客户端：验证 API Token、拉取账号/域名、只读核验 Zone/DNS/SSL，
//! 以及在用户明确操作后幂等创建一条仅 DNS 的 A 记录；源站 HTTPS 验证通过后，
//! 可在记录仍指向已核验源站且 Zone 使用 Full / Full (strict) 时安全开启橙云。
//! token 只在验证/连接时经过 Rust，存进钥匙串后运行时以 `CLOUDFLARE_API_TOKEN` 注入 wrangler，
//! 绝不落盘、绝不进 WebView。

use serde::Serialize;
use serde_json::Value;
use std::net::{IpAddr, Ipv4Addr};
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

#[derive(Default, Clone, Debug, PartialEq)]
pub struct CfDnsRecord {
    pub id: String,
    pub record_type: String,
    pub name: String,
    pub content: String,
    pub proxied: bool,
    pub proxiable: bool,
}

#[derive(Default, Clone, Debug, PartialEq)]
pub struct CfHostnameInspect {
    pub zone_id: String,
    pub zone_name: String,
    pub zone_status: String,
    pub records: Vec<CfDnsRecord>,
    pub ssl_mode: String,
    pub ssl_error: String,
}

#[derive(Serialize, Default)]
pub struct CfVerify {
    /// 期望 "active"。
    pub token_status: String,
    pub accounts: Vec<CfAccount>,
    pub zones: Vec<CfZone>,
}

async fn cf_get_query(token: &str, path: &str, query: &[(&str, &str)]) -> Result<Value, String> {
    let mut url = reqwest::Url::parse(&format!("{API}{path}"))
        .map_err(|error| format!("Cloudflare API 地址无效: {error}"))?;
    if !query.is_empty() {
        let mut pairs = url.query_pairs_mut();
        for (key, value) in query {
            pairs.append_pair(key, value);
        }
    }
    let client = reqwest::Client::new();
    let request = client
        .get(url)
        .header("Authorization", format!("Bearer {token}"))
        .header("Content-Type", "application/json")
        .timeout(Duration::from_secs(15));
    let resp = request
        .send()
        .await
        .map_err(|e| format!("请求 Cloudflare 失败: {e}"))?;
    let status = resp.status();
    let body: Value = resp
        .json()
        .await
        .map_err(|e| format!("Cloudflare 响应不是 JSON: {e}"))?;
    if !status.is_success()
        || !body
            .get("success")
            .and_then(Value::as_bool)
            .unwrap_or(false)
    {
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

async fn cf_get(token: &str, path: &str) -> Result<Value, String> {
    cf_get_query(token, path, &[]).await
}

async fn cf_post_json(token: &str, path: &str, payload: &Value) -> Result<Value, String> {
    let url = reqwest::Url::parse(&format!("{API}{path}"))
        .map_err(|error| format!("Cloudflare API 地址无效: {error}"))?;
    let resp = reqwest::Client::new()
        .post(url)
        .header("Authorization", format!("Bearer {token}"))
        .header("Content-Type", "application/json")
        .json(payload)
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| format!("请求 Cloudflare 失败: {e}"))?;
    let status = resp.status();
    let body: Value = resp
        .json()
        .await
        .map_err(|e| format!("Cloudflare 响应不是 JSON: {e}"))?;
    if !status.is_success()
        || !body
            .get("success")
            .and_then(Value::as_bool)
            .unwrap_or(false)
    {
        let msg = body
            .get("errors")
            .and_then(Value::as_array)
            .and_then(|errors| errors.first())
            .and_then(|error| error.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("未知错误（请确认 Token 具有 Zone · DNS · Edit 权限）");
        let permission_hint = if matches!(status.as_u16(), 401 | 403) {
            "；请重新连接具有 Zone · DNS · Edit 权限的 Token"
        } else {
            ""
        };
        return Err(format!("Cloudflare {status}: {msg}{permission_hint}"));
    }
    Ok(body)
}

async fn cf_patch_json(token: &str, path: &str, payload: &Value) -> Result<Value, String> {
    let url = reqwest::Url::parse(&format!("{API}{path}"))
        .map_err(|error| format!("Cloudflare API 地址无效: {error}"))?;
    let resp = reqwest::Client::new()
        .patch(url)
        .header("Authorization", format!("Bearer {token}"))
        .header("Content-Type", "application/json")
        .json(payload)
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| format!("请求 Cloudflare 失败: {e}"))?;
    let status = resp.status();
    let body: Value = resp
        .json()
        .await
        .map_err(|e| format!("Cloudflare 响应不是 JSON: {e}"))?;
    if !status.is_success()
        || !body
            .get("success")
            .and_then(Value::as_bool)
            .unwrap_or(false)
    {
        let msg = body
            .get("errors")
            .and_then(Value::as_array)
            .and_then(|errors| errors.first())
            .and_then(|error| error.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("未知错误（请确认 Token 具有 Zone · DNS · Edit 权限）");
        let permission_hint = if matches!(status.as_u16(), 401 | 403) {
            "；请重新连接具有 Zone · DNS · Edit 权限的 Token"
        } else {
            ""
        };
        return Err(format!("Cloudflare {status}: {msg}{permission_hint}"));
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
                        x.get("id")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_string(),
                        x.get("name")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_string(),
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

/// 只读检查一个主机名在指定 Cloudflare 账号里的 Zone、DNS 记录和 SSL 模式。
/// `Ok(None)` 表示这个账号下没有该 Zone；不会创建或修改任何 Cloudflare 配置。
pub async fn inspect_hostname(
    token: &str,
    account_id: &str,
    zone_name: &str,
    hostname: &str,
) -> Result<Option<CfHostnameInspect>, String> {
    let mut zone_query = vec![("name", zone_name), ("per_page", "50")];
    if !account_id.is_empty() {
        zone_query.push(("account.id", account_id));
    }
    let zones = cf_get_query(token, "/zones", &zone_query).await?;
    let zone = zones
        .get("result")
        .and_then(Value::as_array)
        .and_then(|items| {
            items.iter().find(|item| {
                item.get("name")
                    .and_then(Value::as_str)
                    .is_some_and(|name| name.eq_ignore_ascii_case(zone_name))
            })
        });
    let Some(zone) = zone else {
        return Ok(None);
    };
    let zone_id = zone
        .get("id")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    if zone_id.is_empty() {
        return Err("Cloudflare 返回了缺少 ID 的 Zone".into());
    }
    let zone_name = zone
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or(zone_name)
        .to_string();
    let zone_status = zone
        .get("status")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();

    let records_body = cf_get_query(
        token,
        &format!("/zones/{zone_id}/dns_records"),
        &[("name", hostname), ("per_page", "100")],
    )
    .await?;
    let records = records_body
        .get("result")
        .and_then(Value::as_array)
        .map(|items| {
            items
                .iter()
                .map(|item| CfDnsRecord {
                    id: item
                        .get("id")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_string(),
                    record_type: item
                        .get("type")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_string(),
                    name: item
                        .get("name")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_string(),
                    content: item
                        .get("content")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_string(),
                    proxied: item
                        .get("proxied")
                        .and_then(Value::as_bool)
                        .unwrap_or(false),
                    proxiable: item
                        .get("proxiable")
                        .and_then(Value::as_bool)
                        .unwrap_or(false),
                })
                .collect()
        })
        .unwrap_or_default();

    // 橙云记录必须知道 SSL 模式才能安全决定是否继续；权限不足时保留 DNS 结果并单独返回原因。
    let (ssl_mode, ssl_error) = match cf_get(token, &format!("/zones/{zone_id}/settings/ssl")).await
    {
        Ok(body) => (
            body.get("result")
                .and_then(|result| result.get("value"))
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_string(),
            String::new(),
        ),
        Err(error) => (String::new(), error),
    };

    Ok(Some(CfHostnameInspect {
        zone_id,
        zone_name,
        zone_status,
        records,
        ssl_mode,
        ssl_error,
    }))
}

fn should_create_dns_only_a(
    records: &[CfDnsRecord],
    hostname: &str,
    address: Ipv4Addr,
) -> Result<bool, String> {
    let relevant = records
        .iter()
        .filter(|record| {
            record
                .name
                .trim_end_matches('.')
                .eq_ignore_ascii_case(hostname)
                && matches!(record.record_type.as_str(), "A" | "AAAA" | "CNAME")
        })
        .collect::<Vec<_>>();
    if relevant.is_empty() {
        return Ok(true);
    }
    if relevant.len() == 1
        && relevant[0].record_type == "A"
        && relevant[0]
            .content
            .parse::<Ipv4Addr>()
            .is_ok_and(|current| current == address)
    {
        return Ok(false);
    }
    let summary = relevant
        .iter()
        .map(|record| format!("{} {}", record.record_type, record.content))
        .collect::<Vec<_>>()
        .join("、");
    Err(format!(
        "{hostname} 已存在 DNS 记录（{summary}）。为避免覆盖或制造双源站，Pilot 未修改 Cloudflare"
    ))
}

/// 用户点击“一键创建 A 记录”后调用。创建前会重新读取精确主机名，已有同值 A 时幂等成功，
/// 发现其他 A / AAAA / CNAME 时拒绝覆盖。返回 true 表示本次创建，false 表示同值记录已存在。
pub async fn create_dns_only_a_record(
    token: &str,
    account_id: &str,
    zone_name: &str,
    hostname: &str,
    address: Ipv4Addr,
) -> Result<bool, String> {
    let inspect = inspect_hostname(token, account_id, zone_name, hostname)
        .await?
        .ok_or_else(|| format!("Cloudflare 账号中没有找到 Zone {zone_name}"))?;
    if inspect.zone_status != "active" {
        return Err(format!(
            "Cloudflare Zone 当前状态为 {}，激活后才能创建公网记录",
            if inspect.zone_status.is_empty() {
                "未知"
            } else {
                &inspect.zone_status
            }
        ));
    }
    if !should_create_dns_only_a(&inspect.records, hostname, address)? {
        return Ok(false);
    }

    let body = cf_post_json(
        token,
        &format!("/zones/{}/dns_records", inspect.zone_id),
        &serde_json::json!({
            "type": "A",
            "name": hostname,
            "content": address.to_string(),
            "ttl": 1,
            "proxied": false
        }),
    )
    .await?;
    let created = body
        .get("result")
        .ok_or("Cloudflare 未返回已创建的 DNS 记录")?;
    let created_type = created
        .get("type")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let created_name = created
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let created_content = created
        .get("content")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if created_type != "A"
        || !created_name
            .trim_end_matches('.')
            .eq_ignore_ascii_case(hostname)
        || created_content != address.to_string()
    {
        return Err("Cloudflare 返回的已创建记录与请求不一致，请到控制台核对".into());
    }
    Ok(true)
}

fn safe_proxy_records<'a>(
    records: &'a [CfDnsRecord],
    hostname: &str,
    expected_addresses: &[IpAddr],
    allowed_cname_target: Option<&str>,
) -> Result<Vec<&'a CfDnsRecord>, String> {
    let relevant = records
        .iter()
        .filter(|record| {
            record
                .name
                .trim_end_matches('.')
                .eq_ignore_ascii_case(hostname)
                && matches!(record.record_type.as_str(), "A" | "AAAA" | "CNAME")
        })
        .collect::<Vec<_>>();
    if relevant.is_empty() {
        return Err(format!("{hostname} 没有可开启橙云的 A、AAAA 或 CNAME 记录"));
    }
    for record in &relevant {
        let target_matches = match record.record_type.as_str() {
            "A" | "AAAA" => record
                .content
                .parse::<IpAddr>()
                .is_ok_and(|address| expected_addresses.contains(&address)),
            "CNAME" => allowed_cname_target.is_some_and(|target| {
                record
                    .content
                    .trim_end_matches('.')
                    .eq_ignore_ascii_case(target)
            }),
            _ => false,
        };
        if !target_matches {
            return Err(format!(
                "{hostname} 的 {} 记录仍指向 {}，与已核验源站不一致，未开启橙云",
                record.record_type, record.content
            ));
        }
        if !record.proxiable {
            return Err(format!(
                "Cloudflare 标记 {hostname} 的 {} 记录不可代理",
                record.record_type
            ));
        }
        if record.id.is_empty() {
            return Err(format!(
                "Cloudflare 未返回 {hostname} 的记录 ID，无法安全开启橙云"
            ));
        }
    }
    Ok(relevant)
}

/// 在 GCMS 源站 HTTPS 已通过后开启精确主机名的橙云代理。
///
/// 更新前、更新后都会重新读取记录；只接受仍指向已核验服务器的 A / AAAA，或跳转域名
/// 指向主域名的 CNAME。不会修改记录内容、TTL 或 Zone 级 SSL 模式。
pub async fn enable_proxy_for_hostname(
    token: &str,
    account_id: &str,
    zone_name: &str,
    hostname: &str,
    expected_addresses: &[IpAddr],
    allowed_cname_target: Option<&str>,
) -> Result<bool, String> {
    let inspect = inspect_hostname(token, account_id, zone_name, hostname)
        .await?
        .ok_or_else(|| format!("Cloudflare 账号中没有找到 Zone {zone_name}"))?;
    if inspect.zone_status != "active" {
        return Err(format!(
            "Cloudflare Zone 当前状态为 {}，暂不能开启橙云",
            if inspect.zone_status.is_empty() {
                "未知"
            } else {
                &inspect.zone_status
            }
        ));
    }
    if !inspect.ssl_error.is_empty() {
        return Err(format!(
            "无法读取 Cloudflare SSL/TLS 模式：{}",
            inspect.ssl_error
        ));
    }
    if !matches!(inspect.ssl_mode.as_str(), "full" | "strict") {
        return Err(format!(
            "Cloudflare SSL/TLS 当前为 {}；请先改为 Full 或 Full (strict)，Pilot 未自动修改 Zone 级设置",
            if inspect.ssl_mode.is_empty() {
                "未知"
            } else {
                &inspect.ssl_mode
            }
        ));
    }
    let records = safe_proxy_records(
        &inspect.records,
        hostname,
        expected_addresses,
        allowed_cname_target,
    )?;
    let mut changed = false;
    for record in records.into_iter().filter(|record| !record.proxied) {
        let body = cf_patch_json(
            token,
            &format!("/zones/{}/dns_records/{}", inspect.zone_id, record.id),
            &serde_json::json!({ "proxied": true }),
        )
        .await?;
        let updated = body
            .get("result")
            .ok_or("Cloudflare 未返回已更新的 DNS 记录")?;
        if !updated
            .get("proxied")
            .and_then(Value::as_bool)
            .unwrap_or(false)
        {
            return Err(format!("Cloudflare 未确认 {hostname} 的橙云状态"));
        }
        changed = true;
    }

    let confirmed = inspect_hostname(token, account_id, zone_name, hostname)
        .await?
        .ok_or_else(|| format!("开启橙云后无法重新读取 Zone {zone_name}"))?;
    let records = safe_proxy_records(
        &confirmed.records,
        hostname,
        expected_addresses,
        allowed_cname_target,
    )?;
    if records.iter().any(|record| !record.proxied) {
        return Err(format!("{hostname} 仍有记录未开启橙云，请稍后重试"));
    }
    Ok(changed)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_id_name_lists_without_panicking_on_partial_items() {
        let value = serde_json::json!({
            "result": [
                { "id": "one", "name": "Example" },
                { "id": "two" }
            ]
        });
        assert_eq!(
            parse_id_name(&value),
            vec![
                ("one".into(), "Example".into()),
                ("two".into(), String::new())
            ]
        );
    }

    fn record(record_type: &str, name: &str, content: &str, proxied: bool) -> CfDnsRecord {
        CfDnsRecord {
            id: format!("{record_type}-{name}"),
            record_type: record_type.into(),
            name: name.into(),
            content: content.into(),
            proxied,
            proxiable: matches!(record_type, "A" | "AAAA" | "CNAME"),
        }
    }

    #[test]
    fn dns_only_a_creation_is_idempotent_and_never_overwrites() {
        let ip = "203.0.113.8".parse::<Ipv4Addr>().unwrap();
        assert!(should_create_dns_only_a(&[], "cms.example.com", ip).unwrap());
        assert!(!should_create_dns_only_a(
            &[record("A", "cms.example.com", "203.0.113.8", true)],
            "cms.example.com",
            ip,
        )
        .unwrap());
        assert!(should_create_dns_only_a(
            &[record("TXT", "cms.example.com", "verification", false)],
            "cms.example.com",
            ip,
        )
        .unwrap());
        assert!(should_create_dns_only_a(
            &[record("A", "cms.example.com", "203.0.113.9", false)],
            "cms.example.com",
            ip,
        )
        .is_err());
        assert!(should_create_dns_only_a(
            &[record("AAAA", "cms.example.com", "2001:db8::8", false)],
            "cms.example.com",
            ip,
        )
        .is_err());
        assert!(should_create_dns_only_a(
            &[record(
                "CNAME",
                "cms.example.com",
                "other.example.com",
                false,
            )],
            "cms.example.com",
            ip,
        )
        .is_err());
    }

    #[test]
    fn orange_cloud_only_accepts_the_verified_origin() {
        let expected = vec!["203.0.113.8".parse::<IpAddr>().unwrap()];
        let matching = record("A", "cms.example.com", "203.0.113.8", false);
        assert_eq!(
            safe_proxy_records(&[matching], "cms.example.com", &expected, None)
                .unwrap()
                .len(),
            1
        );
        let stale = record("A", "cms.example.com", "203.0.113.9", false);
        assert!(safe_proxy_records(&[stale], "cms.example.com", &expected, None).is_err());
        let alias = record("CNAME", "www.example.com", "cms.example.com", false);
        assert!(safe_proxy_records(
            &[alias],
            "www.example.com",
            &expected,
            Some("cms.example.com")
        )
        .is_ok());
    }
}
