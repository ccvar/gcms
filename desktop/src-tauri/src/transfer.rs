//! Pilot 配置与会话迁移。
//!
//! 迁移包是一个版本化 JSON 文件。普通配置可以明文导出；一旦包含钥匙串凭据或
//! SSH 私钥，载荷会用 Argon2id 派生密钥 + AES-256-GCM 加密。导入只按显式选择的
//! 分类写入，不会把密钥打印到预览、日志或错误信息里。

use aes_gcm::{
    aead::{Aead, KeyInit},
    Aes256Gcm, Nonce,
};
use argon2::{Algorithm, Argon2, ParamsBuilder, Version};
use base64::{engine::general_purpose::STANDARD as B64, Engine as _};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::HashMap;
use std::fs;
use std::path::{Component, Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use crate::convo::Conversation;
use crate::managed::ManagedSite;
use crate::pack::Connection;
use crate::tasks::ScheduledTask;
use crate::{cf_templates, keychain, AppState};

const FORMAT: &str = "gcms-pilot-transfer";
const VERSION: u32 = 1;

#[derive(Clone, Serialize, Deserialize, Default)]
pub(super) struct TransferSelection {
    #[serde(default)]
    pub all: bool,
    #[serde(default)]
    pub connections: bool,
    #[serde(default)]
    pub sessions: bool,
    #[serde(default)]
    pub tasks: bool,
    #[serde(default)]
    pub managed: bool,
    #[serde(default)]
    pub templates: bool,
    #[serde(default)]
    pub preferences: bool,
}

impl TransferSelection {
    fn connections(&self) -> bool {
        self.all || self.connections
    }
    fn sessions(&self) -> bool {
        self.all || self.sessions
    }
    fn tasks(&self) -> bool {
        self.all || self.tasks
    }
    fn managed(&self) -> bool {
        self.all || self.managed
    }
    fn templates(&self) -> bool {
        self.all || self.templates
    }
    fn preferences(&self) -> bool {
        self.all || self.preferences
    }
}

#[derive(Clone, Serialize, Deserialize)]
struct TransferFile {
    path: String,
    bytes: String,
}

#[derive(Clone, Serialize, Deserialize)]
struct TransferTemplate {
    slug: String,
    name: String,
    desc: String,
    category: String,
    created_at: u64,
    files: Vec<TransferFile>,
}

#[derive(Clone, Serialize, Deserialize)]
struct TransferConnection {
    connection: Connection,
    #[serde(default)]
    secret: Option<String>,
    #[serde(default)]
    ssh_private_key: Option<String>,
}

#[derive(Clone, Serialize, Deserialize, Default)]
struct TransferPayload {
    #[serde(default)]
    connections: Vec<TransferConnection>,
    #[serde(default)]
    conversations: Vec<Conversation>,
    #[serde(default)]
    tasks: Vec<ScheduledTask>,
    #[serde(default)]
    managed: Vec<ManagedSite>,
    #[serde(default)]
    templates: Vec<TransferTemplate>,
    #[serde(default)]
    preferences: Option<Value>,
}

#[derive(Serialize, Deserialize)]
struct TransferEnvelope {
    format: String,
    version: u32,
    created_at: u64,
    encrypted: bool,
    kdf: String,
    cipher: String,
    salt: String,
    nonce: String,
    payload: String,
}

#[derive(Clone, Serialize, Default)]
pub(super) struct TransferCounts {
    pub connections: usize,
    pub sessions: usize,
    pub tasks: usize,
    pub managed: usize,
    pub templates: usize,
    pub preferences: bool,
    pub secrets: bool,
}

#[derive(Clone, Serialize)]
pub(super) struct TransferExportResult {
    pub path: String,
    pub encrypted: bool,
    pub counts: TransferCounts,
}

#[derive(Clone, Serialize)]
pub(super) struct TransferPreview {
    pub encrypted: bool,
    pub version: u32,
    pub counts: TransferCounts,
}

#[derive(Clone, Serialize)]
pub(super) struct TransferImportResult {
    pub imported: TransferCounts,
    pub skipped: TransferCounts,
    pub backup_path: String,
    /// localStorage 中的 Pilot 偏好由前端落回；后端不把它写进磁盘配置。
    pub preferences: Option<Value>,
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

fn counts(payload: &TransferPayload) -> TransferCounts {
    let secrets = payload.connections.iter().any(|c| {
        c.secret.as_ref().is_some_and(|s| !s.is_empty())
            || c.ssh_private_key.as_ref().is_some_and(|s| !s.is_empty())
    });
    TransferCounts {
        connections: payload.connections.len(),
        sessions: payload.conversations.len(),
        tasks: payload.tasks.len(),
        managed: payload.managed.len(),
        templates: payload.templates.len(),
        preferences: payload.preferences.is_some(),
        secrets,
    }
}

fn derive_key(password: &str, salt: &[u8]) -> Result<[u8; 32], String> {
    let params = ParamsBuilder::new()
        .m_cost(32 * 1024)
        .t_cost(3)
        .p_cost(1)
        .output_len(32)
        .build()
        .map_err(|e| format!("初始化加密参数失败：{e}"))?;
    let argon = Argon2::new(Algorithm::Argon2id, Version::V0x13, params);
    let mut key = [0u8; 32];
    argon
        .hash_password_into(password.as_bytes(), salt, &mut key)
        .map_err(|e| format!("生成加密密钥失败：{e}"))?;
    Ok(key)
}

fn encrypt_payload(raw: &[u8], password: &str) -> Result<(String, String, String), String> {
    if password.chars().count() < 8 {
        return Err("加密导出密码至少需要 8 个字符".into());
    }
    let salt = uuid::Uuid::new_v4().into_bytes();
    let nonce = uuid::Uuid::new_v4().into_bytes()[..12].to_vec();
    let key = derive_key(password, &salt)?;
    let cipher = Aes256Gcm::new_from_slice(&key).map_err(|e| format!("初始化加密失败：{e}"))?;
    let nonce = Nonce::try_from(nonce.as_slice()).map_err(|_| "生成加密随机数失败".to_string())?;
    let encrypted = cipher
        .encrypt(&nonce, raw)
        .map_err(|_| "配置包加密失败".to_string())?;
    Ok((B64.encode(salt), B64.encode(nonce), B64.encode(encrypted)))
}

fn decrypt_payload(envelope: &TransferEnvelope, password: &str) -> Result<Vec<u8>, String> {
    if password.is_empty() {
        return Err("这是加密配置包，请输入导出密码".into());
    }
    let salt = B64
        .decode(&envelope.salt)
        .map_err(|_| "配置包盐值损坏".to_string())?;
    let nonce = B64
        .decode(&envelope.nonce)
        .map_err(|_| "配置包随机数损坏".to_string())?;
    let ciphertext = B64
        .decode(&envelope.payload)
        .map_err(|_| "配置包内容损坏".to_string())?;
    if nonce.len() != 12 {
        return Err("配置包随机数长度不正确".into());
    }
    let key = derive_key(password, &salt)?;
    let cipher = Aes256Gcm::new_from_slice(&key).map_err(|e| format!("初始化解密失败：{e}"))?;
    let nonce =
        Nonce::try_from(nonce.as_slice()).map_err(|_| "配置包随机数长度不正确".to_string())?;
    cipher
        .decrypt(&nonce, ciphertext.as_ref())
        .map_err(|_| "解密失败：密码不正确或配置包已损坏".into())
}

fn read_payload(path: &str, password: &str) -> Result<(TransferEnvelope, TransferPayload), String> {
    let raw = fs::read(path).map_err(|e| format!("读取配置包失败：{e}"))?;
    let envelope: TransferEnvelope =
        serde_json::from_slice(&raw).map_err(|e| format!("配置包格式无效：{e}"))?;
    if envelope.format != FORMAT {
        return Err("这不是 GCMS Pilot 配置包".into());
    }
    if envelope.version > VERSION {
        return Err(format!(
            "配置包版本过新（v{}），请先升级 Pilot",
            envelope.version
        ));
    }
    let bytes = if envelope.encrypted {
        decrypt_payload(&envelope, password)?
    } else {
        B64.decode(&envelope.payload)
            .map_err(|_| "配置包内容损坏".to_string())?
    };
    let payload = serde_json::from_slice(&bytes).map_err(|e| format!("配置包数据无效：{e}"))?;
    Ok((envelope, payload))
}

fn selected_payload(
    state: &AppState,
    selection: &TransferSelection,
    include_secrets: bool,
    preferences: Option<Value>,
) -> Result<TransferPayload, String> {
    let connections = if selection.connections() {
        state
            .conns
            .list()
            .into_iter()
            .map(|connection| {
                let secret = if include_secrets {
                    keychain::get_key(&connection.id).ok()
                } else {
                    None
                };
                let ssh_private_key = if include_secrets
                    && connection.kind == "ssh"
                    && connection.ssh_auth == "key"
                {
                    fs::read_to_string(&connection.ssh_key_path).ok()
                } else {
                    None
                };
                TransferConnection {
                    connection,
                    secret,
                    ssh_private_key,
                }
            })
            .collect()
    } else {
        Vec::new()
    };
    let conversations = if selection.sessions() {
        state.convos.list()
    } else {
        Vec::new()
    };
    let tasks = if selection.tasks() {
        state.tasks.list()
    } else {
        Vec::new()
    };
    let managed = if selection.managed() {
        state.managed.list()
    } else {
        Vec::new()
    };
    let templates = if selection.templates() {
        export_templates(&state.data_dir)?
    } else {
        Vec::new()
    };
    Ok(TransferPayload {
        connections,
        conversations,
        tasks,
        managed,
        templates,
        preferences: if selection.preferences() {
            preferences
        } else {
            None
        },
    })
}

fn export_templates(data_dir: &Path) -> Result<Vec<TransferTemplate>, String> {
    let mut out = Vec::new();
    for template in cf_templates::list(&data_dir.join("templates")) {
        if template.builtin {
            continue;
        }
        let root = data_dir.join("templates").join(&template.slug);
        let mut files = Vec::new();
        collect_files(&root, &root, &mut files)?;
        out.push(TransferTemplate {
            slug: template.slug,
            name: template.name,
            desc: template.desc,
            category: template.category,
            created_at: template.created_at,
            files,
        });
    }
    Ok(out)
}

fn collect_files(root: &Path, dir: &Path, out: &mut Vec<TransferFile>) -> Result<(), String> {
    for entry in fs::read_dir(dir).map_err(|e| format!("读取模板目录失败：{e}"))? {
        let entry = entry.map_err(|e| format!("读取模板目录失败：{e}"))?;
        let path = entry.path();
        if path.is_dir() {
            collect_files(root, &path, out)?;
        } else if path.is_file() {
            let rel = path.strip_prefix(root).map_err(|e| e.to_string())?;
            let rel = rel.to_string_lossy().replace('\\', "/");
            let bytes = fs::read(&path).map_err(|e| format!("读取模板文件失败：{e}"))?;
            out.push(TransferFile {
                path: rel,
                bytes: B64.encode(bytes),
            });
        }
    }
    Ok(())
}

fn write_envelope(
    path: &str,
    payload: &TransferPayload,
    include_secrets: bool,
    password: &str,
) -> Result<bool, String> {
    if include_secrets && password.is_empty() {
        return Err("包含敏感凭据时必须设置配置包密码".into());
    }
    let raw = serde_json::to_vec(payload).map_err(|e| format!("序列化配置失败：{e}"))?;
    let (encrypted, salt, nonce, encoded) = if include_secrets {
        let (salt, nonce, encoded) = encrypt_payload(&raw, password)?;
        (true, salt, nonce, encoded)
    } else {
        (false, String::new(), String::new(), B64.encode(raw))
    };
    let envelope = TransferEnvelope {
        format: FORMAT.into(),
        version: VERSION,
        created_at: now(),
        encrypted,
        kdf: if encrypted {
            "Argon2id".into()
        } else {
            String::new()
        },
        cipher: if encrypted {
            "AES-256-GCM".into()
        } else {
            String::new()
        },
        salt,
        nonce,
        payload: encoded,
    };
    let bytes = serde_json::to_vec_pretty(&envelope).map_err(|e| format!("写入配置包失败：{e}"))?;
    fs::write(path, bytes).map_err(|e| format!("保存配置包失败：{e}"))?;
    Ok(encrypted)
}

fn safe_relative(path: &str) -> Result<PathBuf, String> {
    let p = Path::new(path);
    if p.is_absolute()
        || p.components().any(|c| {
            matches!(
                c,
                Component::ParentDir | Component::RootDir | Component::Prefix(_)
            )
        })
    {
        return Err("模板包包含非法路径".into());
    }
    Ok(p.to_path_buf())
}

fn safe_slug(slug: &str) -> bool {
    !slug.is_empty()
        && slug.len() <= 80
        && slug
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-' || c == '_')
}

fn unique_slug(root: &Path, slug: &str) -> String {
    if !root.join(slug).exists() {
        return slug.to_string();
    }
    for i in 2..1000 {
        let candidate = format!("{slug}-copy-{i}");
        if !root.join(&candidate).exists() {
            return candidate;
        }
    }
    format!("{slug}-{}", &uuid::Uuid::new_v4().to_string()[..8])
}

fn write_template(root: &Path, template: &TransferTemplate, mode: &str) -> Result<bool, String> {
    if !safe_slug(&template.slug) {
        return Err("模板 slug 不合法".into());
    }
    fs::create_dir_all(root).map_err(|e| format!("创建模板目录失败：{e}"))?;
    let slug = if mode == "duplicate" {
        unique_slug(root, &template.slug)
    } else {
        template.slug.clone()
    };
    let dir = root.join(&slug);
    if dir.exists() {
        if mode == "incremental" {
            return Ok(false);
        }
        fs::remove_dir_all(&dir).map_err(|e| format!("替换模板失败：{e}"))?;
    }
    fs::create_dir_all(&dir).map_err(|e| format!("创建模板目录失败：{e}"))?;
    for file in &template.files {
        let rel = safe_relative(&file.path)?;
        let dest = dir.join(rel);
        if let Some(parent) = dest.parent() {
            fs::create_dir_all(parent).map_err(|e| format!("创建模板子目录失败：{e}"))?;
        }
        let bytes = B64
            .decode(&file.bytes)
            .map_err(|_| "模板文件内容损坏".to_string())?;
        fs::write(dest, bytes).map_err(|e| format!("写入模板文件失败：{e}"))?;
    }
    let manifest = serde_json::json!({"name": template.name, "desc": template.desc, "category": template.category, "created_at": template.created_at});
    fs::write(
        dir.join("pilot-template.json"),
        serde_json::to_vec_pretty(&manifest).unwrap(),
    )
    .map_err(|e| format!("写入模板清单失败：{e}"))?;
    Ok(true)
}

fn build_backup(
    state: &AppState,
    selection: &TransferSelection,
    password: &str,
) -> Result<String, String> {
    let payload = selected_payload(state, selection, !password.is_empty(), None)?;
    let path = state
        .data_dir
        .join(format!("pilot-transfer-backup-{}.json", now()));
    write_envelope(
        path.to_string_lossy().as_ref(),
        &payload,
        !password.is_empty(),
        password,
    )?;
    Ok(path.to_string_lossy().into_owned())
}

fn remap_id(map: &HashMap<String, String>, id: &str) -> String {
    map.get(id).cloned().unwrap_or_else(|| id.to_string())
}

#[tauri::command]
pub(super) fn export_pilot_transfer(
    state: tauri::State<'_, AppState>,
    path: String,
    selection: TransferSelection,
    include_secrets: bool,
    password: String,
    preferences: Option<Value>,
) -> Result<TransferExportResult, String> {
    if path.trim().is_empty() {
        return Err("请选择导出文件位置".into());
    }
    let payload = selected_payload(&state, &selection, include_secrets, preferences)?;
    let encrypted = write_envelope(&path, &payload, include_secrets, &password)?;
    Ok(TransferExportResult {
        path,
        encrypted,
        counts: counts(&payload),
    })
}

#[tauri::command]
pub(super) fn inspect_pilot_transfer(
    path: String,
    password: String,
) -> Result<TransferPreview, String> {
    let (envelope, payload) = read_payload(&path, &password)?;
    Ok(TransferPreview {
        encrypted: envelope.encrypted,
        version: envelope.version,
        counts: counts(&payload),
    })
}

#[tauri::command]
pub(super) fn import_pilot_transfer(
    state: tauri::State<'_, AppState>,
    path: String,
    password: String,
    mode: String,
    selection: TransferSelection,
) -> Result<TransferImportResult, String> {
    if !matches!(mode.as_str(), "incremental" | "overwrite" | "duplicate") {
        return Err("未知导入模式".into());
    }
    let (_, payload) = read_payload(&path, &password)?;
    let backup_path = if mode == "overwrite" {
        build_backup(&state, &selection, &password)?
    } else {
        String::new()
    };
    let mut imported = TransferCounts::default();
    let mut skipped = TransferCounts::default();
    let mut conn_map = HashMap::new();
    let mut conv_map = HashMap::new();
    let mut task_map = HashMap::new();
    let existing = state.conns.list();

    if selection.connections() {
        for item in payload.connections {
            let old_id = item.connection.id.clone();
            let exists = existing.iter().any(|c| c.id == old_id);
            if mode == "incremental" && exists {
                conn_map.insert(old_id, item.connection.id.clone());
                skipped.connections += 1;
                continue;
            }
            let target_id = if mode == "duplicate" {
                uuid::Uuid::new_v4().to_string()
            } else {
                old_id.clone()
            };
            conn_map.insert(old_id, target_id.clone());
            let mut connection = item.connection;
            let old_local = existing.iter().find(|c| c.id == target_id);
            connection.id = target_id.clone();
            if let Some(local) = old_local {
                connection.skill_dir = local.skill_dir.clone();
            } else if !Path::new(&connection.skill_dir).exists() {
                connection.skill_dir = state
                    .data_dir
                    .join("packs")
                    .join(&target_id)
                    .to_string_lossy()
                    .into_owned();
                fs::create_dir_all(&connection.skill_dir)
                    .map_err(|e| format!("创建连接工作区失败：{e}"))?;
            }
            if let Some(private_key) = item.ssh_private_key.filter(|x| !x.is_empty()) {
                let key_dir = state.data_dir.join("ssh-keys");
                fs::create_dir_all(&key_dir).map_err(|e| format!("创建私钥目录失败：{e}"))?;
                let key_path = key_dir.join(format!("{target_id}.key"));
                fs::write(&key_path, private_key).map_err(|e| format!("保存 SSH 私钥失败：{e}"))?;
                #[cfg(unix)]
                {
                    use std::os::unix::fs::PermissionsExt;
                    fs::set_permissions(&key_path, fs::Permissions::from_mode(0o600)).ok();
                }
                connection.ssh_key_path = key_path.to_string_lossy().into_owned();
            }
            if let Some(secret) = item.secret.filter(|x| !x.is_empty()) {
                keychain::set_key(&target_id, &secret)?;
            }
            state.conns.upsert_imported(connection)?;
            imported.connections += 1;
        }
    }

    if selection.sessions() {
        for mut conversation in payload.conversations {
            let old_id = conversation.id.clone();
            if mode == "incremental" && state.convos.get(&old_id).is_some() {
                conv_map.insert(old_id, conversation.id.clone());
                skipped.sessions += 1;
                continue;
            }
            let target_id = if mode == "duplicate" {
                uuid::Uuid::new_v4().to_string()
            } else {
                old_id.clone()
            };
            conv_map.insert(old_id, target_id.clone());
            conversation.id = target_id;
            conversation.conn_id = remap_id(&conn_map, &conversation.conn_id);
            conversation.session_ref.clear();
            conversation.status = "idle".into();
            state.convos.upsert(conversation)?;
            imported.sessions += 1;
        }
    }
    if selection.tasks() {
        for mut task in payload.tasks {
            let old_id = task.id.clone();
            if mode == "incremental" && state.tasks.get(&task.id).is_some() {
                task_map.insert(old_id, task.id.clone());
                skipped.tasks += 1;
                continue;
            }
            let target_id = if mode == "duplicate" {
                uuid::Uuid::new_v4().to_string()
            } else {
                old_id.clone()
            };
            task_map.insert(old_id, target_id.clone());
            task.id = target_id;
            task.conn_id = remap_id(&conn_map, &task.conn_id);
            task.last_conv_id = remap_id(&conv_map, &task.last_conv_id);
            state.tasks.upsert(task)?;
            imported.tasks += 1;
        }
    }
    if selection.managed() {
        for mut managed in payload.managed {
            if mode == "incremental" && state.managed.get(&managed.id).is_some() {
                skipped.managed += 1;
                continue;
            }
            if mode == "duplicate" {
                managed.id = uuid::Uuid::new_v4().to_string();
                managed.task_ids = managed
                    .task_ids
                    .iter()
                    .map(|id| remap_id(&task_map, id))
                    .collect();
            } else {
                managed.task_ids = managed
                    .task_ids
                    .iter()
                    .map(|id| remap_id(&task_map, id))
                    .collect();
            }
            managed.conn_id = remap_id(&conn_map, &managed.conn_id);
            state.managed.upsert(managed)?;
            imported.managed += 1;
        }
    }
    if selection.templates() {
        let root = state.data_dir.join("templates");
        for template in payload.templates {
            if write_template(&root, &template, &mode)? {
                imported.templates += 1;
            } else {
                skipped.templates += 1;
            }
        }
    }
    let preferences = if selection.preferences() && mode != "incremental" {
        payload.preferences
    } else {
        None
    };
    imported.preferences = preferences.is_some();
    skipped.preferences = selection.preferences() && preferences.is_none();
    Ok(TransferImportResult {
        imported,
        skipped,
        backup_path,
        preferences,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn encrypt_roundtrip_and_wrong_password_fails() {
        let (salt, nonce, payload) = encrypt_payload(b"pilot", "correct horse battery").unwrap();
        let envelope = TransferEnvelope {
            format: FORMAT.into(),
            version: VERSION,
            created_at: 0,
            encrypted: true,
            kdf: "Argon2id".into(),
            cipher: "AES-256-GCM".into(),
            salt,
            nonce,
            payload,
        };
        assert_eq!(
            decrypt_payload(&envelope, "correct horse battery").unwrap(),
            b"pilot"
        );
        assert!(decrypt_payload(&envelope, "wrong password").is_err());
    }

    #[test]
    fn rejects_template_traversal() {
        assert!(safe_relative("../secret").is_err());
        assert!(safe_relative("nested/index.html").is_ok());
    }
}
