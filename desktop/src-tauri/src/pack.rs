//! 技能包导入：解压 zip → 找到技能目录（含 scripts/gcms.js）→ 解析 .env →
//! key 剥离进 Keychain，.env 只留 GCMS_API_BASE → 连接元数据存 connections.json。

use serde::{Deserialize, Serialize};
use std::fs;
use std::io::Write as _;
use std::path::{Path, PathBuf};

use crate::keychain;

#[derive(Clone, Serialize, Deserialize)]
pub struct Connection {
    pub id: String,
    pub name: String,
    pub api_base: String,
    /// 技能目录（gcms.js 的 cwd），绝对路径。
    pub skill_dir: String,
    /// 仅用于展示的 key 前缀，完整 key 在 Keychain。
    pub key_prefix: String,
    /// gcmsp_（平台多站）或 gcms_（单站）。
    pub key_kind: String,
    pub created_at: String,
}

/// 导入结果：包里没有嵌密钥且调用方也没提供时，返回 NeedsKey 让 UI 弹输入框。
#[derive(Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum ImportOutcome {
    Imported { connection: Connection },
    NeedsKey { api_base: String },
}

#[derive(Clone)]
pub struct ConnStore {
    file: PathBuf,
    packs_dir: PathBuf,
}

impl ConnStore {
    pub fn new(data_dir: &Path) -> Result<Self, String> {
        let packs_dir = data_dir.join("packs");
        fs::create_dir_all(&packs_dir).map_err(|e| format!("create packs dir: {e}"))?;
        Ok(Self {
            file: data_dir.join("connections.json"),
            packs_dir,
        })
    }

    pub fn list(&self) -> Vec<Connection> {
        let Ok(raw) = fs::read(&self.file) else {
            return Vec::new();
        };
        serde_json::from_slice(&raw).unwrap_or_default()
    }

    fn save(&self, conns: &[Connection]) -> Result<(), String> {
        let raw = serde_json::to_vec_pretty(conns).map_err(|e| e.to_string())?;
        // 同目录临时文件 + rename，避免写一半（崩溃/磁盘满）清空整个连接列表。
        let tmp = self.file.with_extension("json.tmp");
        fs::write(&tmp, &raw).map_err(|e| format!("write connections tmp: {e}"))?;
        fs::rename(&tmp, &self.file).map_err(|e| format!("replace connections.json: {e}"))
    }

    pub fn get(&self, id: &str) -> Result<Connection, String> {
        self.list()
            .into_iter()
            .find(|c| c.id == id)
            .ok_or_else(|| format!("未找到连接 {id}"))
    }

