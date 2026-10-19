//! Read-only organization reports from explicitly allowlisted official APIs.
//! Billing credentials never enter inference requests, profile JSON or IPC views.
//!
//! Schemas: https://developers.openai.com/cookbook/examples/completions_usage_api
//! https://platform.claude.com/docs/en/manage-claude/usage-cost-api

use std::collections::HashSet;
use std::path::Path;
use std::time::Duration;

use chrono::{DateTime, Datelike, SecondsFormat, TimeZone, Utc};
use reqwest::{header, Client, Url};
use serde::Serialize;
use serde_json::Value;
use zeroize::Zeroizing;

use crate::api_profiles::{self, ApiProfile, NativeSecrets, SecretStore};

const MAX_PAGES: usize = 16;
const MAX_PAGE_BYTES: usize = 2 * 1024 * 1024;
const MAX_REPORT_BYTES: usize = 8 * 1024 * 1024;
const INVALID_REPORT: &str = "服务商报表格式不完整或无效，未显示不完整统计";

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum BillingProvider {
    Openai,
    Anthropic,
    Unsupported,
}

#[derive(Debug, Serialize)]
pub struct BillingSettings {
    pub provider: BillingProvider,
    pub credential_configured: bool,
    pub billing_url: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct BillingSnapshot {
    pub fetched_at: String,
    pub period_start: String,
    pub period_end: String,
    pub currency: &'static str,
    pub month_cost: Option<f64>,
    pub today_cost: Option<f64>,
    pub month_input_tokens: Option<u64>,
    pub month_output_tokens: Option<u64>,
    pub cost_error: Option<String>,
    pub usage_error: Option<String>,
}

fn provider(profile: &ApiProfile) -> BillingProvider {
    let Ok(url) = Url::parse(&profile.base_url) else {
        return BillingProvider::Unsupported;
    };
    if url.scheme() != "https"
        || url.port_or_known_default() != Some(443)
        || url.path().trim_end_matches('/') != "/v1"
        || !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return BillingProvider::Unsupported;
    }
    match (url.host_str(), profile.protocol.as_str()) {
        (Some("api.openai.com"), "openai_responses" | "openai_chat") => BillingProvider::Openai,
        (Some("api.anthropic.com"), "anthropic_messages") => BillingProvider::Anthropic,
        _ => BillingProvider::Unsupported,
    }
}

fn settings_view(provider: BillingProvider, configured: bool) -> BillingSettings {
    let billing_url = match provider {
        BillingProvider::Openai => {
            Some("https://platform.openai.com/settings/organization/billing/overview")
        }
        BillingProvider::Anthropic => Some("https://platform.claude.com/settings/billing"),
        BillingProvider::Unsupported => None,
    };
    BillingSettings {
        provider,
        credential_configured: configured,
        billing_url: billing_url.map(str::to_string),
    }
}

fn settings_with(
    data_dir: &Path,
    id: &str,
    secrets: &impl SecretStore,
) -> Result<BillingSettings, String> {
    let (_lock, profile) = api_profiles::locked_profile(data_dir, id)?;
    let provider = provider(&profile);
    if provider == BillingProvider::Unsupported {
        return Ok(settings_view(provider, false));
    }
    let key = Zeroizing::new(secrets.get(&api_profiles::billing_secret_id(&profile.id))?);
    Ok(settings_view(
        provider,
        key.as_ref().is_some_and(|key| !key.is_empty()),
    ))
}

pub fn settings(data_dir: &Path, id: &str) -> Result<BillingSettings, String> {
    settings_with(data_dir, id, &NativeSecrets)
}

fn save_key_with(
    data_dir: &Path,
    id: &str,
    api_key: String,
    secrets: &impl SecretStore,
) -> Result<BillingSettings, String> {
    let key = Zeroizing::new(api_key);
    let key = key.trim();
    if key.is_empty() || key.len() > 8192 || !key.bytes().all(|byte| (b'!'..=b'~').contains(&byte))
    {
        return Err("请输入有效的账单查询 Key，不得包含空白或控制字符".into());
    }
    let (_lock, profile) = api_profiles::locked_profile(data_dir, id)?;
    let provider = provider(&profile);
    if provider == BillingProvider::Unsupported {
        return Err("此接口暂不支持账单查询；不会保存或转发账单查询 Key".into());
    }
    let secret_id = api_profiles::billing_secret_id(&profile.id);
    let previous = Zeroizing::new(secrets.get(&secret_id)?);
    if secrets.set(&secret_id, key).is_err() {
        let rollback = match previous.as_deref() {
            Some(value) => secrets.set(&secret_id, value),
            None => secrets.delete(&secret_id),
        };
        return Err(if rollback.is_err() {
            "账单查询 Key 保存失败且恢复未完成，请重新配置".into()
        } else {
            "账单查询 Key 未能保存到系统凭据存储".into()
        });
    }
    Ok(settings_view(provider, true))
}

