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
    /// 连接类型：gcms（导入技能包）| cloudflare（CF token 建站）| ssh（远程机器）。旧连接缺省即 gcms。
    #[serde(default = "default_kind")]
    pub kind: String,
    pub api_base: String,
    /// 技能目录（gcms.js 的 cwd）；CF 连接则是该连接的工作区目录（技能包 + 项目都在其下）。
    pub skill_dir: String,
    /// 仅用于展示的 key 前缀，完整 key 在 Keychain。
    pub key_prefix: String,
    /// gcmsp_（平台多站）/ gcms_（单站）/ cf_token（Cloudflare）。
    pub key_kind: String,
    /// Cloudflare 账号 id（仅 kind=cloudflare）；gcms 连接为空。
    #[serde(default)]
    pub account_id: String,
    /// 以下均仅 kind=ssh。密码/私钥口令进钥匙串（account = conn_id，与其它 kind 同约定，
    /// 因此 remove 无需特殊处理、不会留孤儿密钥）；私钥只存路径引用，不拷贝用户私钥。
    #[serde(default)]
    pub ssh_host: String,
    #[serde(default)]
    pub ssh_port: u16,
    #[serde(default)]
    pub ssh_user: String,
    /// password | key
    #[serde(default)]
    pub ssh_auth: String,
    #[serde(default)]
    pub ssh_key_path: String,
    /// 用户已确认的主机指纹（TOFU）：此后每次连接必须匹配，不匹配即拒（防中间人）。
    #[serde(default)]
    pub ssh_fingerprint: String,
    /// 远端系统（/etc/os-release 的 PRETTY_NAME，如 "Ubuntu 24.04.1 LTS"）。首次连上时探一次并存下来，
    /// 之后直接显示、不再连（空 = 还没探过）。
    #[serde(default)]
    pub ssh_os: String,
    /// 发行版 id（os-release 的 ID，如 ubuntu/debian/alpine）——UI 据此选发行版图标。
    #[serde(default)]
    pub ssh_os_id: String,
    /// 技能包版本（= 下发它的服务端版本）。空 = 未知（旧导入 / 服务端太老没有版本端点）。
    #[serde(default)]
    pub pack_version: String,
    pub created_at: String,
}

fn default_kind() -> String {
    "gcms".into()
}

