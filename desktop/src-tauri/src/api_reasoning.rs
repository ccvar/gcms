//! API reasoning capabilities shared with the model picker. Unknown model IDs
//! keep provider defaults; CLI effort names must never leak into API requests.
//! API selections are stored as `api:<level>`: unprefixed historical effort
//! values were ignored by the original API runner and must stay at defaults.
use serde::Deserialize;
use serde_json::{json, Value};
use std::sync::OnceLock;

#[derive(Debug, Deserialize)]
struct Capability {
    protocol: String,
    models: Vec<String>,
    levels: Vec<String>,
    thinking: Option<String>,
}

fn capabilities() -> &'static [Capability] {
    static CATALOG: OnceLock<Vec<Capability>> = OnceLock::new();
    CATALOG.get_or_init(|| {
        serde_json::from_str(include_str!("../../src/lib/apiReasoningCapabilities.json"))
            .expect("bundled API reasoning capability catalog must be valid")
    })
}

fn model_matches(model: &str, base: &str) -> bool {
    if model == base {
        return true;
    }
    let Some(snapshot) = model.strip_prefix(base).and_then(|v| v.strip_prefix('-')) else {
        return false;
    };
    // Only vendor date snapshots may inherit a documented base model's schema.
    // Names such as `-latest`, `-custom`, or another model generation must not.
    match snapshot.len() {
        8 => snapshot.bytes().all(|b| b.is_ascii_digit()),
        10 => snapshot.bytes().enumerate().all(|(i, b)| {
            if i == 4 || i == 7 {
                b == b'-'
            } else {
                b.is_ascii_digit()
            }
        }),
        _ => false,
    }
}

fn capability(protocol: &str, model: &str) -> Option<&'static Capability> {
    capabilities().iter().find(|entry| {
        entry.protocol == protocol && entry.models.iter().any(|base| model_matches(model, base))
    })
}

pub(crate) fn uses_chat_completion_tokens(model: &str) -> bool {
    capability("openai_chat", model).is_some()
}

pub(crate) fn after_model_switch(
    old_brain: &str,
    new_brain: &str,
    old_protocol: Option<&str>,
    new_protocol: Option<&str>,
    new_model: &str,
    effort: &str,
) -> String {
    match (old_brain.starts_with("api:"), new_brain.starts_with("api:")) {
        (false, false) => effort.into(),
        (true, true) if old_protocol.is_some() && old_protocol == new_protocol => {
            if effort.starts_with("api:")
                && validate(new_protocol.unwrap(), new_model, effort).is_ok()
            {
                effort.into()
            } else {
                String::new()
            }
        }
        _ => String::new(),
    }
}

pub(crate) fn validate(protocol: &str, model: &str, effort: &str) -> Result<(), String> {
    let Some(effort) = effort.strip_prefix("api:") else {
        return Ok(());
    };
    let Some(entry) = capability(protocol, model) else {
        return Err("此 API 模型尚未声明可用的思考等级，请将思考等级改为“默认”后重试".into());
    };
    if !entry.levels.iter().any(|level| level == effort) {
        return Err("此 API 模型不支持所选思考等级，请重新选择支持的档位或“默认”".into());
    }
    Ok(())
}