pub fn save_key(data_dir: &Path, id: &str, api_key: String) -> Result<BillingSettings, String> {
    save_key_with(data_dir, id, api_key, &NativeSecrets)
}

fn delete_key_with(
    data_dir: &Path,
    id: &str,
    secrets: &impl SecretStore,
) -> Result<BillingSettings, String> {
    let (_lock, profile) = api_profiles::locked_profile(data_dir, id)?;
    let secret_id = api_profiles::billing_secret_id(&profile.id);
    let previous = Zeroizing::new(secrets.get(&secret_id)?);
    if secrets.delete(&secret_id).is_err() {
        let rollback = match previous.as_deref() {
            Some(value) => secrets.set(&secret_id, value),
            None => secrets.delete(&secret_id),
        };
        return Err(if rollback.is_err() {
            "账单查询 Key 移除失败且恢复未完成，请重新配置".into()
        } else {
            "无法移除账单查询 Key，请检查系统凭据存储授权".into()
        });
    }
    Ok(settings_view(provider(&profile), false))
}

pub fn delete_key(data_dir: &Path, id: &str) -> Result<BillingSettings, String> {
    delete_key_with(data_dir, id, &NativeSecrets)
}

#[derive(Clone, Copy)]
enum ReportKind {
    Cost,
    Usage,
}

#[derive(Clone, Copy)]
struct Period {
    start: i64,
    today: i64,
    end: i64,
}

impl Period {
    fn current(now: DateTime<Utc>) -> Self {
        let today = now.date_naive().and_hms_opt(0, 0, 0).unwrap().and_utc();
        Self {
            start: Utc
                .with_ymd_and_hms(now.year(), now.month(), 1, 0, 0, 0)
                .single()
                .unwrap()
                .timestamp(),
            today: today.timestamp(),
            // Claude reports whole UTC buckets; use tomorrow's boundary so the
            // current (incomplete, possibly delayed) day is not excluded.
            end: (today + chrono::Duration::days(1)).timestamp(),
        }
    }
}

fn iso(timestamp: i64) -> String {
    DateTime::from_timestamp(timestamp, 0)
        .unwrap()
        .to_rfc3339_opts(SecondsFormat::Secs, true)
}

fn report_url(provider: BillingProvider, kind: ReportKind) -> Result<Url, String> {
    let url = match (provider, kind) {
        (BillingProvider::Openai, ReportKind::Cost) => {
            "https://api.openai.com/v1/organization/costs"
        }
        (BillingProvider::Openai, ReportKind::Usage) => {
            "https://api.openai.com/v1/organization/usage/completions"
        }
        (BillingProvider::Anthropic, ReportKind::Cost) => {
            "https://api.anthropic.com/v1/organizations/cost_report"
        }
        (BillingProvider::Anthropic, ReportKind::Usage) => {
            "https://api.anthropic.com/v1/organizations/usage_report/messages"
        }
        _ => return Err("此服务商暂不支持账单查询".into()),
    };
    Url::parse(url).map_err(|_| "账单接口地址无效".into())
}

fn client() -> Result<Client, String> {
    Client::builder()
        .https_only(true)
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(10))
        .timeout(Duration::from_secs(15))
        .build()
        .map_err(|_| "无法初始化账单查询连接".into())
}

fn status_error(status: reqwest::StatusCode) -> String {
    let message = match status.as_u16() {
        401 => "账单查询 Key 无效或已过期",
        403 => "账单查询 Key 权限不足，请使用有组织用量与费用读取权限的凭据",
        429 => "账单查询受到频率限制，请稍后重试",
        300..=399 => "账单接口返回重定向，已阻止转发查询凭据",
        500..=599 => "服务商账单服务暂时不可用，请稍后重试",
        _ => "账单查询失败，请检查凭据权限和服务商支持情况",
    };
    format!("{message}（HTTP {}）", status.as_u16())
}

#[derive(Default, Debug)]
struct Totals {
    month_cost: f64,
    today_cost: f64,
    input_tokens: u64,
    output_tokens: u64,
}

fn token(value: &Value, key: &str) -> Result<u64, String> {
    value
        .get(key)
        .and_then(Value::as_u64)
        .ok_or_else(|| INVALID_REPORT.into())
}

fn add_tokens(total: &mut u64, value: u64) -> Result<(), String> {
    *total = total.checked_add(value).ok_or(INVALID_REPORT)?;
    Ok(())
}

