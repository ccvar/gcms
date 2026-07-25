//! “增长托管”的纯决策内核。
//!
//! 这个模块故意不持有连接、Google 授权或定时任务状态，只接收 GCMS 内容清单、
//! `/stats/search` 和 `/stats/pages` 已有响应的等价结构，输出可解释的候选机会。
//! 调用层负责：
//! 1. 拉取数据并把站点定位匹配结果填入 `StrategyMatch`；
//! 2. 把 `GrowthCandidate` 转换为持久化的托管机会；
//! 3. 在真正发布/修改后，用实际时间重算复盘时间。

use serde::{Deserialize, Serialize};
use std::collections::HashMap;

const DAY_SECS: u64 = 24 * 60 * 60;

/// GCMS 内容与 Google 页面指标之间的最小映射信息。
///
/// `public_path` 应由内容 API/路由层给出；引擎不根据 slug 猜测站点路由。
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct ContentDocument {
    pub id: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub language: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub slug: String,
    #[serde(default)]
    pub public_path: String,
    #[serde(default)]
    pub canonical_url: String,
    #[serde(default)]
    pub published_at: u64,
    #[serde(default)]
    pub updated_at: u64,
}

/// 对应 `/stats/search?group=query_page&compare=1` 的一行。
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct SearchMetricRow {
    #[serde(default)]
    pub query: String,
    #[serde(default)]
    pub page: String,
    #[serde(default)]
    pub clicks: u64,
    #[serde(default)]
    pub impressions: u64,
    #[serde(default)]
    pub ctr: f64,
    #[serde(default)]
    pub position: f64,
    #[serde(default)]
    pub prev_clicks: Option<u64>,
    #[serde(default)]
    pub prev_impressions: Option<u64>,
    #[serde(default)]
    pub prev_ctr: Option<f64>,
    #[serde(default)]
    pub prev_position: Option<f64>,
}

/// 对应 `/stats/pages` 的一行。
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct AnalyticsPageRow {
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub active_users: u64,
    #[serde(default)]
    pub sessions: u64,
    #[serde(default)]
    pub engagement_rate: f64,
    #[serde(default)]
    pub average_session_duration: f64,
}

/// 站点定位对某个查询词的结构化判定。
///
/// `fit` 为 0..1；`excluded=true` 是硬禁区，即使搜索量很大也不会进入可执行队列。
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct StrategyMatch {
    pub query: String,
    #[serde(default)]
    pub pillar: String,
    #[serde(default)]
    pub fit: f64,
    #[serde(default)]
    pub excluded: bool,
}