    /// 导入 zip 技能包。key 进 Keychain，不落任何盘面文件。
    /// 支持两种包：嵌密钥包（.env 内置 key）与原始包（.env.example 占位）——
    /// 原始包需要调用方经 `provided_key` 传入手动粘贴的密钥，否则返回 NeedsKey。
    pub fn import_zip(
        &self,
        zip_path: &str,
        name: Option<String>,
        provided_key: Option<String>,
    ) -> Result<ImportOutcome, String> {
        let file = fs::File::open(zip_path).map_err(|e| format!("打开 zip 失败: {e}"))?;
        let mut archive =
            zip::ZipArchive::new(file).map_err(|e| format!("读取 zip 失败: {e}"))?;

        let conn_id = uuid::Uuid::new_v4().to_string();
        let dest = self.packs_dir.join(&conn_id);
        fs::create_dir_all(&dest).map_err(|e| format!("create pack dir: {e}"))?;
        archive
            .extract(&dest)
            .map_err(|e| format!("解压失败: {e}"))?;

        let result = (|| {
            let skill_dir = find_skill_dir(&dest)
                .ok_or("zip 里没有找到技能目录（缺少 scripts/gcms.js）")?;
            let env_path = skill_dir.join(".env");
            let example = skill_dir.join(".env.example");
            let env_file = if env_path.exists() {
                env_path.clone()
            } else if example.exists() {
                example
            } else {
                return Err("技能包里没有 .env / .env.example".to_string());
            };
            let (api_base, embedded_key) = parse_env(&env_file)?;
            if api_base.is_empty() {
                return Err("技能包 .env 缺少 GCMS_API_BASE".to_string());
            }
            let api_key = if !key_missing(&embedded_key) {
                embedded_key
            } else if let Some(k) = provided_key.as_deref().map(str::trim).filter(|k| !k.is_empty())
            {
                k.to_string()
            } else {
                // 原始包且没给密钥：让 UI 弹输入框后带 key 重试。
                return Ok(ImportOutcome::NeedsKey { api_base });
            };
            let key_kind = if api_key.starts_with("gcmsp_") {
                "gcmsp_"
            } else if api_key.starts_with("gcms_") {
                "gcms_"
            } else {
                return Err("访问密钥前缀不是 gcmsp_/gcms_，无法识别".to_string());
            };
            // 去重：同地址 + 同密钥前缀视为同一连接，避免重复导入静默堆积。
            let prefix = keychain::key_prefix(&api_key);
            if let Some(dup) = self
                .list()
                .iter()
                .find(|c| c.api_base == api_base && c.key_prefix == prefix)
            {
                return Err(format!(
                    "已存在相同的连接「{}」（同一地址与密钥）。如需重新导入，请先删除旧连接。",
                    dup.name
                ));
            }

            // 剥离：Keychain 收 key，.env 只留 base。
            keychain::set_key(&conn_id, &api_key)?;
            let mut f = fs::File::create(&env_path).map_err(|e| format!("重写 .env: {e}"))?;
            writeln!(f, "GCMS_API_BASE={api_base}").map_err(|e| e.to_string())?;
            writeln!(f, "# GCMS_API_KEY 已由 gcms Pilot 保管在 macOS 钥匙串，运行时自动注入")
                .map_err(|e| e.to_string())?;

            let conn = Connection {
                id: conn_id.clone(),
                name: name
                    .filter(|s| !s.trim().is_empty())
                    .unwrap_or_else(|| default_name(&api_base)),
                api_base,
                skill_dir: skill_dir.to_string_lossy().into_owned(),
                key_prefix: prefix,
                key_kind: key_kind.to_string(),
                created_at: chrono_now(),
            };
            let mut conns = self.list();
            conns.push(conn.clone());
            self.save(&conns)?;
            Ok(ImportOutcome::Imported { connection: conn })
        })();

        // 只有真正建立连接才保留解压目录；失败或等待密钥都回滚清理。
        if !matches!(result, Ok(ImportOutcome::Imported { .. })) {
            let _ = fs::remove_dir_all(&dest);
            let _ = keychain::delete_key(&conn_id);
        }
        result
    }

    pub fn remove(&self, id: &str) -> Result<(), String> {
        let mut conns = self.list();
        let before = conns.len();
        conns.retain(|c| c.id != id);
        if conns.len() == before {
            return Err(format!("未找到连接 {id}"));
        }
        // 先删钥匙串：失败则中止（连接保留，可重试），绝不留下无 UI 句柄的孤儿密钥。
        keychain::delete_key(id)?;
        self.save(&conns)?;
        let _ = fs::remove_dir_all(self.packs_dir.join(id));
        Ok(())
    }
}

/// 在解压目录下找包含 scripts/gcms.js 的技能目录（兼容平台包/单站包/嵌套一层的情况）。
fn find_skill_dir(root: &Path) -> Option<PathBuf> {
    fn check(dir: &Path) -> bool {
        dir.join("scripts").join("gcms.js").is_file()
    }
    if check(root) {
        return Some(root.to_path_buf());
    }
    // 两层内广度优先：zip 顶层通常是 README.md + <skill-folder>/。
    let mut level = vec![root.to_path_buf()];
    for _ in 0..2 {
        let mut next = Vec::new();
        for dir in &level {
            let Ok(entries) = fs::read_dir(dir) else { continue };
            for e in entries.flatten() {
                let p = e.path();
                if p.is_dir() {
                    if check(&p) {
                        return Some(p);
                    }
                    next.push(p);
                }
            }
        }
        level = next;
    }
    None
}

