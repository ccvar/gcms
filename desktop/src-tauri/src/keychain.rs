//! 平台密钥只进 macOS Keychain，绝不落盘、绝不进 WebView。
//! service = bundle id（冻结值），account = 连接 id。
//!
//! 直接链 keyring-core + apple-native-keyring-store：keyring 4.1.3 的 v1 兼容层
//! 有一个 compare_exchange 误用（成功返回旧值 Ok(false)，永远 != Ok(true)），
//! 导致默认 store 从未安装，Entry::new 必报 "No default store has been set"。

use std::collections::{HashMap, HashSet};
use std::sync::{Mutex, OnceLock};

const SERVICE: &str = "com.ccvar.gcms.pilot";
const CLAUDE_CREDENTIAL_ACCOUNT: &str = "brain:claude:credential";
const VAULT_ACCOUNT: &str = "pilot:credential-vault:v1";

#[derive(serde::Serialize, serde::Deserialize)]
struct ClaudeCredential {
    secret: String,
}

/// 所有 Pilot 凭据收敛到同一个系统安全存储条目。
///
/// macOS 的钥匙串授权是按条目判断的；开发版 ad-hoc 签名每次构建都会变化，旧实现的
/// “一个连接一个条目”会在启动时连续询问多次。统一保险库后每个进程只读取一次条目，
/// 仍由系统钥匙串保护，但授权频次不再随连接数量增长。
#[derive(Default, serde::Serialize, serde::Deserialize)]
struct CredentialVault {
    #[serde(default)]
    secrets: HashMap<String, String>,
    /// 迁移记录，仅用于兼容已经落盘的 v1 格式；不能作为禁止回退旧条目的依据。
    /// 早期版本会在临时读不到旧条目时也写入这里，若据此跳过恢复会永久丢失凭据。
    #[serde(default)]
    legacy_resolved: HashSet<String>,
}

#[derive(Default)]
struct VaultCache {
    loaded: bool,
    vault: CredentialVault,
    /// 本进程已经确认不存在的旧条目。只放内存、不写进保险库：另一个 Pilot 进程
    /// 可能稍后写回兼容条目，下次启动必须允许重新探测，不能永久判死刑。
    legacy_missing: HashSet<String>,
}

/// 进程内只装一次 macOS 原生 Keychain store。
fn ensure_store() -> Result<(), String> {
    static INIT: OnceLock<Result<(), String>> = OnceLock::new();
    INIT.get_or_init(|| {
        #[cfg(target_os = "macos")]
        let store = apple_native_keyring_store::keychain::Store::new()
            .map_err(|e| format!("初始化钥匙串存储失败: {e}"))?;
        #[cfg(target_os = "windows")]
        let store = windows_native_keyring_store::Store::new()
            .map_err(|e| format!("初始化凭据存储失败: {e}"))?;
        #[cfg(not(any(target_os = "macos", target_os = "windows")))]
        return Err("当前平台暂不支持原生密钥存储".to_string());
        #[cfg(any(target_os = "macos", target_os = "windows"))]
        {
            keyring_core::set_default_store(store);
            Ok(())
        }
    })
    .clone()
}

fn entry(conn_id: &str) -> Result<keyring_core::Entry, String> {
    ensure_store()?;
    keyring_core::Entry::new(SERVICE, conn_id).map_err(|e| format!("keychain entry: {e}"))
}

fn vault_cache() -> &'static Mutex<VaultCache> {
    static CACHE: OnceLock<Mutex<VaultCache>> = OnceLock::new();
    CACHE.get_or_init(|| Mutex::new(VaultCache::default()))
}

fn read_vault_from_store() -> Result<CredentialVault, String> {
    let vault = match entry(VAULT_ACCOUNT)?.get_password() {
        Ok(raw) => {
            serde_json::from_str(&raw).map_err(|error| format!("凭据保险库格式损坏：{error}"))?
        }
        Err(keyring_core::Error::NoEntry) => CredentialVault::default(),
        Err(error) => return Err(format!("credential vault read: {error}")),
    };
    Ok(vault)
}

fn load_vault(cache: &mut VaultCache) -> Result<(), String> {
    if cache.loaded {
        return Ok(());
    }
    cache.vault = read_vault_from_store()?;
    cache.loaded = true;
    Ok(())
}

fn save_vault(cache: &VaultCache) -> Result<(), String> {
    let raw = serde_json::to_string(&cache.vault)
        .map_err(|error| format!("credential vault encode: {error}"))?;
    entry(VAULT_ACCOUNT)?
        .set_password(&raw)
        .map_err(|error| format!("credential vault write: {error}"))
}

