//! 对话式会话：一次会话 = 用户与某个模型围绕某站点的多轮对话，
//! 模型在对话过程中通过 node scripts/gcms.js 把事情做掉。
//! 持久化到 conversations.json（原子写 + 互斥串行化）。
//! 底层多轮机制：claude 用 --session-id/--resume，codex 用 exec/exec resume。

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

#[derive(Clone, Serialize, Deserialize)]
pub struct ToolCall {
    pub label: String,
    pub detail: String,
}

/// AI 在对话里提议的定时任务（用户确认后才真正创建）。
#[derive(Clone, Serialize, Deserialize)]
pub struct TaskProposal {
    pub title: String,
    pub prompt: String,
    pub every_minutes: u64,
    #[serde(default)]
    pub first_run: String,
}

/// begin_turn 的结果。
pub enum TurnStart {
    Started,
    Busy,
    NotFound,
    NoSession,
}

#[derive(Clone, Serialize, Deserialize)]
pub struct Message {
    pub role: String, // "user" | "assistant"
    pub text: String,
    #[serde(default)]
    pub tools: Vec<ToolCall>,
    pub ts: u64,
    /// 内部框架消息（kickoff 等），不在界面展示。
    #[serde(default)]
    pub hidden: bool,
    #[serde(default)]
    pub error: bool,
    /// AI 提议的定时任务（界面上渲染成确认卡）。
    #[serde(default)]
    pub proposal: Option<TaskProposal>,
}

#[derive(Clone, Serialize, Deserialize)]
pub struct Conversation {
    pub id: String,
    pub conn_id: String,
    pub conn_name: String,
    pub site_slug: String,
    pub site_name: String,
    /// article | sitebuild | free
    pub task_type: String,
    pub brain: String,
    pub model: String,
    /// 权限档位：plan | ask | auto | full。空串＝旧会话＝full（保持 0.1.10）。
    #[serde(default)]
    pub perm_mode: String,
    /// 思考等级（推理强度）：'' 默认 | low | medium | high。每轮下发，运行中可改。
    #[serde(default)]
    pub effort: String,
    /// claude 的 session uuid 或 codex 的 thread_id；首轮后回填。
    pub session_ref: String,
    pub title: String,
    pub messages: Vec<Message>,
    /// idle | running
    pub status: String,
    pub created_at: u64,
    pub updated_at: u64,
    /// 最近一轮的上下文 token（≈当前会话大小），用于「上下文 X/上限」。0＝没数据。
    #[serde(default)]
    pub ctx_tokens: u64,
    /// 本会话累计处理的 token（每轮 input+cache+output 相加）。
    #[serde(default)]
    pub total_tokens: u64,
}

#[derive(Clone)]
pub struct ConvStore {
    file: PathBuf,
    lock: Arc<Mutex<()>>,
}