/// 导入结果：包里没有嵌密钥且调用方也没提供时，返回 NeedsKey 让 UI 弹输入框。
/// Upgraded = 检测到同一连接（同地址+同密钥）→ 就地升级技能文件，连接/密钥/对话全保留。
#[derive(Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum ImportOutcome {
    Imported { connection: Connection },
    Upgraded { connection: Connection },
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
                // 原始包且没给密钥：后台常驻下载的都是无密钥包，而完整密钥只显示一次——
                // 若恰有一个同地址的 gcms 连接，直接就地升级它（密钥用钥匙串里那把，无需重填）。
                // 导入 zip 本就等于信任其中的脚本，按地址匹配不降低信任边界；多个同地址连接时无法消歧，仍走弹窗。
                let same_base: Vec<Connection> = self
                    .list()
                    .into_iter()
                    .filter(|c| c.kind == "gcms" && c.api_base == api_base)
                    .collect();
                if same_base.len() == 1 {
                    let mut dup = same_base.into_iter().next().expect("len checked");
                    overlay_skill_dir(&skill_dir, Path::new(&dup.skill_dir))?;
                    let v = read_pack_version(Path::new(&dup.skill_dir));
                    if !v.is_empty() && v != dup.pack_version {
                        dup.pack_version = v;
                        self.set_pack_version(&dup.id, &dup.pack_version)?;
                    }
                    return Ok(ImportOutcome::Upgraded { connection: dup });
                }
                // 没有可升级的目标：让 UI 弹输入框后带 key 重试。
                return Ok(ImportOutcome::NeedsKey { api_base });
            };
            let key_kind = if api_key.starts_with("gcmsp_") {
                "gcmsp_"
            } else if api_key.starts_with("gcms_") {
                "gcms_"
            } else {
                return Err("访问密钥前缀不是 gcmsp_/gcms_，无法识别".to_string());
            };
            // 同地址 + 同密钥前缀 = 同一连接 → 就地升级技能文件（不再报错逼用户删连接丢对话）。
            let prefix = keychain::key_prefix(&api_key);
            if let Some(mut dup) = self
                .list()
                .into_iter()
                .find(|c| c.api_base == api_base && c.key_prefix == prefix)
            {
                overlay_skill_dir(&skill_dir, Path::new(&dup.skill_dir))?;
                // 新包（v1.3.10+）带 PACK_VERSION 标记：升级后落版本，避免「有更新」徽标误报纠缠。
                let v = read_pack_version(Path::new(&dup.skill_dir));
                if !v.is_empty() && v != dup.pack_version {
                    dup.pack_version = v;
                    self.set_pack_version(&dup.id, &dup.pack_version)?;
                }
                return Ok(ImportOutcome::Upgraded { connection: dup });
            }

            // 剥离：Keychain 收 key，.env 只留 base。
            keychain::set_key(&conn_id, &api_key)?;
            let mut f = fs::File::create(&env_path).map_err(|e| format!("重写 .env: {e}"))?;
            writeln!(f, "GCMS_API_BASE={api_base}").map_err(|e| e.to_string())?;
            writeln!(f, "# GCMS_API_KEY 已由 GCMS Pilot 保管在 macOS 钥匙串，运行时自动注入")
                .map_err(|e| e.to_string())?;

            let conn = Connection {
                id: conn_id.clone(),
                name: name
                    .filter(|s| !s.trim().is_empty())
                    .unwrap_or_else(|| default_name(&api_base)),
                kind: "gcms".into(),
                api_base,
                skill_dir: skill_dir.to_string_lossy().into_owned(),
                key_prefix: prefix,
                key_kind: key_kind.to_string(),
                account_id: String::new(),
                ssh_host: String::new(),
                ssh_port: 0,
                ssh_user: String::new(),
                ssh_auth: String::new(),
                ssh_key_path: String::new(),
                ssh_fingerprint: String::new(),
                ssh_os: String::new(),
                ssh_os_id: String::new(),
                pack_version: read_pack_version(&skill_dir),
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

    /// 连接 Cloudflare：token 进钥匙串，建该连接的工作区目录，写 connections.json。
    /// 调用方应已先验证过 token（lib.rs 的 connect_cloudflare 会验证）。
    pub fn add_cloudflare(
        &self,
        name: &str,
        token: &str,
        account_id: &str,
    ) -> Result<Connection, String> {
        let token = token.trim();
        if token.is_empty() {
            return Err("Token 不能为空".into());
        }
        let prefix = keychain::key_prefix(token);
        // 去重：同账号 + 同 token 前缀视为同一连接。
        if let Some(dup) = self.list().iter().find(|c| {
            c.kind == "cloudflare" && c.account_id == account_id && c.key_prefix == prefix
        }) {
            return Err(format!("已存在相同的 Cloudflare 连接「{}」。", dup.name));
        }
        let conn_id = uuid::Uuid::new_v4().to_string();
        // 每个 CF 连接一个工作区目录（内置技能包 + 站点项目都放这下面；Slice 3 填充）。
        let dir = self.packs_dir.join(&conn_id);
        fs::create_dir_all(&dir).map_err(|e| format!("create cf workspace: {e}"))?;
        keychain::set_key(&conn_id, token)?;
        let conn = Connection {
            id: conn_id.clone(),
            name: if name.trim().is_empty() { "Cloudflare".into() } else { name.trim().into() },
            kind: "cloudflare".into(),
            api_base: String::new(),
            skill_dir: dir.to_string_lossy().into_owned(),
            key_prefix: prefix,
            key_kind: "cf_token".into(),
            account_id: account_id.trim().to_string(),
            ssh_host: String::new(),
            ssh_port: 0,
            ssh_user: String::new(),
            ssh_auth: String::new(),
            ssh_key_path: String::new(),
            ssh_fingerprint: String::new(),
            ssh_os: String::new(),
            ssh_os_id: String::new(),
            pack_version: String::new(),
            created_at: chrono_now(),
        };
        let mut conns = self.list();
        conns.push(conn.clone());
        if let Err(e) = self.save(&conns) {
            let _ = keychain::delete_key(&conn_id);
            let _ = fs::remove_dir_all(&dir);
            return Err(e);
        }
        Ok(conn)
    }

    /// 新建 SSH 远程连接：密码/私钥口令进钥匙串，建工作区目录，写 connections.json。
    /// 调用方必须已先 probe 过并由用户确认了主机指纹（TOFU）——没有指纹一律拒绝。
    #[allow(clippy::too_many_arguments)]
    pub fn add_ssh(
        &self,
        name: &str,
        host: &str,
        port: u16,
        user: &str,
        auth: &str,
        key_path: &str,
        secret: &str,
        fingerprint: &str,
    ) -> Result<Connection, String> {
        let (host, user, key_path, fingerprint) =
            (host.trim(), user.trim(), key_path.trim(), fingerprint.trim());
        if host.is_empty() {
            return Err("主机不能为空".into());
        }
        if user.is_empty() {
            return Err("用户名不能为空".into());
        }
        if fingerprint.is_empty() {
            return Err("缺少主机指纹：请先「测试连接」并确认指纹".into());
        }
        match auth {
            "password" if secret.is_empty() => return Err("密码不能为空".into()),
            "key" if key_path.is_empty() => return Err("请选择私钥文件".into()),
            "password" | "key" => {}
            other => return Err(format!("未知认证方式: {other}")),
        }
        let port = if port == 0 { 22 } else { port };
        // 去重：同一台机器的同一用户视为同一连接。
        if let Some(dup) = self.list().iter().find(|c| {
            c.kind == "ssh" && c.ssh_host == host && c.ssh_port == port && c.ssh_user == user
        }) {
            return Err(format!(
                "已存在相同的远程连接「{}」（{user}@{host}:{port}）。",
                dup.name
            ));
        }
        let conn_id = uuid::Uuid::new_v4().to_string();
        let dir = self.packs_dir.join(&conn_id);
        fs::create_dir_all(&dir).map_err(|e| format!("create ssh workspace: {e}"))?;
        // 无口令的私钥没有秘密可存 —— 不写钥匙串（读侧用 get_key(..).ok() 兜底）。
        if !secret.is_empty() {
            if let Err(e) = keychain::set_key(&conn_id, secret) {
                let _ = fs::remove_dir_all(&dir);
                return Err(e);
            }
        }
        let conn = Connection {
            id: conn_id.clone(),
            name: if name.trim().is_empty() {
                format!("{user}@{host}")
            } else {
                name.trim().into()
            },
            kind: "ssh".into(),
            api_base: String::new(),
            skill_dir: dir.to_string_lossy().into_owned(),
            // 故意留空：key_prefix 会显示在连接列表里，SSH 这里的秘密是密码/口令，
            // 照抄前缀等于把密码前 13 位印在 UI 上。副标题改显 user@host:port。
            key_prefix: String::new(),
            key_kind: if auth == "key" { "ssh_key".into() } else { "ssh_password".into() },
            account_id: String::new(),
            ssh_host: host.into(),
            ssh_port: port,
            ssh_user: user.into(),
            ssh_auth: auth.into(),
            ssh_key_path: key_path.into(),
            ssh_fingerprint: fingerprint.into(),
            // 系统信息首次连上时再探（add 时不连，避免加连接就多一次登录记录）。
            ssh_os: String::new(),
            ssh_os_id: String::new(),
            pack_version: String::new(),
            created_at: chrono_now(),
        };
        let mut conns = self.list();
        conns.push(conn.clone());
        if let Err(e) = self.save(&conns) {
            let _ = keychain::delete_key(&conn_id);
            let _ = fs::remove_dir_all(&dir);
            return Err(e);
        }
        Ok(conn)
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

    /// 记录远端系统信息（首次连上探到后写入，之后 UI 直接显示、不再连）。
    pub fn set_ssh_os(&self, id: &str, pretty: &str, os_id: &str) -> Result<Connection, String> {
        let mut conns = self.list();
        let Some(slot) = conns.iter_mut().find(|c| c.id == id) else {
            return Err(format!("未找到连接 {id}"));
        };
        slot.ssh_os = pretty.trim().to_string();
        slot.ssh_os_id = os_id.trim().to_lowercase();
        let out = slot.clone();
        self.save(&conns)?;
        Ok(out)
    }

    /// 修改已有的远程连接。
    ///
    /// 密码/口令：`secret` 为空 = 不动钥匙串里那条（UI 不回显密码，留空即保持原样）。
    /// **换认证方式时必须给新密钥**——旧钥匙串条目存的是另一种东西（密码 vs 私钥口令），留着必错。
    /// 指纹由调用方给（UI 侧刚试连确认过的那个）：换机器/换端口后旧指纹不再作数。
    #[allow(clippy::too_many_arguments)]
    pub fn update_ssh(
        &self,
        id: &str,
        name: &str,
        host: &str,
        port: u16,
        user: &str,
        auth: &str,
        key_path: &str,
        secret: &str,
        fingerprint: &str,
    ) -> Result<Connection, String> {
        let (host, user, key_path, fingerprint) =
            (host.trim(), user.trim(), key_path.trim(), fingerprint.trim());
        if host.is_empty() {
            return Err("主机不能为空".into());
        }
        if user.is_empty() {
            return Err("用户名不能为空".into());
        }
        if fingerprint.is_empty() {
            return Err("缺少主机指纹：请先「测试连接」并确认指纹".into());
        }
        let port = if port == 0 { 22 } else { port };
        let mut conns = self.list();
        let Some(old) = conns.iter().find(|c| c.id == id).cloned() else {
            return Err(format!("未找到连接 {id}"));
        };
        if old.kind != "ssh" {
            return Err("这不是远程连接".into());
        }
        match auth {
            "key" if key_path.is_empty() => return Err("请选择私钥文件".into()),
            "password" if secret.is_empty() && old.ssh_auth != "password" => {
                return Err("换成密码登录时必须填密码".into())
            }
            "key" | "password" => {}
            other => return Err(format!("未知认证方式: {other}")),
        }
        // 去重：改成另一台已存在的机器（同 user@host:port）＝和那条连接撞车。
        if let Some(dup) = conns.iter().find(|c| {
            c.id != id && c.kind == "ssh" && c.ssh_host == host && c.ssh_port == port && c.ssh_user == user
        }) {
            return Err(format!(
                "已存在相同的远程连接「{}」（{user}@{host}:{port}）。",
                dup.name
            ));
        }
        // 动钥匙串前先把旧值抄下来：存盘要是失败了，得原样放回去（无口令私钥本就没有条目 → None）。
        let old_secret = keychain::get_key(id).ok();
        // 钥匙串先写：写失败就整个中止（连接还是原样，可重试），别让记录和密钥对不上。
        if !secret.is_empty() {
            keychain::set_key(id, secret)?;
        } else if auth == "key" && old.ssh_auth == "password" {
            // 密码登录 → 无口令私钥：旧密码留着就是个错的口令，必须清掉。
            let _ = keychain::delete_key(id);
        }
        let restore_secret = || match &old_secret {
            Some(s) => {
                let _ = keychain::set_key(id, s);
            }
            None => {
                let _ = keychain::delete_key(id);
            }
        };
        let slot = conns.iter_mut().find(|c| c.id == id).expect("checked above");
        slot.name = if name.trim().is_empty() { format!("{user}@{host}") } else { name.trim().into() };
        slot.ssh_host = host.into();
        slot.ssh_port = port;
        slot.ssh_user = user.into();
        slot.ssh_auth = auth.into();
        slot.ssh_key_path = if auth == "key" { key_path.into() } else { String::new() };
        slot.ssh_fingerprint = fingerprint.into();
        // 换了机器就别留旧系统信息（下次连上重探）。
        if old.ssh_host != host || old.ssh_port != port {
            slot.ssh_os = String::new();
            slot.ssh_os_id = String::new();
        }
        let out = slot.clone();
        if let Err(e) = self.save(&conns) {
            // 存盘失败：把钥匙串放回旧值，别留下「密钥是新的、记录是旧的」的错位状态。
            restore_secret();
            return Err(e);
        }
        Ok(out)
    }

    /// 记录连接的技能包版本（升级/首次核对后写入）。
    pub fn set_pack_version(&self, id: &str, v: &str) -> Result<(), String> {
        let mut conns = self.list();
        let Some(slot) = conns.iter_mut().find(|c| c.id == id) else {
            return Err(format!("未找到连接 {id}"));
        };
        slot.pack_version = v.trim().to_string();
        self.save(&conns)
    }

    /// 一键升级：用（从服务端下载的）zip 就地升级已有连接的技能目录。
    /// 连接 id / 钥匙串密钥 / skill_dir 路径 / 对话全部不变，只覆盖包内文件。
    pub fn upgrade_from_zip(&self, conn_id: &str, zip_path: &str) -> Result<Connection, String> {
        let conn = self.get(conn_id)?;
        if conn.kind != "gcms" {
            return Err("只有 gcms 技能包连接支持升级".into());
        }
        let file = fs::File::open(zip_path).map_err(|e| format!("打开 zip 失败: {e}"))?;
        let mut archive = zip::ZipArchive::new(file).map_err(|e| format!("读取 zip 失败: {e}"))?;
        let tmp = self.packs_dir.join(format!("upgrade-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&tmp).map_err(|e| format!("create tmp dir: {e}"))?;
        let result = (|| {
            archive.extract(&tmp).map_err(|e| format!("解压失败: {e}"))?;
            let src = find_skill_dir(&tmp).ok_or("zip 里没有找到技能目录（缺少 scripts/gcms.js）")?;
            overlay_skill_dir(&src, Path::new(&conn.skill_dir))?;
            let mut conn = conn;
            let v = read_pack_version(Path::new(&conn.skill_dir));
            if !v.is_empty() && v != conn.pack_version {
                conn.pack_version = v;
                self.set_pack_version(&conn.id, &conn.pack_version)?;
            }
            Ok(conn)
        })();
        let _ = fs::remove_dir_all(&tmp);
        result
    }
}

/// 读技能目录里的 PACK_VERSION 标记（=下发该包的服务端版本）；没有则空串（老包）。
fn read_pack_version(skill_dir: &Path) -> String {
    fs::read_to_string(skill_dir.join("PACK_VERSION"))
        .map(|s| s.trim().to_string())
        .unwrap_or_default()
}

/// 把新包的技能目录覆盖到已有技能目录上：只写包里有的文件，绝不删除既有文件；
/// 根级 `.env` 跳过（由 Pilot 管理：只含 GCMS_API_BASE，密钥在钥匙串）——
/// uploads/、shots/ 等用户/运行时文件因此天然保留。
fn overlay_skill_dir(src: &Path, dest: &Path) -> Result<(), String> {
    fn walk(src: &Path, dest: &Path, root: bool) -> Result<(), String> {
        fs::create_dir_all(dest).map_err(|e| format!("create dir {}: {e}", dest.display()))?;
        let entries = fs::read_dir(src).map_err(|e| format!("read dir {}: {e}", src.display()))?;
        for e in entries.flatten() {
            let name = e.file_name();
            let ns = name.to_string_lossy();
            if root && (ns == ".env" || ns == ".env.example") {
                continue; // 保留现有 .env（含正确 base）；example 也不需要
            }
            let sp = e.path();
            let dp = dest.join(&name);
            if sp.is_dir() {
                walk(&sp, &dp, false)?;
            } else {
                fs::copy(&sp, &dp).map_err(|e| format!("覆盖 {} 失败: {e}", dp.display()))?;
            }
        }
        Ok(())
    }
    if !dest.is_dir() {
        return Err(format!("原技能目录不存在：{}", dest.display()));
    }
    walk(src, dest, true)
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

    /// 改远程连接：改地址/改名/换认证方式，以及不该发生的事（撞车、非 ssh、缺指纹）。
    /// 注意不碰钥匙串（CI 无钥匙串）：只走 secret 留空的路径。
    #[test]
    fn update_ssh_rules() {
        let base = std::env::temp_dir().join(format!("pilot-upd-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&base).unwrap();
        let store = ConnStore::new(&base).unwrap();
        let mk = |id: &str, host: &str, user: &str| Connection {
            id: id.into(), name: format!("{user}@{host}"), kind: "ssh".into(),
            api_base: String::new(), skill_dir: String::new(), key_prefix: String::new(),
            key_kind: String::new(), account_id: String::new(),
            ssh_host: host.into(), ssh_port: 22, ssh_user: user.into(),
            ssh_auth: "key".into(), ssh_key_path: "/k/id_ed25519".into(),
            ssh_fingerprint: "SHA256:old".into(),
            ssh_os: "Ubuntu 24.04".into(), ssh_os_id: "ubuntu".into(),
            pack_version: String::new(), created_at: "t".into(),
        };
        let gcms = Connection { kind: "gcms".into(), ..mk("g1", "", "") };
        // Connection 没有 Debug（也不该为了测试给它加），自己取错误文本
        fn err_of(r: Result<Connection, String>) -> String {
            match r { Err(e) => e, Ok(_) => panic!("这一步本该失败") }
        }
        store.save(&[mk("c1", "1.1.1.1", "root"), mk("c2", "2.2.2.2", "root"), gcms]).unwrap();

        // 改名 + 换指纹（同机器）：系统信息保留，不必重探
        let c = store.update_ssh("c1", "生产机", "1.1.1.1", 22, "root", "key", "/k/id_ed25519", "", "SHA256:new").unwrap();
        assert_eq!(c.name, "生产机");
        assert_eq!(c.ssh_fingerprint, "SHA256:new");
        assert_eq!(c.ssh_os, "Ubuntu 24.04", "同一台机器不该丢掉已探到的系统信息");

        // 换了机器：系统信息必须清掉（否则显示的是上一台机器的系统）
        let c = store.update_ssh("c1", "", "9.9.9.9", 2222, "root", "key", "/k/id_ed25519", "", "SHA256:x").unwrap();
        assert_eq!(c.ssh_host, "9.9.9.9");
        assert_eq!(c.ssh_port, 2222);
        assert_eq!(c.ssh_os, "");
        assert_eq!(c.ssh_os_id, "");
        assert_eq!(c.name, "root@9.9.9.9", "名字留空＝回到默认 user@host");

        // 撞上另一条已有连接（同 user@host:port）→ 拒
        let err = err_of(store.update_ssh("c1", "", "2.2.2.2", 22, "root", "key", "/k/id_ed25519", "", "SHA256:x"));
        assert!(err.contains("已存在相同的远程连接"), "{err}");
        // 自己和自己不算撞车
        assert!(store.update_ssh("c1", "", "9.9.9.9", 2222, "root", "key", "/k/id_ed25519", "", "SHA256:x").is_ok());

        // 缺指纹 → 拒（TOFU 的底线：没确认过就不给存）
        assert!(store.update_ssh("c1", "", "9.9.9.9", 2222, "root", "key", "/k/id_ed25519", "", "  ").is_err());
        // key 认证但没给私钥路径 → 拒
        assert!(store.update_ssh("c1", "", "9.9.9.9", 2222, "root", "key", "", "", "SHA256:x").is_err());
        // 从密钥换成密码却不给密码 → 拒（钥匙串里那条是私钥口令，不是密码，留着必错）
        let err = err_of(store.update_ssh("c1", "", "9.9.9.9", 2222, "root", "password", "", "", "SHA256:x"));
        assert!(err.contains("必须填密码"), "{err}");
        // 非 ssh 连接 → 拒
        assert!(store.update_ssh("g1", "x", "h", 22, "u", "key", "/k", "", "SHA256:x").is_err());
        // 不存在的连接 → 拒
        assert!(store.update_ssh("nope", "x", "h", 22, "u", "key", "/k", "", "SHA256:x").is_err());

        // 没被碰过的那条要原样还在（别把别人写坏了）
        let c2 = store.get("c2").unwrap();
        assert_eq!(c2.ssh_host, "2.2.2.2");
        assert_eq!(c2.ssh_fingerprint, "SHA256:old");
        fs::remove_dir_all(&base).ok();
    }

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

    #[test]
    fn import_raw_pack_upgrades_single_matching_connection_without_key() {
        use std::io::Write as _;
        let base = std::env::temp_dir().join(format!("pilot-upg-{}", uuid::Uuid::new_v4()));
        fs::create_dir_all(&base).unwrap();
        let store = ConnStore::new(&base).unwrap();
        // 既有连接：同 api_base 的 gcms 连接，技能目录里有旧脚本 + Pilot 管理的 .env
        let dest = base.join("skill-old");
        fs::create_dir_all(dest.join("scripts")).unwrap();
        fs::write(dest.join("scripts").join("gcms.js"), "OLD").unwrap();
        fs::write(dest.join(".env"), "GCMS_API_BASE=https://x.example/api/platform/v1").unwrap();
        let conn = Connection {
            id: "c1".into(), name: "x".into(), kind: "gcms".into(),
            api_base: "https://x.example/api/platform/v1".into(),
            skill_dir: dest.to_string_lossy().into_owned(),
            key_prefix: "gcmsp_ab".into(), key_kind: "gcmsp_".into(),
            account_id: String::new(),
            ssh_host: String::new(), ssh_port: 0, ssh_user: String::new(),
            ssh_auth: String::new(), ssh_key_path: String::new(), ssh_fingerprint: String::new(),
            ssh_os: String::new(), ssh_os_id: String::new(),
            pack_version: String::new(), created_at: "t".into(),
        };
        fs::write(base.join("connections.json"), serde_json::to_vec_pretty(&[conn]).unwrap()).unwrap();
        // 新原始包 zip：无密钥（.env.example 占位）+ 新脚本 + PACK_VERSION
        let zip_path = base.join("pack.zip");
        {
            let f = fs::File::create(&zip_path).unwrap();
            let mut zw = zip::ZipWriter::new(f);
            let o = zip::write::SimpleFileOptions::default().compression_method(zip::CompressionMethod::Stored);
            zw.start_file("kit/skill/scripts/gcms.js", o).unwrap();
            zw.write_all(b"NEW with relink").unwrap();
            zw.start_file("kit/skill/.env.example", o).unwrap();
            zw.write_all(b"GCMS_API_BASE=https://x.example/api/platform/v1\nGCMS_API_KEY=gcmsp_xxx\n").unwrap();
            zw.start_file("kit/skill/PACK_VERSION", o).unwrap();
            zw.write_all(b"v9.9.9\n").unwrap();
            zw.finish().unwrap();
        }
        // 不提供密钥导入 → 应命中唯一同地址连接，就地升级而非 NeedsKey
        let out = store.import_zip(zip_path.to_str().unwrap(), None, None).unwrap();
        match out {
            ImportOutcome::Upgraded { connection } => {
                assert_eq!(connection.id, "c1");
                assert_eq!(connection.pack_version, "v9.9.9");
            }
            _ => panic!("expected Upgraded outcome"),
        }
        assert_eq!(fs::read_to_string(dest.join("scripts").join("gcms.js")).unwrap(), "NEW with relink");
        // Pilot 管理的 .env 未被包内占位覆盖
        assert!(fs::read_to_string(dest.join(".env")).unwrap().contains("x.example"));
        assert_eq!(store.get("c1").unwrap().pack_version, "v9.9.9");
        fs::remove_dir_all(&base).ok();
    }

    #[test]
    fn overlay_replaces_pack_files_keeps_user_files_and_env() {
        let base = std::env::temp_dir().join(format!("pilot-ovl-{}", uuid::Uuid::new_v4()));
        let dest = base.join("skill");
        let src = base.join("new");
        // 现有技能目录：旧脚本 + Pilot 管理的 .env + 用户文件（uploads/、shots/）
        fs::create_dir_all(dest.join("scripts")).unwrap();
        fs::create_dir_all(dest.join("uploads")).unwrap();
        fs::write(dest.join("scripts").join("gcms.js"), "OLD CLI").unwrap();
        fs::write(dest.join(".env"), "GCMS_API_BASE=https://a.example").unwrap();
        fs::write(dest.join("uploads").join("logo.png"), "userdata").unwrap();
        // 新包：新脚本 + 新文档 + 占位 .env/.env.example（不得覆盖现有 .env）
        fs::create_dir_all(src.join("scripts")).unwrap();
        fs::write(src.join("scripts").join("gcms.js"), "NEW CLI with relink").unwrap();
        fs::write(src.join("SKILL.md"), "new docs").unwrap();
        fs::write(src.join(".env"), "GCMS_API_BASE=WRONG\nGCMS_API_KEY=leak").unwrap();
        fs::write(src.join(".env.example"), "placeholder").unwrap();

        overlay_skill_dir(&src, &dest).unwrap();

        assert_eq!(fs::read_to_string(dest.join("scripts").join("gcms.js")).unwrap(), "NEW CLI with relink");
        assert_eq!(fs::read_to_string(dest.join("SKILL.md")).unwrap(), "new docs");
        assert_eq!(fs::read_to_string(dest.join(".env")).unwrap(), "GCMS_API_BASE=https://a.example"); // 未被包覆盖
        assert_eq!(fs::read_to_string(dest.join("uploads").join("logo.png")).unwrap(), "userdata"); // 用户文件保留
        assert!(!dest.join(".env.example").exists()); // 占位不引入
        // 目标不存在 → 明确报错
        assert!(overlay_skill_dir(&src, &base.join("nope")).is_err());
        fs::remove_dir_all(&base).ok();
    }
}
