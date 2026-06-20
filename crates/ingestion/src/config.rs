use serde::Deserialize;
use std::time::Duration;
use tracing::warn;

use crate::node_pool::{NodeConfig, NodeType};

/// Top-level structure for the `nodes.yaml` configuration file.
#[derive(Debug, Deserialize)]
pub struct NodesFileConfig {
    pub nodes: Vec<NodeEntry>,
    #[serde(default = "default_min_healthy")]
    pub min_healthy_nodes: usize,
}

fn default_min_healthy() -> usize {
    1
}

/// A single node entry as represented in the YAML config.
#[derive(Debug, Deserialize)]
pub struct NodeEntry {
    pub name: String,
    pub url: String,
    #[serde(rename = "type")]
    pub node_type: String,
    #[serde(default = "default_priority")]
    pub priority: u32,
}

fn default_priority() -> u32 {
    10
}

/// Expand `${VAR}` patterns in a string using environment variables.
/// Unknown vars are left as-is with a warning.
pub fn expand_env_vars(input: &str) -> String {
    expand_env_vars_with(input, |key| std::env::var(key).ok())
}

/// Expand `${VAR}` patterns using a custom resolver function.
///
/// This is the core implementation. The resolver is called for each
/// `${VAR}` occurrence; if it returns `None`, the placeholder is
/// preserved unchanged.
fn expand_env_vars_with(input: &str, resolver: impl Fn(&str) -> Option<String>) -> String {
    let mut result = String::with_capacity(input.len());
    let mut chars = input.chars().peekable();
    while let Some(c) = chars.next() {
        if c == '$' && chars.peek() == Some(&'{') {
            chars.next(); // consume '{'
            let var_name: String = chars.by_ref().take_while(|&ch| ch != '}').collect();
            match resolver(&var_name) {
                Some(val) => result.push_str(&val),
                None => {
                    warn!(var = %var_name, "Environment variable not set, leaving placeholder");
                    result.push_str(&format!("${{{}}}", var_name));
                }
            }
        } else {
            result.push(c);
        }
    }
    result
}

fn parse_node_type(s: &str) -> NodeType {
    match s.to_lowercase().as_str() {
        "websocket" | "ws" | "wss" => NodeType::WebSocket,
        "ipc" => NodeType::Ipc,
        "http" | "https" => NodeType::Http,
        other => {
            warn!(node_type = %other, "Unknown node type, defaulting to Http");
            NodeType::Http
        }
    }
}

