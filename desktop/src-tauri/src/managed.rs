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
    /// L3 专用：每周最多修改的存量篇数。prompt 声明配额 + 要求 AI 自数并在周报如实汇报；
    /// Pilot 侧另有**硬闸**：managed_prefire 按 week_stats 的 edited 口径（updated_at≥周一
    /// && published_at<周一）实测本周已改篇数，配额用完时注入「今天禁止修改存量」权威行。
    #[serde(default = "default_edit_limit")]
    pub weekly_edit_limit: u32,
    /// 90 天运营计划文本（向导生成或手写；是每日任务的方向依据）。
    pub plan: String,
    #[serde(default)]
    pub custom_daily_prompt: String,
    #[serde(default)]
    pub custom_audit_prompt: String,
    #[serde(default)]
    pub custom_report_prompt: String,
    /// 配套任务用的厂商/模型/强度（向导第 3 步选定；旧记录缺省为空）。
    #[serde(default)]
    pub brain: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub effort: String,
    /// 主模型不可用时使用的备用执行器（旧记录缺省为空＝不启用）。
    #[serde(default)]
    pub fallback_brain: String,
    #[serde(default)]
    pub fallback_model: String,
    #[serde(default)]
    pub fallback_effort: String,
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
    /// 上次审计回灌的要点（AUDIT-NOTES 块内容；注入每日任务，空＝还没审计过）。
    #[serde(default)]
    pub audit_notes: String,
    /// 托管开启时间（unix 秒）——周上限配额爬坡的起点。旧记录缺省 0：
    /// 视为已过爬坡期（days_since_enabled 返回 MAX），别把老站掐死。
    #[serde(default)]
    pub enabled_at: u64,
    /// 周报归档（新到旧，封顶 26 条≈半年；content 截断 REPORT_MAX_CHARS）。
    #[serde(default)]
    pub reports: Vec<ReportEntry>,
    pub created_at: u64,
    pub updated_at: u64,
}

/// 归档的一份周报。
#[derive(Clone, Serialize, Deserialize)]
pub struct ReportEntry {
    pub ts: u64,
    /// 周报正文（REPORT-METRICS 块已剥掉；超长截断）。
    pub content: String,
    #[serde(default)]
    pub metrics: ReportMetrics,
}

/// 周报核心指标（AI 按固定块汇报；None＝拿不到，AI 写 -）。仪表盘趋势小柱用。
#[derive(Clone, Serialize, Deserialize, Default)]
pub struct ReportMetrics {
    pub published: Option<i64>,
    pub drafts_new: Option<i64>,
    pub rejected: Option<i64>,
    /// 本周标记报废（discard）的草稿数；旧归档/旧服务端＝None。
    pub discarded: Option<i64>,
    pub tokens: Option<i64>,
    pub impressions: Option<i64>,
    pub clicks: Option<i64>,
}

/// 周报归档条数上限（≈半年）与正文截断长度。
pub const MAX_REPORTS: usize = 26;
pub const REPORT_MAX_CHARS: usize = 20_000;

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
        Self {
            file: data_dir.join("managed.json"),
            lock: Arc::new(Mutex::new(())),
        }
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
        self.read()
            .into_iter()
            .find(|m| m.conn_id == conn_id && m.site_slug == site_slug)
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
        slot.reports.truncate(MAX_REPORTS);
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
/// 有打回记录时注入「近期编辑打回意见」，有审计要点时注入「上次审计要点」。
/// plan/notes/limit/level/audit_notes 任一变化后要重新生成并回写任务。
/// 等级差异只在第 1 条边界：L0 只草稿；L1/L2 允许直接发布**常规文章**（结构性动作仍全部禁止）。
pub fn daily_prompt(
    site_name: &str,
    plan: &str,
    weekly_limit: u32,
    notes: &[ReviewNote],
    level: &str,
    weekly_edit_limit: u32,
    audit_notes: &str,
) -> String {
    let auto_publish = matches!(level, "l1" | "l2" | "l3");
    let rule1 = if auto_publish {
        "1. 常规文章完成并自检通过后，**可以直接发布**（status=published）；把握不足、话题敏感或实验性的内容仍存草稿待审；\
审计纪要等元内容一律草稿。绝不定时发布（不把 status 设为 scheduled）。\
服务端发布有最低质量门槛（正文≥400 字、摘要与 SEO 描述必填、标题 8~120 字），被拒（quality_gate）时按错误提示补齐后重试，不得注水凑字数。\n"
    } else {
        "1. 只创建/修改**草稿**（status=draft）。绝不发布、绝不定时发布——任何时候都不得把 status 设为 published 或 scheduled。\n"
    };
    let mut s = format!(
        "你是站点「{site_name}」的托管运营助手（{lvl}{mode}）。\n\
【90 天运营计划（唯一方向依据）】\n{}\n\n\
今天的任务：按计划推进内容创作。先看近期内容与现有草稿（node scripts/gcms.js list …），\
选定今天最该写的 1 个选题，完成一篇高质量文章（含摘要、SEO 元信息、合适分类、真实贴切的配图与准确 alt；不得用占位图代替）。\
正文须包含 2-3 条指向站内已有相关内容的自然内链（用 similar/list 找目标，锚文本用描述性文字）；\
确实找不到相关文可不加内链，并在会话里注明。\n\
【数据驱动选题（所有等级）】动笔前先跑 `node scripts/gcms.js search-stats --site <本站slug> --days 28`：\
选题优先承接「有曝光但点击低」与「有搜索需求但站内缺内容」的词，并在会话里写明选题依据（哪个词、什么数据）。\
命令报错（密钥缺 stats:read 或服务端较旧）时，注明「统计不可用，按运营计划选题」后继续正常创作——这不算失败。\
关键词只用于定选题与标题方向：正文面向读者自然写作，不得机械重复目标词，标题与首段自然覆盖一次即可——宁可少覆盖，不可堆砌。\n\
【商品站分支（启用了商品类型的站才生效）】开工先确认站点是否启用商品类型：跑 `node scripts/gcms.js types --site <本站slug>` \
或用 `list products` 试探——命令报错或没有商品类型＝纯内容站，本段全部忽略（这不算失败）。启用则本周产出在配额内**商品与文章并重**：\
a) 优先补齐已有商品的缺项（规格不足 3 行、无图、缺 meta_desc、缺英文配对——用 list/get 逐个排查）；\
b) 按 search-stats 里有曝光的商品词优化对应商品页的标题/meta；\
c) 确有新品资料才新建商品（规格≥3 行＋配图＋多语种配对；发布质量门会拦不合格的，被拒（422）按提示补齐，不得虚构凑数）；\
d) 商品同样算入本周产出配额与动笔前查重要求；\
e) 主题配置槽维护：跑 `node scripts/gcms.js theme-options --site <本站slug>` 查看当前主题声明的配置槽与现值\
（命令报错＝服务端较旧，本项整体跳过——这不算失败）；空缺或与实际不符的槽（实力数字/流程承诺/CTA 文案/FAQ/应用行业）\
按站点真实情况补写或更新（PATCH site-profile，仅限 factory.* 键）；**每次改动必须在会话里逐项写明：哪个槽、改前值→改后值、依据**；\
不确定的宁可不改，绝不编造工厂数字（成立年份/员工数等没有依据就留空）。\n\
【硬性边界——逐条遵守，违反任何一条即算失败】\n\
{rule1}\
2. 本周（周一起）新产出（发布＋新建草稿）总数不得超过 {weekly_limit} 篇：以 Pilot 注入的实测计数为准（若无注入则先自行清点），已达上限就只打磨已有草稿，不再新建。\n\
3. 绝不删除任何内容；绝不修改导航、站点资料、语言设置；绝不创建或启用内容类型（唯一例外：主题配置槽——factory.* 各项，见下）。\n\
4. 发布之外一切对线上生效的操作（删除/结构调整）一律留给站点主人在 Pilot 里手动完成。\n\
5. 原创性红线：绝不翻译/改写他站文章成文；绝不虚构第一手经验、测评数据、用户案例或人物引言；\
具体事实与数字必须可溯源（在文中写明依据，拿不准就不写）；不以真人口吻虚构作者身份。\n\
6. 动笔前查重：选题定稿后、动笔前必须跑 `node scripts/gcms.js similar posts --title \"选题标题\" --site <本站slug>`（站内近似查重），\
命中同主题已有文（得分高）就改为打磨该文/相关草稿或换选题，并在会话里写明查重结果；\
similar 命令报错（服务端较旧）时，改用 `node scripts/gcms.js list posts --q <核心词>` 以 2-3 个核心词分别检索替代——这不算失败，\
但查重不看服务端新旧：替代检索也必须执行，不得跳过。\n\
7. 废弃草稿不许静默遗弃：确认不再需要的草稿，用 `node scripts/gcms.js discard posts <id> --reason \"一句话理由\"` 标记报废\
（理由写给管理员看，如「与 #12 重复，内容已并入」「选题放弃：搜索数据不支持」）；标记只对草稿有效，删除永远由站点主人执行；\
命令报错（服务端较旧）时，改为在该草稿正文开头写一行【建议弃用：理由】——这不算失败。\n",
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
E. 再次强调绝对禁区：删除任何内容；修改导航、站点资料、语言设置、内容类型（唯一例外：主题配置槽——factory.* 各项，见「商品站分支」e 项）。\n"
        ));
    }
    if !notes.is_empty() {
        s.push_str("【近期编辑打回意见——务必规避同类问题】\n");
        for n in notes.iter().take(MAX_NOTES) {
            s.push_str(&format!("- 《{}》：{}\n", n.title, n.reason));
        }
    }
    if !audit_notes.trim().is_empty() {
        s.push_str("【上次审计要点——选题避开重复主题、创作时落实内链】\n");
        s.push_str(audit_notes.trim());
        s.push('\n');
    }
    s.push_str("完成后用两三句话汇报：做了什么、内容 id 与状态、明天建议的选题。");
    s
}

