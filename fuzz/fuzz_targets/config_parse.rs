#![no_main]

use aether_discovery::config::DiscoveryConfig;
use aether_ingestion::config::{expand_env_vars, NodesFileConfig};
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    let Ok(text) = std::str::from_utf8(data) else {
        return;
    };
    let capped = if text.len() > 65536 {
        &text[..65536]
    } else {
        text
    };
    let _ = toml::from_str::<DiscoveryConfig>(capped);
    let expanded = expand_env_vars(capped);
    let _ = toml::from_str::<DiscoveryConfig>(&expanded);
    let _ = serde_yaml::from_str::<NodesFileConfig>(capped);
    if !capped.is_empty() {
        let _ = DiscoveryConfig::parse_protocol(capped.trim());
    }
});
