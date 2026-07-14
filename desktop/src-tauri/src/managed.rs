//! 托管站点（P1 · L0 试运行）：把站点交给 AI 按周循环运营，**边界机制化**——
//! 开启托管＝存一条 ManagedSite 记录 + 自动创建两个配套定时任务（每日内容 / 每周审计），
//! AI 永远只产草稿，发布/打回都在 Pilot 的待审队列里由人完成。
//! 持久化 managed.json（原子写 + 互斥，仿 tasks.rs）；HTTP 侧仿 scheduled.rs
//!（同款 Bearer 鉴权 + discovery 解析 api_base）。

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::discovery;
use crate::keychain;
use crate::pack::Connection;

/// 编辑打回记录（注入每日任务 prompt 让 AI 规避同类问题；保留最近 20 条）。
#[derive(Clone, Serialize, Deserialize)]
pub struct ReviewNote {
    pub ts: u64,
    pub post_id: i64,
    pub title: String,
    pub reason: String,
}

/// 审核事件（批准=true / 打回=false，最新在前，留 20 条）——自动降级判定的数据源。
#[derive(Clone, Serialize, Deserialize)]
pub struct ReviewEvent {
    pub ts: u64,
    pub approved: bool,
}

#[derive(Clone, Serialize, Deserialize)]
pub struct ManagedSite {
    pub id: String,
    pub conn_id: String,
    pub site_slug: String,
    pub site_name: String,
    /// P1 只有 "l0"（试运行：只产草稿，人审人发）。
    pub level: String,
    /// 每周新建草稿上限（写进每日任务的硬约束）。
    pub weekly_post_limit: u32,
    /// L3 专用：每周最多修改的存量篇数。**软约束**——prompt 声明配额 + 要求 AI 自数并在
    /// 周报如实汇报；Pilot 侧无法精确计数 AI 的修改行为，硬闸留待 cms 侧提供修改计数后补。
    #[serde(default = "default_edit_limit")]
    pub weekly_edit_limit: u32,
    /// 90 天运营计划文本（向导生成或手写；是每日任务的方向依据）。
    pub plan: String,
    /// 配套任务用的厂商/模型/强度（向导第 3 步选定；旧记录缺省为空）。
    #[serde(default)]
    pub brain: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub effort: String,
    /// 配套定时任务 id：[0]=每日内容，[1]=每周审计，[2]=每周周报（P1 老记录可能只有前两个）。
    pub task_ids: Vec<String>,
    pub paused: bool,
    #[serde(default)]
    pub review_notes: Vec<ReviewNote>,
    /// 每周 token 预算（0=不限）。触顶自动熔断：暂停全部配套任务，恢复需手动。
    #[serde(default)]
    pub token_weekly_budget: u64,
    /// 预算熔断时间（0=未熔断）；手动恢复（resume）时清零。
    #[serde(default)]
    pub fused_at: u64,
    /// 审核事件流（批准/打回，最新在前，留 20）——自动降级的判定数据。
    #[serde(default)]
    pub review_events: Vec<ReviewEvent>,
    /// 自动降级说明（托管卡黄字；手动调级时清空）。
    #[serde(default)]
    pub demote_note: String,
    pub created_at: u64,
    pub updated_at: u64,
}

const MAX_NOTES: usize = 20;

fn default_edit_limit() -> u32 {
    2
}

#[derive(Clone)]
pub struct ManagedStore {
    file: PathBuf,
    lock: Arc<Mutex<()>>,
}

impl ManagedStore {
    pub fn new(data_dir: &Path) -> Self {
        Self { file: data_dir.join("managed.json"), lock: Arc::new(Mutex::new(())) }
    }

    fn read(&self) -> Vec<ManagedSite> {
        match fs::read(&self.file) {
            Ok(raw) => serde_json::from_slice(&raw).unwrap_or_default(),
            Err(_) => Vec::new(),
        }
    }

    fn save(&self, list: &[ManagedSite]) -> Result<(), String> {
        let raw = serde_json::to_vec_pretty(list).map_err(|e| e.to_string())?;
        let tmp = self.file.with_extension("json.tmp");
        fs::write(&tmp, &raw).map_err(|e| format!("write managed tmp: {e}"))?;
        fs::rename(&tmp, &self.file).map_err(|e| format!("replace managed.json: {e}"))
    }

    pub fn list(&self) -> Vec<ManagedSite> {
        let _g = self.lock.lock().unwrap();
        self.read()
    }

    pub fn get(&self, id: &str) -> Option<ManagedSite> {
        let _g = self.lock.lock().unwrap();
        self.read().into_iter().find(|m| m.id == id)
    }

    /// 同一连接同一站点只允许一条托管记录。
    pub fn find_site(&self, conn_id: &str, site_slug: &str) -> Option<ManagedSite> {
        let _g = self.lock.lock().unwrap();
        self.read().into_iter().find(|m| m.conn_id == conn_id && m.site_slug == site_slug)
    }