/// 每周内容审计任务的 prompt：检查草稿质量/站内查重/内链建议，产出一篇「内容审计纪要」草稿；
/// 汇报末尾要求输出固定 AUDIT-NOTES 块（Pilot 提取后回灌每日任务）。
pub fn audit_prompt(site_name: &str) -> String {
    format!(
        "你是站点「{site_name}」的托管内容审计助手（L0 试运行）。本周任务：产出一份内容审计纪要。\n\
步骤：\n\
1. 列出全部草稿与最近发布的内容（node scripts/gcms.js list …），逐篇检查：标题/摘要/正文质量、SEO 元信息是否齐全、语种覆盖是否一致、\
原创性红线与关键词堆砌痕迹（有无虚构第一手经验/人物引言、不可溯源的数字、翻译改写他站的痕迹、目标词机械重复）。\n\
2. 站内查重：指出主题高度相似或重复的内容（给出 id 与理由）。\n\
3. 内链建议：为最近内容给出 3-5 条具体的站内互链建议（谁链谁、锚文本）。\n\
4. 存量优化建议：跑 `node scripts/gcms.js search-stats --site <本站slug> --days 28`，\
列出「词位在 8~20 的存量优化建议清单」（哪篇页面、哪个词、当前位次/曝光点击、建议动作）——\
**只写进纪要作为建议，不要执行任何修改**（存量修改仅 L3 的每日任务有权执行）；\
统计不可用（缺 stats:read/旧服务端）时注明并跳过本节。\n\
5. 商品健康（启用了商品类型的站才做；`types`/`list products` 报错＝未启用，注明并跳过本节）：\
逐个检查规格完整性（≥3 行）、封面/图集、excerpt/meta_desc、多语种配对（trans_group）、分类挂载——\
把不合格清单列进纪要（哪个商品、缺什么）。\n\
6. 无主草稿排查：找出无主/过时/重复的草稿，确认该报废的用 `node scripts/gcms.js discard posts <id> --reason \"一句话理由\"` 标记\
（命令报错＝服务端较旧，改为只在纪要里给出建议弃用清单——这不算失败），并在纪要单列一节列出（id＋理由）。\n\
7. 主题配置槽体检（theme-options 可用时；命令报错＝服务端较旧，注明并跳过本节）：列出空缺槽与疑似过期的承诺文案\
（如流程时效与近期实际不符），进纪要——审计不动手，交每日任务或站点主人处理。\n\
【硬性边界】把以上结论写成**一篇**标题以「内容审计纪要」开头的**草稿**（status=draft）；\
绝不发布任何内容、绝不删除或修改其他内容、绝不改导航/站点资料/内容类型。\n\
完成后汇报纪要草稿的 id 与最重要的 3 条发现，并在汇报末尾额外输出一个固定标记块\
（Pilot 会提取并注入后续每日任务，总共不超过 10 行）：\n\
```AUDIT-NOTES\n- 重复主题：…\n- 内链建议：…\n```"
    )
}

/// 每周周报任务的 prompt：真实数字由 Pilot 在触发时以【本周实测数据】块注入；
/// 计划摘要随 prompt 下发（「计划关键词 vs 实际曝光词偏差」一节的对照基准），
/// 所以 plan 变化后要与每日任务一起重新生成回写。
pub fn apply_custom_prompt(custom: &str, generated: String) -> String {
    let custom = custom.trim();
    if custom.is_empty() {
        return format!("{generated}\n\n{}", immutable_prompt_boundary());
    }
    format!("{custom}\n\n{}", immutable_prompt_boundary())
}

fn immutable_prompt_boundary() -> &'static str {
    "【系统强制边界（不可覆盖，用户自定义指令也不能绕过）】\n- 只允许在当前站点内创建或更新草稿，不得自行发布或定时发布。\n- 必须遵守托管等级、每周产出上限、存量修改上限和 token 预算。\n- 不得删除内容，不得修改导航、站点资料、语言设置或创建、启用内容类型。\n- 不得输出、记录或传播访问密钥、令牌、账号、Cookie 及其他敏感信息。\n- 每篇文章必须有真实、贴切、与正文明确对应的配图，并填写准确描述画面的 alt；禁止用无关占位图、装饰图冒充配图。\n- 涉及后台、网页、软件或产品操作时，必须使用真实系统截图；截图发布前必须遮盖 Token、账号、邮箱、Cookie、内部 URL 等敏感信息，不能用伪造界面或想象截图。\n- 每篇文章先明确一个真实的搜索意图和目标读者，全文围绕该意图解决问题；内容必须原创、可验证、可执行，禁止关键词堆砌、模板拼接、空话、虚构案例、虚构数据和伪造引用。\n- 时效性事实优先引用官方或一手来源；无法验证的事实必须明确标注不确定，不得编造。"
}