fn parse_time(value: &Value, key: &str, provider: BillingProvider) -> Result<i64, String> {
    if provider == BillingProvider::Openai {
        return value
            .get(key)
            .and_then(Value::as_i64)
            .ok_or_else(|| INVALID_REPORT.into());
    }
    value
        .get(key)
        .and_then(Value::as_str)
        .and_then(|raw| DateTime::parse_from_rfc3339(raw).ok())
        .map(|date| date.timestamp())
        .ok_or_else(|| INVALID_REPORT.into())
}

fn amount(value: &Value, provider: BillingProvider) -> Result<f64, String> {
    let (amount, currency) = if provider == BillingProvider::Openai {
        let amount = value.get("amount").ok_or(INVALID_REPORT)?;
        (
            amount
                .get("value")
                .and_then(Value::as_f64)
                .ok_or(INVALID_REPORT)?,
            amount.get("currency"),
        )
    } else {
        let raw = value
            .get("amount")
            .and_then(Value::as_str)
            .ok_or(INVALID_REPORT)?;
        let decimal = raw.strip_prefix('-').unwrap_or(raw);
        if decimal.is_empty()
            || decimal.len() > 128
            || decimal.chars().filter(|c| *c == '.').count() > 1
            || !decimal.bytes().all(|b| b.is_ascii_digit() || b == b'.')
            || !decimal.bytes().any(|b| b.is_ascii_digit())
        {
            return Err(INVALID_REPORT.into());
        }
        (
            raw.parse::<f64>().map_err(|_| INVALID_REPORT)? / 100.0,
            value.get("currency"),
        )
    };
    if !amount.is_finite()
        || !currency
            .and_then(Value::as_str)
            .is_some_and(|currency| currency.eq_ignore_ascii_case("USD"))
    {
        return Err(INVALID_REPORT.into());
    }
    Ok(amount)
}

fn parse_page(
    value: &Value,
    provider: BillingProvider,
    kind: ReportKind,
    period: Period,
    totals: &mut Totals,
    buckets: &mut HashSet<(i64, i64)>,
) -> Result<Option<String>, String> {
    let data = value
        .get("data")
        .and_then(Value::as_array)
        .ok_or(INVALID_REPORT)?;
    let has_more = value
        .get("has_more")
        .and_then(Value::as_bool)
        .ok_or(INVALID_REPORT)?;
    let next_page = value.get("next_page").ok_or(INVALID_REPORT)?;
    let next_page = match next_page {
        Value::Null if !has_more => None,
        Value::String(cursor)
            if has_more
                && !cursor.is_empty()
                && cursor.len() <= 4096
                && !cursor.chars().any(char::is_control) =>
        {
            Some(cursor.clone())
        }
        _ => return Err(INVALID_REPORT.into()),
    };
    if has_more && data.is_empty() {
        return Err(INVALID_REPORT.into());
    }
    for bucket in data {
        let (start_key, end_key) = if provider == BillingProvider::Openai {
            ("start_time", "end_time")
        } else {
            ("starting_at", "ending_at")
        };
        let start = parse_time(bucket, start_key, provider)?;
        let end = parse_time(bucket, end_key, provider)?;
        if start < period.start
            || end > period.end
            || end <= start
            || end - start > 86_400
            || start >= period.end
            || buckets
                .iter()
                .any(|(other_start, other_end)| start < *other_end && end > *other_start)
            || !buckets.insert((start, end))
        {
            return Err(INVALID_REPORT.into());
        }
        let results = bucket
            .get("results")
            .and_then(Value::as_array)
            .ok_or(INVALID_REPORT)?;
        for result in results {
            match kind {
                ReportKind::Cost => {
                    let value = amount(result, provider)?;
                    totals.month_cost += value;
                    if start >= period.today {
                        totals.today_cost += value;
                    }
                    if !totals.month_cost.is_finite() || !totals.today_cost.is_finite() {
                        return Err(INVALID_REPORT.into());
                    }
                }
                ReportKind::Usage => {
                    if provider == BillingProvider::Openai {
                        // input_tokens already includes cached input tokens.
                        add_tokens(&mut totals.input_tokens, token(result, "input_tokens")?)?;
                    } else {
                        add_tokens(
                            &mut totals.input_tokens,
                            token(result, "uncached_input_tokens")?,
                        )?;
                        add_tokens(
                            &mut totals.input_tokens,
                            token(result, "cache_read_input_tokens")?,
                        )?;
                        let cache = result.get("cache_creation").ok_or(INVALID_REPORT)?;
                        add_tokens(
                            &mut totals.input_tokens,
                            token(cache, "ephemeral_1h_input_tokens")?,
                        )?;
                        add_tokens(
                            &mut totals.input_tokens,
                            token(cache, "ephemeral_5m_input_tokens")?,
                        )?;
                    }
                    add_tokens(&mut totals.output_tokens, token(result, "output_tokens")?)?;
                }
            }
        }
    }
    Ok(next_page)
}