/// 原始包（tokenless）识别：没有 key，或只是 .env.example 的 gcmsp_xxx / gcms_xxx 占位。
fn key_missing(key: &str) -> bool {
    key.is_empty() || key.ends_with("_xxx")
}

fn parse_env(path: &Path) -> Result<(String, String), String> {
    let raw = fs::read_to_string(path).map_err(|e| format!("读取 .env: {e}"))?;
    // 兼容手工编辑过的 .env：BOM、CRLF、`export ` 前缀、引号包裹的值。
    let raw = raw.strip_prefix('\u{feff}').unwrap_or(&raw);
    let mut base = String::new();
    let mut key = String::new();
    for line in raw.lines() {
        let line = line.trim();
        let line = line.strip_prefix("export ").unwrap_or(line).trim_start();
        if line.starts_with('#') {
            continue;
        }
        if let Some(v) = line.strip_prefix("GCMS_API_BASE=") {
            base = unquote(v.trim()).trim_end_matches('/').to_string();
        } else if let Some(v) = line.strip_prefix("GCMS_API_KEY=") {
            key = unquote(v.trim()).to_string();
        }
    }
    Ok((base, key))
}

fn unquote(s: &str) -> &str {
    let b = s.as_bytes();
    if b.len() >= 2
        && ((b[0] == b'"' && b[b.len() - 1] == b'"') || (b[0] == b'\'' && b[b.len() - 1] == b'\''))
    {
        &s[1..s.len() - 1]
    } else {
        s
    }
}

fn default_name(api_base: &str) -> String {
    api_base
        .trim_start_matches("https://")
        .trim_start_matches("http://")
        .split('/')
        .next()
        .unwrap_or("gcms")
        .to_string()
}

fn chrono_now() -> String {
    // 避免引入 chrono：秒级 unix 时间足够（UI 端格式化）。
    let secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    secs.to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_env_extracts_base_and_key() {
        let dir = std::env::temp_dir().join(format!("pilot-test-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&dir).unwrap();
        let env = dir.join(".env");
        fs::write(
            &env,
            "# comment\nGCMS_API_BASE=https://x.test/api/platform/v1/\nGCMS_API_KEY=gcmsp_abc123\n",
        )
        .unwrap();
        let (base, key) = parse_env(&env).unwrap();
        assert_eq!(base, "https://x.test/api/platform/v1"); // 尾斜杠被去掉
        assert_eq!(key, "gcmsp_abc123");
        fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn find_skill_dir_handles_nesting() {
        let root = std::env::temp_dir().join(format!("pilot-test-{}", uuid::Uuid::new_v4()));
        // zip 顶层：README.md + gcms-platform-assistant/scripts/gcms.js
        let skill = root.join("gcms-platform-assistant");
        fs::create_dir_all(skill.join("scripts")).unwrap();
        fs::write(skill.join("scripts").join("gcms.js"), "// cli").unwrap();
        fs::write(root.join("README.md"), "readme").unwrap();
        assert_eq!(find_skill_dir(&root).unwrap(), skill);
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn find_skill_dir_none_when_missing() {
        let root = std::env::temp_dir().join(format!("pilot-test-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(root.join("docs")).unwrap();
        assert!(find_skill_dir(&root).is_none());
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn default_name_strips_scheme_and_path() {
        assert_eq!(default_name("https://cms.ccvar.com/api/platform/v1"), "cms.ccvar.com");
    }

    #[test]
    fn key_missing_detects_placeholders() {
        assert!(key_missing(""));
        assert!(key_missing("gcmsp_xxx")); // 平台原始包占位
        assert!(key_missing("gcms_xxx")); // 单站原始包占位
        assert!(!key_missing("gcmsp_livetoken123"));
        assert!(!key_missing("gcms_livetoken123"));
    }
}