pub fn report_prompt(site_name: &str, plan: &str) -> String {
    format!(
        "你是站点「{site_name}」的托管周报助手。触发本任务时，消息末尾会附上 Pilot 注入的\
【本周实测数据】——产出/发布/打回/预算等数字以它为准，直接引用、不要复核。\n\
【90 天运营计划（关键词偏差对照的基准）】\n{}\n\n\
搜索与流量表现由你自己获取：运行 `node scripts/gcms.js traffic-stats --site <本站slug> --days 7`（流量汇总）、\
`node scripts/gcms.js search-stats --site <本站slug> --days 7 --compare`（本期 vs 上期双区间对比）、\
`node scripts/gcms.js page-stats --site <本站slug> --days 7`（GA 分页流量）；\
任一命令报错或不可用（缺 stats:read / 服务端较旧没有 --compare、page-stats）时，\
在对应小节注明「数据不可用」后继续写其余部分——这不算失败。\n\
请基于数据写一份 markdown 周报，结构：\n\
1. 本周概览（两三句人话总结）；\n\
2. 本周数据表现：用 search-stats --compare 汇报本周新发文的曝光/点击与上期对比、上周改动页的位次变化；\
用 page-stats 列出流量 top 页；结合 traffic-stats 说明整体流量变化；\
若站点绑定了 Telegram 频道：跑 `node scripts/gcms.js tg-stats --site <本站slug>` 汇报当前订阅数\
（与上周周报里的数字对比给环比）；命令报错或未配置时注明「TG 未配置/不可用」即可——这不算失败；\
商品站：汇报本周商品新增/修改数与商品页的搜索表现（search-stats 里商品 URL 对应的词）；未启用商品类型就略过——这不算失败；\n\
3. 计划关键词 vs 实际曝光词偏差：把上方 90 天计划里的关键词方向与 search-stats 实际曝光词对照，\
指出「计划了但没曝光」与「没计划却有曝光」的词，给出下阶段的校准建议；\n\
4. 产出与发布明细（本周改过主题配置槽的，非 L3 也在此带一句改了哪些槽）；5. 打回与整改要点（有打回时逐条回应改进方向）；\
6. 预算与用量（有预算时）；7. 下周计划建议（结合运营计划与数据，给 3-5 条具体选题/动作）。\n\
若数据块含「L3 存量维护」条目，额外单列两节：『本周修改清单』（哪篇改了什么、依据是什么；主题配置槽的改动也如实列入）与\
『观察名单』（改过的页面，提醒主人未来 1-2 周跟踪数据是否回落，可用后台修订历史一键回滚）。\n\
【硬性边界】周报只在对话里输出正文——绝不创建、修改或发布任何站点内容，也不建草稿。\n\
周报写完后，在汇报最末尾额外输出一个固定标记块（Pilot 会提取归档；数字来自上方 Pilot 注入的\
【本周实测数据】与你自跑的 search-stats，拿不到的项写 -，不要编造）：\n\
```REPORT-METRICS\npublished: N\ndrafts_new: N\nrejected: N\ndiscarded: N|-\ntokens: N\nimpressions: N|-\nclicks: N|-\n```",
        plan_snippet(plan, 1200),
    )
}

/// 从周报最后一条 assistant 消息提取 ```REPORT-METRICS``` 块：返回（剥掉块后的正文, 指标）。
/// 无块/畸形块（缺闭合围栏）→ 正文原样、指标全空。行格式 `key: value`，value 为整数才计入，
/// `-`/非数字＝拿不到（None）；未知 key 忽略。
pub fn extract_report_metrics(text: &str) -> (String, ReportMetrics) {
    const TAG: &str = "```REPORT-METRICS";
    let mut metrics = ReportMetrics::default();
    let Some(start) = text.rfind(TAG) else {
        return (text.trim().to_string(), metrics);
    };
    let rest = &text[start + TAG.len()..];
    let Some(end) = rest.find("```") else {
        return (text.trim().to_string(), metrics); // 畸形块：不剥不解析
    };
    for line in rest[..end].lines() {
        let Some((k, v)) = line.split_once(':') else {
            continue;
        };
        let val = v.trim().parse::<i64>().ok();
        match k.trim() {
            "published" => metrics.published = val,
            "drafts_new" => metrics.drafts_new = val,
            "rejected" => metrics.rejected = val,
            "discarded" => metrics.discarded = val,
            "tokens" => metrics.tokens = val,
            "impressions" => metrics.impressions = val,
            "clicks" => metrics.clicks = val,
            _ => {}
        }
    }
    let stripped = format!("{}{}", &text[..start], &rest[end + 3..]);
    (stripped.trim().to_string(), metrics)
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
    s.push_str(&format!(
        "- 本周发布：{} 篇；本周新增草稿：{} 篇（周上限 {}）；当前草稿共 {} 篇\n",
        f.published, f.drafts_new, f.weekly_limit, f.drafts_total
    ));
    if f.budget > 0 {
        s.push_str(&format!(
            "- token 用量：本周 {} / 预算 {}\n",
            f.week_tokens, f.budget
        ));
    } else {
        s.push_str(&format!(
            "- token 用量：本周 {}（未设预算）\n",
            f.week_tokens
        ));
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
        s.push_str(&format!(
            "- L3 存量维护：修改配额 {} 篇/周。请在周报单列『本周修改清单』与『观察名单』两节。\n",
            f.edit_limit
        ));
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
    format!(
        "【本周产出（Pilot 实测，权威计数，直接采用、不必自查）】已产出 {week_output}/{limit} 篇。"
    )
}

/// L3 存量修改硬闸的注入行：edited 为 week_stats 的 edited 口径实测（updated_at≥周一 &&
/// published_at<周一）。配额用完→**不跳过整个任务**，只禁改存量、常规创作照做；未用完→注入权威计数。
pub fn edit_cap_line(edited: u32, limit: u32) -> String {
    if edited >= limit {
        format!("【存量修改配额（Pilot 实测，权威计数）】本周已修改 {edited}/{limit} 篇，配额已用完——今天禁止修改任何存量内容，只做常规创作。")
    } else {
        format!("【存量修改配额（Pilot 实测，权威计数）】本周已修改 {edited}/{limit} 篇。")
    }
}

/// 新站配额爬坡：托管开启天数 → 周产出上限允许的上界（<30 天→7、<60 天→14、之后 50）。
/// 防止新站短期批量灌内容被搜索引擎判责。
pub fn ramp_cap(days_since_enabled: u32) -> u32 {
    if days_since_enabled < 30 {
        7
    } else if days_since_enabled < 60 {
        14
    } else {
        50
    }
}

/// enabled_at → 已开启天数。旧记录 enabled_at=0（字段面世前开启的站）视为已过爬坡期，
/// 返回 u32::MAX——别把老站掐死。
pub fn days_since_enabled(enabled_at: u64, now: u64) -> u32 {
    if enabled_at == 0 {
        return u32::MAX;
    }
    (now.saturating_sub(enabled_at) / 86_400).min(u32::MAX as u64) as u32
}

/// 统计探测归类（预检用）：2xx→ok；否则先看响应体 error 字段（服务端 apiError 形状
/// {"error":code,"message":…}），再拿 HTTP 码兜底——search_console_not_connected→not_connected；
/// missing_scope 或 403→no_scope；其余（404 旧服务端没有该路由等）→unavailable。
pub fn stats_probe_class(status: u16, body: &str) -> &'static str {
    if (200..300).contains(&status) {
        return "ok";
    }
    let code = serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(String::from))
        .unwrap_or_default();
    if code == "search_console_not_connected" {
        return "not_connected";
    }
    if code == "missing_scope" || status == 403 {
        return "no_scope";
    }
    "unavailable"
}

