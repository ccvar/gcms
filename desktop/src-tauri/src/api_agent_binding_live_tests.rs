//! Explicit, bounded live regression for Claude thinking-prefix bindings.
//! This ignored test uses only a selected, already-configured official Claude
//! profile and a synthetic maths conversation in a temporary directory. It
//! never reads real conversation history, runs tools, or logs credentials,
//! request/response text, thinking content, or signatures. Four requests max.
//! Opt in with PILOT_LIVE_DATA_DIR and PILOT_LIVE_PROFILE_ID; the caller must
//! authorize the small paid test. The strict beta is test-only, not production.
use super::*;

const MODEL: &str = "claude-opus-5-5";
const ORIGINAL_SYSTEM: &str = "You are a careful mathematics assistant. Solve the user's number theory questions accurately. Keep final answers concise, with at most three short sentences. Do not call tools.";
const UPDATED_SYSTEM: &str = "You are a careful mathematics assistant. Solve the user's number theory questions accurately. Keep final answers concise, with at most three short sentences. Do not call tools. This is an updated application instruction prefix.";

fn signed_thinking_count(items: &[Value]) -> usize {
    items
        .iter()
        .filter_map(|item| item["content"].as_array())
        .flatten()
        .filter(|block| {
            block["type"] == "thinking"
                && block["signature"]
                    .as_str()
                    .is_some_and(|signature| !signature.is_empty())
        })
        .count()
}

fn without_thinking(items: &[Value]) -> Vec<Value> {
    let mut items = items.to_vec();
    items.retain_mut(|item| {
        if item["role"] != "assistant" {
            return true;
        }
        let Some(blocks) = item["content"].as_array_mut() else {
            return true;
        };
        let before = blocks.len();
        blocks.retain(|block| {
            !matches!(
                block["type"].as_str(),
                Some("thinking" | "redacted_thinking")
            )
        });
        before == blocks.len() || !blocks.is_empty()
    });
    items
}

fn strict_request(profile: &ApiProfile, effort: &str, system: &str, items: &[Value]) -> Value {
    let mut body = match request_body(profile, MODEL, effort, system, items, &[]) {
        Ok(body) => body,
        Err(_) => panic!("live fixture request construction failed; details suppressed"),
    };
    // Force a real signed block even for the UI's default effort setting and
    // make the negative control fail instead of silently dropping old blocks.
    body["thinking"] = json!({
        "type": "adaptive",
        "block_binding": { "prefix_mismatch_behavior": "error" }
    });
    body
}

async fn bounded_request(
    client: &reqwest::Client,
    profile: &ApiProfile,
    key: &str,
    body: Value,
) -> Result<Reply, String> {
    match tokio::time::timeout(
        Duration::from_secs(120),
        stream_request(client, profile, key, body, |_| {}),
    )
    .await
    {
        Ok(result) => result,
        Err(_) => Err("live fixture timeout".into()),
    }
}

fn accepted_reply(reply: Result<Reply, String>, phase: &str) -> Reply {
    let reply = match reply {
        Ok(reply) => reply,
        Err(_) => panic!("live fixture phase {phase} failed; provider details suppressed"),
    };
    assert!(
        !reply.output_limited,
        "live fixture phase {phase} hit the fixed output cap; inconclusive"
    );
    assert!(
        reply.calls().is_ok_and(|calls| calls.is_empty()),
        "live fixture returned an unexpected tool call; no tools were executed"
    );
    eprintln!(
        "Claude binding live: model={MODEL} phase={phase} status=ok input_tokens={} output_tokens={} signed_blocks={}",
        reply.usage.input,
        reply.usage.output,
        signed_thinking_count(&reply.items)
    );
    reply
}

fn persist(path: &Path, history: &History) {
    assert!(
        save_history(path, history).is_ok(),
        "cannot persist synthetic live fixture; details suppressed"
    );
}