/// 最近已执行动作，用于阻止同一查询重复建文、同一页面被频繁修改。
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct RecentGrowthAction {
    pub action: OpportunityAction,
    #[serde(default)]
    pub query: String,
    #[serde(default)]
    pub target_path: String,
    pub happened_at: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OpportunityAction {
    NewContent,
    RefreshContent,
    CtrOptimize,
    InternalLink,
    Watch,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CandidateStatus {
    Candidate,
    Observing,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Confidence {
    Low,
    Medium,
    High,
}

/// 机会判断所使用的原始证据快照。
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct CandidateEvidence {
    pub query: String,
    pub page: String,
    pub normalized_path: String,
    pub clicks: u64,
    pub impressions: u64,
    pub ctr: f64,
    pub position: f64,
    pub prev_clicks: Option<u64>,
    pub prev_impressions: Option<u64>,
    pub prev_ctr: Option<f64>,
    pub prev_position: Option<f64>,
    #[serde(default)]
    pub analytics: Option<AnalyticsPageRow>,
}

/// 纯引擎输出。持久化层可在不依赖本模块内部规则的情况下完整解释一次推荐。
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct GrowthCandidate {
    pub action: OpportunityAction,
    pub status: CandidateStatus,
    pub query: String,
    pub pillar: String,
    #[serde(default)]
    pub target_content_id: Option<String>,
    pub target_path: String,
    /// 0..100，确定性规则计算；不交给模型自由打分。
    pub score: f64,
    pub confidence: Confidence,
    pub reason: String,
    pub evidence: CandidateEvidence,
    /// 冷却中的机会才有值。
    #[serde(default)]
    pub cooldown_until: Option<u64>,
    /// “若现在执行”的建议最早复盘时间；执行层应在实际发布/修改时重算。
    pub review_at: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum GrowthDataMode {
    PositioningDriven,
    Hybrid,
    DataDriven,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct GrowthDataHealth {
    pub mode: GrowthDataMode,
    pub search_rows: usize,
    pub analytics_rows: usize,
    pub mapped_search_pages: usize,
    pub distinct_search_pages: usize,
    pub mapping_rate: f64,
    pub warnings: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct GrowthScanResult {
    pub candidates: Vec<GrowthCandidate>,
    pub health: GrowthDataHealth,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct GrowthScanInput {
    pub now: u64,
    #[serde(default)]
    pub contents: Vec<ContentDocument>,
    #[serde(default)]
    pub search_rows: Vec<SearchMetricRow>,
    #[serde(default)]
    pub analytics_rows: Vec<AnalyticsPageRow>,
    #[serde(default)]
    pub strategy_matches: Vec<StrategyMatch>,
    #[serde(default)]
    pub recent_actions: Vec<RecentGrowthAction>,
}

/// 第一版规则都集中在这里，便于后续通过配置灰度，而不是散落在 prompt 里。
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct GrowthRules {
    pub min_strategy_fit: f64,
    pub min_impressions: u64,
    pub high_confidence_impressions: u64,
    pub min_analytics_sessions: u64,
    pub weak_engagement_rate: f64,
    pub strong_engagement_rate: f64,
    pub weak_session_duration: f64,
    pub near_page_min_position: f64,
    pub near_page_max_position: f64,
    pub content_action_cooldown_days: u64,
    pub new_content_cooldown_days: u64,
    pub observation_days: u64,
    pub max_candidates: usize,
    pub include_watch: bool,
}

impl Default for GrowthRules {
    fn default() -> Self {
        Self {
            min_strategy_fit: 0.6,
            min_impressions: 50,
            high_confidence_impressions: 300,
            min_analytics_sessions: 10,
            weak_engagement_rate: 0.35,
            strong_engagement_rate: 0.55,
            weak_session_duration: 20.0,
            near_page_min_position: 8.0,
            near_page_max_position: 20.0,
            content_action_cooldown_days: 28,
            new_content_cooldown_days: 56,
            observation_days: 28,
            max_candidates: 50,
            include_watch: true,
        }
    }
}

/// 把 GSC 完整 URL、GA pagePath 与 GCMS public path 规整成同一个键。
///
/// - 只接受 http(s)、协议相对 URL 或站内 path；
/// - 去掉 query/fragment、重复斜线和尾斜线；
/// - 解码合法 UTF-8 百分号编码；
/// - `/index.html` 与 `/` 归为同一路径。
pub fn normalize_page_path(raw: &str) -> Option<String> {
    let raw = raw.trim();
    if raw.is_empty() {
        return None;
    }

    let path = if let Some(rest) = raw
        .strip_prefix("https://")
        .or_else(|| raw.strip_prefix("http://"))
        .or_else(|| raw.strip_prefix("//"))
    {
        match rest.find('/') {
            Some(i) => &rest[i..],
            None => "/",
        }
    } else if raw.starts_with('/') {
        raw
    } else if raw.contains("://") {
        return None;
    } else {
        if let Some(colon) = raw.find(':') {
            let slash = raw.find('/').unwrap_or(usize::MAX);
            if colon < slash && !raw[..colon].contains('.') {
                return None;
            }
        }
        // `example.com/a` 是常见的人工录入形式；普通 `posts/a` 则仍按站内 path 处理。
        let first = raw.split('/').next().unwrap_or_default();
        if first.contains('.') && !first.starts_with('.') {
            match raw.find('/') {
                Some(i) => &raw[i..],
                None => "/",
            }
        } else {
            raw
        }
    };

    let end = [path.find('?'), path.find('#')]
        .into_iter()
        .flatten()
        .min()
        .unwrap_or(path.len());
    let decoded = percent_decode_utf8(&path[..end]);
    let mut segments: Vec<&str> = Vec::new();
    for part in decoded.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                segments.pop();
            }
            _ => segments.push(part),
        }
    }

    if segments
        .last()
        .map(|s| s.eq_ignore_ascii_case("index.html"))
        .unwrap_or(false)
    {
        segments.pop();
    }
    if segments.is_empty() {
        Some("/".to_string())
    } else {
        Some(format!("/{}", segments.join("/")))
    }
}

fn percent_decode_utf8(raw: &str) -> String {
    let bytes = raw.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let (Some(a), Some(b)) = (hex(bytes[i + 1]), hex(bytes[i + 2])) {
                out.push(a * 16 + b);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8(out).unwrap_or_else(|_| raw.to_string())
}

fn hex(v: u8) -> Option<u8> {
    match v {
        b'0'..=b'9' => Some(v - b'0'),
        b'a'..=b'f' => Some(v - b'a' + 10),
        b'A'..=b'F' => Some(v - b'A' + 10),
        _ => None,
    }
}

fn normalize_query(raw: &str) -> String {
    raw.split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
        .to_lowercase()
}

/// 运行一次确定性机会扫描。
pub fn scan_growth_opportunities(input: &GrowthScanInput, rules: &GrowthRules) -> GrowthScanResult {
    let content_index = build_content_index(&input.contents);
    let analytics_index = build_analytics_index(&input.analytics_rows);
    let strategy_index: HashMap<String, &StrategyMatch> = input
        .strategy_matches
        .iter()
        .map(|m| (normalize_query(&m.query), m))
        .collect();

    let health = assess_data_health(
        &input.search_rows,
        &input.analytics_rows,
        &content_index,
        &strategy_index,
        rules,
    );

    let mut candidates = Vec::new();
    for search in &input.search_rows {
        let query_key = normalize_query(&search.query);
        if query_key.is_empty() || search.position <= 0.0 {
            continue;
        }
        let path = normalize_page_path(&search.page).unwrap_or_default();
        let ambiguous_content_path = content_path_is_ambiguous(&content_index, &path);
        let content = unique_content_for_path(&content_index, &path);
        let analytics = analytics_index.get(&path).cloned();
        let strategy = strategy_index.get(&query_key).copied();

        let (mut action, mut reason) = classify(
            search,
            content,
            ambiguous_content_path,
            analytics.as_ref(),
            strategy,
            rules,
        );
        if action == OpportunityAction::Watch && !rules.include_watch {
            continue;
        }

        let mut cooldown_until = None;
        if action != OpportunityAction::Watch {
            cooldown_until = active_cooldown(
                action,
                &query_key,
                &path,
                input.now,
                &input.recent_actions,
                rules,
            );
            if let Some(until) = cooldown_until {
                action = OpportunityAction::Watch;
                reason = format!(
                    "相同查询或页面仍在冷却期（到 {}），先观察，避免重复建文或频繁修改。",
                    until
                );
            }
        }

        let fit = strategy.map(|m| m.fit.clamp(0.0, 1.0)).unwrap_or(0.0);
        let confidence = candidate_confidence(search, analytics.as_ref(), fit, action, rules);
        let score = candidate_score(search, analytics.as_ref(), fit, action, content.is_some());
        let review_at = cooldown_until.unwrap_or_else(|| {
            input
                .now
                .saturating_add(rules.observation_days.saturating_mul(DAY_SECS))
        });
        candidates.push(GrowthCandidate {
            action,
            status: if action == OpportunityAction::Watch {
                CandidateStatus::Observing
            } else {
                CandidateStatus::Candidate
            },
            query: search.query.trim().to_string(),
            pillar: strategy
                .map(|m| m.pillar.trim().to_string())
                .unwrap_or_default(),
            target_content_id: content.map(|c| c.id.clone()),
            target_path: path.clone(),
            score,
            confidence,
            reason,
            evidence: CandidateEvidence {
                query: search.query.clone(),
                page: search.page.clone(),
                normalized_path: path,
                clicks: search.clicks,
                impressions: search.impressions,
                ctr: search.ctr,
                position: search.position,
                prev_clicks: search.prev_clicks,
                prev_impressions: search.prev_impressions,
                prev_ctr: search.prev_ctr,
                prev_position: search.prev_position,
                analytics,
            },
            cooldown_until,
            review_at,
        });
    }

    // 同一查询、动作和目标只保留证据最强的一条。
    let mut dedup: HashMap<(String, OpportunityAction, String), GrowthCandidate> = HashMap::new();
    for item in candidates {
        let key = (
            normalize_query(&item.query),
            item.action,
            item.target_path.clone(),
        );
        match dedup.get(&key) {
            Some(old) if old.score >= item.score => {}
            _ => {
                dedup.insert(key, item);
            }
        }
    }
    let mut candidates: Vec<GrowthCandidate> = dedup.into_values().collect();
    candidates.sort_by(|a, b| {
        action_rank(a.action)
            .cmp(&action_rank(b.action))
            .then_with(|| {
                b.score
                    .partial_cmp(&a.score)
                    .unwrap_or(std::cmp::Ordering::Equal)
            })
            .then_with(|| a.query.cmp(&b.query))
    });
    candidates.truncate(rules.max_candidates);

    GrowthScanResult { candidates, health }
}

fn classify(
    search: &SearchMetricRow,
    content: Option<&ContentDocument>,
    ambiguous_content_path: bool,
    analytics: Option<&AnalyticsPageRow>,
    strategy: Option<&StrategyMatch>,
    rules: &GrowthRules,
) -> (OpportunityAction, String) {
    let Some(strategy) = strategy else {
        return (
            OpportunityAction::Watch,
            "尚未完成与站点定位的匹配，不能仅凭流量信号自动执行。".to_string(),
        );
    };
    if strategy.excluded || strategy.fit < rules.min_strategy_fit {
        return (
            OpportunityAction::Watch,
            "搜索词不符合站点定位或命中内容禁区，不进入执行队列。".to_string(),
        );
    }
    if search.impressions < rules.min_impressions {
        return (
            OpportunityAction::Watch,
            format!(
                "当前仅 {} 次曝光，样本不足，继续积累数据。",
                search.impressions
            ),
        );
    }

    if ambiguous_content_path {
        return (
            OpportunityAction::Watch,
            "同一路径映射到多个 GCMS 内容，可能存在 canonical 冲突或关键词蚕食；先人工确认，不能自动新建或修改。"
                .to_string(),
        );
    }

    if content.is_none() {
        return (
            OpportunityAction::NewContent,
            format!(
                "符合“{}”方向，近期待搜索需求，但没有映射到 GCMS 内容页，建议先查重再新建。",
                display_pillar(strategy)
            ),
        );
    }

    if let Some(ga) = analytics {
        if ga.sessions >= rules.min_analytics_sessions
            && (ga.engagement_rate < rules.weak_engagement_rate
                || ga.average_session_duration < rules.weak_session_duration)
        {
            return (
                OpportunityAction::RefreshContent,
                format!(
                    "页面已有搜索访问，但 GA 互动偏弱（互动率 {:.0}%、平均停留 {:.0} 秒），优先修正搜索意图与内容结构。",
                    ga.engagement_rate * 100.0,
                    ga.average_session_duration
                ),
            );
        }
    }

    let expected = expected_ctr(search.position);
    if search.position <= 10.0 && search.ctr < expected * 0.55 {
        return (
            OpportunityAction::CtrOptimize,
            format!(
                "页面平均排名 {:.1}，但 CTR {:.1}% 明显低于该排名段参考值，优先优化标题与摘要，不重复建文。",
                search.position,
                search.ctr * 100.0
            ),
        );
    }

    if (rules.near_page_min_position..=rules.near_page_max_position).contains(&search.position) {
        if analytics
            .map(|ga| {
                ga.sessions >= rules.min_analytics_sessions
                    && ga.engagement_rate >= rules.strong_engagement_rate
            })
            .unwrap_or(false)
        {
            return (
                OpportunityAction::InternalLink,
                format!(
                    "页面平均排名 {:.1} 且访问质量较好，优先补充相关内链推动进入首页。",
                    search.position
                ),
            );
        }
        return (
            OpportunityAction::RefreshContent,
            format!(
                "页面平均排名 {:.1}，处于 8～20 名机会区间，建议补充旧文而不是创建重复文章。",
                search.position
            ),
        );
    }

    if let Some(prev) = search.prev_impressions {
        if prev >= rules.min_impressions
            && search.impressions.saturating_mul(10) < prev.saturating_mul(7)
            && search.position <= 30.0
        {
            return (
                OpportunityAction::RefreshContent,
                format!(
                    "曝光由上期 {} 降至 {}，建议检查内容时效性、意图匹配与内链。",
                    prev, search.impressions
                ),
            );
        }
    }

    (
        OpportunityAction::Watch,
        "当前没有足够强的新增或修改信号，保持观察。".to_string(),
    )
}

fn display_pillar(strategy: &StrategyMatch) -> &str {
    if strategy.pillar.trim().is_empty() {
        "已确认定位"
    } else {
        strategy.pillar.trim()
    }
}

fn expected_ctr(position: f64) -> f64 {
    if position <= 1.5 {
        0.20
    } else if position <= 2.5 {
        0.12
    } else if position <= 3.5 {
        0.08
    } else if position <= 5.5 {
        0.05
    } else if position <= 10.0 {
        0.03
    } else {
        0.015
    }
}

fn candidate_score(
    search: &SearchMetricRow,
    analytics: Option<&AnalyticsPageRow>,
    fit: f64,
    action: OpportunityAction,
    mapped: bool,
) -> f64 {
    let fit_score = fit.clamp(0.0, 1.0) * 35.0;
    let demand = ((search.impressions as f64 + 1.0).log10() / 3.0).clamp(0.0, 1.0) * 20.0;
    let attainable = if (4.0..=20.0).contains(&search.position) {
        1.0
    } else if search.position < 4.0 {
        0.65
    } else if search.position <= 40.0 {
        0.6
    } else {
        0.3
    } * 20.0;
    let action_signal = match action {
        OpportunityAction::CtrOptimize => 1.0,
        OpportunityAction::NewContent => 0.95,
        OpportunityAction::RefreshContent => 0.9,
        OpportunityAction::InternalLink => 0.8,
        OpportunityAction::Watch => 0.2,
    } * 15.0;
    let ga_signal = analytics
        .map(|ga| {
            if ga.sessions == 0 {
                0.25
            } else if ga.engagement_rate < 0.35 {
                1.0
            } else if ga.engagement_rate >= 0.55 {
                0.8
            } else {
                0.55
            }
        })
        .unwrap_or(if mapped { 0.25 } else { 0.5 })
        * 5.0;
    let momentum = match (search.prev_impressions, search.prev_position) {
        (Some(prev_imp), Some(prev_pos)) if prev_imp > 0 => {
            let imp_ratio = search.impressions as f64 / prev_imp as f64;
            let imp_part = ((imp_ratio - 0.7) / 0.6).clamp(0.0, 1.0);
            let position_part = ((prev_pos - search.position + 3.0) / 6.0).clamp(0.0, 1.0);
            (imp_part + position_part) / 2.0
        }
        _ => 0.5,
    } * 5.0;

    let sample_penalty = if search.impressions < 50 { 10.0 } else { 0.0 };
    ((fit_score + demand + attainable + action_signal + ga_signal + momentum - sample_penalty)
        .clamp(0.0, 100.0)
        * 10.0)
        .round()
        / 10.0
}

fn candidate_confidence(
    search: &SearchMetricRow,
    analytics: Option<&AnalyticsPageRow>,
    fit: f64,
    action: OpportunityAction,
    rules: &GrowthRules,
) -> Confidence {
    let mut value = if search.impressions >= rules.high_confidence_impressions {
        0.45
    } else if search.impressions >= rules.min_impressions.saturating_mul(2) {
        0.32
    } else if search.impressions >= rules.min_impressions {
        0.2
    } else {
        0.05
    };
    value += fit.clamp(0.0, 1.0) * 0.3;
    if search.prev_impressions.is_some() && search.prev_position.is_some() {
        value += 0.15;
    }
    if analytics
        .map(|ga| ga.sessions >= rules.min_analytics_sessions)
        .unwrap_or(false)
    {
        value += 0.1;
    }
    let confidence = if value >= 0.78 {
        Confidence::High
    } else if value >= 0.48 {
        Confidence::Medium
    } else {
        Confidence::Low
    };
    if action == OpportunityAction::Watch && confidence == Confidence::High {
        Confidence::Medium
    } else {
        confidence
    }
}

fn active_cooldown(
    action: OpportunityAction,
    query_key: &str,
    target_path: &str,
    now: u64,
    recent: &[RecentGrowthAction],
    rules: &GrowthRules,
) -> Option<u64> {
    let days = if action == OpportunityAction::NewContent {
        rules.new_content_cooldown_days
    } else {
        rules.content_action_cooldown_days
    };
    let seconds = days.saturating_mul(DAY_SECS);
    recent
        .iter()
        .filter_map(|item| {
            if item.action == OpportunityAction::Watch {
                return None;
            }
            let same_query = action == OpportunityAction::NewContent
                && !query_key.is_empty()
                && normalize_query(&item.query) == query_key;
            let same_page = action != OpportunityAction::NewContent
                && !target_path.is_empty()
                && normalize_page_path(&item.target_path).as_deref() == Some(target_path);
            if !same_query && !same_page {
                return None;
            }
            let until = item.happened_at.saturating_add(seconds);
            (until > now).then_some(until)
        })
        .max()
}

fn action_rank(action: OpportunityAction) -> u8 {
    if action == OpportunityAction::Watch {
        1
    } else {
        0
    }
}

fn build_content_index<'a>(
    contents: &'a [ContentDocument],
) -> HashMap<String, Vec<&'a ContentDocument>> {
    let mut out: HashMap<String, Vec<&ContentDocument>> = HashMap::new();
    for content in contents {
        let mut paths = Vec::new();
        if let Some(path) = normalize_page_path(&content.public_path) {
            paths.push(path);
        }
        if let Some(path) = normalize_page_path(&content.canonical_url) {
            if !paths.contains(&path) {
                paths.push(path);
            }
        }
        for path in paths {
            out.entry(path).or_default().push(content);
        }
    }
    out
}

/// 路径同时指向多个内容 ID 时不猜测，防止把关键词蚕食误判成一个正常页面。
fn unique_content_for_path<'a>(
    index: &'a HashMap<String, Vec<&'a ContentDocument>>,
    path: &str,
) -> Option<&'a ContentDocument> {
    let matches = index.get(path)?;
    let first = *matches.first()?;
    matches.iter().all(|x| x.id == first.id).then_some(first)
}

fn content_path_is_ambiguous(index: &HashMap<String, Vec<&ContentDocument>>, path: &str) -> bool {
    let Some(matches) = index.get(path) else {
        return false;
    };
    let Some(first) = matches.first() else {
        return false;
    };
    matches.iter().any(|item| item.id != first.id)
}

fn build_analytics_index(rows: &[AnalyticsPageRow]) -> HashMap<String, AnalyticsPageRow> {
    let mut out = HashMap::<String, AnalyticsPageRow>::new();
    for row in rows {
        let Some(path) = normalize_page_path(&row.path) else {
            continue;
        };
        let entry = out.entry(path.clone()).or_insert_with(|| AnalyticsPageRow {
            path,
            ..AnalyticsPageRow::default()
        });
        let old_sessions = entry.sessions;
        let total_sessions = old_sessions.saturating_add(row.sessions);
        if total_sessions > 0 {
            entry.engagement_rate = (entry.engagement_rate * old_sessions as f64
                + row.engagement_rate * row.sessions as f64)
                / total_sessions as f64;
            entry.average_session_duration = (entry.average_session_duration * old_sessions as f64
                + row.average_session_duration * row.sessions as f64)
                / total_sessions as f64;
        }
        entry.active_users = entry.active_users.saturating_add(row.active_users);
        entry.sessions = total_sessions;
    }
    out
}

fn assess_data_health(
    search_rows: &[SearchMetricRow],
    analytics_rows: &[AnalyticsPageRow],
    content_index: &HashMap<String, Vec<&ContentDocument>>,
    strategy_index: &HashMap<String, &StrategyMatch>,
    rules: &GrowthRules,
) -> GrowthDataHealth {
    let mut pages: Vec<String> = search_rows
        .iter()
        .filter_map(|row| normalize_page_path(&row.page))
        .collect();
    pages.sort();
    pages.dedup();
    let mapped = pages
        .iter()
        .filter(|path| unique_content_for_path(content_index, path).is_some())
        .count();
    let mapping_rate = if pages.is_empty() {
        0.0
    } else {
        mapped as f64 / pages.len() as f64
    };
    let enough_search = search_rows
        .iter()
        .any(|row| row.impressions >= rules.min_impressions);
    let enough_strategy = search_rows
        .iter()
        .filter(|row| strategy_index.contains_key(&normalize_query(&row.query)))
        .count();
    let mut warnings = Vec::new();
    if search_rows.is_empty() {
        warnings.push("尚无 GSC 数据，将按站点定位积累基础内容。".to_string());
    } else if !enough_search {
        warnings.push("GSC 样本量较少，暂不做激进的数据优化。".to_string());
    }
    if analytics_rows.is_empty() {
        warnings.push("尚无 GA 页面质量信号，仍可依据定位与 GSC 发现机会。".to_string());
    }
    if !pages.is_empty() && mapping_rate < 0.5 {
        warnings.push("Google 页面与 GCMS 内容映射率偏低，需要先校准 canonical/path。".to_string());
    }
    if !search_rows.is_empty() && enough_strategy * 2 < search_rows.len() {
        warnings.push("超过一半搜索词尚未完成站点定位匹配。".to_string());
    }

    let mode = if search_rows.is_empty() || !enough_search {
        GrowthDataMode::PositioningDriven
    } else if analytics_rows.is_empty() || mapping_rate < 0.5 {
        GrowthDataMode::Hybrid
    } else {
        GrowthDataMode::DataDriven
    };
    GrowthDataHealth {
        mode,
        search_rows: search_rows.len(),
        analytics_rows: analytics_rows.len(),
        mapped_search_pages: mapped,
        distinct_search_pages: pages.len(),
        mapping_rate,
        warnings,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn content(id: &str, path: &str) -> ContentDocument {
        ContentDocument {
            id: id.into(),
            title: format!("Content {id}"),
            public_path: path.into(),
            ..ContentDocument::default()
        }
    }

    fn search(
        query: &str,
        page: &str,
        impressions: u64,
        ctr: f64,
        position: f64,
    ) -> SearchMetricRow {
        SearchMetricRow {
            query: query.into(),
            page: page.into(),
            clicks: ((impressions as f64) * ctr).round() as u64,
            impressions,
            ctr,
            position,
            prev_clicks: Some(2),
            prev_impressions: Some(impressions.saturating_sub(20)),
            prev_ctr: Some(0.02),
            prev_position: Some(position + 2.0),
        }
    }

    fn strategy(query: &str) -> StrategyMatch {
        StrategyMatch {
            query: query.into(),
            pillar: "设备选型".into(),
            fit: 0.9,
            excluded: false,
        }
    }

    fn scan(
        rows: Vec<SearchMetricRow>,
        contents: Vec<ContentDocument>,
        analytics: Vec<AnalyticsPageRow>,
        strategies: Vec<StrategyMatch>,
    ) -> GrowthScanResult {
        scan_growth_opportunities(
            &GrowthScanInput {
                now: 1_700_000_000,
                contents,
                search_rows: rows,
                analytics_rows: analytics,
                strategy_matches: strategies,
                recent_actions: vec![],
            },
            &GrowthRules::default(),
        )
    }

    #[test]
    fn page_path_normalization_matches_gsc_ga_and_content() {
        assert_eq!(
            normalize_page_path("https://Example.COM/zh/posts/%E6%B5%8B%E8%AF%95/?utm=x#top"),
            Some("/zh/posts/测试".into())
        );
        assert_eq!(
            normalize_page_path("/zh//posts/./guide/index.html"),
            Some("/zh/posts/guide".into())
        );
        assert_eq!(
            normalize_page_path("example.com/a/../b/"),
            Some("/b".into())
        );
        assert_eq!(normalize_page_path("mailto:a@example.com"), None);
    }

    #[test]
    fn low_ctr_existing_page_becomes_ctr_optimization() {
        let row = search(
            "industrial laser guide",
            "https://example.com/en/posts/laser-guide/",
            500,
            0.005,
            5.0,
        );
        let result = scan(
            vec![row],
            vec![content("42", "/en/posts/laser-guide")],
            vec![],
            vec![strategy("industrial laser guide")],
        );
        let item = &result.candidates[0];
        assert_eq!(item.action, OpportunityAction::CtrOptimize);
        assert_eq!(item.target_content_id.as_deref(), Some("42"));
        assert!(item.reason.contains("标题与摘要"));
        assert!(item.score > 60.0);
    }

    #[test]
    fn near_page_with_good_engagement_prefers_internal_link() {
        let row = search(
            "laser maintenance",
            "https://example.com/en/posts/maintenance/",
            280,
            0.025,
            12.0,
        );
        let ga = AnalyticsPageRow {
            path: "/en/posts/maintenance".into(),
            active_users: 50,
            sessions: 60,
            engagement_rate: 0.72,
            average_session_duration: 95.0,
        };
        let result = scan(
            vec![row],
            vec![content("7", "/en/posts/maintenance/")],
            vec![ga],
            vec![strategy("laser maintenance")],
        );
        assert_eq!(result.candidates[0].action, OpportunityAction::InternalLink);
        assert_eq!(result.health.mode, GrowthDataMode::DataDriven);
    }

    #[test]
    fn weak_ga_engagement_prefers_refresh_over_more_content() {
        let row = search(
            "laser cost",
            "https://example.com/en/posts/cost/",
            350,
            0.04,
            7.0,
        );
        let ga = AnalyticsPageRow {
            path: "/en/posts/cost/?ref=nav".into(),
            active_users: 30,
            sessions: 40,
            engagement_rate: 0.2,
            average_session_duration: 9.0,
        };
        let result = scan(
            vec![row],
            vec![content("9", "https://example.com/en/posts/cost/")],
            vec![ga],
            vec![strategy("laser cost")],
        );
        assert_eq!(
            result.candidates[0].action,
            OpportunityAction::RefreshContent
        );
        assert!(result.candidates[0].reason.contains("互动偏弱"));
    }

    #[test]
    fn relevant_unmapped_query_becomes_new_content() {
        let row = search(
            "laser maintenance checklist",
            "https://example.com/en/category/maintenance/",
            420,
            0.012,
            14.0,
        );
        let result = scan(
            vec![row],
            vec![content("1", "/en/posts/existing")],
            vec![],
            vec![strategy("laser maintenance checklist")],
        );
        assert_eq!(result.candidates[0].action, OpportunityAction::NewContent);
        assert!(result.candidates[0].reason.contains("先查重"));
        assert_eq!(result.health.mode, GrowthDataMode::Hybrid);
    }

    #[test]
    fn missing_or_excluded_strategy_never_executes() {
        let mut excluded = strategy("cheap consumer laser");
        excluded.excluded = true;
        let result = scan(
            vec![
                search("unknown demand", "https://example.com/a", 500, 0.01, 9.0),
                search(
                    "cheap consumer laser",
                    "https://example.com/b",
                    2_000,
                    0.01,
                    4.0,
                ),
            ],
            vec![],
            vec![],
            vec![excluded],
        );
        assert_eq!(result.candidates.len(), 2);
        assert!(result
            .candidates
            .iter()
            .all(|x| x.action == OpportunityAction::Watch));
    }

    #[test]
    fn cooldown_turns_an_executable_candidate_into_watch() {
        let now = 1_700_000_000;
        let query = "laser checklist";
        let result = scan_growth_opportunities(
            &GrowthScanInput {
                now,
                contents: vec![],
                search_rows: vec![search(
                    query,
                    "https://example.com/category/laser/",
                    400,
                    0.01,
                    13.0,
                )],
                analytics_rows: vec![],
                strategy_matches: vec![strategy(query)],
                recent_actions: vec![RecentGrowthAction {
                    action: OpportunityAction::NewContent,
                    query: " LASER   CHECKLIST ".into(),
                    target_path: String::new(),
                    happened_at: now - 7 * DAY_SECS,
                }],
            },
            &GrowthRules::default(),
        );
        let item = &result.candidates[0];
        assert_eq!(item.action, OpportunityAction::Watch);
        assert_eq!(item.status, CandidateStatus::Observing);
        assert!(item.cooldown_until.unwrap() > now);
        assert_eq!(item.review_at, item.cooldown_until.unwrap());
    }

    #[test]
    fn ambiguous_content_path_is_not_silently_attributed() {
        let query = "laser guide";
        let result = scan(
            vec![search(
                query,
                "https://example.com/en/posts/guide/",
                300,
                0.03,
                10.0,
            )],
            vec![
                content("1", "/en/posts/guide"),
                content("2", "/en/posts/guide/"),
            ],
            vec![],
            vec![strategy(query)],
        );
        assert_eq!(result.candidates[0].action, OpportunityAction::Watch);
        assert!(result.candidates[0].target_content_id.is_none());
        assert!(result.candidates[0].reason.contains("关键词蚕食"));
        assert!(result.health.mapping_rate < 0.5);
    }

    #[test]
    fn duplicate_normalized_rows_keep_one_candidate() {
        let query = "laser guide";
        let result = scan(
            vec![
                search(query, "https://example.com/en/posts/guide/", 200, 0.01, 6.0),
                search(query, "/en/posts/guide?x=1", 400, 0.01, 6.0),
            ],
            vec![content("1", "/en/posts/guide")],
            vec![],
            vec![strategy(query)],
        );
        assert_eq!(result.candidates.len(), 1);
        assert_eq!(result.candidates[0].evidence.impressions, 400);
    }
}
