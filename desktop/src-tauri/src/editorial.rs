// One source for Pilot and the server-generated content skills.
pub const PUBLIC_COPY_POLICY: &str = include_str!("../../../internal/web/public_copy_policy.md");

/// Apply before provider dispatch on every turn, including resumed sessions.
/// Do not impose website-writing requirements on unrelated SSH/workspace work.
pub fn for_turn(message: String, connection_kind: &str) -> String {
    if matches!(connection_kind, "gcms" | "cloudflare") {
        format!("{message}\n\n<pilot-public-copy-policy>\n{PUBLIC_COPY_POLICY}</pilot-public-copy-policy>")
    } else {
        message
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn public_copy_policy_is_scoped_and_keeps_user_message_intact() {
        for kind in ["gcms", "cloudflare"] {
            // No session state: fresh and resumed turns receive the same policy.
            for message in ["创建首页", "继续修改 FAQ"] {
                let composed = for_turn(message.into(), kind);
                assert!(composed.starts_with(message));
                assert!(composed.contains(PUBLIC_COPY_POLICY));
            }
        }
        for kind in ["ssh", "workspace", "unknown"] {
            assert_eq!(for_turn("检查日志".into(), kind), "检查日志");
        }
    }
}