#[tokio::test]
#[ignore = "paid official Claude integration; requires explicit opt-in profile env vars"]
async fn live_claude_thinking_prefix_resume_and_migration() {
    let data_dir = match std::env::var_os("PILOT_LIVE_DATA_DIR") {
        Some(path) if !path.is_empty() => PathBuf::from(path),
        _ => panic!("PILOT_LIVE_DATA_DIR is required for this opt-in test"),
    };
    let profile_id = match std::env::var("PILOT_LIVE_PROFILE_ID") {
        Ok(id) if !id.trim().is_empty() => id,
        _ => panic!("PILOT_LIVE_PROFILE_ID is required for this opt-in test"),
    };
    let loaded = match api_profiles::load_enabled(&data_dir, &profile_id) {
        Ok(loaded) => loaded,
        Err(_) => panic!("cannot load opted-in enabled API profile; details suppressed"),
    };
    assert!(
        loaded.profile.protocol == "anthropic_messages"
            && loaded.profile.base_url == "https://api.anthropic.com/v1"
            && loaded.profile.models.iter().any(|model| model == MODEL),
        "live fixture requires the official Claude endpoint and configured opus 5.5 model"
    );
    let mut profile = loaded.profile.clone();
    profile.max_output_tokens = 4096;
    let mut headers = reqwest::header::HeaderMap::new();
    headers.insert(
        "anthropic-beta",
        reqwest::header::HeaderValue::from_static("thinking-binding-controls-2026-08-01"),
    );
    let client = match reqwest::Client::builder()
        .default_headers(headers)
        .redirect(reqwest::redirect::Policy::none())
        .connect_timeout(Duration::from_secs(15))
        .timeout(Duration::from_secs(120))
        .build()
    {
        Ok(client) => client,
        Err(_) => panic!("cannot construct strict live fixture client"),
    };
    let temporary = match tempfile::tempdir() {
        Ok(directory) => directory,
        Err(_) => panic!("cannot create synthetic fixture directory"),
    };
    let path = temporary.path().join("synthetic-history.json");
    let mut history = History {
        scope: "synthetic-live-binding-fixture".into(),
        protocol: "anthropic_messages".into(),
        system: ORIGINAL_SYSTEM.into(),
        thinking_prefix: String::new(),
        items: vec![json!({
            "role": "user",
            "content": "How many positive integers less than 500 have exactly six positive divisors? Give the count and a short derivation."
        })],
        interrupted: false,
    };
    assert!(history.prepare_thinking_prefix(ORIGINAL_SYSTEM, &[]) == 0);
    persist(&path, &history);

    // 1. Default effort, with test-only adaptive thinking for observable proof.
    let first = accepted_reply(
        bounded_request(
            &client,
            &profile,
            &loaded.api_key,
            strict_request(&profile, "", ORIGINAL_SYSTEM, &history.items),
        )
        .await,
        "signed-first-turn",
    );
    assert!(
        signed_thinking_count(&first.items) > 0,
        "Claude returned no signed thinking block; binding verification is inconclusive"
    );
    history.items.extend(first.items);
    persist(&path, &history);

    // Reload production-shaped history from disk, keeping every block exact.
    let raw = match std::fs::read(&path) {
        Ok(raw) => raw,
        Err(_) => panic!("cannot reload synthetic history"),
    };
    let mut history: History = match serde_json::from_slice(&raw) {
        Ok(history) => history,
        Err(_) => panic!("cannot parse synthetic history"),
    };
    let original_items = history.items.clone();
    assert!(history.prepare_thinking_prefix(ORIGINAL_SYSTEM, &[]) == 0);
    assert!(
        history.items == original_items,
        "same-prefix history changed"
    );
    history.items.push(json!({
        "role": "user",
        "content": "Among those integers, how many are odd? Give the count and a short check."
    }));
    persist(&path, &history);

    // 2. Same prefix after restart and an effort switch must retain signatures.
    let resumed = accepted_reply(
        bounded_request(
            &client,
            &profile,
            &loaded.api_key,
            strict_request(&profile, "api:high", ORIGINAL_SYSTEM, &history.items),
        )
        .await,
        "unchanged-prefix-resume-high",
    );
    history.items.extend(resumed.items);
    history.items.push(json!({
        "role": "user",
        "content": "State the final total count and odd-only count again in one sentence."
    }));
    persist(&path, &history);

    // 3. Negative control: prove the provider rejects the exact former bug.
    let negative = bounded_request(
        &client,
        &profile,
        &loaded.api_key,
        strict_request(&profile, "api:high", UPDATED_SYSTEM, &history.items),
    )
    .await;
    let error = match negative {
        Err(error) => error,
        Ok(_) => panic!("changed-prefix negative control unexpectedly succeeded; inconclusive"),
    };
    let lower = error.to_ascii_lowercase();
    assert!(
        error.contains("API HTTP 400")
            && lower.contains("thinking")
            && lower.contains("signature")
            && (lower.contains("different conversation")
                || lower.contains("system")
                || lower.contains("prefix")),
        "negative control did not return the expected signature-binding 400; details suppressed"
    );
    eprintln!(
        "Claude binding live: model={MODEL} phase=changed-prefix-negative status=expected_http_400"
    );

    // 4. Production migration keeps non-thinking context, then succeeds.
    let visible_context = without_thinking(&history.items);
    let removed = history.prepare_thinking_prefix(UPDATED_SYSTEM, &[]);
    assert!(removed > 0, "changed prefix did not migrate stale thinking");
    assert!(
        history.items == visible_context,
        "migration changed non-thinking context"
    );
    assert!(signed_thinking_count(&history.items) == 0);
    persist(&path, &history);
    let migrated = accepted_reply(
        bounded_request(
            &client,
            &profile,
            &loaded.api_key,
            strict_request(&profile, "api:high", UPDATED_SYSTEM, &history.items),
        )
        .await,
        "changed-prefix-migrated",
    );
    history.items.extend(migrated.items);
    persist(&path, &history);
    eprintln!("Claude binding live: model={MODEL} status=passed request_count=4 migrated_blocks={removed}");
}
