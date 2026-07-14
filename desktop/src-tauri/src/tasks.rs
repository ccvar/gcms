//! 定时任务：Pilot 自己存的「提示词 + 站点 + 模型 + 周期」，托盘常驻时到点自动跑一轮
//! agent（每次开一个全新对话），结果进对话历史。持久化到 tasks.json（原子写 + 互斥）。

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

#[derive(Clone, Serialize, Deserialize)]
pub struct ScheduledTask {
    pub id: String,
    pub conn_id: String,
    pub conn_name: String,
    pub site_slug: String,
    pub site_name: String,
    /// article | sitebuild | free
    pub task_type: String,
    pub brain: String,
    pub model: String,
    /// 思考等级（推理强度）：'' 默认 | low | medium | high。
    #[serde(default)]
    pub effort: String,
    /// 多站点：非空时到点对每个站点各开一个会话跑同一指令（顺序执行）。
    /// 空 = 旧单站任务。site_slug/site_name 始终存第一个站，供列表展示与旧版兼容。
    #[serde(default)]
    pub site_slugs: Vec<String>,
    /// 与 site_slugs 对齐的站点名（缺位回落 slug）。
    #[serde(default)]
    pub site_names: Vec<String>,
    pub title: String,
    pub prompt: String,
    /// 周期（分钟）。1440=每天，60=每小时…
    pub interval_minutes: u64,
    /// 下次触发（unix 秒）
    pub next_run: u64,
    pub enabled: bool,
    pub last_run: u64,
    /// "" | ok | error
    pub last_status: String,
    pub last_summary: String,
    /// 上次触发生成的对话 id（点进去能看）
    pub last_conv_id: String,
    pub runs: u64,
    /// 运行记录（新到旧，最多保留最近 20 次）：每次触发一条，含每站结果与会话链接。
    #[serde(default)]
    pub history: Vec<TaskRun>,
    pub created_at: u64,
    pub updated_at: u64,
}

/// 一次触发的运行记录。
#[derive(Clone, Serialize, Deserialize)]
pub struct TaskRun {
    pub ts: u64,
    pub ok: bool,
    pub summary: String,
    #[serde(default)]
    pub sites: Vec<TaskRunSite>,
    /// 订阅限额顺延（非失败语义）：本次没跑或中途撞限，next_run 已顺延到恢复点之后。
    #[serde(default)]
    pub deferred: bool,
}

/// 单个站点在某次触发中的结果。
#[derive(Clone, Serialize, Deserialize)]
pub struct TaskRunSite {
    pub slug: String,
    pub ok: bool,
    #[serde(default)]
    pub conv_id: String,
    #[serde(default)]
    pub error: String,
    /// 该站因订阅限额被顺延/中断（非失败语义）。
    #[serde(default)]
    pub deferred: bool,
}

impl ScheduledTask {
    /// 本次要跑的站点清单 [(slug, name)]：多站任务用 site_slugs，旧单站回落 site_slug。
    pub fn targets(&self) -> Vec<(String, String)> {
        if self.site_slugs.is_empty() {
            return vec![(self.site_slug.clone(), self.site_name.clone())];
        }
        self.site_slugs
            .iter()
            .enumerate()
            .map(|(i, s)| {
                let name = self
                    .site_names
                    .get(i)
                    .filter(|n| !n.trim().is_empty())
                    .cloned()
                    .unwrap_or_else(|| s.clone());
                (s.clone(), name)
            })
            .collect()
    }

    /// 把 next_run 推进到严格大于 now 的下一个整周期点（补跑时跳过错过的窗口，避免紧循环）。
    pub fn advance_past(&mut self, now: u64) {
        let step = self.interval_minutes.max(1) * 60;
        if self.next_run == 0 {
            self.next_run = now + step;
            return;
        }
        while self.next_run <= now {
            self.next_run += step;
        }
    }
}

#[derive(Clone)]
pub struct TaskStore {
    file: PathBuf,
    lock: Arc<Mutex<()>>,
}

impl TaskStore {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            file: data_dir.join("tasks.json"),
            lock: Arc::new(Mutex::new(())),
        }
    }

    fn read(&self) -> Vec<ScheduledTask> {
        let mut v: Vec<ScheduledTask> = match fs::read(&self.file) {
            Ok(raw) => serde_json::from_slice(&raw).unwrap_or_default(),
            Err(_) => Vec::new(),
        };
        v.sort_by(|a, b| a.next_run.cmp(&b.next_run));
        v
    }

    pub fn list(&self) -> Vec<ScheduledTask> {
        let _g = self.lock.lock().unwrap();
        self.read()
    }

    pub fn get(&self, id: &str) -> Option<ScheduledTask> {
        let _g = self.lock.lock().unwrap();
        self.read().into_iter().find(|t| t.id == id)
    }

    fn save(&self, list: &[ScheduledTask]) -> Result<(), String> {
        let raw = serde_json::to_vec_pretty(list).map_err(|e| e.to_string())?;
        let tmp = self.file.with_extension("json.tmp");
        fs::write(&tmp, &raw).map_err(|e| format!("write tasks tmp: {e}"))?;
        fs::rename(&tmp, &self.file).map_err(|e| format!("replace tasks.json: {e}"))
    }

    pub fn upsert(&self, task: ScheduledTask) -> Result<(), String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        if let Some(slot) = list.iter_mut().find(|t| t.id == task.id) {
            *slot = task;
        } else {
            list.push(task);
        }
        self.save(&list)
    }

    pub fn remove(&self, id: &str) -> Result<(), String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        list.retain(|t| t.id != id);
        self.save(&list)
    }

    pub fn mutate<F>(&self, id: &str, f: F) -> Result<Option<ScheduledTask>, String>
    where
        F: FnOnce(&mut ScheduledTask),
    {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        let Some(slot) = list.iter_mut().find(|t| t.id == id) else {
            return Ok(None);
        };
        f(slot);
        let updated = slot.clone();
        self.save(&list)?;
        Ok(Some(updated))
    }
}