/// Called for every model request, including requests after tool results.
pub(crate) fn apply(
    protocol: &str,
    model: &str,
    effort: &str,
    body: &mut Value,
) -> Result<(), String> {
    validate(protocol, model, effort)?;
    let Some(effort) = effort.strip_prefix("api:") else {
        return Ok(());
    };
    match protocol {
        "openai_responses" => body["reasoning"] = json!({ "effort": effort }),
        "anthropic_messages" => {
            body["output_config"] = json!({ "effort": effort });
            if let Some(thinking) = capability(protocol, model).and_then(|c| c.thinking.as_deref())
            {
                body["thinking"] = json!({ "type": thinking });
            }
        }
        "openai_chat" => body["reasoning_effort"] = json!(effort),
        _ => return Err("此 API 协议不支持思考等级设置".into()),
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn only_exact_ids_and_date_snapshots_match() {
        for model in [
            "claude-opus-5-5",
            "claude-opus-5-5-2026-09-15",
            "claude-opus-5-5-20260915",
        ] {
            assert!(model_matches(model, "claude-opus-5-5"));
        }
        for model in [
            "claude-opus-5-50",
            "claude-opus-5-5-latest",
            "claude-opus-5-5-custom",
            "claude-opus-5-5-2026-9-15",
            "claude-opus-5-5-20260915-extra",
            "vendor/claude-opus-5-5",
            "CLAUDE-OPUS-5-5",
        ] {
            assert!(!model_matches(model, "claude-opus-5-5"), "{model}");
        }
    }

    #[test]
    fn default_does_not_add_parameters_even_for_unknown_models() {
        for protocol in ["anthropic_messages", "openai_responses", "openai_chat"] {
            for effort in ["", "high", "max", "ultra"] {
                for model in ["unknown-model", "claude-opus-5-5", "gpt-6-sol"] {
                    let mut body = json!({ "max_tokens": 4096 });
                    let original = body.clone();
                    apply(protocol, model, effort, &mut body).unwrap();
                    assert_eq!(body, original);
                }
            }
        }
    }

    #[test]
    fn unknown_model_or_cli_only_level_is_rejected() {
        assert!(validate("anthropic_messages", "unknown-model", "api:high").is_err());
        assert!(validate("anthropic_messages", "claude-opus-5-5", "api:ultra").is_err());
        assert!(validate("anthropic_messages", "claude-opus-5-5", "api:DEFAULT").is_err());
        assert!(validate("anthropic_messages", "claude-opus-5-5", "api: high ").is_err());
        assert!(validate("anthropic_messages", "claude-opus-5-5", "api:").is_err());
    }

    #[test]
    fn switching_models_keeps_only_compatible_api_efforts() {
        let claude = Some("anthropic_messages");
        let responses = Some("openai_responses");
        assert_eq!(
            after_model_switch("claude", "codex", None, None, "gpt-6-sol", "high"),
            "high"
        );
        assert_eq!(
            after_model_switch("claude", "api:a", None, claude, "claude-opus-5-5", "high"),
            ""
        );
        assert_eq!(
            after_model_switch("api:a", "claude", claude, None, "opus", "api:xhigh"),
            ""
        );
        assert_eq!(
            after_model_switch(
                "api:a",
                "api:b",
                claude,
                claude,
                "claude-opus-5-5",
                "api:xhigh"
            ),
            "api:xhigh"
        );
        assert_eq!(
            after_model_switch(
                "api:a",
                "api:b",
                claude,
                claude,
                "claude-opus-4-6",
                "api:xhigh"
            ),
            ""
        );
        assert_eq!(
            after_model_switch("api:a", "api:b", claude, responses, "gpt-6-sol", "api:high"),
            ""
        );
        assert_eq!(
            after_model_switch("api:a", "api:b", None, responses, "gpt-6-sol", "api:high"),
            ""
        );
        assert_eq!(
            after_model_switch("api:a", "api:b", claude, claude, "unknown", "api:high"),
            ""
        );
        assert_eq!(
            after_model_switch("api:a", "api:b", claude, claude, "claude-opus-5-5", "high"),
            ""
        );
    }

    #[test]
    fn catalog_has_unique_models_and_only_known_api_effort_levels() {
        let mut models = HashSet::new();
        for entry in capabilities() {
            assert!(matches!(
                entry.protocol.as_str(),
                "anthropic_messages" | "openai_responses" | "openai_chat"
            ));
            assert!(!entry.models.is_empty());
            assert!(!entry.levels.is_empty());
            assert!(entry
                .thinking
                .as_deref()
                .is_none_or(|mode| mode == "adaptive"));
            assert!(entry.thinking.is_none() || entry.protocol == "anthropic_messages");
            for model in &entry.models {
                assert!(models.insert((&entry.protocol, model)), "duplicate {model}");
            }
            let mut levels = HashSet::new();
            for level in &entry.levels {
                assert!(matches!(
                    level.as_str(),
                    "none" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max"
                ));
                assert!(levels.insert(level));
            }
        }
    }

    #[test]
    fn every_supported_selection_uses_only_its_protocol_parameters() {
        for entry in capabilities() {
            for model in &entry.models {
                for level in &entry.levels {
                    let mut body = json!({"model": model, "max_output_tokens": 8192});
                    apply(&entry.protocol, model, &format!("api:{level}"), &mut body).unwrap();
                    assert_eq!(body["max_output_tokens"], 8192);
                    match entry.protocol.as_str() {
                        "openai_responses" => {
                            assert_eq!(body["reasoning"]["effort"], *level);
                            assert!(body.get("reasoning_effort").is_none());
                            assert!(body.get("output_config").is_none());
                            assert!(body.get("thinking").is_none());
                        }
                        "openai_chat" => {
                            assert_eq!(body["reasoning_effort"], *level);
                            assert!(body.get("reasoning").is_none());
                            assert!(body.get("output_config").is_none());
                            assert!(body.get("thinking").is_none());
                        }
                        "anthropic_messages" => {
                            assert_eq!(body["output_config"]["effort"], *level);
                            assert_eq!(
                                body["thinking"]["type"].as_str(),
                                entry.thinking.as_deref()
                            );
                            assert!(body.get("reasoning").is_none());
                            assert!(body.get("reasoning_effort").is_none());
                        }
                        _ => unreachable!(),
                    }
                }
            }
        }
    }
}
