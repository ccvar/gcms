//! Conservative compatibility check for legacy Anthropic checkpoints.
//!
//! Older histories kept `interrupted=true` after persisting a whole batch of
//! tool results. Only a complete transcript ending in an acknowledged batch is
//! safe to continue: this does not authorize replaying any of those tools.

use serde_json::Value;
use std::collections::HashSet;

fn identifier(value: &Value) -> Option<&str> {
    value.as_str().filter(|value| {
        !value.is_empty()
            && value.len() <= 256
            && !value.chars().any(|c| c.is_whitespace() || c.is_control())
    })
}

fn tool_name(value: &Value) -> bool {
    value.as_str().is_some_and(|value| {
        !value.is_empty()
            && value.len() <= 64
            && value
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'-'))
    })
}

fn text_block(block: &Value) -> bool {
    block.is_object() && block["type"] == "text" && block["text"].is_string()
}

fn user_text(content: &Value) -> bool {
    content.as_str().is_some_and(|text| !text.is_empty())
        || content
            .as_array()
            .is_some_and(|blocks| !blocks.is_empty() && blocks.iter().all(text_block))
}

fn tool_result_content(content: &Value) -> bool {
    // Pilot's native tools persist strings. Text-block arrays are also valid
    // Anthropic results. Unknown multimodal/server-tool blocks fail closed.
    content.is_string()
        || content
            .as_array()
            .is_some_and(|blocks| blocks.iter().all(text_block))
}