    pub fn upsert(&self, m: ManagedSite) -> Result<(), String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        if let Some(slot) = list.iter_mut().find(|x| x.id == m.id) {
            *slot = m;
        } else {
            list.push(m);
        }
        self.save(&list)
    }

    pub fn remove(&self, id: &str) -> Result<(), String> {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        list.retain(|m| m.id != id);
        self.save(&list)
    }

    pub fn mutate<F>(&self, id: &str, now: u64, f: F) -> Result<Option<ManagedSite>, String>
    where
        F: FnOnce(&mut ManagedSite),
    {
        let _g = self.lock.lock().unwrap();
        let mut list = self.read();
        let Some(slot) = list.iter_mut().find(|m| m.id == id) else {
            return Ok(None);
        };
        f(slot);
        slot.review_notes.truncate(MAX_NOTES);
        slot.review_events.truncate(MAX_NOTES);
        slot.updated_at = now;
        let updated = slot.clone();
        self.save(&list)?;
        Ok(Some(updated))
    }
}

// ---- prompt 组装（纯函数，单测覆盖）----

/// 计划文本截断（prompt 里只放摘要，防止把 90 天长文整个塞进每日任务）。
fn plan_snippet(plan: &str, max_chars: usize) -> String {
    let t = plan.trim();
    if t.chars().count() <= max_chars {
        return t.to_string();
    }
    let cut: String = t.chars().take(max_chars).collect();
    format!("{cut}\n（计划较长已截断，以上为前 {max_chars} 字）")
}

/// 等级展示名（prompt 与卡片共用口径）。
pub fn level_label(level: &str) -> &'static str {
    match level {
        "l1" => "L1 自动发布",
        "l2" => "L2 自动发布+抽检",
        "l3" => "L3 存量维护",
        _ => "L0 试运行",
    }
}

/// 打回率过高时的降级目标：一次降一档到风险更低的形态（l3→l2，l2/l1→l0）；l0 不再降。
pub fn demote_target(level: &str) -> Option<&'static str> {
    match level {
        "l3" => Some("l2"),
        "l1" | "l2" => Some("l0"),
        _ => None,
    }
}

/// 每日内容任务的 prompt：站点方向来自 plan 摘要；硬边界写死在文本里，
/// 有打回记录时注入「近期编辑打回意见」。plan/notes/limit/level 任一变化后要重新生成并回写任务。
/// 等级差异只在第 1 条边界：L0 只草稿；L1/L2 允许直接发布**常规文章**（结构性动作仍全部禁止）。
pub fn daily_prompt(site_name: &str, plan: &str, weekly_limit: u32, notes: &[ReviewNote], level: &str, weekly_edit_limit: u32) -> String {
    let auto_publish = matches!(level, "l1" | "l2" | "l3");
    let rule1 = if auto_publish {
        "1. 常规文章完成并自检通过后，**可以直接发布**（status=published）；把握不足、话题敏感或实验性的内容仍存草稿待审；\
审计纪要等元内容一律草稿。绝不定时发布（不把 status 设为 scheduled）。\n"
    } else {
        "1. 只创建/修改**草稿**（status=draft）。绝不发布、绝不定时发布——任何时候都不得把 status 设为 published 或 scheduled。\n"
    };
    let mut s = format!(
        "你是站点「{site_name}」的托管运营助手（{lvl}{mode}）。\n\
【90 天运营计划（唯一方向依据）】\n{}\n\n\
今天的任务：按计划推进内容创作。先看近期内容与现有草稿（node scripts/gcms.js list …），\
选定今天最该写的 1 个选题，完成一篇高质量文章（含摘要、SEO 元信息、合适分类；需要配图先用占位说明）。\n\
【数据驱动选题（所有等级）】动笔前先跑 `node scripts/gcms.js search-stats --site <本站slug> --days 28`：\
选题优先承接「有曝光但点击低」与「有搜索需求但站内缺内容」的词，并在会话里写明选题依据（哪个词、什么数据）。\
命令报错（密钥缺 stats:read 或服务端较旧）时，注明「统计不可用，按运营计划选题」后继续正常创作——这不算失败。\n\
【硬性边界——逐条遵守，违反任何一条即算失败】\n\
{rule1}\
2. 本周（周一起）新产出（发布＋新建草稿）总数不得超过 {weekly_limit} 篇：以 Pilot 注入的实测计数为准（若无注入则先自行清点），已达上限就只打磨已有草稿，不再新建。\n\
3. 绝不删除任何内容；绝不修改导航、站点资料、语言设置；绝不创建或启用内容类型。\n\
4. 发布之外一切对线上生效的操作（删除/结构调整）一律留给站点主人在 Pilot 里手动完成。\n",
        plan_snippet(plan, 2000),
        lvl = level_label(level),
        mode = if auto_publish { "" } else { "：一切产出只到草稿，由站点主人审核发布" },
    );
    if level == "l3" {
        s.push_str(&format!(
            "【L3 存量维护——在上述边界内额外开放，并附加以下规则】\n\
A. 动手前先拉数据：`node scripts/gcms.js search-stats --site <本站slug> --days 28`（GSC 词位）\
与 `node scripts/gcms.js traffic-stats --site <本站slug> --days 7`（流量汇总）。命令报错或没有权限\
（密钥缺 stats:read）时，**禁止凭感觉修改任何存量内容**——存量维护当天停做（常规选题按上文「统计不可用」降级条款继续），并在汇报里说明数据不可用。\n\
B. 可以依据数据优化存量：修改旧文的标题/正文/meta/内链。每次修改**必须先在会话里写明依据**\
（针对哪个搜索词、当前排名/点击数据如何、预期改善什么），再动手。\n\
C. 低质旧文只能**转草稿下线**（把 status 改回 draft），绝不删除。\n\
D. 本周（周一起）修改的存量总数（含转草稿下线）不得超过 {weekly_edit_limit} 篇：自行清点本周已改篇数，\
达到即停、只做常规创作；修改清单要在周报里如实汇报。\n\
E. 再次强调绝对禁区：删除任何内容；修改导航、站点资料、语言设置、内容类型。\n"
        ));
    }
    if !notes.is_empty() {
        s.push_str("【近期编辑打回意见——务必规避同类问题】\n");
        for n in notes.iter().take(MAX_NOTES) {
            s.push_str(&format!("- 《{}》：{}\n", n.title, n.reason));
        }
    }
    s.push_str("完成后用两三句话汇报：做了什么、内容 id 与状态、明天建议的选题。");
    s
}