/// 写入前必须重新读取系统里的最新保险库再合并。正式版和 Dev 版是两个进程，各自的
/// 内存缓存可能不同；直接把旧缓存整包写回会把另一个进程刚保存的连接凭据抹掉。
fn merge_secret_into_vault(
    cache: &mut VaultCache,
    account: &str,
    value: &str,
) -> Result<(), String> {
    let mut latest = read_vault_from_store()?;
    latest
        .secrets
        .insert(account.to_string(), value.to_string());
    latest.legacy_resolved.insert(account.to_string());
    cache.vault = latest;
    cache.loaded = true;
    save_vault(cache)
}

fn should_read_legacy(cache: &VaultCache, account: &str) -> bool {
    !cache.vault.secrets.contains_key(account) && !cache.legacy_missing.contains(account)
}

fn read_secret(account: &str) -> Result<Option<String>, String> {
    if account == VAULT_ACCOUNT {
        return Err("凭据 account 与内部保险库冲突".into());
    }
    let mut cache = vault_cache().lock().unwrap();
    load_vault(&mut cache)?;
    if let Some(value) = cache.vault.secrets.get(account) {
        return Ok(Some(value.clone()));
    }
    if !should_read_legacy(&cache, account) {
        return Ok(None);
    }

    // 旧版本每个连接各存一条。保险库缺项时回退读取，并修复保险库。NoEntry 只在
    // 本进程缓存，不能持久化：正式版与 dev 并行时，另一个进程可能随后写入旧条目。
    let legacy = entry(account)?;
    let value = match legacy.get_password() {
        Ok(value) => Some(value),
        Err(keyring_core::Error::NoEntry) => {
            cache.legacy_missing.insert(account.to_string());
            None
        }
        Err(error) => return Err(format!("credential read: {error}")),
    };
    if let Some(value) = value.as_ref() {
        merge_secret_into_vault(&mut cache, account, value)?;
        cache.legacy_missing.remove(account);
    }
    Ok(value)
}

fn write_secret(account: &str, value: &str) -> Result<(), String> {
    if account == VAULT_ACCOUNT {
        return Err("凭据 account 与内部保险库冲突".into());
    }
    let mut cache = vault_cache().lock().unwrap();
    // 保留一份旧格式兼容条目作为恢复副本。正常读取始终命中统一保险库，不会因此
    // 增加启动授权框；只有保险库被另一进程的旧快照覆盖时才会访问它并自动修复。
    entry(account)?
        .set_password(value)
        .map_err(|error| format!("credential backup write: {error}"))?;
    merge_secret_into_vault(&mut cache, account, value)?;
    cache.legacy_missing.remove(account);
    Ok(())
}

fn remove_secret(account: &str) -> Result<(), String> {
    if account == VAULT_ACCOUNT {
        return Err("凭据 account 与内部保险库冲突".into());
    }
    let mut cache = vault_cache().lock().unwrap();
    // 删除也基于系统里的最新版本，不能用进程启动时的旧缓存覆盖其他进程的新增项。
    cache.vault = read_vault_from_store()?;
    cache.loaded = true;
    cache.vault.secrets.remove(account);
    cache.vault.legacy_resolved.insert(account.to_string());
    cache.legacy_missing.insert(account.to_string());
    save_vault(&cache)?;
    match entry(account)?.delete_credential() {
        Ok(()) | Err(keyring_core::Error::NoEntry) => Ok(()),
        Err(error) => Err(format!("credential legacy delete: {error}")),
    }
}

/// Pilot 控制台等附属凭据使用独立 account 命名空间，避免覆盖技能包连接密钥。
pub fn set_named_secret(account: &str, value: &str) -> Result<(), String> {
    write_secret(account, value)
}

pub fn get_named_secret(account: &str) -> Result<String, String> {
    read_secret(account)?.ok_or_else(|| "credential read: NoEntry".to_string())
}

pub fn delete_named_secret(account: &str) -> Result<(), String> {
    remove_secret(account)
}

/// 保存用户显式粘贴的 Claude CLI 凭据。完整值只进入系统安全存储；调用方和前端
/// 永远只处理 kind，不读取或展示 secret。
pub fn set_claude_oauth_token(secret: &str) -> Result<(), String> {
    let value = serde_json::to_string(&ClaudeCredential {
        secret: secret.to_string(),
    })
    .map_err(|e| format!("credential encode: {e}"))?;
    set_named_secret(CLAUDE_CREDENTIAL_ACCOUNT, &value)
}