pub(super) fn can_resume_legacy(protocol: &str, items: &[Value]) -> bool {
    if protocol != "anthropic_messages" || items.is_empty() || items[0]["role"] != "user" {
        return false;
    }

    let mut seen_calls = HashSet::new();
    let mut expected: HashSet<&str> = HashSet::new();
    let mut has_batch = false;
    let mut last_was_tool_results = false;

    for message in items {
        if !message.is_object() {
            return false;
        }
        let Some(content) = message.get("content") else {
            return false;
        };
        if !expected.is_empty() {
            // A result must immediately follow its assistant batch; no user
            // prose, assistant reply, missing result or other batch may intervene.
            if message["role"] != "user" {
                return false;
            }
            let Some(results) = content.as_array() else {
                return false;
            };
            if results.len() != expected.len() {
                return false;
            }
            for result in results {
                if !result.is_object()
                    || result["type"] != "tool_result"
                    || !result.get("content").is_some_and(tool_result_content)
                    || result
                        .get("is_error")
                        .is_some_and(|value| !value.is_boolean())
                {
                    return false;
                }
                let Some(id) = identifier(&result["tool_use_id"]) else {
                    return false;
                };
                if !expected.remove(id) {
                    return false;
                }
            }
            if !expected.is_empty() {
                return false;
            }
            has_batch = true;
            last_was_tool_results = true;
            continue;
        }

        last_was_tool_results = false;
        match message["role"].as_str() {
            Some("user") => {
                if !user_text(content) {
                    return false;
                }
            }
            Some("assistant") => {
                let Some(blocks) = content.as_array().filter(|blocks| !blocks.is_empty()) else {
                    return false;
                };
                for block in blocks {
                    if !block.is_object() {
                        return false;
                    }
                    match block["type"].as_str() {
                        Some("text") if text_block(block) => {}
                        Some("thinking")
                            if block["thinking"].is_string()
                                && block["signature"]
                                    .as_str()
                                    .is_some_and(|signature| !signature.is_empty()) => {}
                        Some("redacted_thinking")
                            if block["data"].as_str().is_some_and(|data| !data.is_empty()) => {}
                        Some("tool_use") => {
                            let Some(id) = identifier(&block["id"]) else {
                                return false;
                            };
                            if !tool_name(&block["name"])
                                || !block["input"].is_object()
                                || !seen_calls.insert(id)
                                || !expected.insert(id)
                            {
                                return false;
                            }
                        }
                        // In particular, never infer that server_tool_use,
                        // web_search_tool_result, pause-turn or unknown blocks
                        // are equivalent to an acknowledged native tool call.
                        _ => return false,
                    }
                }
            }
            _ => return false,
        }
    }

    has_batch && last_was_tool_results && expected.is_empty()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn call(id: &str) -> Value {
        json!({"type":"tool_use","id":id,"name":"write_file","input":{"path":"index.html","content":"already written","overwrite":true}})
    }
    fn result(id: &str) -> Value {
        json!({"type":"tool_result","tool_use_id":id,"content":"{\"written\":\"index.html\"}"})
    }
    fn history() -> Vec<Value> {
        vec![
            json!({"role":"user","content":"Create a website"}),
            json!({"role":"assistant","content":[
                {"type":"thinking","thinking":"signed thinking","signature":"provider-signature"},
                {"type":"text","text":"Writing the page."},
                call("tool-1")
            ]}),
            json!({"role":"user","content":[result("tool-1")]}),
        ]
    }
    fn resumable(items: &[Value]) -> bool {
        can_resume_legacy("anthropic_messages", items)
    }

    #[test]
    fn legacy_complete_website_checkpoint_is_resumable_without_replaying_tools() {
        assert!(resumable(&history()));
        let mut items = history();
        items.extend([
            json!({"role":"assistant","content":[{"type":"text","text":"Inspecting the result."}]}),
            json!({"role":"user","content":[{"type":"text","text":"Continue checking."}]}),
            json!({"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"},call("tool-2"),call("tool-3")]}),
            json!({"role":"user","content":[result("tool-3"),{"type":"tool_result","tool_use_id":"tool-2","content":[{"type":"text","text":"permission denied"}],"is_error":true}]}),
        ]);
        assert!(
            resumable(&items),
            "error results are acknowledged results, not a request to rerun"
        );
    }

    #[test]
    fn legacy_requires_exact_result_ids_once() {
        for id in ["unknown", "", "bad id"] {
            let mut items = history();
            items[2]["content"][0]["tool_use_id"] = json!(id);
            assert!(!resumable(&items), "{id}");
        }
        for id in [Value::Null, json!(""), json!("bad id")] {
            let mut items = history();
            items[1]["content"][2]["id"] = id;
            assert!(!resumable(&items));
        }
        let mut missing = history();
        missing[2]["content"][0]
            .as_object_mut()
            .unwrap()
            .remove("tool_use_id");
        assert!(!resumable(&missing));
        let mut items = history();
        items[2]["content"] = json!([]);
        assert!(!resumable(&items));
        items[2]["content"] = json!([result("tool-1"), result("extra")]);
        assert!(!resumable(&items));
        items[1]["content"] = json!([call("tool-1"), call("tool-2")]);
        items[2]["content"] = json!([result("tool-1"), result("tool-1")]);
        assert!(!resumable(&items));
        items[1]["content"] = json!([call("tool-1"), call("tool-1")]);
        assert!(!resumable(&items));
    }

    #[test]
    fn legacy_checks_all_earlier_batches_not_just_the_tail() {
        let mut items = history();
        items[2]["content"] = json!([result("missing-earlier-call")]);
        items.extend([
            json!({"role":"assistant","content":[call("tool-2")]}),
            json!({"role":"user","content":[result("tool-2")]}),
        ]);
        assert!(!resumable(&items));
        items[2]["content"] = json!([result("tool-1")]);
        assert!(resumable(&items));
        items.remove(2);
        assert!(
            !resumable(&items),
            "missing earlier result cannot be repaired by a valid later batch"
        );
        let mut reused = history();
        reused.extend([
            json!({"role":"assistant","content":[call("tool-1")]}),
            json!({"role":"user","content":[result("tool-1")]}),
        ]);
        assert!(
            !resumable(&reused),
            "call IDs must be unique across the transcript"
        );
    }

    #[test]
    fn legacy_requires_tool_result_tail_and_supported_protocol() {
        for protocol in ["openai_responses", "openai_chat", "", "anthropic"] {
            assert!(!can_resume_legacy(protocol, &history()));
        }
        assert!(!resumable(&[]));
        assert!(!resumable(&[json!({"role":"user","content":"hello"})]));
        let mut items = history();
        items.push(json!({"role":"user","content":"Continue"}));
        assert!(!resumable(&items));
        items.pop();
        items.push(json!({"role":"assistant","content":[{"type":"text","text":"done"}]}));
        assert!(!resumable(&items));
        items.pop();
        items.pop();
        assert!(!resumable(&items));
    }

    #[test]
    fn legacy_rejects_malformed_tools_results_and_roles() {
        for input in [Value::Null, json!("{}"), json!([]), json!(true)] {
            let mut items = history();
            items[1]["content"][2]["input"] = input;
            assert!(!resumable(&items));
        }
        for name in ["", "bad name", "write_file\n"] {
            let mut items = history();
            items[1]["content"][2]["name"] = json!(name);
            assert!(!resumable(&items));
        }
        for content in [
            Value::Null,
            json!({}),
            json!(false),
            json!(["not a text block"]),
        ] {
            let mut items = history();
            items[2]["content"][0]["content"] = content;
            assert!(!resumable(&items));
        }
        let mut items = history();
        items[2]["content"][0]
            .as_object_mut()
            .unwrap()
            .remove("content");
        assert!(!resumable(&items));
        let mut items = history();
        items[2]["content"][0]["is_error"] = json!("false");
        assert!(!resumable(&items));
        for role in ["system", "tool", "", "Assistant"] {
            let mut items = history();
            items[1]["role"] = json!(role);
            assert!(!resumable(&items));
        }
        let mut items = history();
        items[0] = json!({"role":"user"});
        assert!(!resumable(&items));
        let mut items = history();
        items[1]["content"][0]
            .as_object_mut()
            .unwrap()
            .remove("signature");
        assert!(!resumable(&items));
    }

    #[test]
    fn legacy_rejects_server_tools_unknown_blocks_and_mixed_result_prose() {
        for kind in [
            "server_tool_use",
            "web_search_tool_result",
            "tool_result",
            "unknown",
        ] {
            let mut items = history();
            items[1]["content"][2]["type"] = json!(kind);
            assert!(!resumable(&items));
        }
        let mut items = history();
        items[2]["content"]
            .as_array_mut()
            .unwrap()
            .push(json!({"type":"text","text":"continue"}));
        assert!(!resumable(&items));
        let mut items = history();
        items[0]["content"] = json!([result("orphan")]);
        assert!(!resumable(&items));
    }
}