/// 每周内容审计任务的 prompt：检查草稿质量/站内查重/内链建议，产出一篇「内容审计纪要」草稿。
pub fn audit_prompt(site_name: &str) -> String {
    format!(
        "你是站点「{site_name}」的托管内容审计助手（L0 试运行）。本周任务：产出一份内容审计纪要。\n\
步骤：\n\
1. 列出全部草稿与最近发布的内容（node scripts/gcms.js list …），逐篇检查：标题/摘要/正文质量、SEO 元信息是否齐全、语种覆盖是否一致。\n\
2. 站内查重：指出主题高度相似或重复的内容（给出 id 与理由）。\n\
3. 内链建议：为最近内容给出 3-5 条具体的站内互链建议（谁链谁、锚文本）。\n\
4. 存量优化建议：跑 `node scripts/gcms.js search-stats --site <本站slug> --days 28`，\
列出「词位在 8~20 的存量优化建议清单」（哪篇页面、哪个词、当前位次/曝光点击、建议动作）——\
**只写进纪要作为建议，不要执行任何修改**（存量修改仅 L3 的每日任务有权执行）；\
统计不可用（缺 stats:read/旧服务端）时注明并跳过本节。\n\
【硬性边界】把以上结论写成**一篇**标题以「内容审计纪要」开头的**草稿**（status=draft）；\
绝不发布任何内容、绝不删除或修改其他内容、绝不改导航/站点资料/内容类型。\n\
完成后汇报纪要草稿的 id 与最重要的 3 条发现。"
    )
}

/// 每周周报任务的 prompt（静态模板）：真实数字由 Pilot 在触发时以【本周实测数据】块注入。
pub fn report_prompt(site_name: &str) -> String {
    format!(
        "你是站点「{site_name}」的托管周报助手。触发本任务时，消息末尾会附上 Pilot 注入的\
【本周实测数据】——产出/发布/打回/预算等数字以它为准，直接引用、不要复核。\
搜索与流量表现则由你自己获取：运行 `node scripts/gcms.js traffic-stats --site <本站slug> --days 7`\
与 `node scripts/gcms.js search-stats --site <本站slug> --days 28`；命令不可用（缺 stats:read/旧服务端）\
就在该节注明「数据不可用」。\n\
请基于数据写一份 markdown 周报，结构：\n\
1. 本周概览（两三句人话总结）；2. 本周数据表现（流量/搜索词位变化，来自 traffic-stats 与 search-stats）；\
3. 产出与发布明细；4. 打回与整改要点（有打回时逐条回应改进方向）；\
5. 预算与用量（有预算时）；6. 下周计划建议（结合运营计划与数据，给 3-5 条具体选题/动作）。\n\
若数据块含「L3 存量维护」条目，额外单列两节：『本周修改清单』（哪篇改了什么、依据是什么）与\
『观察名单』（改过的页面，提醒主人未来 1-2 周跟踪数据是否回落，可用后台修订历史一键回滚）。\n\
【硬性边界】周报只在对话里输出正文——绝不创建、修改或发布任何站点内容，也不建草稿。"
    )
}

/// 周报注入的数据事实（由 lib.rs 汇总真实数字后传入）。
pub struct WeekFacts {
    pub published: u32,
    pub drafts_total: u32,
    pub drafts_new: u32,
    pub weekly_limit: u32,
    pub week_tokens: u64,
    pub budget: u64,
    /// 每个配套任务一行近况（标题：成功/失败/未跑）。
    pub task_lines: Vec<String>,
    /// 最近打回理由（新到旧，取几条）。
    pub reject_reasons: Vec<String>,
    /// L2/L3 抽检：本周发布清单（其余等级传空）。
    pub published_titles: Vec<String>,
    /// L3 修改配额（篇/周）；0＝非 L3，不出修改清单/观察名单段。
    pub edit_limit: u32,
    /// L3：本周被修改的已发布内容（updated_at≥周一且 published_at<周一，实测口径）。
    pub edited_titles: Vec<String>,
}