/// 托管开启前预检的软警示（不硬拦，只提醒）：存量 <15 篇 / GSC 未绑定 / 密钥缺 stats:read。
/// stats=="unavailable"（旧服务端）不警示——prompt 自带降级条款；"ok" 自然也不警示。
pub fn precheck_warnings(published_count: u32, stats: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    if published_count < 15 {
        let head = if published_count == 0 {
            "站点还没有已发布内容".to_string()
        } else {
            format!("站点存量内容较少（仅 {published_count} 篇已发布）")
        };
        out.push(format!(
            "{head}：托管的查重、内链、数据驱动选题都需要存量支撑，100% AI 内容的新站也是搜索引擎批量内容判责的高危画像。建议先用对话模式打底：定位与内容支柱、15-20 篇种子内容，再回来开托管。"
        ));
    }
    match stats {
        "not_connected" => out.push(
            "站点未绑定 Google Search Console：数据驱动选题与周报数据节将长期不可用，托管会盲打。建议先在 cms 后台完成绑定。".into(),
        ),
        "no_scope" => out.push(
            "当前密钥缺 stats:read 权限：托管拿不到搜索数据。建议在 cms 后台给密钥勾上「统计数据」。".into(),
        ),
        _ => {}
    }
    out
}

/// 从审计会话最后一条 assistant 消息里提取 ```AUDIT-NOTES``` 块内容（不含围栏，取最后一个块）。
/// 无块/空块返回 None；超过 10 行或 1000 字防御性截断（audit_prompt 已要求 ≤10 行）。
pub fn extract_audit_notes(text: &str) -> Option<String> {
    const TAG: &str = "```AUDIT-NOTES";
    let start = text.rfind(TAG)?;
    let rest = &text[start + TAG.len()..];
    let end = rest.find("```")?;
    let block = rest[..end].trim();
    if block.is_empty() {
        return None;
    }
    let joined = block.lines().take(10).collect::<Vec<_>>().join("\n");
    Some(if joined.chars().count() > 1000 {
        joined.chars().take(1000).collect()
    } else {
        joined
    })
}

/// 预算熔断判定（budget=0 表示不限）。
pub fn budget_exceeded(week_tokens: u64, budget: u64) -> bool {
    budget > 0 && week_tokens >= budget
}

