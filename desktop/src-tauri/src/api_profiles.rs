//! Direct model API connections. Only non-secret metadata is written to disk;
//! API keys use the existing operating-system credential vault.

use std::collections::HashSet;
use std::fs::{self, File, OpenOptions};
use std::io::{Read, Write};
use std::path::Path;
use std::time::Duration;

use reqwest::{Client, Url};
use serde::{Deserialize, Serialize};
use zeroize::{Zeroize, Zeroizing};

const FILE_NAME: &str = "model-api-profiles.json";
const LOCK_NAME: &str = "model-api-profiles.lock";
const MAX_PROFILES: usize = 64;
const MAX_MODELS: usize = 512;
const MAX_CONFIG_BYTES: u64 = 8 * 1024 * 1024;
const MAX_RESPONSE_BYTES: usize = 4 * 1024 * 1024;

fn default_enabled() -> bool {
    true
}

fn default_max_output_tokens() -> u32 {
    8192
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct ApiProfile {
    pub id: String,
    pub name: String,
    pub protocol: String,
    pub base_url: String,
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    #[serde(default)]
    pub models: Vec<String>,
    #[serde(default)]
    pub default_model: String,
    #[serde(default = "default_max_output_tokens")]
    pub max_output_tokens: u32,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ApiProfileInput {
    #[serde(default)]
    pub id: Option<String>,
    pub name: String,
    pub protocol: String,
    #[serde(default)]
    pub base_url: String,
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    #[serde(default)]
    pub models: Vec<String>,
    #[serde(default)]
    pub default_model: String,
    #[serde(default = "default_max_output_tokens")]
    pub max_output_tokens: u32,
}

#[derive(Clone, Debug, Serialize)]
pub struct ApiProfileView {
    #[serde(flatten)]
    pub profile: ApiProfile,
    pub key_configured: bool,
}

// Deliberately neither Debug nor Serialize: credentials cannot accidentally be
// logged or returned across Tauri IPC alongside the public profile metadata.
pub struct LoadedApiProfile {
    pub profile: ApiProfile,
    pub api_key: String,
}

impl Drop for LoadedApiProfile {
    fn drop(&mut self) {
        self.api_key.zeroize();
    }
}

#[derive(Debug, Serialize)]
pub struct ApiTestResult {
    pub ok: bool,
    pub message: String,
    pub model_count: usize,
}

pub(crate) trait SecretStore {
    fn get(&self, id: &str) -> Result<Option<String>, String>;
    fn set(&self, id: &str, key: &str) -> Result<(), String>;
    fn delete(&self, id: &str) -> Result<(), String>;
}

pub(crate) struct NativeSecrets;

fn secret_account(id: &str) -> String {
    // Billing credentials have a separate native-vault namespace. Public
    // profile IDs are UUID-validated, so inference cannot select this namespace.
    match id.strip_prefix("billing:") {
        Some(id) => format!("model-api-billing:{id}"),
        None => format!("model-api:{id}"),
    }
}

pub(crate) fn billing_secret_id(id: &str) -> String {
    format!("billing:{id}")
}

impl SecretStore for NativeSecrets {
    fn get(&self, id: &str) -> Result<Option<String>, String> {
        let account = secret_account(id);
        let result = if id.starts_with("billing:") {
            // An Admin key removed or replaced by another Pilot process must
            // not be resurrected from this process's cache or legacy backup.
            crate::keychain::get_named_secret_fresh(&account)
        } else {
            crate::keychain::get_named_secret(&account)
        };
        match result {
            Ok(key) => Ok(Some(key)),
            Err(error) if error.contains("NoEntry") => Ok(None),
            Err(_) => Err("无法读取系统凭据存储，请检查钥匙串授权后重试".into()),
        }
    }

    fn set(&self, id: &str, key: &str) -> Result<(), String> {
        crate::keychain::set_named_secret(&secret_account(id), key)
            .map_err(|_| "API Key 保存到系统凭据存储失败".into())
    }

    fn delete(&self, id: &str) -> Result<(), String> {
        crate::keychain::delete_named_secret(&secret_account(id))
            .map_err(|_| "无法从系统凭据存储移除 API Key".into())
    }
}

fn normalized_id(id: &str) -> Result<String, String> {
    let id = uuid::Uuid::parse_str(id).map_err(|_| "API 连接 ID 无效")?;
    if id.is_nil() {
        return Err("API 连接 ID 无效".into());
    }
    Ok(id.to_string())
}

fn normalized_base_url(protocol: &str, raw: &str) -> Result<String, String> {
    let raw = raw.trim();
    let raw = if raw.is_empty() {
        match protocol {
            "openai_responses" => "https://api.openai.com/v1",
            "anthropic_messages" => "https://api.anthropic.com/v1",
            _ => return Err("请填写兼容服务的 API 接口地址".into()),
        }
    } else {
        raw
    };
    if raw.len() > 2048 || raw.chars().any(char::is_control) {
        return Err("API 接口地址无效".into());
    }
    let url = Url::parse(raw).map_err(|_| "API 接口地址格式不正确")?;
    if !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
    {
        return Err("API 地址不能包含账号、密码、查询参数或锚点".into());
    }
    let host = url.host_str().ok_or("API 接口地址缺少主机名")?;
    let is_loopback = host.eq_ignore_ascii_case("localhost")
        || host
            .trim_start_matches('[')
            .trim_end_matches(']')
            .parse::<std::net::IpAddr>()
            .map(|ip| ip.is_loopback())
            .unwrap_or(false);
    if url.scheme() != "https" && !(url.scheme() == "http" && is_loopback) {
        return Err("API 接口必须使用 HTTPS；仅本机回环地址允许 HTTP".into());
    }
    Ok(url.as_str().trim_end_matches('/').to_string())
}

fn model_id_valid(model: &str) -> bool {
    !model.is_empty()
        && model.len() <= 256
        && !model.chars().any(|c| c.is_whitespace() || c.is_control())
}

fn normalize(input: ApiProfileInput, id: String) -> Result<ApiProfile, String> {
    if !matches!(
        input.protocol.as_str(),
        "openai_responses" | "anthropic_messages" | "openai_chat"
    ) {
        return Err("不支持的模型 API 协议".into());
    }
    let name = input.name.trim().to_string();
    if name.is_empty() || name.chars().count() > 80 || name.chars().any(char::is_control) {
        return Err("连接名称须为 1–80 个字符，且不能包含控制字符".into());
    }
    if !(1..=1_048_576).contains(&input.max_output_tokens) {
        return Err("最大输出 Token 数须在 1–1048576 之间".into());
    }
    if input.models.len() > MAX_MODELS {
        return Err("每个 API 连接最多配置 512 个模型".into());
    }
    let mut seen = HashSet::new();
    let mut models = Vec::new();
    for model in input.models {
        let model = model.trim();
        if !model_id_valid(model) {
            return Err("模型 ID 须为 1–256 字节，且不能包含空白或控制字符".into());
        }
        if seen.insert(model.to_string()) {
            models.push(model.to_string());
        }
    }
    let default_model = input.default_model.trim().to_string();
    if !default_model.is_empty() && !models.contains(&default_model) {
        return Err("默认模型必须包含在此连接的可用模型列表中".into());
    }
    let default_model = if default_model.is_empty() {
        models.first().cloned().unwrap_or_default()
    } else {
        default_model
    };
    Ok(ApiProfile {
        id: normalized_id(&id)?,
        name,
        base_url: normalized_base_url(&input.protocol, &input.base_url)?,
        protocol: input.protocol,
        enabled: input.enabled,
        models,
        default_model,
        max_output_tokens: input.max_output_tokens,
    })
}

fn validate_stored(profile: ApiProfile) -> Result<ApiProfile, String> {
    normalize(
        ApiProfileInput {
            id: Some(profile.id.clone()),
            name: profile.name,
            protocol: profile.protocol,
            base_url: profile.base_url,
            enabled: profile.enabled,
            models: profile.models,
            default_model: profile.default_model,
            max_output_tokens: profile.max_output_tokens,
        },
        profile.id,
    )
}

// A persistent lock file plus OS file lock serializes read-modify-write across
// both threads and simultaneously running Dev/release Pilot processes. Dropping
// File releases the lock, including after errors or process termination.
fn lock_store(data_dir: &Path) -> Result<File, String> {
    fs::create_dir_all(data_dir).map_err(|_| "无法创建 API 连接配置目录")?;
    let lock = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(data_dir.join(LOCK_NAME))
        .map_err(|_| "无法打开 API 连接配置锁")?;
    lock.lock().map_err(|_| "无法锁定 API 连接配置")?;
    Ok(lock)
}

fn read_profiles(data_dir: &Path) -> Result<Vec<ApiProfile>, String> {
    let path = data_dir.join(FILE_NAME);
    let file = match File::open(&path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(_) => return Err("无法读取 API 连接配置".into()),
    };
    let mut raw = Vec::new();
    file.take(MAX_CONFIG_BYTES + 1)
        .read_to_end(&mut raw)
        .map_err(|_| "读取 API 连接配置失败")?;
    if raw.len() as u64 > MAX_CONFIG_BYTES {
        return Err("API 连接配置文件超出大小限制".into());
    }
    let profiles: Vec<ApiProfile> =
        serde_json::from_slice(&raw).map_err(|_| "API 连接配置格式损坏，请恢复有效备份")?;
    if profiles.len() > MAX_PROFILES {
        return Err("API 连接数量超出限制".into());
    }
    let mut seen = HashSet::new();
    profiles
        .into_iter()
        .map(|profile| {
            let profile = validate_stored(profile)?;
            if !seen.insert(profile.id.clone()) {
                return Err("API 连接配置中存在重复 ID".into());
            }
            Ok(profile)
        })
        .collect()
}

/// Keep the same cross-process lock alive throughout a credential operation.
/// Billing never needs to load the inference key.
pub(crate) fn locked_profile(data_dir: &Path, id: &str) -> Result<(File, ApiProfile), String> {
    let id = normalized_id(id)?;
    let lock = lock_store(data_dir)?;
    let profile = read_profiles(data_dir)?
        .into_iter()
        .find(|profile| profile.id == id)
        .ok_or("API 连接已移除，请刷新后重试")?;
    Ok((lock, profile))
}

fn restore_secret(secrets: &impl SecretStore, id: &str, key: Option<&str>) -> Result<(), String> {
    match key {
        Some(value) => secrets.set(id, value),
        None => secrets.delete(id),
    }
}

fn write_profiles(data_dir: &Path, profiles: &[ApiProfile]) -> Result<(), String> {
    let raw = serde_json::to_vec_pretty(profiles).map_err(|_| "无法编码 API 连接配置")?;
    if raw.len() as u64 > MAX_CONFIG_BYTES {
        return Err("API 连接配置文件超出大小限制".into());
    }
    let mut temp =
        tempfile::NamedTempFile::new_in(data_dir).map_err(|_| "无法创建 API 连接配置临时文件")?;
    temp.write_all(&raw).map_err(|_| "写入 API 连接配置失败")?;
    temp.as_file()
        .sync_all()
        .map_err(|_| "同步 API 连接配置失败")?;
    temp.persist(data_dir.join(FILE_NAME))
        .map_err(|_| "替换 API 连接配置失败")?;
    Ok(())
}

fn key_input(api_key: Option<String>) -> Result<Option<String>, String> {
    let Some(mut key) = api_key else {
        return Ok(None);
    };
    let trimmed = key.trim().to_string();
    key.zeroize();
    if trimmed.is_empty() {
        return Ok(None);
    }
    if trimmed.len() > 8192 || !trimmed.bytes().all(|b| (b'!'..=b'~').contains(&b)) {
        return Err("API Key 格式无效，请勿包含空白或控制字符".into());
    }
    Ok(Some(trimmed))
}

fn list_with(data_dir: &Path, secrets: &impl SecretStore) -> Result<Vec<ApiProfileView>, String> {
    let _lock = lock_store(data_dir)?;
    read_profiles(data_dir)?
        .into_iter()
        .map(|profile| {
            let key = secrets.get(&profile.id)?;
            let key_configured = key.as_ref().is_some_and(|value| !value.is_empty());
            if let Some(mut key) = key {
                key.zeroize();
            }
            Ok(ApiProfileView {
                profile,
                key_configured,
            })
        })
        .collect()
}

pub fn list(data_dir: &Path) -> Result<Vec<ApiProfileView>, String> {
    list_with(data_dir, &NativeSecrets)
}

fn save_with(
    data_dir: &Path,
    input: ApiProfileInput,
    api_key: Option<String>,
    confirm_endpoint_change: bool,
    secrets: &impl SecretStore,
) -> Result<ApiProfileView, String> {
    let editing = input.id.is_some();
    let id = match input.id.as_deref() {
        Some(id) => normalized_id(id)?,
        None => uuid::Uuid::new_v4().to_string(),
    };
    let profile = normalize(input, id)?;
    let mut new_key = Zeroizing::new(key_input(api_key)?);
    let _lock = lock_store(data_dir)?;
    let mut profiles = read_profiles(data_dir)?;
    let existing = profiles.iter().position(|item| item.id == profile.id);
    if editing && existing.is_none() {
        return Err("此 API 连接已不存在，请刷新后重试".into());
    }
    if existing.is_none() && profiles.len() >= MAX_PROFILES {
        return Err("最多添加 64 个 API 连接".into());
    }
    let mut old_key = Zeroizing::new(secrets.get(&profile.id)?);
    let endpoint_changed = existing.is_some_and(|index| {
        profiles[index].base_url != profile.base_url || profiles[index].protocol != profile.protocol
    });
    let billing_id = billing_secret_id(&profile.id);
    let old_billing_key = Zeroizing::new(if endpoint_changed {
        secrets.get(&billing_id)?
    } else {
        None
    });
    if let Some(index) = existing {
        let old = &profiles[index];
        if (old.base_url != profile.base_url || old.protocol != profile.protocol)
            && old_key.is_some()
            && new_key.is_none()
            && !confirm_endpoint_change
        {
            if let Some(key) = old_key.as_mut() {
                key.zeroize();
            }
            return Err(
                "接口地址或协议已改变，请重新输入 API Key，或明确确认向新接口使用原 Key".into(),
            );
        }
        profiles[index] = profile.clone();
    } else {
        profiles.push(profile.clone());
    }
    if let Some(key) = new_key.as_deref() {
        if let Err(error) = secrets.set(&profile.id, key) {
            // The native vault updates both a recovery entry and the shared
            // vault. Restore the old value if only one of those writes worked.
            let rollback = match old_key.as_deref() {
                Some(old) => secrets.set(&profile.id, old),
                None => secrets.delete(&profile.id),
            };
            if let Some(key) = old_key.as_mut() {
                key.zeroize();
            }
            if let Some(key) = new_key.as_mut() {
                key.zeroize();
            }
            return Err(if rollback.is_err() {
                "凭据保存失败且恢复未完成，请重新保存此 API 连接".into()
            } else {
                error
            });
        }
    }
    // Even an explicitly approved inference-key migration must not carry a
    // high-privilege billing key to a different endpoint or protocol.
    if endpoint_changed {
        if let Err(error) = secrets.delete(&billing_id) {
            let billing_rollback = restore_secret(secrets, &billing_id, old_billing_key.as_deref());
            let inference_rollback = if new_key.is_some() {
                restore_secret(secrets, &profile.id, old_key.as_deref())
            } else {
                Ok(())
            };
            return Err(
                if billing_rollback.is_err() || inference_rollback.is_err() {
                    "账单凭据清理失败且恢复未完成，请重新保存此 API 连接".into()
                } else {
                    error
                },
            );
        }
    }
    let write_result = write_profiles(data_dir, &profiles);
    if write_result.is_err() && endpoint_changed {
        let billing_rollback = restore_secret(secrets, &billing_id, old_billing_key.as_deref());
        if billing_rollback.is_err() {
            if new_key.is_some() {
                let _ = restore_secret(secrets, &profile.id, old_key.as_deref());
            }
            return Err("配置保存失败，账单凭据恢复也未完成，请重新保存此 API 连接".into());
        }
    }
    if write_result.is_err() && new_key.is_some() {
        let rollback = match old_key.as_deref() {
            Some(key) => secrets.set(&profile.id, key),
            None => secrets.delete(&profile.id),
        };
        if rollback.is_err() {
            if let Some(key) = old_key.as_mut() {
                key.zeroize();
            }
            if let Some(key) = new_key.as_mut() {
                key.zeroize();
            }
            return Err("配置保存失败，凭据恢复也未完成，请重新保存此 API 连接".into());
        }
    }
    let key_configured = new_key.is_some() || old_key.is_some();
    if let Some(key) = old_key.as_mut() {
        key.zeroize();
    }
    if let Some(key) = new_key.as_mut() {
        key.zeroize();
    }
    write_result?;
    Ok(ApiProfileView {
        profile,
        key_configured,
    })
}

pub fn save(
    data_dir: &Path,
    input: ApiProfileInput,
    api_key: Option<String>,
    confirm_endpoint_change: bool,
) -> Result<ApiProfileView, String> {
    save_with(
        data_dir,
        input,
        api_key,
        confirm_endpoint_change,
        &NativeSecrets,
    )
}

fn delete_with(data_dir: &Path, id: &str, secrets: &impl SecretStore) -> Result<(), String> {
    let id = normalized_id(id)?;
    let _lock = lock_store(data_dir)?;
    let mut profiles = read_profiles(data_dir)?;
    let index = profiles
        .iter()
        .position(|item| item.id == id)
        .ok_or("此 API 连接已不存在，请刷新后重试")?;
    let mut key = Zeroizing::new(secrets.get(&id)?);
    let billing_id = billing_secret_id(&id);
    let billing_key = Zeroizing::new(secrets.get(&billing_id)?);
    if let Err(error) = secrets.delete(&id) {
        let rollback = key.as_deref().map(|value| secrets.set(&id, value));
        if let Some(key) = key.as_mut() {
            key.zeroize();
        }
        return Err(if rollback.is_some_and(|result| result.is_err()) {
            "凭据移除失败且恢复未完成，请重新保存此 API 连接".into()
        } else {
            error
        });
    }
    if let Err(error) = secrets.delete(&billing_id) {
        let billing_rollback = restore_secret(secrets, &billing_id, billing_key.as_deref());
        let inference_rollback = restore_secret(secrets, &id, key.as_deref());
        return Err(
            if billing_rollback.is_err() || inference_rollback.is_err() {
                "账单凭据移除失败且恢复未完成，请重新保存此 API 连接".into()
            } else {
                error
            },
        );
    }
    profiles.remove(index);
    let result = write_profiles(data_dir, &profiles);
    if result.is_err() {
        let billing_rollback = restore_secret(secrets, &billing_id, billing_key.as_deref());
        if billing_rollback.is_err() {
            let _ = restore_secret(secrets, &id, key.as_deref());
            return Err("配置移除失败，账单凭据恢复也未完成，请重新保存此 API 连接".into());
        }
        if let Some(value) = key.as_deref() {
            if secrets.set(&id, value).is_err() {
                if let Some(key) = key.as_mut() {
                    key.zeroize();
                }
                return Err("配置移除失败，凭据恢复也未完成，请重新保存此 API 连接".into());
            }
        }
    }
    if let Some(key) = key.as_mut() {
        key.zeroize();
    }
    result
}

pub fn delete(data_dir: &Path, id: &str) -> Result<(), String> {
    delete_with(data_dir, id, &NativeSecrets)
}

fn load_with(
    data_dir: &Path,
    id: &str,
    enabled_only: bool,
    secrets: &impl SecretStore,
) -> Result<LoadedApiProfile, String> {
    let id = normalized_id(id)?;
    let _lock = lock_store(data_dir)?;
    let profile = read_profiles(data_dir)?
        .into_iter()
        .find(|item| item.id == id)
        .ok_or("API 连接已移除，请重新选择模型")?;
    if enabled_only && !profile.enabled {
        return Err("此 API 连接已停用，请在模型服务设置中启用".into());
    }
    let api_key = secrets
        .get(&id)?
        .filter(|key| !key.is_empty())
        .ok_or("此 API 连接尚未配置 API Key，请编辑连接后保存")?;
    Ok(LoadedApiProfile { profile, api_key })
}

pub fn load_enabled(data_dir: &Path, id: &str) -> Result<LoadedApiProfile, String> {
    load_with(data_dir, id, true, &NativeSecrets)
}

/// Construct only known API routes without allowing an absolute URL to replace
/// the user-approved credential destination.
pub fn endpoint(profile: &ApiProfile, route: &str) -> Result<Url, String> {
    if !matches!(
        route,
        "models" | "responses" | "messages" | "chat/completions"
    ) {
        return Err("不支持的模型 API 路径".into());
    }
    let base = normalized_base_url(&profile.protocol, &profile.base_url)?;
    Url::parse(&format!("{base}/{route}")).map_err(|_| "模型 API 路径无效".into())
}

/// Never follow redirects: a gateway must not redirect x-api-key or bearer
/// credentials to a different destination, even within a shared parent domain.
pub fn http_client() -> Result<Client, String> {
    Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(15))
        .timeout(Duration::from_secs(300))
        .build()
        .map_err(|_| "无法初始化模型 API 网络连接".into())
}

fn status_error(status: reqwest::StatusCode) -> String {
    let detail = match status.as_u16() {
        401 => "API Key 无效或已过期",
        403 => "此 API Key 无权读取模型列表；若仅授予推理权限，请手动填写模型 ID",
        404 | 405 => "接口不提供模型列表，请检查 API 根地址或手动填写模型 ID",
        429 => "请求受限，请稍后重试",
        300..=399 => "接口返回重定向，已阻止转发 API Key；请填写最终 API 根地址",
        500..=599 => "模型服务暂时不可用，请稍后重试",
        _ => "模型列表读取失败，请检查协议和 API 根地址",
    };
    format!("{detail}（HTTP {}）", status.as_u16())
}

// Protocol references:
// https://developers.openai.com/api/reference/resources/models/methods/list
// https://platform.claude.com/docs/en/api/models/list
async fn fetch_models(loaded: &LoadedApiProfile) -> Result<Vec<String>, String> {
    let client = http_client()?;
    let mut models = Vec::new();
    let mut seen = HashSet::new();
    let mut cursor: Option<String> = None;
    // Anthropic paginates; other compatible providers may implement only a
    // simple OpenAI-style data array. Never follow a server-provided next URL.
    for _ in 0..10 {
        let mut url = endpoint(&loaded.profile, "models")?;
        if loaded.profile.protocol == "anthropic_messages" {
            url.query_pairs_mut().append_pair("limit", "1000");
            if let Some(after_id) = cursor.as_deref() {
                url.query_pairs_mut().append_pair("after_id", after_id);
            }
        }
        let request = client.get(url).timeout(Duration::from_secs(30));
        let request = if loaded.profile.protocol == "anthropic_messages" {
            request
                .header("x-api-key", &loaded.api_key)
                .header("anthropic-version", "2023-06-01")
        } else {
            request.bearer_auth(&loaded.api_key)
        };
        let mut response = request.send().await.map_err(|error| {
            if error.is_timeout() {
                "模型服务连接超时"
            } else {
                "无法连接模型服务，请检查网络和接口地址"
            }
            .to_string()
        })?;
        if !response.status().is_success() {
            return Err(status_error(response.status()));
        }
        if response
            .content_length()
            .is_some_and(|len| len > MAX_RESPONSE_BYTES as u64)
        {
            return Err("模型列表响应超出大小限制".into());
        }
        let mut raw = Vec::new();
        while let Some(chunk) = response.chunk().await.map_err(|_| "模型列表响应读取失败")?
        {
            if raw.len() + chunk.len() > MAX_RESPONSE_BYTES {
                return Err("模型列表响应超出大小限制".into());
            }
            raw.extend_from_slice(&chunk);
        }
        let body: serde_json::Value = serde_json::from_slice(&raw)
            .map_err(|_| "接口返回的模型列表不是有效 JSON，请检查协议和 API 根地址")?;
        let items = body
            .get("data")
            .and_then(|value| value.as_array())
            .ok_or("接口未返回有效的模型列表，请手动填写模型 ID")?;
        for item in items {
            let Some(id) = item.get("id").and_then(|value| value.as_str()) else {
                continue;
            };
            // A server error/echo must never expose our API key through a model
            // name, including a malicious gateway returning it in a data item.
            if model_id_valid(id) && !id.contains(&loaded.api_key) && seen.insert(id.to_string()) {
                models.push(id.to_string());
            }
            if models.len() >= MAX_MODELS {
                return Ok(models);
            }
        }
        if loaded.profile.protocol != "anthropic_messages"
            || body.get("has_more").and_then(|value| value.as_bool()) != Some(true)
        {
            return Ok(models);
        }
        let next = body
            .get("last_id")
            .and_then(|value| value.as_str())
            .filter(|id| model_id_valid(id) && !id.contains(&loaded.api_key))
            .ok_or("模型列表分页信息无效")?;
        if cursor.as_deref() == Some(next) {
            return Err("模型列表分页重复，请手动填写模型 ID".into());
        }
        cursor = Some(next.to_string());
    }
    Err("模型列表分页数量超出限制，请手动填写模型 ID".into())
}

pub async fn list_models(data_dir: &Path, id: &str) -> Result<Vec<String>, String> {
    let loaded = load_with(data_dir, id, false, &NativeSecrets)?;
    fetch_models(&loaded).await
}

/// Read-only authentication/catalog check. No prompt, generation, tool call,
/// billing request or local conversation content is sent by this operation.
pub async fn test(data_dir: &Path, id: &str) -> Result<ApiTestResult, String> {
    let models = list_models(data_dir, id).await?;
    Ok(ApiTestResult {
        ok: true,
        message: "连接与模型列表认证成功；未调用模型生成，具体模型及工具能力以实际会话为准".into(),
        model_count: models.len(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::sync::Mutex;

    #[derive(Default)]
    struct MemorySecrets(Mutex<HashMap<String, String>>);

    impl SecretStore for MemorySecrets {
        fn get(&self, id: &str) -> Result<Option<String>, String> {
            Ok(self.0.lock().unwrap().get(id).cloned())
        }
        fn set(&self, id: &str, key: &str) -> Result<(), String> {
            self.0
                .lock()
                .unwrap()
                .insert(id.to_string(), key.to_string());
            Ok(())
        }
        fn delete(&self, id: &str) -> Result<(), String> {
            self.0.lock().unwrap().remove(id);
            Ok(())
        }
    }

    fn input() -> ApiProfileInput {
        ApiProfileInput {
            id: None,
            name: "  OpenAI 工作账号  ".into(),
            protocol: "openai_responses".into(),
            base_url: String::new(),
            enabled: true,
            models: vec!["test-model".into()],
            default_model: "test-model".into(),
            max_output_tokens: 8192,
        }
    }

    fn edit(profile: &ApiProfile) -> ApiProfileInput {
        serde_json::from_value(serde_json::to_value(profile).unwrap()).unwrap()
    }

    #[test]
    fn endpoint_security_and_protocol_defaults() {
        let id = uuid::Uuid::new_v4().to_string();
        let profile = normalize(input(), id.clone()).unwrap();
        assert_eq!(
            endpoint(&profile, "responses").unwrap().as_str(),
            "https://api.openai.com/v1/responses"
        );
        let mut anthropic = input();
        anthropic.protocol = "anthropic_messages".into();
        let profile = normalize(anthropic, id).unwrap();
        assert_eq!(
            endpoint(&profile, "messages").unwrap().as_str(),
            "https://api.anthropic.com/v1/messages"
        );
        assert!(endpoint(&profile, "https://other.example/key").is_err());
        for raw in [
            "http://api.example.com/v1",
            "https://u:secret@api.example.com/v1",
            "https://api.example.com/v1?key=secret",
            "https://api.example.com/v1#fragment",
            "file:///tmp/key",
        ] {
            assert!(
                normalized_base_url("openai_chat", raw).is_err(),
                "accepted {raw}"
            );
        }
        for raw in [
            "http://localhost:1234/v1",
            "http://127.0.0.1:8080/v1",
            "http://[::1]:8080/v1",
            "https://api.example.com/proxy/v1/",
        ] {
            assert!(
                normalized_base_url("openai_chat", raw).is_ok(),
                "rejected {raw}"
            );
        }
        assert!(normalized_base_url("openai_chat", "").is_err());
    }

    #[test]
    fn profile_validation_bounds_and_defaults() {
        let id = uuid::Uuid::new_v4().to_string();
        let mut config = input();
        config.models = vec![" test-model ".into(), "test-model".into()];
        config.default_model.clear();
        let profile = normalize(config, id.clone()).unwrap();
        assert_eq!(profile.name, "OpenAI 工作账号");
        assert_eq!(profile.models, vec!["test-model"]);
        assert_eq!(profile.default_model, "test-model");
        let mut config = input();
        config.default_model = "absent".into();
        assert!(normalize(config, id.clone()).is_err());
        let mut config = input();
        config.max_output_tokens = 0;
        assert!(normalize(config, id.clone()).is_err());
        let mut config = input();
        config.models = vec!["bad\nmodel".into()];
        assert!(normalize(config, id.clone()).is_err());
        let mut config = input();
        config.models.clear();
        config.default_model.clear();
        assert!(normalize(config, id).is_ok());
        assert!(normalized_id("../../outside").is_err());
        assert!(key_input(Some("key\nheader".into())).is_err());
    }

    #[test]
    fn keys_never_enter_profile_file_or_view() {
        let dir = tempfile::tempdir().unwrap();
        let secrets = MemorySecrets::default();
        let view = save_with(
            dir.path(),
            input(),
            Some("secret-never-in-config".into()),
            false,
            &secrets,
        )
        .unwrap();
        assert!(view.key_configured);
        let saved = fs::read_to_string(dir.path().join(FILE_NAME)).unwrap();
        assert!(!saved.contains("secret-never-in-config"));
        assert!(!saved.contains("api_key"));
        assert!(!serde_json::to_string(&view)
            .unwrap()
            .contains("secret-never-in-config"));
        assert!(list_with(dir.path(), &secrets).unwrap()[0].key_configured);
        let loaded = load_with(dir.path(), &view.profile.id, true, &secrets).unwrap();
        assert_eq!(loaded.api_key, "secret-never-in-config");
    }

    #[test]
    fn editing_keeps_key_and_requires_approval_for_new_destination() {
        let dir = tempfile::tempdir().unwrap();
        let secrets = MemorySecrets::default();
        let first = save_with(
            dir.path(),
            input(),
            Some("original-key".into()),
            false,
            &secrets,
        )
        .unwrap();
        let mut update = edit(&first.profile);
        update.name = "Renamed".into();
        let view = save_with(dir.path(), update, Some("  ".into()), false, &secrets).unwrap();
        assert!(view.key_configured);
        assert_eq!(
            secrets.get(&view.profile.id).unwrap().as_deref(),
            Some("original-key")
        );
        let mut update = edit(&view.profile);
        update.base_url = "https://new.example/v1".into();
        assert!(save_with(dir.path(), update.clone(), None, false, &secrets).is_err());
        assert_eq!(
            read_profiles(dir.path()).unwrap()[0].base_url,
            first.profile.base_url
        );
        save_with(dir.path(), update, None, true, &secrets).unwrap();
        let mut update = edit(&read_profiles(dir.path()).unwrap()[0]);
        update.protocol = "openai_chat".into();
        save_with(
            dir.path(),
            update,
            Some("replacement-key".into()),
            false,
            &secrets,
        )
        .unwrap();
        assert_eq!(
            secrets.get(&first.profile.id).unwrap().as_deref(),
            Some("replacement-key")
        );
    }

    #[test]
    fn disabled_and_deleted_profiles_fail_closed() {
        let dir = tempfile::tempdir().unwrap();
        let secrets = MemorySecrets::default();
        let mut config = input();
        config.enabled = false;
        let saved =
            save_with(dir.path(), config, Some("test-key".into()), false, &secrets).unwrap();
        assert!(load_with(dir.path(), &saved.profile.id, true, &secrets).is_err());
        assert!(load_with(dir.path(), &saved.profile.id, false, &secrets).is_ok());
        delete_with(dir.path(), &saved.profile.id, &secrets).unwrap();
        assert!(secrets.get(&saved.profile.id).unwrap().is_none());
        assert!(read_profiles(dir.path()).unwrap().is_empty());
        assert!(save_with(dir.path(), edit(&saved.profile), None, false, &secrets).is_err());
    }

    #[test]
    fn billing_credentials_are_removed_on_destination_edits_and_profile_deletion() {
        let dir = tempfile::tempdir().unwrap();
        let secrets = MemorySecrets::default();
        let saved = save_with(
            dir.path(),
            input(),
            Some("inference-key".into()),
            false,
            &secrets,
        )
        .unwrap();
        let id = &saved.profile.id;
        let billing_id = billing_secret_id(id);
        assert_ne!(secret_account(id), secret_account(&billing_id));
        secrets.set(&billing_id, "admin-key").unwrap();
        let mut renamed = edit(&saved.profile);
        renamed.name = "Renamed".into();
        let renamed = save_with(dir.path(), renamed, None, false, &secrets).unwrap();
        assert_eq!(
            secrets.get(&billing_id).unwrap().as_deref(),
            Some("admin-key")
        );
        let mut changed = edit(&renamed.profile);
        changed.base_url = "https://proxy.test/v1".into();
        assert!(save_with(dir.path(), changed.clone(), None, false, &secrets).is_err());
        assert_eq!(
            secrets.get(&billing_id).unwrap().as_deref(),
            Some("admin-key")
        );
        let changed = save_with(dir.path(), changed, None, true, &secrets).unwrap();
        assert!(secrets.get(&billing_id).unwrap().is_none());
        assert_eq!(secrets.get(id).unwrap().as_deref(), Some("inference-key"));
        secrets.set(&billing_id, "admin-key-2").unwrap();
        let mut protocol = edit(&changed.profile);
        protocol.protocol = "openai_chat".into();
        save_with(dir.path(), protocol, None, true, &secrets).unwrap();
        assert!(secrets.get(&billing_id).unwrap().is_none());
        secrets.set(&billing_id, "admin-key-3").unwrap();
        delete_with(dir.path(), id, &secrets).unwrap();
        assert!(secrets.get(&billing_id).unwrap().is_none());
        assert!(secrets.get(id).unwrap().is_none());
    }

    #[test]
    fn corrupted_configuration_is_never_overwritten() {
        let dir = tempfile::tempdir().unwrap();
        let secrets = MemorySecrets::default();
        fs::write(dir.path().join(FILE_NAME), b"not json").unwrap();
        assert!(save_with(
            dir.path(),
            input(),
            Some("test-key".into()),
            false,
            &secrets
        )
        .is_err());
        assert_eq!(fs::read(dir.path().join(FILE_NAME)).unwrap(), b"not json");
        assert!(secrets.0.lock().unwrap().is_empty());
    }

    #[test]
    fn concurrent_saves_preserve_all_profiles() {
        let dir = tempfile::tempdir().unwrap();
        let secrets = MemorySecrets::default();
        std::thread::scope(|scope| {
            for number in 0..8 {
                let path = dir.path();
                let secrets = &secrets;
                scope.spawn(move || {
                    let mut config = input();
                    config.name = format!("Connection {number}");
                    save_with(path, config, None, false, secrets).unwrap();
                });
            }
        });
        assert_eq!(read_profiles(dir.path()).unwrap().len(), 8);
        assert_eq!(fs::read_dir(dir.path()).unwrap().count(), 2);
    }

    #[test]
    fn error_messages_never_use_untrusted_response_bodies() {
        for code in [301, 400, 401, 403, 404, 429, 500] {
            let error = status_error(reqwest::StatusCode::from_u16(code).unwrap());
            assert!(error.contains(&code.to_string()));
            assert!(!error.contains("api_key"));
        }
    }

    async fn mock_server(
        status: &str,
        headers: &str,
        body: &str,
    ) -> (String, tokio::task::JoinHandle<String>) {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let server = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/v1", server.local_addr().unwrap());
        let response = format!(
            "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n{headers}\r\n{body}",
            body.len()
        );
        let handle = tokio::spawn(async move {
            let (mut socket, _) = server.accept().await.unwrap();
            let mut request = Vec::new();
            let mut buffer = [0u8; 1024];
            loop {
                let count = socket.read(&mut buffer).await.unwrap();
                if count == 0 {
                    break;
                }
                request.extend_from_slice(&buffer[..count]);
                if request.windows(4).any(|part| part == b"\r\n\r\n") {
                    break;
                }
                assert!(request.len() < 16384);
            }
            socket.write_all(response.as_bytes()).await.unwrap();
            String::from_utf8(request).unwrap()
        });
        (url, handle)
    }

    #[tokio::test]
    async fn model_discovery_uses_only_get_and_protocol_headers() {
        for protocol in ["openai_responses", "openai_chat", "anthropic_messages"] {
            let (url, request) =
                mock_server("200 OK", "", r#"{"data":[{"id":"chat-model"}]}"#).await;
            let mut profile = normalize(input(), uuid::Uuid::new_v4().to_string()).unwrap();
            profile.protocol = protocol.into();
            profile.base_url = url;
            let loaded = LoadedApiProfile {
                profile,
                api_key: "synthetic-api-key-for-local-tests".into(),
            };
            assert_eq!(fetch_models(&loaded).await.unwrap(), vec!["chat-model"]);
            let request = request.await.unwrap().to_lowercase();
            assert!(request.starts_with("get /v1/models"));
            assert!(!request.contains("/responses"));
            assert!(!request.contains("/messages"));
            if protocol == "anthropic_messages" {
                assert!(request.contains("x-api-key: synthetic-api-key-for-local-tests"));
                assert!(request.contains("anthropic-version: 2023-06-01"));
                assert!(!request.contains("authorization:"));
            } else {
                assert!(request.contains("authorization: bearer synthetic-api-key-for-local-tests"));
                assert!(!request.contains("x-api-key:"));
            }
        }
    }

    #[tokio::test]
    async fn redirects_cannot_forward_credentials() {
        let destination = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let headers = format!(
            "Location: http://{}/stolen\r\n",
            destination.local_addr().unwrap()
        );
        let (url, request) = mock_server("302 Found", &headers, "").await;
        let mut profile = normalize(input(), uuid::Uuid::new_v4().to_string()).unwrap();
        profile.base_url = url;
        let loaded = LoadedApiProfile {
            profile,
            api_key: "synthetic-local-key".into(),
        };
        let error = fetch_models(&loaded).await.unwrap_err();
        assert!(error.contains("HTTP 302"));
        request.await.unwrap();
        assert!(
            tokio::time::timeout(Duration::from_millis(50), destination.accept())
                .await
                .is_err()
        );
    }

    #[tokio::test]
    async fn provider_error_body_and_secret_echo_are_not_exposed() {
        let key = "synthetic-secret-not-for-display";
        let error_body = format!(r#"{{"error":{{"message":"invalid key {key}"}}}}"#);
        let (url, request) = mock_server("401 Unauthorized", "", &error_body).await;
        let mut profile = normalize(input(), uuid::Uuid::new_v4().to_string()).unwrap();
        profile.base_url = url;
        let mut loaded = LoadedApiProfile {
            profile,
            api_key: key.into(),
        };
        let error = fetch_models(&loaded).await.unwrap_err();
        assert!(!error.contains(key));
        assert!(error.contains("HTTP 401"));
        request.await.unwrap();
        let body = format!(r#"{{"data":[{{"id":"{key}"}},{{"id":"normal-model"}}]}}"#);
        let (url, request) = mock_server("200 OK", "", &body).await;
        loaded.profile.base_url = url;
        assert_eq!(fetch_models(&loaded).await.unwrap(), vec!["normal-model"]);
        request.await.unwrap();
    }
}