/// 组装【本周实测数据】注入块（纯函数，单测覆盖）。
pub fn weekly_report_data(f: &WeekFacts) -> String {
    let mut s = String::from("【本周实测数据（Pilot 注入，以此为准）】\n");
    s.push_str(&format!("- 本周发布：{} 篇；本周新增草稿：{} 篇（周上限 {}）；当前草稿共 {} 篇\n", f.published, f.drafts_new, f.weekly_limit, f.drafts_total));
    if f.budget > 0 {
        s.push_str(&format!("- token 用量：本周 {} / 预算 {}\n", f.week_tokens, f.budget));
    } else {
        s.push_str(&format!("- token 用量：本周 {}（未设预算）\n", f.week_tokens));
    }
    for l in &f.task_lines {
        s.push_str(&format!("- 任务：{l}\n"));
    }
    if f.reject_reasons.is_empty() {
        s.push_str("- 本周无打回记录\n");
    } else {
        s.push_str("- 编辑打回理由（新到旧）：\n");
        for r in &f.reject_reasons {
            s.push_str(&format!("  - {r}\n"));
        }
    }
    if !f.published_titles.is_empty() {
        s.push_str("- 本周自动发布清单（请主人抽查，周报里原样列出）：\n");
        for t in &f.published_titles {
            s.push_str(&format!("  - {t}\n"));
        }
    }
    if f.edit_limit > 0 {
        s.push_str(&format!("- L3 存量维护：修改配额 {} 篇/周。请在周报单列『本周修改清单』与『观察名单』两节。\n", f.edit_limit));
        if f.edited_titles.is_empty() {
            s.push_str("  - 实测（updated_at 口径）：本周没有已发布内容被修改；若每日任务自述有修改，请以其汇报核对后列出。\n");
        } else {
            s.push_str("  - 本周被修改的已发布内容（实测 updated_at 口径；观察名单＝同一清单，提醒主人未来 1-2 周跟踪这些页面的数据是否回落，必要时用后台修订历史一键回滚）：\n");
            for t in &f.edited_titles {
                s.push_str(&format!("    - {t}\n"));
            }
        }
    }
    s
}

// ---- 托管闸门（纯函数，单测覆盖）----

/// 周上限硬闸：本周产出（发布＋新增草稿）已达上限 → 返回跳过原因（任务记录原样展示）。
pub fn weekly_cap_skip(week_output: u32, limit: u32) -> Option<String> {
    (week_output >= limit).then(|| format!("本周已达上限 {week_output}/{limit}，跳过本次运行"))
}

/// 未达上限时注入每日任务 prompt 的权威计数行（替代 AI 自查）。
pub fn weekly_count_line(week_output: u32, limit: u32) -> String {
    format!("【本周产出（Pilot 实测，权威计数，直接采用、不必自查）】已产出 {week_output}/{limit} 篇。")
}

/// 预算熔断判定（budget=0 表示不限）。
pub fn budget_exceeded(week_tokens: u64, budget: u64) -> bool {
    budget > 0 && week_tokens >= budget
}

/// 本周 token 累计：条目为（运行时间戳, 该次会话累计 token），只算 week_start 之后的。
pub fn sum_week_tokens(entries: &[(u64, u64)], week_start: u64) -> u64 {
    entries.iter().filter(|(ts, _)| *ts >= week_start).map(|(_, n)| n).sum()
}

/// 自动降级判定（events 最新在前）：连续 3 次打回；或最近 10 条样本≥5 且打回占比≥50%。
/// 样本下限防止「刚打回 1 次就降级」的过敏触发。命中返回人话原因。
pub fn should_demote(events: &[ReviewEvent]) -> Option<String> {
    if events.len() >= 3 && events.iter().take(3).all(|e| !e.approved) {
        return Some("连续 3 次打回".into());
    }
    let recent: Vec<&ReviewEvent> = events.iter().take(10).collect();
    if recent.len() >= 5 {
        let rejects = recent.iter().filter(|e| !e.approved).count();
        if rejects * 2 >= recent.len() {
            return Some(format!("最近 {} 次审核中 {} 次被打回", recent.len(), rejects));
        }
    }
    None
}

// ---- gcms API（仿 scheduled.rs：discovery 解析 api_base + Bearer key）----

#[derive(Serialize, Clone)]
pub struct DraftItem {
    pub id: i64,
    pub title: String,
    pub lang: String,
    pub updated_at: String,
}

/// 周报数字卡数据。
#[derive(Serialize)]
pub struct ManagedSummary {
    pub published_this_week: u32,
    pub drafts: u32,
    /// 本周新增草稿数（created_at 口径）。
    pub drafts_new: u32,
    /// 本地周一 00:00 的 unix 秒（前端展示「本周」口径用）。
    pub week_start: i64,
    /// 本周托管会话累计 token（配套任务运行记录关联的会话求和）。
    pub week_tokens: u64,
    pub tasks: Vec<TaskBrief>,
}

#[derive(Serialize)]
pub struct TaskBrief {
    pub id: String,
    pub title: String,
    pub enabled: bool,
    pub last_status: String,
    pub last_run: u64,
    pub next_run: u64,
    /// 上次运行的对话（周报卡「查看周报」直达）。
    pub last_conv_id: String,
}

/// 解析站点 api_base + 取连接密钥。
async fn site_api(conn: &Connection, site_slug: &str) -> Result<(String, String), String> {
    let key = keychain::get_key(&conn.id)?;
    let disc = discovery::discover(conn).await?;
    let sites = disc.get("items").and_then(|i| i.as_array()).cloned().unwrap_or_default();
    let api_base = sites
        .iter()
        .find(|s| s.get("slug").and_then(|v| v.as_str()) == Some(site_slug))
        .and_then(|s| s.get("api_base").and_then(|v| v.as_str()))
        .unwrap_or("")
        .trim_end_matches('/')
        .to_string();
    if api_base.is_empty() {
        return Err("没有找到该站点的接口地址".into());
    }
    Ok((api_base, key))
}

async fn get_posts(api_base: &str, key: &str, status: &str) -> Result<Vec<serde_json::Value>, String> {
    let url = format!("{api_base}/posts?status={status}&lang=all&limit=100");
    let resp = reqwest::Client::new()
        .get(&url)
        .header("Authorization", format!("Bearer {key}"))
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if !resp.status().is_success() {
        return Err(format!("读取内容列表失败：{}", resp.status()));
    }
    let body: serde_json::Value = resp.json().await.map_err(|e| e.to_string())?;
    Ok(body.get("items").and_then(|i| i.as_array()).cloned().unwrap_or_default())
}