/// 本周 token 累计：条目为（运行时间戳, 该次会话累计 token），只算 week_start 之后的。
pub fn sum_week_tokens(entries: &[(u64, u64)], week_start: u64) -> u64 {
    entries
        .iter()
        .filter(|(ts, _)| *ts >= week_start)
        .map(|(_, n)| n)
        .sum()
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
            return Some(format!(
                "最近 {} 次审核中 {} 次被打回",
                recent.len(),
                rejects
            ));
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
    /// 待审草稿数（已排除 AI 标记报废的）。
    pub drafts: u32,
    /// 本周新增草稿数（created_at 口径）。
    pub drafts_new: u32,
    /// AI 标记报废、等主人清理的草稿数（托管卡「待清理」，>0 才显示）。
    pub drafts_discarded: u32,
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
    let sites = disc
        .get("items")
        .and_then(|i| i.as_array())
        .cloned()
        .unwrap_or_default();
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

async fn get_posts(
    api_base: &str,
    key: &str,
    status: &str,
) -> Result<Vec<serde_json::Value>, String> {
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
    Ok(body
        .get("items")
        .and_then(|i| i.as_array())
        .cloned()
        .unwrap_or_default())
}

/// 托管开启前的机械预检结果（向导软警示用）。
#[derive(Serialize)]
pub struct PrecheckResult {
    /// 已发布条数，≥16 封顶 16（UI 显示口径「≥16」；预检只关心够不够 15 篇）。
    pub published_count: u32,
    /// 统计可用性：ok | not_connected | no_scope | unavailable。
    pub stats: String,
    /// precheck_warnings 生成的人话警示（空＝没问题）。
    pub warnings: Vec<String>,
}

/// 托管开启前预检：① 存量条数（published 前 16 条，lang=all 与 list 口径一致）；
/// ② 统计可用性探测（GET /stats/search?days=1&limit=1，只看可用性不看数据）。
/// 网络失败整体 Err——UI 静默降级（不警示也不拦，预检绝不能挡住向导）。
pub async fn precheck(conn: &Connection, site_slug: &str) -> Result<PrecheckResult, String> {
    let (api_base, key) = site_api(conn, site_slug).await?;
    let client = reqwest::Client::new();
    let resp = client
        .get(format!(
            "{api_base}/posts?status=published&lang=all&limit=16"
        ))
        .header("Authorization", format!("Bearer {key}"))
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if !resp.status().is_success() {
        return Err(format!("读取内容列表失败：{}", resp.status()));
    }
    let body: serde_json::Value = resp.json().await.map_err(|e| e.to_string())?;
    let published_count = body
        .get("items")
        .and_then(|i| i.as_array())
        .map(|a| a.len())
        .unwrap_or(0)
        .min(16) as u32;
    let resp = client
        .get(format!("{api_base}/stats/search?days=1&limit=1"))
        .header("Authorization", format!("Bearer {key}"))
        .timeout(Duration::from_secs(15))
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = resp.status().as_u16();
    let text = resp.text().await.unwrap_or_default();
    let stats = stats_probe_class(status, &text).to_string();
    let warnings = precheck_warnings(published_count, &stats);
    Ok(PrecheckResult {
        published_count,
        stats,
        warnings,
    })
}

/// 待审队列：该站全部草稿（id/标题/语种/更新时间）。
pub async fn list_drafts(conn: &Connection, site_slug: &str) -> Result<Vec<DraftItem>, String> {
    let (api_base, key) = site_api(conn, site_slug).await?;
    let items = get_posts(&api_base, &key, "draft").await?;
    let mut out: Vec<DraftItem> = items
        .iter()
        .filter(|it| !item_discarded(it)) // AI 标记报废的草稿不进待审队列（等主人清理）
        .map(|it| DraftItem {
            id: it.get("id").and_then(|v| v.as_i64()).unwrap_or(0),
            title: it
                .get("title")
                .and_then(|v| v.as_str())
                .unwrap_or("(无标题)")
                .to_string(),
            lang: it
                .get("lang")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string(),
            updated_at: it
                .get("updated_at")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string(),
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
    let monday =
        now.date_naive() - chrono::Duration::days(now.weekday().num_days_from_monday() as i64);
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
    /// 本周被修改的存量总数（edited_titles 同口径，不受清单 20 条上限影响）——L3 修改配额硬闸用。
    pub edited_count: u32,
    /// 被 AI 标记报废（discarded）的草稿数——不计入待审（drafts_total），托管卡「待清理」用。
    pub drafts_discarded: u32,
}

/// 列表项是否被 AI 标记报废（discard）。老服务端没有 discarded 字段＝false，行为零变化。
fn item_discarded(it: &serde_json::Value) -> bool {
    it.get("discarded")
        .and_then(|v| v.as_bool())
        .unwrap_or(false)
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
    for it in published
        .iter()
        .filter(|it| item_time_since(it, "published_at", ws))
    {
        if published_titles.len() >= 20 {
            break;
        }
        let title = it
            .get("title")
            .and_then(|v| v.as_str())
            .unwrap_or("(无标题)");
        let lang = it.get("lang").and_then(|v| v.as_str()).unwrap_or("");
        let date = it
            .get("published_at")
            .and_then(|v| v.as_str())
            .map(|s| s.chars().take(10).collect::<String>())
            .unwrap_or_default();
        published_titles.push(format!("《{title}》（{lang} · {date}）"));
    }
    let published_this_week = published
        .iter()
        .filter(|it| item_time_since(it, "published_at", ws))
        .count() as u32;
    // L3 观察名单 + 修改配额硬闸：本周被动过的存量（updated_at 落在本周、但发布时间在本周之前）。
    let edited_count = published
        .iter()
        .filter(|it| {
            item_time_since(it, "updated_at", ws) && !item_time_since(it, "published_at", ws)
        })
        .count() as u32;
    let mut edited_titles: Vec<String> = Vec::new();
    for it in published.iter().filter(|it| {
        item_time_since(it, "updated_at", ws) && !item_time_since(it, "published_at", ws)
    }) {
        if edited_titles.len() >= 20 {
            break;
        }
        let title = it
            .get("title")
            .and_then(|v| v.as_str())
            .unwrap_or("(无标题)");
        let lang = it.get("lang").and_then(|v| v.as_str()).unwrap_or("");
        edited_titles.push(format!(
            "《{title}》（{lang}，id {}）",
            it.get("id").and_then(|v| v.as_i64()).unwrap_or(0)
        ));
    }
    let drafts = get_posts(&api_base, &key, "draft").await?;
    // 周产出配额按「本周新建」全量计（含已报废的——报废不返还配额，防先建后废钻空子）；
    // 待审数（drafts_total）排除已报废，另计 drafts_discarded 给「待清理」。
    let drafts_new = drafts
        .iter()
        .filter(|it| item_time_since(it, "created_at", ws))
        .count() as u32;
    let drafts_discarded = drafts.iter().filter(|it| item_discarded(it)).count() as u32;
    Ok(WeekStats {
        published_this_week,
        drafts_total: drafts.len() as u32 - drafts_discarded,
        drafts_new,
        week_start: ws,
        published_titles,
        edited_titles,
        edited_count,
        drafts_discarded,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn note(title: &str, reason: &str) -> ReviewNote {
        ReviewNote {
            ts: 1,
            post_id: 7,
            title: title.into(),
            reason: reason.into(),
        }
    }

    fn site(id: &str, slug: &str) -> ManagedSite {
        ManagedSite {
            id: id.into(),
            conn_id: "c1".into(),
            site_slug: slug.into(),
            site_name: slug.into(),
            level: "l0".into(),
            weekly_post_limit: 3,
            weekly_edit_limit: 2,
            plan: "定位：科技博客".into(),
            custom_daily_prompt: String::new(),
            custom_audit_prompt: String::new(),
            custom_report_prompt: String::new(),
            brain: "claude".into(),
            model: "sonnet".into(),
            effort: String::new(),
            fallback_brain: String::new(),
            fallback_model: String::new(),
            fallback_effort: String::new(),
            task_ids: vec!["t-daily".into(), "t-audit".into(), "t-report".into()],
            paused: false,
            review_notes: vec![],
            token_weekly_budget: 0,
            fused_at: 0,
            review_events: vec![],
            demote_note: String::new(),
            audit_notes: String::new(),
            enabled_at: 1,
            reports: vec![],
            created_at: 1,
            updated_at: 1,
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
        assert_eq!(v[0].audit_notes, "", "旧记录没有审计要点");
        assert_eq!(v[0].enabled_at, 0, "旧记录 enabled_at=0（视为已过爬坡期）");
        assert!(v[0].reports.is_empty(), "旧记录没有周报归档");
    }

    /// REPORT-METRICS 提取：有块（数字/-混合）、无块、畸形块、剥离验证、归档封顶。
    #[test]
    fn report_metrics_extraction() {
        let msg = "# 周报正文\n一切正常。\n```REPORT-METRICS\npublished: 3\ndrafts_new: 2\nrejected: 0\ndiscarded: 1\ntokens: 120000\nimpressions: -\nclicks: -\n```\n";
        let (content, m) = extract_report_metrics(msg);
        assert_eq!(m.published, Some(3));
        assert_eq!(m.drafts_new, Some(2));
        assert_eq!(m.rejected, Some(0));
        assert_eq!(m.discarded, Some(1), "本周报废数入指标");
        assert_eq!(m.tokens, Some(120000));
        assert_eq!(m.impressions, None, "写 - 的项为 None");
        assert_eq!(m.clicks, None);
        assert!(content.contains("周报正文") && content.contains("一切正常"));
        assert!(
            !content.contains("REPORT-METRICS") && !content.contains("published:"),
            "块从正文剥掉"
        );
        // 无块：正文原样、指标全空
        let (c2, m2) = extract_report_metrics("普通周报，没有标记块");
        assert_eq!(c2, "普通周报，没有标记块");
        assert!(m2.published.is_none() && m2.clicks.is_none());
        // 畸形块（缺闭合围栏）：不剥不解析
        let (c3, m3) = extract_report_metrics("正文\n```REPORT-METRICS\npublished: 9");
        assert!(c3.contains("published: 9"), "畸形块保留原样");
        assert!(m3.published.is_none());
        // 多块取最后一个；未知 key 忽略
        let (c4, m4) = extract_report_metrics("```REPORT-METRICS\npublished: 1\n```\n中段\n```REPORT-METRICS\npublished: 7\nbogus: 5\n```");
        assert_eq!(m4.published, Some(7));
        assert!(c4.contains("中段"));
        // 归档封顶：mutate 后 reports 只留 26 条
        let dir = std::env::temp_dir().join(format!("managed-{}", uuid::Uuid::new_v4()));
        std::fs::create_dir_all(&dir).unwrap();
        let st = ManagedStore::new(&dir);
        st.upsert(site("m1", "blog")).unwrap();
        for i in 0..30u64 {
            st.mutate("m1", i, |x| {
                x.reports.insert(
                    0,
                    ReportEntry {
                        ts: i,
                        content: format!("r{i}"),
                        metrics: ReportMetrics::default(),
                    },
                )
            })
            .unwrap();
        }
        let m = st.get("m1").unwrap();
        assert_eq!(m.reports.len(), MAX_REPORTS, "周报归档封顶 26 条");
        assert_eq!(m.reports[0].content, "r29", "最新在前");
        std::fs::remove_dir_all(&dir).ok();
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
        let u = st
            .mutate("m1", 99, |m| {
                m.paused = true;
                m.review_notes.insert(0, note("t", "标题党"));
            })
            .unwrap()
            .unwrap();
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
            st.mutate("m1", i, |m| {
                m.review_notes.insert(0, note(&format!("p{i}"), "r"))
            })
            .unwrap();
        }
        let m = st.get("m1").unwrap();
        assert_eq!(m.review_notes.len(), 20, "打回记录只留最近 20 条");
        assert_eq!(m.review_notes[0].title, "p24", "最新的在前");
        std::fs::remove_dir_all(&dir).ok();
    }

    /// 每日 prompt（L0）：计划摘要 + 硬边界（只草稿/限量/不动结构）+ 打回意见注入。
    #[test]
    fn daily_prompt_has_plan_limits_and_notes() {
        let p = daily_prompt(
            "科技站",
            "定位：面向开发者的 AI 周刊",
            3,
            &[note("旧文", "标题太夸张"), note("另一篇", "缺少来源")],
            "l0",
            2,
            "",
        );
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
        let p2 = daily_prompt("科技站", "计划", 5, &[], "l0", 2, "");
        assert!(!p2.contains("打回意见"));
        assert!(p2.contains("不得超过 5 篇"));
    }

    /// L1/L2 与 L0 的唯一差异在第 1 条边界：放开常规文章直接发布，结构性禁令原样保留。
    #[test]
    fn daily_prompt_level_differences() {
        let l1 = daily_prompt("s", "p", 3, &[], "l1", 2, "");
        assert!(l1.contains("L1 自动发布"));
        assert!(l1.contains("可以直接发布"));
        assert!(!l1.contains("绝不发布"), "L1 不应再有全面禁发条款");
        assert!(l1.contains("绝不定时发布"), "scheduled 仍禁");
        assert!(
            l1.contains("绝不删除任何内容") && l1.contains("绝不修改导航"),
            "结构性禁令保留"
        );
        let l2 = daily_prompt("s", "p", 3, &[], "l2", 2, "");
        assert!(l2.contains("L2 自动发布+抽检"));
        assert!(l2.contains("可以直接发布"));
        // 未知等级按 L0 处理（安全侧）
        let unk = daily_prompt("s", "p", 3, &[], "weird", 2, "");
        assert!(unk.contains("L0 试运行") && unk.contains("绝不发布"));
    }

    /// L3 存量维护：配额注入、下线仅转草稿、禁删、数据依据要求、无数据禁改。
    #[test]
    fn daily_prompt_l3_rules() {
        let p = daily_prompt("s", "p", 3, &[], "l3", 4, "");
        assert!(p.contains("L3 存量维护"));
        assert!(p.contains("可以直接发布"), "L3 含 L1 的直发能力");
        assert!(
            p.contains("search-stats") && p.contains("traffic-stats"),
            "先拉数据"
        );
        assert!(p.contains("stats:read"));
        assert!(p.contains("禁止凭感觉修改"), "无数据时禁改存量");
        assert!(p.contains("不得超过 4 篇"), "修改配额注入");
        assert!(p.contains("转草稿下线"));
        assert!(p.contains("绝不删除"), "下线只能转草稿、不能删");
        assert!(p.contains("写明依据"));
        assert!(p.contains("绝对禁区：删除任何内容；修改导航"));
        // 非 L3 不出现存量维护段
        let l1 = daily_prompt("s", "p", 3, &[], "l1", 4, "");
        assert!(!l1.contains("存量维护"));
    }

    /// 全等级数据驱动：每日选题（含降级条款）、审计建议清单、周报数据表现段。
    #[test]
    fn prompts_data_driven_all_levels() {
        // 每日（L0 起就有）：选题前跑 search-stats + 不可用降级条款
        let p = daily_prompt("s", "p", 3, &[], "l0", 2, "");
        assert!(p.contains("数据驱动选题（所有等级）"));
        assert!(p.contains("search-stats") && p.contains("--days 28"));
        assert!(p.contains("有曝光但点击低") && p.contains("缺内容"));
        assert!(p.contains("写明选题依据"));
        assert!(
            p.contains("统计不可用，按运营计划选题"),
            "降级条款：统计不可用继续创作、不算失败"
        );
        assert!(p.contains("不算失败"));
        // L3 的“无数据禁改存量”只约束存量修改，常规选题按上面的降级条款继续
        let l3 = daily_prompt("s", "p", 3, &[], "l3", 2, "");
        assert!(l3.contains("统计不可用，按运营计划选题"));
        assert!(l3.contains("存量维护当天停做"));
        // 审计：词位 8~20 建议清单 + 只建议不动手 + 不可用跳过
        let a = audit_prompt("s");
        assert!(a.contains("search-stats") && a.contains("8~20"));
        assert!(a.contains("只写进纪要作为建议"));
        assert!(a.contains("仅 L3 的每日任务有权执行"));
        assert!(a.contains("注明并跳过"));
        // 周报：自跑 traffic-stats + search-stats --compare + page-stats、加数据表现段、不可用注明
        let r = report_prompt("s", "关键词方向：A、B");
        assert!(r.contains("traffic-stats") && r.contains("--days 7"));
        assert!(
            r.contains("search-stats") && r.contains("--compare"),
            "双区间对比"
        );
        assert!(r.contains("page-stats"), "GA 分页流量");
        assert!(r.contains("本周数据表现"));
        assert!(r.contains("本周新发文的曝光/点击与上期对比"));
        assert!(r.contains("上周改动页的位次变化"));
        assert!(r.contains("流量 top 页"));
        assert!(r.contains("计划关键词 vs 实际曝光词偏差"));
        assert!(r.contains("校准建议"));
        assert!(
            r.contains("关键词方向：A、B"),
            "90 天计划随 prompt 下发（偏差对照基准）"
        );
        assert!(r.contains("数据不可用"));
        assert!(
            r.contains("这不算失败"),
            "新命令带降级条款（老服务端没有 --compare/page-stats）"
        );
        // Telegram 频道订阅数（cms 侧新命令 tg-stats，带降级：未配置/旧服务端不算失败）
        assert!(r.contains("tg-stats --site"));
        assert!(r.contains("汇报当前订阅数"));
        assert!(r.contains("对比给环比"));
        assert!(r.contains("TG 未配置/不可用"));
        // 注入数据（产出/预算）仍以 Pilot 为准
        assert!(r.contains("以它为准"));
    }

    /// 原创性红线 / 动笔前查重 / 反堆砌 / 内链要求：全等级的每日 prompt 都在场。
    #[test]
    fn daily_prompt_originality_dedup_and_interlinks() {
        let p = daily_prompt("s", "p", 3, &[], "l0", 2, "");
        // 1. 原创性红线（硬性边界第 5 条）
        assert!(p.contains("原创性红线"));
        assert!(p.contains("绝不翻译/改写他站文章成文"));
        assert!(p.contains("绝不虚构第一手经验、测评数据、用户案例或人物引言"));
        assert!(p.contains("可溯源") && p.contains("拿不准就不写"));
        assert!(p.contains("不以真人口吻虚构作者身份"));
        // 2. 动笔前查重（similar + 降级替代必须执行）
        assert!(p.contains("动笔前查重"));
        assert!(p.contains("similar posts --title"));
        assert!(p.contains("写明查重结果"));
        assert!(p.contains("list posts --q"), "similar 不可用的替代检索");
        assert!(p.contains("替代检索也必须执行"));
        // 3. 反堆砌
        assert!(p.contains("不得机械重复目标词"));
        assert!(p.contains("宁可少覆盖，不可堆砌"));
        // 4. 内链要求
        assert!(p.contains("2-3 条指向站内已有相关内容的自然内链"));
        assert!(p.contains("确实找不到相关文可不加内链"));
        // L1/L3 同样在场（硬性边界不分等级）
        let l3 = daily_prompt("s", "p", 3, &[], "l3", 2, "");
        assert!(l3.contains("原创性红线") && l3.contains("similar posts --title"));
    }

    /// 主题配置槽（factory.*）受控口子：红线唯一例外句、商品分支 e 项、审计只读体检、周报口径。
    #[test]
    fn prompts_theme_option_slots() {
        let p = daily_prompt("s", "p", 3, &[], "l0", 2, "");
        assert!(
            p.contains("（唯一例外：主题配置槽——factory.* 各项，见下）"),
            "第 3 条红线开受控口子"
        );
        assert!(p.contains("e) 主题配置槽维护"));
        assert!(p.contains("theme-options --site"));
        assert!(p.contains("本项整体跳过——这不算失败"), "旧服务端降级");
        assert!(p.contains("仅限 factory.* 键"));
        assert!(p.contains("哪个槽、改前值→改后值、依据"));
        assert!(p.contains("绝不编造工厂数字"));
        // L3 绝对禁区同步同一例外（全 prompt 共两处例外句）
        let l3 = daily_prompt("s", "p", 3, &[], "l3", 2, "");
        assert!(l3.contains("绝对禁区"));
        assert!(
            l3.matches("唯一例外：主题配置槽").count() >= 2,
            "第 3 条与 L3 禁区各一处"
        );
        // 审计：体检只进纪要不动手；审计边界不开口子（保持只读）
        let a = audit_prompt("s");
        assert!(a.contains("7. 主题配置槽体检"));
        assert!(a.contains("审计不动手"));
        assert!(
            a.contains("绝不改导航/站点资料/内容类型"),
            "审计红线原样，不开例外"
        );
        // 周报：L3 修改清单含配置槽；非 L3 在产出汇报带一句
        let r = report_prompt("s", "计划");
        assert!(r.contains("主题配置槽的改动也如实列入"));
        assert!(r.contains("非 L3 也在此带一句改了哪些槽"));
    }

    /// AI 标记删除接入：每日硬边界第 7 条、审计第 6 步、REPORT-METRICS 新增 discarded 行（全带降级）。
    #[test]
    fn prompts_discard_clauses() {
        let p = daily_prompt("s", "p", 3, &[], "l0", 2, "");
        assert!(p.contains("7. 废弃草稿不许静默遗弃"));
        assert!(p.contains("discard posts <id> --reason"));
        assert!(p.contains("理由写给管理员看"));
        assert!(p.contains("标记只对草稿有效，删除永远由站点主人执行"));
        assert!(
            p.contains("写一行【建议弃用：理由】——这不算失败"),
            "旧服务端降级：正文开头写建议弃用"
        );
        let a = audit_prompt("s");
        assert!(a.contains("6. 无主草稿排查"));
        assert!(a.contains("discard posts <id> --reason"));
        assert!(
            a.contains("建议弃用清单——这不算失败"),
            "旧服务端降级：只列纪要"
        );
        assert!(a.contains("（id＋理由）"));
        let r = report_prompt("s", "计划");
        assert!(r.contains("discarded: N|-"), "指标块含本周报废数");
    }

    /// 商品站分支：三个 prompt 的商品感知条款（全部带降级：报错/无商品类型＝纯内容站零变化）。
    #[test]
    fn prompts_product_site_branch() {
        // 每日：试探→忽略降级、四条 a-d（补缺项/词优化/新建门槛/算配额与查重）
        let p = daily_prompt("s", "p", 3, &[], "l0", 2, "");
        assert!(p.contains("【商品站分支（启用了商品类型的站才生效）】"));
        assert!(p.contains("types --site") && p.contains("list products"));
        assert!(
            p.contains("纯内容站，本段全部忽略（这不算失败）"),
            "降级：旧服务端/未启用零变化"
        );
        assert!(p.contains("商品与文章并重"));
        assert!(
            p.contains("补齐已有商品的缺项")
                && p.contains("缺 meta_desc")
                && p.contains("缺英文配对")
        );
        assert!(p.contains("有曝光的商品词优化对应商品页"));
        assert!(p.contains("确有新品资料才新建商品") && p.contains("规格≥3 行"));
        assert!(p.contains("被拒（422）按提示补齐"));
        assert!(p.contains("商品同样算入本周产出配额与动笔前查重要求"));
        // 审计：商品健康专项 + 跳过降级
        let a = audit_prompt("s");
        assert!(a.contains("商品健康"));
        assert!(a.contains("规格完整性（≥3 行）"));
        assert!(a.contains("trans_group") && a.contains("分类挂载"));
        assert!(a.contains("把不合格清单列进纪要"));
        assert!(a.contains("报错＝未启用，注明并跳过本节"));
        // 周报：商品新增/修改数 + 商品页搜索表现 + 降级
        let r = report_prompt("s", "计划");
        assert!(r.contains("商品站：汇报本周商品新增/修改数"));
        assert!(r.contains("search-stats 里商品 URL 对应的词"));
        assert!(r.contains("未启用商品类型就略过——这不算失败"));
    }

    /// 发布质量门预告：只挂在 L1+ 直发那条边界上；L0 不直发不需要。
    #[test]
    fn daily_prompt_quality_gate_only_when_publishing() {
        let l1 = daily_prompt("s", "p", 3, &[], "l1", 2, "");
        assert!(l1.contains("正文≥400 字、摘要与 SEO 描述必填、标题 8~120 字"));
        assert!(l1.contains("quality_gate"));
        assert!(l1.contains("不得注水凑字数"));
        let l0 = daily_prompt("s", "p", 3, &[], "l0", 2, "");
        assert!(
            !l0.contains("quality_gate"),
            "L0 只草稿，不需要发布质量门预告"
        );
    }

    /// 审计要点回灌：有要点时注入段落，空/空白不注入。
    #[test]
    fn daily_prompt_injects_audit_notes() {
        let p = daily_prompt(
            "s",
            "p",
            3,
            &[],
            "l0",
            2,
            "- 重复主题：AI 周报连发 3 篇\n- 内链建议：《A》→《B》",
        );
        assert!(p.contains("【上次审计要点——选题避开重复主题、创作时落实内链】"));
        assert!(p.contains("AI 周报连发 3 篇"));
        assert!(p.contains("《A》→《B》"));
        let p2 = daily_prompt("s", "p", 3, &[], "l0", 2, "   ");
        assert!(!p2.contains("上次审计要点"), "空白要点不注入");
    }

    /// AUDIT-NOTES 提取：有块取内容、无块 None、空块 None、超长按 10 行截断。
    #[test]
    fn extract_audit_notes_block() {
        let msg =
            "纪要草稿 id=42，发现三条……\n```AUDIT-NOTES\n- 重复主题：X\n- 内链建议：Y\n```\n以上。";
        assert_eq!(
            extract_audit_notes(msg).unwrap(),
            "- 重复主题：X\n- 内链建议：Y"
        );
        assert!(extract_audit_notes("普通汇报，没有标记块").is_none());
        assert!(
            extract_audit_notes("```AUDIT-NOTES\n   \n```").is_none(),
            "空块不回灌"
        );
        assert!(
            extract_audit_notes("```AUDIT-NOTES\n- 没闭合的块").is_none(),
            "无闭合围栏不猜"
        );
        // 超长截断：15 行只留前 10 行
        let long_block: String = (0..15).map(|i| format!("- 第 {i} 条\n")).collect();
        let got = extract_audit_notes(&format!("x\n```AUDIT-NOTES\n{long_block}```")).unwrap();
        assert_eq!(got.lines().count(), 10, "超过 10 行截断");
        assert!(got.contains("第 9 条") && !got.contains("第 10 条"));
        // 多个块取最后一个（正文里引用过示例块时不误取）
        let two = "示例：\n```AUDIT-NOTES\n- 旧\n```\n真正的：\n```AUDIT-NOTES\n- 新\n```";
        assert_eq!(extract_audit_notes(two).unwrap(), "- 新");
    }

    /// L3 存量修改硬闸注入行：未达注计数、达到/超过禁改存量但不停任务。
    #[test]
    fn edit_cap_line_gate() {
        let under = edit_cap_line(1, 2);
        assert!(under.contains("【存量修改配额（Pilot 实测，权威计数）】"));
        assert!(under.contains("本周已修改 1/2 篇"));
        assert!(!under.contains("禁止修改"), "未达配额不禁改");
        let full = edit_cap_line(2, 2);
        assert!(full.contains("本周已修改 2/2 篇，配额已用完"));
        assert!(full.contains("今天禁止修改任何存量内容，只做常规创作"));
        assert!(edit_cap_line(5, 2).contains("禁止修改"), "超额同样禁改");
    }

    /// 配额爬坡三档 + 老数据兜底（enabled_at=0 视为已过爬坡期）。
    #[test]
    fn ramp_cap_stages_and_legacy_fallback() {
        assert_eq!(ramp_cap(0), 7, "<30 天新站钳到 7 篇/周");
        assert_eq!(ramp_cap(29), 7);
        assert_eq!(ramp_cap(30), 14);
        assert_eq!(ramp_cap(59), 14);
        assert_eq!(ramp_cap(60), 50);
        assert_eq!(ramp_cap(365), 50);
        // enabled_at → 天数换算
        let day = 86_400u64;
        assert_eq!(days_since_enabled(1_000_000, 1_000_000 + 3 * day), 3);
        assert_eq!(
            ramp_cap(days_since_enabled(1_000_000, 1_000_000 + 3 * day)),
            7
        );
        assert_eq!(
            ramp_cap(days_since_enabled(1_000_000, 1_000_000 + 45 * day)),
            14
        );
        // 老数据兜底：enabled_at=0 的旧站不受爬坡限制
        assert_eq!(days_since_enabled(0, 1_800_000_000), u32::MAX);
        assert_eq!(
            ramp_cap(days_since_enabled(0, 1_800_000_000)),
            50,
            "老站视为已过爬坡期"
        );
    }

    /// 预检软警示：0/14 篇警示（0 篇换开头）、15 篇/16 封顶不警示；stats 四态；组合两条齐出。
    #[test]
    fn precheck_warning_rules() {
        // 存量档位
        let w0 = precheck_warnings(0, "ok");
        assert_eq!(w0.len(), 1);
        assert!(w0[0].starts_with("站点还没有已发布内容"), "0 篇改开头");
        assert!(w0[0].contains("批量内容判责的高危画像"));
        assert!(w0[0].contains("15-20 篇种子内容"));
        let w14 = precheck_warnings(14, "ok");
        assert_eq!(w14.len(), 1);
        assert!(w14[0].starts_with("站点存量内容较少（仅 14 篇已发布）"));
        assert!(precheck_warnings(15, "ok").is_empty(), "15 篇达标不警示");
        assert!(
            precheck_warnings(16, "ok").is_empty(),
            "16 封顶（实际≥16）不警示"
        );
        // stats 四态
        let wn = precheck_warnings(20, "not_connected");
        assert_eq!(wn.len(), 1);
        assert!(wn[0].contains("未绑定 Google Search Console") && wn[0].contains("托管会盲打"));
        let ws = precheck_warnings(20, "no_scope");
        assert_eq!(ws.len(), 1);
        assert!(ws[0].contains("缺 stats:read 权限") && ws[0].contains("统计数据"));
        assert!(
            precheck_warnings(20, "unavailable").is_empty(),
            "旧服务端不警示（prompt 自带降级）"
        );
        assert!(precheck_warnings(20, "ok").is_empty());
        // 组合：存量 + 统计各一条（存量在前）
        let both = precheck_warnings(3, "not_connected");
        assert_eq!(both.len(), 2);
        assert!(both[0].contains("仅 3 篇已发布") && both[1].contains("Search Console"));
    }

    /// 统计探测归类：2xx→ok；响应体 error 字段优先于 HTTP 码；403 兜底 no_scope；其余 unavailable。
    #[test]
    fn stats_probe_classification() {
        assert_eq!(stats_probe_class(200, r#"{"ok":true,"rows":[]}"#), "ok");
        assert_eq!(
            stats_probe_class(
                400,
                r#"{"error":"search_console_not_connected","message":"未接入"}"#
            ),
            "not_connected"
        );
        assert_eq!(
            stats_probe_class(
                403,
                r#"{"error":"missing_scope","message":"访问权限不足。"}"#
            ),
            "no_scope"
        );
        assert_eq!(
            stats_probe_class(403, "forbidden"),
            "no_scope",
            "非 JSON 体按 HTTP 403 兜底"
        );
        assert_eq!(
            stats_probe_class(404, "404 page not found"),
            "unavailable",
            "旧服务端没有该路由"
        );
        assert_eq!(
            stats_probe_class(500, r#"{"error":"store_error"}"#),
            "unavailable"
        );
        assert_eq!(
            stats_probe_class(502, r#"{"error":"google_api_error"}"#),
            "unavailable"
        );
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
        let p = daily_prompt("s", &long, 3, &[], "l0", 2, "");
        assert!(p.contains("已截断"), "超长计划应截断");
        // 相对断言：3000 字计划只允许贡献「截断上限 2000 + 截断提示」——固定门槛会随
        // 模板条款增长误报（商品站分支加入时就撞过一次）。
        let base = daily_prompt("s", "", 3, &[], "l0", 2, "");
        assert!(
            p.chars().count() < base.chars().count() + 2100,
            "prompt 不应被长计划撑爆"
        );
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
        // 原创性红线与堆砌痕迹进逐篇检查项；末尾要求固定 AUDIT-NOTES 块（Pilot 回灌用）
        assert!(p.contains("原创性红线与关键词堆砌痕迹"));
        assert!(p.contains("```AUDIT-NOTES"));
        assert!(p.contains("- 重复主题：") && p.contains("- 内链建议："));
        assert!(p.contains("不超过 10 行"));
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
            flags
                .iter()
                .enumerate()
                .map(|(i, a)| ReviewEvent {
                    ts: 100 - i as u64,
                    approved: *a,
                })
                .collect()
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
        let mut eleven = ev(&[
            false, true, false, true, false, true, false, true, false, true,
        ]);
        eleven.push(ReviewEvent {
            ts: 1,
            approved: false,
        });
        assert!(should_demote(&eleven).is_some());
    }

    /// 周报数据注入：真实数字/任务近况/打回理由/预算/抽检清单逐项在场。
    #[test]
    fn weekly_report_data_injects_facts() {
        let f = WeekFacts {
            published: 2,
            drafts_total: 5,
            drafts_new: 3,
            weekly_limit: 4,
            week_tokens: 120_000,
            budget: 500_000,
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
        let f2 = WeekFacts {
            published: 0,
            drafts_total: 0,
            drafts_new: 0,
            weekly_limit: 3,
            week_tokens: 10,
            budget: 0,
            task_lines: vec![],
            reject_reasons: vec![],
            published_titles: vec![],
            edit_limit: 0,
            edited_titles: vec![],
        };
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
            published: 1,
            drafts_total: 2,
            drafts_new: 1,
            weekly_limit: 3,
            week_tokens: 10,
            budget: 0,
            task_lines: vec![],
            reject_reasons: vec![],
            published_titles: vec![],
            edit_limit: 2,
            edited_titles: vec!["《旧文 A》（zh，id 7）".into()],
        };
        let s = weekly_report_data(&base);
        assert!(s.contains("修改配额 2 篇/周"));
        assert!(s.contains("本周修改清单") && s.contains("观察名单"));
        assert!(s.contains("《旧文 A》"));
        assert!(s.contains("回滚"), "提醒可用修订历史回滚");
        let none = WeekFacts {
            edited_titles: vec![],
            ..base
        };
        let s2 = weekly_report_data(&none);
        assert!(s2.contains("没有已发布内容被修改"));
    }

    /// 周报 prompt 模板：只输出正文、不建草稿的边界 + 数据以注入为准。
    #[test]
    fn report_prompt_boundaries() {
        let p = report_prompt("科技站", "定位：科技博客");
        assert!(p.contains("科技站"));
        assert!(p.contains("本周实测数据"));
        assert!(p.contains("不建草稿"));
        assert!(p.contains("绝不创建、修改或发布"));
        // 末尾固定 REPORT-METRICS 块要求（Pilot 提取归档；拿不到写 -）
        assert!(p.contains("```REPORT-METRICS\npublished: N\ndrafts_new: N\nrejected: N\ndiscarded: N|-\ntokens: N\nimpressions: N|-\nclicks: N|-\n```"));
        assert!(p.contains("拿不到的项写 -，不要编造"));
    }
}