impl ConvStore {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            file: data_dir.join("conversations.json"),
            lock: Arc::new(Mutex::new(())),
        }
    }

    fn read(&self) -> Vec<Conversation> {
        let mut v: Vec<Conversation> = match fs::read(&self.file) {
            Ok(raw) => serde_json::from_slice(&raw).unwrap_or_default(),
            Err(_) => Vec::new(),
        };
        v.sort_by(|a, b| b.updated_at.cmp(&a.updated_at));
        v
    }

    /// 最近更新在前。
    pub fn list(&self) -> Vec<Conversation> {
        let _g = self.lock.lock().unwrap();
        self.read()
    }

    pub fn get(&self, id: &str) -> Option<Conversation> {
        let _g = self.lock.lock().unwrap();
        self.read().into_iter().find(|c| c.id == id)
    }

    fn save(&self, list: &[Conversation]) -> Result<(), String> {
        let raw = serde_json::to_vec_pretty(list).map_err(|e| e.to_string())?;
        let tmp = self.file.with_extension("json.tmp");
        fs::write(&tmp, &raw).map_err(|e| format!("write conversations tmp: {e}"))?;
        fs::rename(&tmp, &self.file).map_err(|e| format!("replace conversations.json: {e}"))
    }

    pub fn upsert(&self, conv: Conversation) -> Result<(), String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        if let Some(slot) = list.iter_mut().find(|c| c.id == conv.id) {
            *slot = conv;
        } else {
            list.push(conv);
        }
        list.sort_by(|a, b| b.updated_at.cmp(&a.updated_at));
        list.truncate(300);
        self.save(&list)
    }

    /// 原子开始续对话：锁内检查—追加用户消息—置 running，杜绝同一会话并发起两轮（TOCTOU）。
    pub fn begin_turn(&self, id: &str, now: u64, user: Message) -> TurnStart {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        let Some(slot) = list.iter_mut().find(|c| c.id == id) else {
            return TurnStart::NotFound;
        };
        if slot.status == "running" {
            return TurnStart::Busy;
        }
        if slot.session_ref.trim().is_empty() {
            return TurnStart::NoSession;
        }
        slot.messages.push(user);
        slot.status = "running".into();
        slot.updated_at = now;
        let _ = self.save(&list);
        TurnStart::Started
    }

    /// 原子开始"重试上一轮"：锁内检查 running/session、去掉末尾那条失败/部分的助手消息、
    /// 取最后一条用户消息文本作为要重跑的提示词、置 running。不新增用户消息（重试不是新问）。
    /// 返回要重试的用户消息文本。
    pub fn begin_retry(&self, id: &str, now: u64) -> Result<String, String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        let Some(slot) = list.iter_mut().find(|c| c.id == id) else {
            return Err("会话不存在".into());
        };
        if slot.status == "running" {
            return Err("上一轮还在进行中，请稍候".into());
        }
        if slot.session_ref.trim().is_empty() {
            return Err("这个会话已失效，请新建对话".into());
        }
        // 去掉末尾的助手消息（失败或只写了一半的那条），重试会用一条新的替代它。
        if slot.messages.last().map(|m| m.role == "assistant").unwrap_or(false) {
            slot.messages.pop();
        }
        let Some(text) = slot
            .messages
            .iter()
            .rev()
            .find(|m| m.role == "user")
            .map(|m| m.text.clone())
        else {
            return Err("没有可重试的消息".into());
        };
        slot.status = "running".into();
        slot.updated_at = now;
        self.save(&list)?;
        Ok(text)
    }

    /// 原子开始"重建会话"：同 begin_retry，但**不要求已有 session**——重建本来就是
    /// 为了抛弃损坏/缺失的底层会话另起新的（含首轮就崩、session_ref 为空的情况）。
    pub fn begin_rebuild(&self, id: &str, now: u64) -> Result<String, String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        let Some(slot) = list.iter_mut().find(|c| c.id == id) else {
            return Err("会话不存在".into());
        };
        if slot.status == "running" {
            return Err("上一轮还在进行中，请稍候".into());
        }
        if slot.messages.last().map(|m| m.role == "assistant").unwrap_or(false) {
            slot.messages.pop();
        }
        let Some(text) = slot
            .messages
            .iter()
            .rev()
            .find(|m| m.role == "user")
            .map(|m| m.text.clone())
        else {
            return Err("没有可重跑的消息".into());
        };
        slot.status = "running".into();
        slot.updated_at = now;
        self.save(&list)?;
        Ok(text)
    }

    /// 在锁内对某会话做原子改动（读—改—写不被并发打断）。
    pub fn mutate<F>(&self, id: &str, now: u64, f: F) -> Result<Option<Conversation>, String>
    where
        F: FnOnce(&mut Conversation),
    {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        let Some(slot) = list.iter_mut().find(|c| c.id == id) else {
            return Ok(None);
        };
        f(slot);
        slot.updated_at = now;
        let updated = slot.clone();
        self.save(&list)?;
        Ok(Some(updated))
    }

    pub fn remove(&self, id: &str) -> Result<(), String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        list.retain(|c| c.id != id);
        self.save(&list)
    }

    /// 删除某连接下的所有对话（删连接时级联清理，避免留下够不到密钥的孤儿会话）。
    pub fn remove_by_conn_id(&self, conn_id: &str) -> Result<(), String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        let before = list.len();
        list.retain(|c| c.conn_id != conn_id);
        if list.len() != before {
            self.save(&list)?;
        }
        Ok(())
    }

    /// 启动时把残留 running 的会话置回 idle（进程随退出被杀，不可能还在跑）。
    pub fn mark_idle(&self, now: u64) {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        let mut changed = false;
        for c in list.iter_mut() {
            if c.status == "running" {
                c.status = "idle".into();
                c.updated_at = now;
                changed = true;
            }
        }
        if changed {
            let _ = self.save(&list);
        }
    }
}