/// 待审队列：该站全部草稿（id/标题/语种/更新时间）。
pub async fn list_drafts(conn: &Connection, site_slug: &str) -> Result<Vec<DraftItem>, String> {
    let (api_base, key) = site_api(conn, site_slug).await?;
    let items = get_posts(&api_base, &key, "draft").await?;
    let mut out: Vec<DraftItem> = items
        .iter()
        .map(|it| DraftItem {
            id: it.get("id").and_then(|v| v.as_i64()).unwrap_or(0),
            title: it.get("title").and_then(|v| v.as_str()).unwrap_or("(无标题)").to_string(),
            lang: it.get("lang").and_then(|v| v.as_str()).unwrap_or("").to_string(),
            updated_at: it.get("updated_at").and_then(|v| v.as_str()).unwrap_or("").to_string(),
        })
        .collect();
    // 最近更新在前
    out.sort_by(|a, b| b.updated_at.cmp(&a.updated_at));
    Ok(out)
}

/// 批准发布：PUT posts/{id} 只送 {"status":"published"}——服务端 apiUpdateContent 是
/// 指针字段部分更新（api.go），其余字段一概不动；published_at 为空时服务端自动填当前时间
///（store.UpdatePost）。密钥需要 posts 发布权限，没有会得到 403 的人话报错。
pub async fn publish_post(conn: &Connection, site_slug: &str, id: i64) -> Result<(), String> {
    let (api_base, key) = site_api(conn, site_slug).await?;
    let url = format!("{api_base}/posts/{id}");
    let resp = reqwest::Client::new()
        .put(&url)
        .header("Content-Type", "application/json")
        .header("Authorization", format!("Bearer {key}"))
        .body(r#"{"status":"published"}"#)
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status();
    if status.is_success() {
        return Ok(());
    }
    let text = resp.text().await.unwrap_or_default();
    let msg = serde_json::from_str::<serde_json::Value>(&text)
        .ok()
        .and_then(|v| v.get("message").and_then(|m| m.as_str()).map(String::from))
        .unwrap_or_else(|| text.chars().take(120).collect());
    Err(format!("发布失败：{status} {msg}"))
}

/// 本地时区「本周一 00:00」的 unix 秒。
pub fn week_start_local() -> i64 {
    use chrono::{Datelike, Local, TimeZone};
    let now = Local::now();
    let monday = now.date_naive() - chrono::Duration::days(now.weekday().num_days_from_monday() as i64);
    let midnight = monday.and_hms_opt(0, 0, 0).unwrap_or_default();
    Local
        .from_local_datetime(&midnight)
        .single()
        .map(|d| d.timestamp())
        .unwrap_or(0)
}

/// 本周口径的站点内容统计。
pub struct WeekStats {
    pub published_this_week: u32,
    pub drafts_total: u32,
    /// 本周新建的草稿数（created_at >= 周一；含人手建的——周上限按「本周总产出」保守把关）。
    pub drafts_new: u32,
    pub week_start: i64,
    /// 本周发布清单「《标题》（lang · 日期）」——L2 抽检 & 周报用。
    pub published_titles: Vec<String>,
    /// 本周被修改的**存量**已发布内容（updated_at≥周一且 published_at<周一）——L3 观察名单用。
    pub edited_titles: Vec<String>,
}

/// 某条目的时间字段 >= 起点（RFC3339 解析，解析不了按不命中处理）。
fn item_time_since(it: &serde_json::Value, field: &str, since: i64) -> bool {
    it.get(field)
        .and_then(|v| v.as_str())
        .and_then(|s| chrono::DateTime::parse_from_rfc3339(s).ok())
        .map(|d| d.timestamp() >= since)
        .unwrap_or(false)
}

/// 本周（本地周一起）统计：发布数/清单 + 草稿总数/本周新增（limit=100 视为一周窗口的充分上限）。
pub async fn week_stats(conn: &Connection, site_slug: &str) -> Result<WeekStats, String> {
    let (api_base, key) = site_api(conn, site_slug).await?;
    let ws = week_start_local();
    let published = get_posts(&api_base, &key, "published").await?;
    let mut published_titles: Vec<String> = Vec::new();
    for it in published.iter().filter(|it| item_time_since(it, "published_at", ws)) {
        if published_titles.len() >= 20 {
            break;
        }
        let title = it.get("title").and_then(|v| v.as_str()).unwrap_or("(无标题)");
        let lang = it.get("lang").and_then(|v| v.as_str()).unwrap_or("");
        let date = it
            .get("published_at")
            .and_then(|v| v.as_str())
            .map(|s| s.chars().take(10).collect::<String>())
            .unwrap_or_default();
        published_titles.push(format!("《{title}》（{lang} · {date}）"));
    }
    let published_this_week = published.iter().filter(|it| item_time_since(it, "published_at", ws)).count() as u32;
    // L3 观察名单：本周被动过的存量（updated_at 落在本周、但发布时间在本周之前）。
    let mut edited_titles: Vec<String> = Vec::new();
    for it in published
        .iter()
        .filter(|it| item_time_since(it, "updated_at", ws) && !item_time_since(it, "published_at", ws))
    {
        if edited_titles.len() >= 20 {
            break;
        }
        let title = it.get("title").and_then(|v| v.as_str()).unwrap_or("(无标题)");
        let lang = it.get("lang").and_then(|v| v.as_str()).unwrap_or("");
        edited_titles.push(format!("《{title}》（{lang}，id {}）", it.get("id").and_then(|v| v.as_i64()).unwrap_or(0)));
    }
    let drafts = get_posts(&api_base, &key, "draft").await?;
    let drafts_new = drafts.iter().filter(|it| item_time_since(it, "created_at", ws)).count() as u32;
    Ok(WeekStats {
        published_this_week,
        drafts_total: drafts.len() as u32,
        drafts_new,
        week_start: ws,
        published_titles,
        edited_titles,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn note(title: &str, reason: &str) -> ReviewNote {
        ReviewNote { ts: 1, post_id: 7, title: title.into(), reason: reason.into() }
    }

    fn site(id: &str, slug: &str) -> ManagedSite {
        ManagedSite {
            id: id.into(), conn_id: "c1".into(), site_slug: slug.into(), site_name: slug.into(),
            level: "l0".into(), weekly_post_limit: 3, weekly_edit_limit: 2, plan: "定位：科技博客".into(),
            brain: "claude".into(), model: "sonnet".into(), effort: String::new(),
            task_ids: vec!["t-daily".into(), "t-audit".into(), "t-report".into()], paused: false,
            review_notes: vec![], token_weekly_budget: 0, fused_at: 0,
            review_events: vec![], demote_note: String::new(), created_at: 1, updated_at: 1,
        }
    }

    /// 旧版 managed.json（没有 brain/model/effort/预算/审核事件等新字段）必须还能读——serde default 兜底。
    #[test]
    fn old_records_without_model_fields_still_load() {
        let raw = r#"[{"id":"m1","conn_id":"c1","site_slug":"blog","site_name":"Blog","level":"l0",
            "weekly_post_limit":3,"plan":"p","task_ids":["a","b"],"paused":false,
            "review_notes":[],"created_at":1,"updated_at":1}]"#;
        let v: Vec<ManagedSite> = serde_json::from_str(raw).expect("旧记录可读");
        assert_eq!(v[0].brain, "");
        assert_eq!(v[0].model, "");
        assert_eq!(v[0].effort, "");
        assert_eq!(v[0].token_weekly_budget, 0);
        assert_eq!(v[0].fused_at, 0);
        assert!(v[0].review_events.is_empty());
        assert_eq!(v[0].demote_note, "");
        assert_eq!(v[0].weekly_edit_limit, 2, "旧记录修改配额默认 2");
    }

    #[test]
    fn store_roundtrip_and_unique_lookup() {
        let dir = std::env::temp_dir().join(format!("managed-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let st = ManagedStore::new(&dir);
        st.upsert(site("m1", "blog")).unwrap();
        st.upsert(site("m2", "shop")).unwrap();
        assert_eq!(st.list().len(), 2);
        assert!(st.find_site("c1", "blog").is_some());
        assert!(st.find_site("c1", "nope").is_none());
        // mutate：暂停 + 记打回
        let u = st.mutate("m1", 99, |m| { m.paused = true; m.review_notes.insert(0, note("t", "标题党")); }).unwrap().unwrap();
        assert!(u.paused);
        assert_eq!(u.updated_at, 99);
        assert_eq!(u.review_notes.len(), 1);
        st.remove("m2").unwrap();
        assert_eq!(st.list().len(), 1);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn review_notes_capped_at_20() {
        let dir = std::env::temp_dir().join(format!("managed-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let st = ManagedStore::new(&dir);
        st.upsert(site("m1", "blog")).unwrap();
        for i in 0..25 {
            st.mutate("m1", i, |m| m.review_notes.insert(0, note(&format!("p{i}"), "r"))).unwrap();
        }
        let m = st.get("m1").unwrap();
        assert_eq!(m.review_notes.len(), 20, "打回记录只留最近 20 条");
        assert_eq!(m.review_notes[0].title, "p24", "最新的在前");
        std::fs::remove_dir_all(&dir).ok();
    }

    /// 每日 prompt（L0）：计划摘要 + 硬边界（只草稿/限量/不动结构）+ 打回意见注入。
    #[test]
    fn daily_prompt_has_plan_limits_and_notes() {
        let p = daily_prompt("科技站", "定位：面向开发者的 AI 周刊", 3, &[note("旧文", "标题太夸张"), note("另一篇", "缺少来源")], "l0", 2);
        assert!(p.contains("科技站"));
        assert!(p.contains("L0 试运行"));
        assert!(p.contains("定位：面向开发者的 AI 周刊"));
        assert!(p.contains("不得超过 3 篇"));
        assert!(p.contains("status=draft"));
        assert!(p.contains("绝不发布"), "L0 只草稿边界必须写死");
        assert!(p.contains("绝不删除任何内容"));
        assert!(p.contains("绝不修改导航"));
        assert!(p.contains("打回意见"));
        assert!(p.contains("《旧文》：标题太夸张"));
        assert!(p.contains("《另一篇》：缺少来源"));
        // 没有打回记录时不出现该段落
        let p2 = daily_prompt("科技站", "计划", 5, &[], "l0", 2);
        assert!(!p2.contains("打回意见"));
        assert!(p2.contains("不得超过 5 篇"));
    }

    /// L1/L2 与 L0 的唯一差异在第 1 条边界：放开常规文章直接发布，结构性禁令原样保留。
    #[test]
    fn daily_prompt_level_differences() {
        let l1 = daily_prompt("s", "p", 3, &[], "l1", 2);
        assert!(l1.contains("L1 自动发布"));
        assert!(l1.contains("可以直接发布"));
        assert!(!l1.contains("绝不发布"), "L1 不应再有全面禁发条款");
        assert!(l1.contains("绝不定时发布"), "scheduled 仍禁");
        assert!(l1.contains("绝不删除任何内容") && l1.contains("绝不修改导航"), "结构性禁令保留");
        let l2 = daily_prompt("s", "p", 3, &[], "l2", 2);
        assert!(l2.contains("L2 自动发布+抽检"));
        assert!(l2.contains("可以直接发布"));
        // 未知等级按 L0 处理（安全侧）
        let unk = daily_prompt("s", "p", 3, &[], "weird", 2);
        assert!(unk.contains("L0 试运行") && unk.contains("绝不发布"));
    }

    /// L3 存量维护：配额注入、下线仅转草稿、禁删、数据依据要求、无数据禁改。
    #[test]
    fn daily_prompt_l3_rules() {
        let p = daily_prompt("s", "p", 3, &[], "l3", 4);
        assert!(p.contains("L3 存量维护"));
        assert!(p.contains("可以直接发布"), "L3 含 L1 的直发能力");
        assert!(p.contains("search-stats") && p.contains("traffic-stats"), "先拉数据");
        assert!(p.contains("stats:read"));
        assert!(p.contains("禁止凭感觉修改"), "无数据时禁改存量");
        assert!(p.contains("不得超过 4 篇"), "修改配额注入");
        assert!(p.contains("转草稿下线"));
        assert!(p.contains("绝不删除"), "下线只能转草稿、不能删");
        assert!(p.contains("写明依据"));
        assert!(p.contains("绝对禁区：删除任何内容；修改导航"));
        // 非 L3 不出现存量维护段
        let l1 = daily_prompt("s", "p", 3, &[], "l1", 4);
        assert!(!l1.contains("存量维护"));
    }

    /// 全等级数据驱动：每日选题（含降级条款）、审计建议清单、周报数据表现段。
    #[test]
    fn prompts_data_driven_all_levels() {
        // 每日（L0 起就有）：选题前跑 search-stats + 不可用降级条款
        let p = daily_prompt("s", "p", 3, &[], "l0", 2);
        assert!(p.contains("数据驱动选题（所有等级）"));
        assert!(p.contains("search-stats") && p.contains("--days 28"));
        assert!(p.contains("有曝光但点击低") && p.contains("缺内容"));
        assert!(p.contains("写明选题依据"));
        assert!(p.contains("统计不可用，按运营计划选题"), "降级条款：统计不可用继续创作、不算失败");
        assert!(p.contains("不算失败"));
        // L3 的“无数据禁改存量”只约束存量修改，常规选题按上面的降级条款继续
        let l3 = daily_prompt("s", "p", 3, &[], "l3", 2);
        assert!(l3.contains("统计不可用，按运营计划选题"));
        assert!(l3.contains("存量维护当天停做"));
        // 审计：词位 8~20 建议清单 + 只建议不动手 + 不可用跳过
        let a = audit_prompt("s");
        assert!(a.contains("search-stats") && a.contains("8~20"));
        assert!(a.contains("只写进纪要作为建议"));
        assert!(a.contains("仅 L3 的每日任务有权执行"));
        assert!(a.contains("注明并跳过"));
        // 周报：自跑 traffic-stats + search-stats、加数据表现段、不可用注明
        let r = report_prompt("s");
        assert!(r.contains("traffic-stats") && r.contains("--days 7"));
        assert!(r.contains("search-stats"));
        assert!(r.contains("本周数据表现"));
        assert!(r.contains("数据不可用"));
        // 注入数据（产出/预算）仍以 Pilot 为准
        assert!(r.contains("以它为准"));
    }

    /// 降级链：l3→l2、l2/l1→l0、l0 不降；未知等级不降。
    #[test]
    fn demote_chain() {
        assert_eq!(demote_target("l3"), Some("l2"));
        assert_eq!(demote_target("l2"), Some("l0"));
        assert_eq!(demote_target("l1"), Some("l0"));
        assert_eq!(demote_target("l0"), None);
        assert_eq!(demote_target("bogus"), None);
        assert_eq!(level_label("l3"), "L3 存量维护");
    }

    #[test]
    fn daily_prompt_truncates_long_plan() {
        let long: String = "计".repeat(3000);
        let p = daily_prompt("s", &long, 3, &[], "l0", 2);
        assert!(p.contains("已截断"), "超长计划应截断");
        assert!(p.chars().count() < 3300, "prompt 不应被长计划撑爆");
    }

    #[test]
    fn audit_prompt_has_boundaries() {
        let p = audit_prompt("科技站");
        assert!(p.contains("科技站"));
        assert!(p.contains("内容审计纪要"));
        assert!(p.contains("查重"));
        assert!(p.contains("内链"));
        assert!(p.contains("status=draft"));
        assert!(p.contains("绝不发布"));
    }

    /// 周上限硬闸：达到/超过上限给出跳过原因；未达上限给注入计数行。
    #[test]
    fn weekly_cap_gate() {
        assert!(weekly_cap_skip(2, 3).is_none());
        let r = weekly_cap_skip(3, 3).expect("到顶应跳过");
        assert!(r.contains("3/3") && r.contains("跳过"));
        assert!(weekly_cap_skip(9, 3).is_some());
        let line = weekly_count_line(2, 3);
        assert!(line.contains("2/3") && line.contains("不必自查"));
    }

    /// 预算累计与熔断判定：只累计本周条目；budget=0 永不熔断。
    #[test]
    fn budget_sum_and_fuse() {
        let entries = [(100u64, 50_000u64), (200, 30_000), (10, 999_999)]; // ts=10 在周起点前
        assert_eq!(sum_week_tokens(&entries, 100), 80_000);
        assert_eq!(sum_week_tokens(&entries, 0), 1_079_999);
        assert!(!budget_exceeded(80_000, 0), "0=不限，永不熔断");
        assert!(!budget_exceeded(79_999, 80_000));
        assert!(budget_exceeded(80_000, 80_000), "达到即熔断");
    }

    /// 自动降级：连续 3 次打回；或最近 10 条样本≥5 且打回占比≥50%；样本不足不触发。
    #[test]
    fn demote_rules() {
        let ev = |flags: &[bool]| -> Vec<ReviewEvent> {
            flags.iter().enumerate().map(|(i, a)| ReviewEvent { ts: 100 - i as u64, approved: *a }).collect()
        };
        // 连续 3 拒（最新在前）
        assert!(should_demote(&ev(&[false, false, false, true, true])).is_some());
        // 最新一条是批准 → 连续中断；样本 4 条不足以走占比
        assert!(should_demote(&ev(&[true, false, false, false])).is_none());
        // 样本 5 条、3 拒（60%）→ 降
        assert!(should_demote(&ev(&[false, true, false, true, false])).is_some());
        // 样本 5 条、2 拒（40%）→ 不降
        assert!(should_demote(&ev(&[false, true, false, true, true])).is_none());
        // 只有 2 条打回：既不够连续 3，也不够样本 5 → 不降（防过敏）
        assert!(should_demote(&ev(&[false, false])).is_none());
        // 10 条里 5 拒（50%）→ 降；更早的第 11 条不计
        let mut eleven = ev(&[false, true, false, true, false, true, false, true, false, true]);
        eleven.push(ReviewEvent { ts: 1, approved: false });
        assert!(should_demote(&eleven).is_some());
    }

    /// 周报数据注入：真实数字/任务近况/打回理由/预算/抽检清单逐项在场。
    #[test]
    fn weekly_report_data_injects_facts() {
        let f = WeekFacts {
            published: 2, drafts_total: 5, drafts_new: 3, weekly_limit: 4,
            week_tokens: 120_000, budget: 500_000,
            task_lines: vec!["每日内容：成功 5 次 / 失败 1 次".into()],
            reject_reasons: vec!["《旧文》：标题太夸张".into()],
            published_titles: vec!["《AI 周报》（zh · 2026-07-13）".into()],
            edit_limit: 0,
            edited_titles: vec![],
        };
        let s = weekly_report_data(&f);
        assert!(s.contains("本周发布：2 篇"));
        assert!(s.contains("新增草稿：3 篇"));
        assert!(s.contains("周上限 4"));
        assert!(s.contains("120000 / 预算 500000"));
        assert!(s.contains("每日内容：成功 5 次"));
        assert!(s.contains("《旧文》：标题太夸张"));
        assert!(s.contains("抽查") && s.contains("《AI 周报》"));
        // 无预算/无打回/无清单的形态
        let f2 = WeekFacts { published: 0, drafts_total: 0, drafts_new: 0, weekly_limit: 3, week_tokens: 10, budget: 0, task_lines: vec![], reject_reasons: vec![], published_titles: vec![], edit_limit: 0, edited_titles: vec![] };
        let s2 = weekly_report_data(&f2);
        assert!(s2.contains("未设预算"));
        assert!(s2.contains("本周无打回记录"));
        assert!(!s2.contains("抽查"));
        assert!(!s2.contains("存量维护"), "非 L3 不出修改清单段");
    }

    /// L3 周报注入：修改配额 + 修改清单/观察名单（有无实测清单两种形态）。
    #[test]
    fn weekly_report_data_l3_watchlist() {
        let base = WeekFacts {
            published: 1, drafts_total: 2, drafts_new: 1, weekly_limit: 3,
            week_tokens: 10, budget: 0, task_lines: vec![], reject_reasons: vec![],
            published_titles: vec![], edit_limit: 2,
            edited_titles: vec!["《旧文 A》（zh，id 7）".into()],
        };
        let s = weekly_report_data(&base);
        assert!(s.contains("修改配额 2 篇/周"));
        assert!(s.contains("本周修改清单") && s.contains("观察名单"));
        assert!(s.contains("《旧文 A》"));
        assert!(s.contains("回滚"), "提醒可用修订历史回滚");
        let none = WeekFacts { edited_titles: vec![], ..base };
        let s2 = weekly_report_data(&none);
        assert!(s2.contains("没有已发布内容被修改"));
    }

    /// 周报 prompt 模板：只输出正文、不建草稿的边界 + 数据以注入为准。
    #[test]
    fn report_prompt_boundaries() {
        let p = report_prompt("科技站");
        assert!(p.contains("科技站"));
        assert!(p.contains("本周实测数据"));
        assert!(p.contains("不建草稿"));
        assert!(p.contains("绝不创建、修改或发布"));
    }
}