/// 返回 Claude Code 官方识别的环境变量和值。NoEntry 表示用户没在 Pilot 中配置，
/// 此时继续使用 CLI 自己的 OAuth/keychain 登录，不视为错误。
pub fn claude_oauth_token() -> Result<Option<String>, String> {
    let Some(raw) = read_secret(CLAUDE_CREDENTIAL_ACCOUNT)? else {
        return Ok(None);
    };
    let credential: ClaudeCredential =
        serde_json::from_str(&raw).map_err(|e| format!("credential decode: {e}"))?;
    if credential.secret.trim().is_empty() {
        return Err("Claude 订阅 Token 为空，请重新粘贴".into());
    }
    Ok(Some(credential.secret))
}

pub fn delete_claude_oauth_token() -> Result<(), String> {
    delete_named_secret(CLAUDE_CREDENTIAL_ACCOUNT)
}

pub fn set_key(conn_id: &str, key: &str) -> Result<(), String> {
    write_secret(conn_id, key)
}

pub fn get_key(conn_id: &str) -> Result<String, String> {
    read_secret(conn_id)?.ok_or_else(|| "keychain read: NoEntry".to_string())
}

/// 认证失败时从旧的逐连接备份条目重新读取一次，并把较新的值合并回统一保险库。
///
/// 平时仍只读取统一保险库，因此不会恢复成“连接越多、授权框越多”。只有服务端明确
/// 拒绝了保险库里的凭据时，调用方才走这条恢复路径。这样人工轮换密钥、或旧进程用
/// 过期快照覆盖保险库后，都能由逐连接备份原地修复，而不需要删除连接和会话。
pub fn refresh_key_from_legacy(conn_id: &str) -> Result<Option<String>, String> {
    if conn_id == VAULT_ACCOUNT {
        return Err("凭据 account 与内部保险库冲突".into());
    }
    let legacy = entry(conn_id)?;
    let value = match legacy.get_password() {
        Ok(value) => Some(value),
        Err(keyring_core::Error::NoEntry) => None,
        Err(error) => return Err(format!("credential recovery read: {error}")),
    };
    let Some(value) = value else {
        return Ok(None);
    };
    let mut cache = vault_cache().lock().unwrap();
    merge_secret_into_vault(&mut cache, conn_id, &value)?;
    cache.legacy_missing.remove(conn_id);
    Ok(Some(value))
}

pub fn delete_key(conn_id: &str) -> Result<(), String> {
    remove_secret(conn_id)
}

/// 只暴露前缀用于 UI 展示（gcmsp_ab12…），完整 key 永不出 Rust 层。
/// 按字符截取（字节切片在多字节字符边界会 panic）。
pub fn key_prefix(key: &str) -> String {
    let mut p: String = key.chars().take(13).collect();
    p.push('…');
    p
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn vault_schema_loads_pre_tombstone_data() {
        let vault: CredentialVault =
            serde_json::from_str(r#"{"secrets":{"connection-1":"secret"}}"#).unwrap();
        assert_eq!(
            vault.secrets.get("connection-1").map(String::as_str),
            Some("secret")
        );
        assert!(vault.legacy_resolved.is_empty());
    }

    #[test]
    fn persisted_migration_marker_does_not_block_recovery() {
        let mut vault = CredentialVault::default();
        vault.legacy_resolved.insert("recoverable".into());
        let encoded = serde_json::to_string(&vault).unwrap();
        let vault: CredentialVault = serde_json::from_str(&encoded).unwrap();
        let mut cache = VaultCache {
            loaded: true,
            vault,
            ..VaultCache::default()
        };

        assert!(should_read_legacy(&cache, "recoverable"));
        cache.legacy_missing.insert("recoverable".into());
        assert!(!should_read_legacy(&cache, "recoverable"));
    }

    /// 真实 Keychain 往返（写→读→删）。默认 ignore，本机手动跑：
    /// cargo test -- --ignored keychain_roundtrip
    #[test]
    #[ignore]
    fn keychain_roundtrip() {
        let account = format!("self-test-{}", uuid::Uuid::new_v4());
        set_key(&account, "gcmsp_roundtrip_secret").unwrap();
        assert_eq!(get_key(&account).unwrap(), "gcmsp_roundtrip_secret");
        delete_key(&account).unwrap();
        // 二次删除应幂等成功。
        delete_key(&account).unwrap();
        assert!(get_key(&account).is_err());
    }
}