/// Load nodes configuration from a YAML file.
/// Returns `(Vec<NodeConfig>, min_healthy_nodes)`.
pub fn load_nodes_config(
    path: &str,
) -> Result<(Vec<NodeConfig>, usize), Box<dyn std::error::Error + Send + Sync>> {
    let contents = std::fs::read_to_string(path)?;
    let expanded = expand_env_vars(&contents);
    let file_config: NodesFileConfig = serde_yml::from_str(&expanded)?;

    let configs: Vec<NodeConfig> = file_config
        .nodes
        .into_iter()
        .map(|entry| NodeConfig {
            name: entry.name,
            url: entry.url,
            node_type: parse_node_type(&entry.node_type),
            priority: entry.priority,
            max_retries: 5,
            health_check_interval: Duration::from_secs(30),
        })
        .collect();

    Ok((configs, file_config.min_healthy_nodes))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;

    fn test_resolver(key: &str) -> Option<String> {
        match key {
            "AETHER_TEST_KEY_XYZ" => Some("hello".to_string()),
            "AETHER_A" => Some("1".to_string()),
            "AETHER_B" => Some("2".to_string()),
            "AETHER_URL" => Some("wss://alchemy.example.com/v2/key".to_string()),
            _ => None,
        }
    }

    #[test]
    fn test_expand_env_vars_with_known_var() {
        let result = expand_env_vars_with("prefix/${AETHER_TEST_KEY_XYZ}/suffix", test_resolver);
        assert_eq!(result, "prefix/hello/suffix");
    }

    #[test]
    fn test_expand_env_vars_unknown_var() {
        let result = expand_env_vars_with("${AETHER_NONEXISTENT_VAR_123}", test_resolver);
        assert_eq!(result, "${AETHER_NONEXISTENT_VAR_123}");
    }

    #[test]
    fn test_expand_env_vars_no_vars() {
        let result = expand_env_vars_with("no_vars_here", test_resolver);
        assert_eq!(result, "no_vars_here");
    }

    #[test]
    fn test_expand_env_vars_multiple() {
        let result = expand_env_vars_with("${AETHER_A}-${AETHER_B}", test_resolver);
        assert_eq!(result, "1-2");
    }

    #[test]
    fn test_expand_env_vars_dollar_not_followed_by_brace() {
        let result = expand_env_vars_with("price is $5.00", test_resolver);
        assert_eq!(result, "price is $5.00");
    }

    #[test]
    fn test_expand_env_vars_dollar_at_end_of_string() {
        let result = expand_env_vars_with("price$", test_resolver);
        assert_eq!(result, "price$");
    }

    #[test]
    fn test_expand_env_vars_empty_var_name() {
        let result = expand_env_vars_with("${}", test_resolver);
        assert_eq!(result, "${}");
    }

    #[test]
    fn test_expand_env_vars_unclosed_brace() {
        let result = expand_env_vars_with("${UNCLOSED", test_resolver);
        assert_eq!(result, "${UNCLOSED}");
    }

    #[test]
    fn test_expand_env_vars_adjacent_vars() {
        let result = expand_env_vars_with("${AETHER_A}${AETHER_B}", test_resolver);
        assert_eq!(result, "12");
    }

    #[test]
    fn test_expand_env_vars_empty_string() {
        let result = expand_env_vars_with("", test_resolver);
        assert_eq!(result, "");
    }

    #[test]
    fn test_expand_env_vars_only_dollar_sign() {
        let result = expand_env_vars_with("$", test_resolver);
        assert_eq!(result, "$");
    }

    #[test]
    fn test_expand_env_vars_mixed_known_and_unknown() {
        let result =
            expand_env_vars_with("${AETHER_A}-${UNKNOWN}-${AETHER_B}", test_resolver);
        assert_eq!(result, "1-${UNKNOWN}-2");
    }

    #[serial]
    #[test]
    fn test_expand_env_vars_real_env() {
        std::env::set_var("AETHER_TEST_EXPAND_MARKER", "expanded_value");
        let result = expand_env_vars("prefix/${AETHER_TEST_EXPAND_MARKER}/suffix");
        assert_eq!(result, "prefix/expanded_value/suffix");
        std::env::remove_var("AETHER_TEST_EXPAND_MARKER");
    }

    #[serial]
    #[test]
    fn test_expand_env_vars_real_env_unset() {
        std::env::remove_var("AETHER_TEST_UNSET_MARKER_98765");
        let result = expand_env_vars("${AETHER_TEST_UNSET_MARKER_98765}");
        assert_eq!(result, "${AETHER_TEST_UNSET_MARKER_98765}");
    }

    #[test]
    fn test_expand_env_vars_url_with_env() {
        let result = expand_env_vars_with("wss://example.com/${AETHER_URL}", test_resolver);
        assert_eq!(result, "wss://example.com/wss://alchemy.example.com/v2/key");
    }

    #[test]
    fn test_parse_node_type_variants() {
        assert_eq!(parse_node_type("websocket"), NodeType::WebSocket);
        assert_eq!(parse_node_type("ws"), NodeType::WebSocket);
        assert_eq!(parse_node_type("wss"), NodeType::WebSocket);
        assert_eq!(parse_node_type("ipc"), NodeType::Ipc);
        assert_eq!(parse_node_type("http"), NodeType::Http);
        assert_eq!(parse_node_type("https"), NodeType::Http);
        assert_eq!(parse_node_type("WEBSOCKET"), NodeType::WebSocket);
        assert_eq!(parse_node_type("unknown"), NodeType::Http);
    }

    #[test]
    fn test_parse_node_type_empty_string() {
        assert_eq!(parse_node_type(""), NodeType::Http);
    }

    #[test]
    fn test_parse_node_type_mixed_case() {
        assert_eq!(parse_node_type("WebSocket"), NodeType::WebSocket);
        assert_eq!(parse_node_type("WS"), NodeType::WebSocket);
        assert_eq!(parse_node_type("IPC"), NodeType::Ipc);
        assert_eq!(parse_node_type("HTTP"), NodeType::Http);
    }

    #[test]
    fn test_load_nodes_config_from_yaml() {
        use std::io::Write;
        let dir = std::env::temp_dir().join("aether_test_config");
        std::fs::create_dir_all(&dir).expect("failed to create temp dir");
        let path = dir.join("test_nodes.yaml");

        let yaml = r#"
nodes:
  - name: "test-ws"
    url: "wss://example.com"
    type: "websocket"
    priority: 1
  - name: "test-ipc"
    url: "/tmp/test.ipc"
    type: "ipc"
    priority: 0
  - name: "test-http"
    url: "http://localhost:8545"
    type: "http"
    priority: 2
min_healthy_nodes: 2
"#;
        let mut f = std::fs::File::create(&path).expect("failed to create temp file");
        f.write_all(yaml.as_bytes())
            .expect("failed to write temp file");

        let (configs, min_healthy) =
            load_nodes_config(path.to_str().expect("invalid path")).expect("failed to load config");
        assert_eq!(configs.len(), 3);
        assert_eq!(min_healthy, 2);
        assert_eq!(configs[0].name, "test-ws");
        assert_eq!(configs[0].node_type, NodeType::WebSocket);
        assert_eq!(configs[1].name, "test-ipc");
        assert_eq!(configs[1].node_type, NodeType::Ipc);
        assert_eq!(configs[2].name, "test-http");
        assert_eq!(configs[2].node_type, NodeType::Http);

        std::fs::remove_file(&path).ok();
        std::fs::remove_dir(&dir).ok();
    }

    #[test]
    fn test_load_nodes_config_default_min_healthy() {
        use std::io::Write;
        let dir = std::env::temp_dir().join("aether_test_config_default_min");
        std::fs::create_dir_all(&dir).expect("temp dir");
        let path = dir.join("test_nodes_default.yaml");

        let yaml = r#"
nodes:
  - name: "test-ws"
    url: "wss://example.com"
    type: "websocket"
"#;
        let mut f = std::fs::File::create(&path).expect("create");
        f.write_all(yaml.as_bytes()).expect("write");

        let (_configs, min_healthy) =
            load_nodes_config(path.to_str().expect("path")).expect("load");
        assert_eq!(min_healthy, 1, "default min_healthy_nodes should be 1");

        std::fs::remove_file(&path).ok();
        std::fs::remove_dir(&dir).ok();
    }

    #[test]
    fn test_load_nodes_config_default_priority() {
        use std::io::Write;
        let dir = std::env::temp_dir().join("aether_test_config_default_priority");
        std::fs::create_dir_all(&dir).expect("temp dir");
        let path = dir.join("test_nodes_prio.yaml");

        let yaml = r#"
nodes:
  - name: "test-ws"
    url: "wss://example.com"
    type: "websocket"
"#;
        let mut f = std::fs::File::create(&path).expect("create");
        f.write_all(yaml.as_bytes()).expect("write");

        let (configs, _) =
            load_nodes_config(path.to_str().expect("path")).expect("load");
        assert_eq!(configs[0].priority, 10, "default priority should be 10");

        std::fs::remove_file(&path).ok();
        std::fs::remove_dir(&dir).ok();
    }

    #[serial]
    #[test]
    fn test_load_nodes_config_with_env_var_expansion() {
        use std::io::Write;
        std::env::set_var("AETHER_TEST_NODE_URL", "wss://env-expanded.example.com");

        let dir = std::env::temp_dir().join("aether_test_config_env");
        std::fs::create_dir_all(&dir).expect("temp dir");
        let path = dir.join("test_nodes_env.yaml");

        let yaml = r#"
nodes:
  - name: "env-ws"
    url: "${AETHER_TEST_NODE_URL}"
    type: "ws"
"#;
        let mut f = std::fs::File::create(&path).expect("create");
        f.write_all(yaml.as_bytes()).expect("write");

        let (configs, _) =
            load_nodes_config(path.to_str().expect("path")).expect("load");
        assert_eq!(configs[0].url, "wss://env-expanded.example.com");

        std::env::remove_var("AETHER_TEST_NODE_URL");
        std::fs::remove_file(&path).ok();
        std::fs::remove_dir(&dir).ok();
    }

    #[test]
    fn test_load_nodes_config_invalid_yaml() {
        use std::io::Write;
        let dir = std::env::temp_dir().join("aether_test_config_invalid");
        std::fs::create_dir_all(&dir).expect("temp dir");
        let path = dir.join("bad_nodes.yaml");
        let mut f = std::fs::File::create(&path).expect("create");
        f.write_all(b"nodes: [\n  - name: broken\n    url: \"unclosed")
            .expect("write");
        let err = load_nodes_config(path.to_str().expect("path")).unwrap_err();
        let msg = err.to_string().to_lowercase();
        assert!(
            msg.contains("yaml")
                || msg.contains("parse")
                || msg.contains("node content")
                || msg.contains("expected"),
            "expected YAML parse error, got: {err}"
        );
        std::fs::remove_file(&path).ok();
        std::fs::remove_dir(&dir).ok();
    }

    #[test]
    fn test_load_nodes_config_missing_file() {
        let err = load_nodes_config("/nonexistent/aether_nodes_test.yaml").unwrap_err();
        assert!(
            err.to_string().contains("No such file") || err.to_string().contains("os error"),
            "expected file-not-found error, got: {err}"
        );
    }

    #[test]
    fn test_load_nodes_config_empty_nodes_list() {
        use std::io::Write;
        let dir = std::env::temp_dir().join("aether_test_config_empty");
        std::fs::create_dir_all(&dir).expect("temp dir");
        let path = dir.join("empty_nodes.yaml");

        let yaml = "nodes: []\n";
        let mut f = std::fs::File::create(&path).expect("create");
        f.write_all(yaml.as_bytes()).expect("write");

        let (configs, _) =
            load_nodes_config(path.to_str().expect("path")).expect("load");
        assert!(configs.is_empty());

        std::fs::remove_file(&path).ok();
        std::fs::remove_dir(&dir).ok();
    }

    #[test]
    fn test_load_nodes_config_preserves_max_retries_and_health_check() {
        use std::io::Write;
        let dir = std::env::temp_dir().join("aether_test_config_defaults");
        std::fs::create_dir_all(&dir).expect("temp dir");
        let path = dir.join("test_defaults.yaml");

        let yaml = r#"
nodes:
  - name: "test-ws"
    url: "wss://example.com"
    type: "ws"
"#;
        let mut f = std::fs::File::create(&path).expect("create");
        f.write_all(yaml.as_bytes()).expect("write");

        let (configs, _) =
            load_nodes_config(path.to_str().expect("path")).expect("load");
        assert_eq!(configs[0].max_retries, 5);
        assert_eq!(
            configs[0].health_check_interval,
            Duration::from_secs(30)
        );

        std::fs::remove_file(&path).ok();
        std::fs::remove_dir(&dir).ok();
    }

    #[test]
    fn test_nodes_file_config_defaults() {
        let yaml = r#"
nodes:
  - name: "n"
    url: "wss://x"
    type: "ws"
"#;
        let config: NodesFileConfig = serde_yml::from_str(yaml).unwrap();
        assert_eq!(config.min_healthy_nodes, 1);
        assert_eq!(config.nodes[0].priority, 10);
    }

    #[test]
    fn test_default_min_healthy_value() {
        assert_eq!(default_min_healthy(), 1);
    }

    #[test]
    fn test_default_priority_value() {
        assert_eq!(default_priority(), 10);
    }
}