async fn fetch_report(
    client: &Client,
    endpoint: Url,
    provider: BillingProvider,
    kind: ReportKind,
    key: &str,
    period: Period,
) -> Result<Totals, String> {
    let mut cursor: Option<String> = None;
    let mut cursors = HashSet::new();
    let mut buckets = HashSet::new();
    let mut totals = Totals::default();
    let mut total_bytes = 0usize;
    for _ in 0..MAX_PAGES {
        let mut url = endpoint.clone();
        {
            let mut query = url.query_pairs_mut();
            if provider == BillingProvider::Openai {
                query
                    .append_pair("start_time", &period.start.to_string())
                    .append_pair("end_time", &period.end.to_string());
            } else {
                query
                    .append_pair("starting_at", &iso(period.start))
                    .append_pair("ending_at", &iso(period.end));
            }
            query
                .append_pair("bucket_width", "1d")
                .append_pair("limit", "31");
            if let Some(cursor) = &cursor {
                query.append_pair("page", cursor);
            }
        }
        let mut auth = header::HeaderValue::from_str(if provider == BillingProvider::Anthropic {
            key
        } else {
            ""
        })
        .map_err(|_| "账单查询 Key 格式无效")?;
        let request = if provider == BillingProvider::Anthropic {
            auth.set_sensitive(true);
            client
                .get(url)
                .header("x-api-key", auth)
                .header("anthropic-version", "2023-06-01")
        } else {
            let bearer = Zeroizing::new(format!("Bearer {key}"));
            let mut auth =
                header::HeaderValue::from_str(&bearer).map_err(|_| "账单查询 Key 格式无效")?;
            auth.set_sensitive(true);
            client.get(url).header(header::AUTHORIZATION, auth)
        };
        let mut response = request.send().await.map_err(|error| {
            if error.is_timeout() {
                "账单查询超时，请稍后重试".to_string()
            } else {
                "无法连接服务商账单接口，请检查网络".to_string()
            }
        })?;
        if !response.status().is_success() {
            return Err(status_error(response.status()));
        }
        if response
            .content_length()
            .is_some_and(|length| length > MAX_PAGE_BYTES as u64)
        {
            return Err("服务商账单响应过大，未显示不完整统计".into());
        }
        let mut body = Vec::new();
        while let Some(chunk) = response
            .chunk()
            .await
            .map_err(|_| "读取账单响应失败，请重试")?
        {
            if body.len() + chunk.len() > MAX_PAGE_BYTES
                || total_bytes + chunk.len() > MAX_REPORT_BYTES
            {
                return Err("服务商账单响应过大，未显示不完整统计".into());
            }
            total_bytes += chunk.len();
            body.extend_from_slice(&chunk);
        }
        let page: Value = serde_json::from_slice(&body).map_err(|_| INVALID_REPORT)?;
        cursor = parse_page(&page, provider, kind, period, &mut totals, &mut buckets)?;
        match &cursor {
            None => return Ok(totals),
            Some(cursor) if !cursors.insert(cursor.clone()) => {
                return Err("服务商账单分页重复，未显示不完整统计".into())
            }
            _ => {}
        }
    }
    Err("服务商账单分页超过限制，未显示不完整统计".into())
}

async fn report(
    client: &Client,
    provider: BillingProvider,
    kind: ReportKind,
    key: &str,
    period: Period,
) -> Result<Totals, String> {
    tokio::time::timeout(
        Duration::from_secs(40),
        fetch_report(
            client,
            report_url(provider, kind)?,
            provider,
            kind,
            key,
            period,
        ),
    )
    .await
    .map_err(|_| "账单查询超时，未显示不完整统计".to_string())?
}

pub async fn query(data_dir: &Path, id: &str) -> Result<BillingSnapshot, String> {
    let (original, key) = {
        let (_lock, profile) = api_profiles::locked_profile(data_dir, id)?;
        if provider(&profile) == BillingProvider::Unsupported {
            return Err("此服务商暂不支持账单查询".into());
        }
        let key = Zeroizing::new(
            NativeSecrets
                .get(&api_profiles::billing_secret_id(&profile.id))?
                .filter(|key| !key.is_empty())
                .ok_or("尚未配置账单查询 Key；普通模型调用 Key 不会用于此查询")?,
        );
        (profile, key)
    };
    let provider = provider(&original);
    let period = Period::current(Utc::now());
    let client = client()?;
    let (cost, usage) = tokio::join!(
        report(&client, provider, ReportKind::Cost, &key, period),
        report(&client, provider, ReportKind::Usage, &key, period)
    );
    // A result from an edited/deleted connection must not reappear in the UI.
    verify_query_source(data_dir, &original, &key, &NativeSecrets)?;
    Ok(BillingSnapshot {
        fetched_at: Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true),
        period_start: iso(period.start),
        period_end: iso(period.end),
        currency: "USD",
        month_cost: cost.as_ref().ok().map(|total| total.month_cost),
        today_cost: cost.as_ref().ok().map(|total| total.today_cost),
        month_input_tokens: usage.as_ref().ok().map(|total| total.input_tokens),
        month_output_tokens: usage.as_ref().ok().map(|total| total.output_tokens),
        cost_error: cost.err(),
        usage_error: usage.err(),
    })
}