/// 把历史消息压成文本记录，供「重建会话」注入新会话衔接上下文。
/// 排除：隐藏消息、错误消息、以及**结尾最后一条用户消息**（那是将要重跑的请求本身）。
/// budget 为字符预算，超出时保留最近的并在开头标注省略；单条消息截前 2000 字符。
pub fn recap(messages: &[Message], budget: usize) -> String {
    let last_user = messages.iter().rposition(|m| m.role == "user");
    let mut lines: Vec<String> = Vec::new();
    for (i, m) in messages.iter().enumerate() {
        if Some(i) == last_user || m.hidden || m.error || m.text.trim().is_empty() {
            continue;
        }
        let who = if m.role == "user" { "用户" } else { "助手" };
        let body: String = m.text.trim().chars().take(2000).collect();
        lines.push(format!("{who}：{body}"));
    }
    let mut used = 0usize;
    let mut kept: Vec<&String> = Vec::new();
    for l in lines.iter().rev() {
        let n = l.chars().count() + 2;
        if used + n > budget && !kept.is_empty() {
            break;
        }
        used += n;
        kept.push(l);
        if used >= budget {
            break;
        }
    }
    let truncated = kept.len() < lines.len();
    let mut out = String::new();
    if truncated {
        out.push_str("（更早的历史已省略）\n");
    }
    for l in kept.iter().rev() {
        out.push_str(l);
        out.push_str("\n\n");
    }
    out.trim_end().to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn msg(role: &str, text: &str) -> Message {
        Message { role: role.into(), text: text.into(), tools: vec![], ts: 0, hidden: false, error: role == "assistant" && text.is_empty(), proposal: None }
    }
    fn conv(id: &str, session: &str, msgs: Vec<Message>) -> Conversation {
        Conversation {
            id: id.into(), conn_id: "c".into(), conn_name: "".into(), site_slug: "s".into(), site_name: "".into(),
            task_type: "free".into(), brain: "claude".into(), model: "sonnet".into(), perm_mode: "full".into(), effort: String::new(),
            session_ref: session.into(), title: "t".into(), messages: msgs, status: "idle".into(), created_at: 0, updated_at: 0, ctx_tokens: 0, total_tokens: 0,
        }
    }

    #[test]
    fn begin_retry_pops_assistant_reuses_last_user_and_marks_running() {
        let base = std::env::temp_dir().join(format!("convo-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&base).unwrap();
        let store = ConvStore::new(&base);
        store
            .upsert(conv("a", "sess-1", vec![msg("user", "第一题"), msg("assistant", "回答一"), msg("user", "第二题"), msg("assistant", "")]))
            .unwrap();

        let text = store.begin_retry("a", 10).unwrap();
        assert_eq!(text, "第二题"); // 取最后一条用户消息
        let c = store.get("a").unwrap();
        assert_eq!(c.status, "running"); // 置 running
        assert_eq!(c.messages.len(), 3); // 末尾失败助手被弹掉
        assert_eq!(c.messages.last().unwrap().role, "user");

        // 正在跑时拒绝
        assert!(store.begin_retry("a", 11).is_err());
        std::fs::remove_dir_all(&base).ok();
    }

    #[test]
    fn recap_excludes_last_user_hidden_and_error() {
        let msgs = vec![
            msg("user", "第一问"),
            msg("assistant", "第一答"),
            Message { role: "assistant".into(), text: "内部".into(), tools: vec![], ts: 0, hidden: true, error: false, proposal: None },
            Message { role: "assistant".into(), text: "报错了".into(), tools: vec![], ts: 0, hidden: false, error: true, proposal: None },
            msg("user", "最新请求"),
        ];
        let r = recap(&msgs, 8000);
        assert!(r.contains("用户：第一问"));
        assert!(r.contains("助手：第一答"));
        assert!(!r.contains("最新请求")); // 将要重跑的请求本身不进 recap
        assert!(!r.contains("内部")); // hidden 排除
        assert!(!r.contains("报错了")); // error 排除
        // 预算收紧 → 保留最近 + 省略标注
        let tight = recap(&msgs, 12);
        assert!(tight.contains("（更早的历史已省略）") || tight.contains("第一答"));
    }

    #[test]
    fn begin_rebuild_works_without_session() {
        let base = std::env::temp_dir().join(format!("convo-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&base).unwrap();
        let store = ConvStore::new(&base);
        // session_ref 为空（首轮就崩的会话）也能重建
        store.upsert(conv("r1", "", vec![msg("user", "建个站"), msg("assistant", "")])).unwrap();
        let text = store.begin_rebuild("r1", 5).unwrap();
        assert_eq!(text, "建个站");
        let c = store.get("r1").unwrap();
        assert_eq!(c.status, "running");
        assert_eq!(c.messages.len(), 1); // 失败助手已弹掉
        std::fs::remove_dir_all(&base).ok();
    }

    #[test]
    fn begin_retry_rejects_no_session_and_missing() {
        let base = std::env::temp_dir().join(format!("convo-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&base).unwrap();
        let store = ConvStore::new(&base);
        store.upsert(conv("b", "", vec![msg("user", "问"), msg("assistant", "")])).unwrap();
        assert!(store.begin_retry("b", 1).is_err()); // 无 session
        assert!(store.begin_retry("nope", 1).is_err()); // 会话不存在
        std::fs::remove_dir_all(&base).ok();
    }
}
