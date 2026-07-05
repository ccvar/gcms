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
    /// claude 的 session uuid 或 codex 的 thread_id；首轮后回填。
    pub session_ref: String,
    pub title: String,
    pub messages: Vec<Message>,
    /// idle | running
    pub status: String,
    pub created_at: u64,
    pub updated_at: u64,
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