fn verify_query_source(
    data_dir: &Path,
    original: &ApiProfile,
    key: &str,
    secrets: &impl SecretStore,
) -> Result<(), String> {
    let (_lock, latest) = api_profiles::locked_profile(data_dir, &original.id)?;
    let latest_key = Zeroizing::new(secrets.get(&api_profiles::billing_secret_id(&original.id))?);
    if latest.base_url != original.base_url
        || latest.protocol != original.protocol
        || latest_key.as_deref() != Some(key)
    {
        return Err("查询期间 API 连接或账单凭据已改变，请重新查询".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::collections::HashMap;
    use std::sync::Mutex;

    #[derive(Default)]
    struct MemorySecrets(Mutex<HashMap<String, String>>);
    impl SecretStore for MemorySecrets {
        fn get(&self, id: &str) -> Result<Option<String>, String> {
            Ok(self.0.lock().unwrap().get(id).cloned())
        }
        fn set(&self, id: &str, key: &str) -> Result<(), String> {
            self.0.lock().unwrap().insert(id.into(), key.into());
            Ok(())
        }
        fn delete(&self, id: &str) -> Result<(), String> {
            self.0.lock().unwrap().remove(id);
            Ok(())
        }
    }
    fn profile() -> ApiProfile {
        ApiProfile {
            id: uuid::Uuid::new_v4().to_string(),
            name: "Test".into(),
            protocol: "openai_responses".into(),
            base_url: "https://api.openai.com/v1".into(),
            enabled: true,
            models: vec![],
            default_model: "".into(),
            max_output_tokens: 8192,
        }
    }
    fn period() -> Period {
        Period {
            start: 1_735_689_600,
            today: 1_735_776_000,
            end: 1_735_862_400,
        }
    }
    fn page(results: Value, start: i64) -> Value {
        json!({"data":[{"start_time":start,"end_time":start+86400,"results":results}],"has_more":false,"next_page":null})
    }
    fn parse(value: Value, provider: BillingProvider, kind: ReportKind) -> Result<Totals, String> {
        let mut totals = Totals::default();
        parse_page(
            &value,
            provider,
            kind,
            period(),
            &mut totals,
            &mut HashSet::new(),
        )?;
        Ok(totals)
    }
    #[test]
    fn official_origin_and_protocol_allowlist() {
        let mut p = profile();
        assert_eq!(provider(&p), BillingProvider::Openai);
        for url in [
            "https://api.openai.com.evil.test/v1",
            "https://proxy.test/v1",
            "http://api.openai.com/v1",
            "https://api.openai.com:8443/v1",
            "https://api.openai.com/v2",
            "https://api.openai.com/proxy/v1",
            "https://api.openai.com/v1?x=1",
            "https://api.openai.com/v1#x",
            "https://u:p@api.openai.com/v1",
        ] {
            p.base_url = url.into();
            assert_eq!(provider(&p), BillingProvider::Unsupported, "{url}");
        }
        p.base_url = "https://api.anthropic.com/v1".into();
        assert_eq!(provider(&p), BillingProvider::Unsupported);
        p.protocol = "anthropic_messages".into();
        assert_eq!(provider(&p), BillingProvider::Anthropic);
    }
    #[test]
    fn billing_secrets_are_separate_and_never_serialized() {
        let dir = tempfile::tempdir().unwrap();
        let p = profile();
        std::fs::write(
            dir.path().join("model-api-profiles.json"),
            serde_json::to_vec(&vec![p.clone()]).unwrap(),
        )
        .unwrap();
        let vault = MemorySecrets::default();
        vault.set(&p.id, "inference-key").unwrap();
        let saved = save_key_with(dir.path(), &p.id, "admin-private-key".into(), &vault).unwrap();
        assert!(saved.credential_configured);
        assert!(!serde_json::to_string(&saved)
            .unwrap()
            .contains("admin-private-key"));
        assert!(
            !std::fs::read_to_string(dir.path().join("model-api-profiles.json"))
                .unwrap()
                .contains("admin-private-key")
        );
        assert_eq!(vault.get(&p.id).unwrap().as_deref(), Some("inference-key"));
        assert!(
            settings_with(dir.path(), &p.id, &vault)
                .unwrap()
                .credential_configured
        );
        delete_key_with(dir.path(), &p.id, &vault).unwrap();
        assert_eq!(vault.get(&p.id).unwrap().as_deref(), Some("inference-key"));
        assert!(
            !settings_with(dir.path(), &p.id, &vault)
                .unwrap()
                .credential_configured
        );
        assert!(save_key_with(
            dir.path(),
            &uuid::Uuid::new_v4().to_string(),
            "key".into(),
            &vault
        )
        .is_err());
    }
    #[test]
    fn edited_removed_or_replaced_credentials_reject_inflight_results() {
        let dir = tempfile::tempdir().unwrap();
        let original = profile();
        let path = dir.path().join("model-api-profiles.json");
        std::fs::write(&path, serde_json::to_vec(&vec![original.clone()]).unwrap()).unwrap();
        let vault = MemorySecrets::default();
        let secret_id = api_profiles::billing_secret_id(&original.id);
        vault.set(&secret_id, "original-admin").unwrap();
        verify_query_source(dir.path(), &original, "original-admin", &vault).unwrap();
        vault.set(&secret_id, "new-account-admin").unwrap();
        assert!(verify_query_source(dir.path(), &original, "original-admin", &vault).is_err());
        vault.delete(&secret_id).unwrap();
        assert!(verify_query_source(dir.path(), &original, "original-admin", &vault).is_err());
        vault.set(&secret_id, "original-admin").unwrap();
        let mut changed = original.clone();
        changed.base_url = "https://proxy.test/v1".into();
        std::fs::write(&path, serde_json::to_vec(&vec![changed]).unwrap()).unwrap();
        assert!(verify_query_source(dir.path(), &original, "original-admin", &vault).is_err());
        std::fs::write(&path, "[]").unwrap();
        assert!(verify_query_source(dir.path(), &original, "original-admin", &vault).is_err());
    }
    #[test]
    fn unsupported_provider_cannot_save_or_read_a_billing_key() {
        struct NoAccess;
        impl SecretStore for NoAccess {
            fn get(&self, _: &str) -> Result<Option<String>, String> {
                panic!("must not read credentials")
            }
            fn set(&self, _: &str, _: &str) -> Result<(), String> {
                panic!("must not write credentials")
            }
            fn delete(&self, _: &str) -> Result<(), String> {
                panic!("must not delete credentials")
            }
        }
        let dir = tempfile::tempdir().unwrap();
        let mut profile = profile();
        profile.base_url = "https://proxy.test/v1".into();
        std::fs::write(
            dir.path().join("model-api-profiles.json"),
            serde_json::to_vec(&vec![profile.clone()]).unwrap(),
        )
        .unwrap();
        let settings = settings_with(dir.path(), &profile.id, &NoAccess).unwrap();
        assert_eq!(settings.provider, BillingProvider::Unsupported);
        assert!(!settings.credential_configured);
        assert!(
            save_key_with(dir.path(), &profile.id, "synthetic-admin".into(), &NoAccess).is_err()
        );
    }
    #[test]
    fn partial_vault_delete_failure_restores_previous_billing_key() {
        struct FailsDeleteOnce {
            values: MemorySecrets,
            fail: std::sync::atomic::AtomicBool,
        }
        impl SecretStore for FailsDeleteOnce {
            fn get(&self, id: &str) -> Result<Option<String>, String> {
                self.values.get(id)
            }
            fn set(&self, id: &str, key: &str) -> Result<(), String> {
                self.values.set(id, key)
            }
            fn delete(&self, id: &str) -> Result<(), String> {
                self.values.delete(id)?;
                if self.fail.swap(false, std::sync::atomic::Ordering::SeqCst) {
                    Err("vault failure".into())
                } else {
                    Ok(())
                }
            }
        }
        let dir = tempfile::tempdir().unwrap();
        let profile = profile();
        std::fs::write(
            dir.path().join("model-api-profiles.json"),
            serde_json::to_vec(&vec![profile.clone()]).unwrap(),
        )
        .unwrap();
        let vault = FailsDeleteOnce {
            values: MemorySecrets::default(),
            fail: true.into(),
        };
        let id = api_profiles::billing_secret_id(&profile.id);
        vault.set(&id, "synthetic-admin").unwrap();
        assert!(delete_key_with(dir.path(), &profile.id, &vault).is_err());
        assert_eq!(vault.get(&id).unwrap().as_deref(), Some("synthetic-admin"));
    }
    #[test]
    fn openai_usd_and_tokens_do_not_double_count_cache() {
        let cost = parse(
            page(
                json!([{"amount":{"value":12.34,"currency":"usd"}}]),
                period().today,
            ),
            BillingProvider::Openai,
            ReportKind::Cost,
        )
        .unwrap();
        assert_eq!(cost.month_cost, 12.34);
        assert_eq!(cost.today_cost, 12.34);
        let usage = parse(
            page(
                json!([{"input_tokens":100,"input_cached_tokens":40,"output_tokens":15}]),
                period().start,
            ),
            BillingProvider::Openai,
            ReportKind::Usage,
        )
        .unwrap();
        assert_eq!(usage.input_tokens, 100);
        assert_eq!(usage.output_tokens, 15);
    }
    #[test]
    fn claude_converts_cents_and_counts_all_input_categories() {
        let cost = json!({"data":[{"starting_at":iso(period().today),"ending_at":iso(period().end),"results":[{"amount":"123.78912","currency":"USD"}]}],"has_more":false,"next_page":null});
        let totals = parse(cost, BillingProvider::Anthropic, ReportKind::Cost).unwrap();
        assert!((totals.month_cost - 1.2378912).abs() < 1e-10);
        let usage = json!({"data":[{"starting_at":iso(period().today),"ending_at":iso(period().end),"results":[{"uncached_input_tokens":100,"cache_read_input_tokens":30,"cache_creation":{"ephemeral_1h_input_tokens":20,"ephemeral_5m_input_tokens":10},"output_tokens":50}]}],"has_more":false,"next_page":null});
        let totals = parse(usage, BillingProvider::Anthropic, ReportKind::Usage).unwrap();
        assert_eq!(totals.input_tokens, 160);
        assert_eq!(totals.output_tokens, 50);
    }
    #[test]
    fn invalid_reports_never_turn_into_zero() {
        for result in [
            json!({}),
            json!({"amount":{"value":null,"currency":"usd"}}),
            json!({"amount":{"value":"3","currency":"usd"}}),
            json!({"amount":{"value":1,"currency":"eur"}}),
        ] {
            assert!(parse(
                page(json!([result]), period().today),
                BillingProvider::Openai,
                ReportKind::Cost
            )
            .is_err());
        }
        assert!(parse(
            json!({"error":"secret-echo"}),
            BillingProvider::Openai,
            ReportKind::Cost
        )
        .is_err());
        assert!(parse(
            json!({"data":[],"has_more":true,"next_page":null}),
            BillingProvider::Openai,
            ReportKind::Cost
        )
        .is_err());
        let empty = parse(
            json!({"data":[],"has_more":false,"next_page":null}),
            BillingProvider::Openai,
            ReportKind::Cost,
        )
        .unwrap();
        assert_eq!(empty.month_cost, 0.0);
        assert!(parse(
            page(json!([{"input_tokens":1}]), period().today),
            BillingProvider::Openai,
            ReportKind::Usage
        )
        .is_err());
        let value = page(json!([]), period().today);
        let mut totals = Totals::default();
        let mut buckets = HashSet::new();
        parse_page(
            &value,
            BillingProvider::Openai,
            ReportKind::Cost,
            period(),
            &mut totals,
            &mut buckets,
        )
        .unwrap();
        assert!(parse_page(
            &value,
            BillingProvider::Openai,
            ReportKind::Cost,
            period(),
            &mut totals,
            &mut buckets
        )
        .is_err());
        let mut overlap = value.clone();
        overlap["data"][0]["end_time"] = json!(period().end - 60);
        assert!(parse_page(
            &overlap,
            BillingProvider::Openai,
            ReportKind::Cost,
            period(),
            &mut totals,
            &mut buckets
        )
        .is_err());
    }
    #[test]
    fn month_boundaries_are_utc_and_include_current_day() {
        let now = Utc.with_ymd_and_hms(2026, 10, 31, 23, 59, 59).unwrap();
        let p = Period::current(now);
        assert_eq!(iso(p.start), "2026-10-01T00:00:00Z");
        assert_eq!(iso(p.today), "2026-10-31T00:00:00Z");
        assert_eq!(iso(p.end), "2026-11-01T00:00:00Z");
    }
    async fn mock_server(responses: Vec<String>) -> (Url, tokio::task::JoinHandle<Vec<String>>) {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let server = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = Url::parse(&format!("http://{}/reports", server.local_addr().unwrap())).unwrap();
        let handle = tokio::spawn(async move {
            let mut requests = vec![];
            for response in responses {
                let (mut socket, _) = server.accept().await.unwrap();
                let mut request = vec![];
                let mut buffer = [0; 4096];
                loop {
                    let n = socket.read(&mut buffer).await.unwrap();
                    if n == 0 {
                        break;
                    }
                    request.extend_from_slice(&buffer[..n]);
                    if request.windows(4).any(|v| v == b"\r\n\r\n") {
                        break;
                    }
                }
                requests.push(String::from_utf8(request).unwrap());
                socket.write_all(response.as_bytes()).await.unwrap();
            }
            requests
        });
        (url, handle)
    }
    fn response(body: Value) -> String {
        let body = body.to_string();
        format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len())
    }
    fn test_client() -> Client {
        Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(2))
            .build()
            .unwrap()
    }
    #[tokio::test]
    async fn billing_pagination_and_headers_use_only_get() {
        let mut first = page(
            json!([{"amount":{"value":2.0,"currency":"usd"}}]),
            period().start,
        );
        first["has_more"] = json!(true);
        first["next_page"] = json!("next-token");
        let (url, requests) = mock_server(vec![
            response(first),
            response(page(
                json!([{"amount":{"value":3.0,"currency":"usd"}}]),
                period().today,
            )),
        ])
        .await;
        let total = fetch_report(
            &test_client(),
            url,
            BillingProvider::Openai,
            ReportKind::Cost,
            "synthetic-admin",
            period(),
        )
        .await
        .unwrap();
        assert_eq!(total.month_cost, 5.0);
        assert_eq!(total.today_cost, 3.0);
        let requests = requests.await.unwrap();
        assert!(requests[0].starts_with("GET /reports?start_time="));
        assert!(requests[0]
            .to_lowercase()
            .contains("authorization: bearer synthetic-admin"));
        assert!(requests[1].contains("page=next-token"));
    }
    #[tokio::test]
    async fn repeated_cursor_and_page_limit_do_not_return_partial_totals() {
        let mut first = page(json!([]), period().start);
        first["has_more"] = json!(true);
        first["next_page"] = json!("same");
        let mut second = page(json!([]), period().today);
        second["has_more"] = json!(true);
        second["next_page"] = json!("same");
        let (url, requests) = mock_server(vec![response(first), response(second)]).await;
        let error = fetch_report(
            &test_client(),
            url,
            BillingProvider::Openai,
            ReportKind::Cost,
            "synthetic-admin",
            period(),
        )
        .await
        .unwrap_err();
        assert!(error.contains("分页重复"));
        requests.await.unwrap();
        let p = Period {
            start: period().start,
            today: period().start + 30 * 86400,
            end: period().start + 31 * 86400,
        };
        let pages = (0..MAX_PAGES)
            .map(|number| {
                let mut page = page(
                    json!([{"amount":{"value":1.0,"currency":"usd"}}]),
                    p.start + number as i64 * 86400,
                );
                page["has_more"] = json!(true);
                page["next_page"] = json!(format!("cursor-{number}"));
                response(page)
            })
            .collect();
        let (url, requests) = mock_server(pages).await;
        let error = fetch_report(
            &test_client(),
            url,
            BillingProvider::Openai,
            ReportKind::Cost,
            "synthetic-admin",
            p,
        )
        .await
        .unwrap_err();
        assert!(error.contains("分页超过限制"));
        assert_eq!(requests.await.unwrap().len(), MAX_PAGES);
    }
    #[tokio::test]
    async fn billing_redirects_and_error_bodies_never_expose_keys() {
        let destination = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let (url, requests) = mock_server(vec![format!(
            "HTTP/1.1 302 Found\r\nLocation: http://{}/stolen\r\nContent-Length: 0\r\n\r\n",
            destination.local_addr().unwrap()
        )])
        .await;
        let error = fetch_report(
            &test_client(),
            url,
            BillingProvider::Anthropic,
            ReportKind::Usage,
            "synthetic-admin",
            period(),
        )
        .await
        .unwrap_err();
        assert!(error.contains("HTTP 302"));
        assert!(!error.contains("synthetic-admin"));
        let request = requests.await.unwrap().remove(0).to_lowercase();
        assert!(request.contains("x-api-key: synthetic-admin"));
        assert!(request.contains("anthropic-version: 2023-06-01"));
        assert!(!request.contains("authorization:"));
        assert!(
            tokio::time::timeout(Duration::from_millis(50), destination.accept())
                .await
                .is_err()
        );
        let body = "synthetic-admin secret-echo";
        let (url, requests) = mock_server(vec![format!(
            "HTTP/1.1 403 Forbidden\r\nContent-Length: {}\r\n\r\n{body}",
            body.len()
        )])
        .await;
        let error = fetch_report(
            &test_client(),
            url,
            BillingProvider::Openai,
            ReportKind::Cost,
            "synthetic-admin",
            period(),
        )
        .await
        .unwrap_err();
        requests.await.unwrap();
        assert!(error.contains("HTTP 403"));
        assert!(!error.contains("secret-echo"));
    }
}
