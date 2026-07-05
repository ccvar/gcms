//! 平台密钥只进 macOS Keychain，绝不落盘、绝不进 WebView。
//! service = bundle id（冻结值），account = 连接 id。
//!
//! 直接链 keyring-core + apple-native-keyring-store：keyring 4.1.3 的 v1 兼容层
//! 有一个 compare_exchange 误用（成功返回旧值 Ok(false)，永远 != Ok(true)），
//! 导致默认 store 从未安装，Entry::new 必报 "No default store has been set"。

use std::sync::OnceLock;

const SERVICE: &str = "com.ccvar.gcms.pilot";

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

pub fn set_key(conn_id: &str, key: &str) -> Result<(), String> {
    entry(conn_id)?
        .set_password(key)
        .map_err(|e| format!("keychain write: {e}"))
}

pub fn get_key(conn_id: &str) -> Result<String, String> {
    entry(conn_id)?
        .get_password()
        .map_err(|e| format!("keychain read: {e}"))
}

pub fn delete_key(conn_id: &str) -> Result<(), String> {
    match entry(conn_id)?.delete_credential() {
        Ok(()) => Ok(()),
        // 已经不存在视为删除成功（重复删除、手动清理过）。
        Err(keyring_core::Error::NoEntry) => Ok(()),
        Err(e) => Err(format!("keychain delete: {e}")),
    }
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
