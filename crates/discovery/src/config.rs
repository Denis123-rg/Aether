use std::path::Path;

use aether_common::types::ProtocolType;
use aether_ingestion::event_decoder::EventSignatures;
use alloy::primitives::{Address, B256};
use serde::Deserialize;

/// Top-level discovery configuration loaded from `config/discovery.toml`.
#[derive(Debug, Clone, Deserialize, Default)]
pub struct DiscoveryConfig {
    #[serde(default)]
    pub discovery: DiscoverySettings,
    #[serde(default)]
    pub scoring: ScoringSettings,
    #[serde(default)]
    pub hot_cache: HotCacheSettings,
    #[serde(default)]
    pub factories: Vec<FactoryConfig>,
    #[serde(default)]
    pub the_graph: TheGraphSettings,
    #[serde(default)]
    pub curve: CurveDiscoverySettings,
    #[serde(default)]
    pub balancer_v3: BalancerV3DiscoverySettings,
}

/// Curve-specific discovery settings (supplements `[[factories]]` entries).
#[derive(Debug, Clone, Deserialize)]
pub struct CurveDiscoverySettings {
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    #[serde(default = "default_curve_fee")]
    pub default_fee_bps: u32,
}

impl Default for CurveDiscoverySettings {
    fn default() -> Self {
        Self {
            enabled: default_enabled(),
            default_fee_bps: default_curve_fee(),
        }
    }
}

/// Balancer V3 vault-based discovery settings.
#[derive(Debug, Clone, Deserialize)]
pub struct BalancerV3DiscoverySettings {
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    #[serde(default = "default_balancer_v3_vault")]
    pub vault_address: String,
    #[serde(default = "default_balancer_v3_fee")]
    pub default_fee_bps: u32,
}

impl Default for BalancerV3DiscoverySettings {
    fn default() -> Self {
        Self {
            enabled: false,
            vault_address: default_balancer_v3_vault(),
            default_fee_bps: default_balancer_v3_fee(),
        }
    }
}

fn default_curve_fee() -> u32 {
    4
}
fn default_balancer_v3_vault() -> String {
    "0xbA1333333333a1BA1108E8412f11850A5C319bA9".to_string()
}
fn default_balancer_v3_fee() -> u32 {
    10
}

/// Factory event type for log subscription and decoding.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum FactoryEventType {
    PairCreated,
    PoolCreatedV3,
    PlainPoolDeployed,
    PoolRegistered,
}

impl FactoryEventType {
    pub fn from_config_str(s: &str) -> Self {
        match s {
            "PoolCreated" | "pool_created" | "PoolCreatedV3" => Self::PoolCreatedV3,
            "PlainPoolDeployed" | "plain_pool_deployed" => Self::PlainPoolDeployed,
            "PoolRegistered" | "pool_registered" => Self::PoolRegistered,
            _ => Self::PairCreated,
        }
    }

    pub fn topic(self) -> B256 {
        match self {
            Self::PairCreated => EventSignatures::pair_created_topic(),
            Self::PoolCreatedV3 => EventSignatures::pool_created_v3_topic(),
            Self::PlainPoolDeployed => EventSignatures::plain_pool_deployed_topic(),
            Self::PoolRegistered => EventSignatures::pool_registered_v3_topic(),
        }
    }
}

/// A configured factory (or vault) entry for event listening.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FactoryEntry {
    pub address: Address,
    pub protocol: ProtocolType,
    pub fee_bps: u32,
    pub event_type: FactoryEventType,
}

#[derive(Debug, Clone, Deserialize)]
pub struct DiscoverySettings {
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    #[serde(default = "default_max_pools")]
    pub max_pools: usize,
    #[serde(default = "default_prune_interval")]
    pub prune_interval_secs: u64,
    #[serde(default = "default_prune_max_age")]
    pub prune_max_age_secs: u64,
    #[serde(default = "default_score_refresh")]
    pub score_refresh_interval_secs: u64,
    #[serde(default = "default_validation_swap")]
    pub validation_swap_eth: f64,
    /// Event listener mode: `websocket`, `poll`, or `auto` (try WS, fall back to poll).
    #[serde(default = "default_listener_mode")]
    pub listener_mode: String,
    /// Optional dedicated WebSocket URL. Falls back to `ETH_WS_URL` or `ETH_RPC_URL` (http→ws).
    #[serde(default)]
    pub ws_url: String,
    /// Polling interval when WS is unavailable or `listener_mode = poll`.
    #[serde(default = "default_poll_interval")]
    pub poll_interval_secs: u64,
    /// When true, keep a polling listener running alongside WS as a safety net.
    #[serde(default = "default_ws_fallback_poll")]
    pub ws_fallback_poll: bool,
    /// Validation mode: `revm` (fork simulation), `analytical` (math only), or `both`.
    #[serde(default = "default_validation_mode")]
    pub validation_mode: String,
}

impl Default for DiscoverySettings {
    fn default() -> Self {
        Self {
            enabled: default_enabled(),
            max_pools: default_max_pools(),
            prune_interval_secs: default_prune_interval(),
            prune_max_age_secs: default_prune_max_age(),
            score_refresh_interval_secs: default_score_refresh(),
            validation_swap_eth: default_validation_swap(),
            listener_mode: default_listener_mode(),
            ws_url: String::new(),
            poll_interval_secs: default_poll_interval(),
            ws_fallback_poll: default_ws_fallback_poll(),
            validation_mode: default_validation_mode(),
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct ScoringSettings {
    #[serde(default = "default_one")]
    pub tvl_weight: f64,
    #[serde(default = "default_one")]
    pub volume_weight: f64,
    #[serde(default = "default_slippage_bps")]
    pub slippage_estimate_bps: u32,
}

impl Default for ScoringSettings {
    fn default() -> Self {
        Self {
            tvl_weight: default_one(),
            volume_weight: default_one(),
            slippage_estimate_bps: default_slippage_bps(),
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct HotCacheSettings {
    #[serde(default = "default_hot_interval")]
    pub update_interval_secs: u64,
    #[serde(default = "default_top_n")]
    pub top_n: usize,
}

impl Default for HotCacheSettings {
    fn default() -> Self {
        Self {
            update_interval_secs: default_hot_interval(),
            top_n: default_top_n(),
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
pub struct FactoryConfig {
    pub name: String,
    pub protocol: String,
    pub address: String,
    #[serde(default = "default_fee_bps")]
    pub fee_bps: u32,
    #[serde(default = "default_event")]
    pub event: String,
}

#[derive(Debug, Clone, Deserialize, Default)]
pub struct TheGraphSettings {
    #[serde(default)]
    pub endpoint: String,
    #[serde(default = "default_graph_timeout")]
    pub timeout_secs: u64,
}

fn default_enabled() -> bool {
    true
}
fn default_max_pools() -> usize {
    50_000
}
fn default_prune_interval() -> u64 {
    3600
}
fn default_prune_max_age() -> u64 {
    3600
}
fn default_score_refresh() -> u64 {
    300
}
fn default_validation_swap() -> f64 {
    0.001
}
fn default_listener_mode() -> String {
    "auto".to_string()
}
fn default_poll_interval() -> u64 {
    12
}
fn default_ws_fallback_poll() -> bool {
    true
}
fn default_validation_mode() -> String {
    "both".to_string()
}
fn default_one() -> f64 {
    1.0
}
fn default_slippage_bps() -> u32 {
    50
}
fn default_hot_interval() -> u64 {
    5
}
fn default_top_n() -> usize {
    500
}
fn default_fee_bps() -> u32 {
    30
}
fn default_event() -> String {
    "PairCreated".to_string()
}
fn default_graph_timeout() -> u64 {
    10
}

impl DiscoveryConfig {
    /// Load configuration from a TOML file. Returns defaults on missing file.
    pub fn load(path: impl AsRef<Path>) -> anyhow::Result<Self> {
        let path = path.as_ref();
        if !path.exists() {
            tracing::warn!(path = %path.display(), "discovery config not found, using defaults");
            return Ok(Self::default());
        }
        let contents = std::fs::read_to_string(path)?;
        let cfg: Self = toml::from_str(&contents)?;
        Ok(cfg)
    }

    pub fn parse_protocol(s: &str) -> Option<ProtocolType> {
        match s {
            "uniswap_v2" => Some(ProtocolType::UniswapV2),
            "uniswap_v3" => Some(ProtocolType::UniswapV3),
            "sushiswap" => Some(ProtocolType::SushiSwap),
            "curve" => Some(ProtocolType::Curve),
            "balancer_v2" => Some(ProtocolType::BalancerV2),
            "balancer_v3" => Some(ProtocolType::BalancerV3),
            "bancor_v3" => Some(ProtocolType::BancorV3),
            _ => None,
        }
    }

    pub fn factory_addresses(&self) -> Vec<(Address, ProtocolType, u32)> {
        self.factory_entries()
            .into_iter()
            .map(|e| (e.address, e.protocol, e.fee_bps))
            .collect()
    }

    /// All factory/vault entries with event type for filtering and decoding.
    pub fn factory_entries(&self) -> Vec<FactoryEntry> {
        let mut entries: Vec<FactoryEntry> = self
            .factories
            .iter()
            .filter_map(|f| {
                let addr = f.address.parse::<Address>().ok()?;
                let protocol = Self::parse_protocol(&f.protocol)?;
                Some(FactoryEntry {
                    address: addr,
                    protocol,
                    fee_bps: f.fee_bps,
                    event_type: FactoryEventType::from_config_str(&f.event),
                })
            })
            .collect();

        if self.balancer_v3.enabled {
            if let Ok(vault) = self.balancer_v3.vault_address.parse::<Address>() {
                entries.push(FactoryEntry {
                    address: vault,
                    protocol: ProtocolType::BalancerV3,
                    fee_bps: self.balancer_v3.default_fee_bps,
                    event_type: FactoryEventType::PoolRegistered,
                });
            }
        }

        entries
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_config_values() {
        let cfg = DiscoveryConfig::default();
        assert!(cfg.discovery.enabled);
        assert_eq!(cfg.discovery.max_pools, 50_000);
        assert_eq!(cfg.hot_cache.top_n, 500);
        assert_eq!(cfg.hot_cache.update_interval_secs, 5);
    }

    #[test]
    fn parse_protocol_variants() {
        assert_eq!(
            DiscoveryConfig::parse_protocol("uniswap_v2"),
            Some(ProtocolType::UniswapV2)
        );
        assert_eq!(
            DiscoveryConfig::parse_protocol("uniswap_v3"),
            Some(ProtocolType::UniswapV3)
        );
        assert_eq!(
            DiscoveryConfig::parse_protocol("curve"),
            Some(ProtocolType::Curve)
        );
        assert!(DiscoveryConfig::parse_protocol("unknown").is_none());
    }

    #[test]
    fn load_missing_file_returns_defaults() {
        let cfg = DiscoveryConfig::load("/tmp/nonexistent_discovery_config.toml").unwrap();
        assert!(cfg.discovery.enabled);
        assert!(cfg.factories.is_empty());
    }

    #[test]
    fn load_workspace_config() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../config/discovery.toml");
        let cfg = DiscoveryConfig::load(path).unwrap();
        assert!(cfg.discovery.enabled);
        assert!(!cfg.factories.is_empty());
        assert_eq!(cfg.hot_cache.top_n, 500);
    }

    #[test]
    fn factory_addresses_parses_valid_entries() {
        let cfg = DiscoveryConfig {
            factories: vec![FactoryConfig {
                name: "uni_v2".into(),
                protocol: "uniswap_v2".into(),
                address: "0x5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f".into(),
                fee_bps: 30,
                event: "PairCreated".into(),
            }],
            ..Default::default()
        };
        let addrs = cfg.factory_addresses();
        assert_eq!(addrs.len(), 1);
        assert_eq!(addrs[0].2, 30);
    }

    #[test]
    fn factory_addresses_skips_invalid() {
        let cfg = DiscoveryConfig {
            factories: vec![
                FactoryConfig {
                    name: "bad".into(),
                    protocol: "not_a_protocol".into(),
                    address: "0x5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f".into(),
                    fee_bps: 30,
                    event: "PairCreated".into(),
                },
                FactoryConfig {
                    name: "bad_addr".into(),
                    protocol: "uniswap_v2".into(),
                    address: "not_an_address".into(),
                    fee_bps: 30,
                    event: "PairCreated".into(),
                },
            ],
            ..Default::default()
        };
        assert!(cfg.factory_addresses().is_empty());
    }

    #[test]
    fn scoring_defaults() {
        let s = ScoringSettings::default();
        assert_eq!(s.tvl_weight, 1.0);
        assert_eq!(s.volume_weight, 1.0);
        assert_eq!(s.slippage_estimate_bps, 50);
    }

    #[test]
    fn discovery_settings_defaults() {
        let d = DiscoverySettings::default();
        assert_eq!(d.prune_interval_secs, 3600);
        assert_eq!(d.validation_swap_eth, 0.001);
    }

    #[test]
    fn hot_cache_settings_defaults() {
        let h = HotCacheSettings::default();
        assert_eq!(h.update_interval_secs, 5);
        assert_eq!(h.top_n, 500);
    }

    #[test]
    fn deserialize_minimal_toml() {
        let toml_str = r#"
[discovery]
enabled = false
max_pools = 1000

[[factories]]
name = "test"
protocol = "uniswap_v2"
address = "0x5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f"
"#;
        let cfg: DiscoveryConfig = toml::from_str(toml_str).unwrap();
        assert!(!cfg.discovery.enabled);
        assert_eq!(cfg.discovery.max_pools, 1000);
        assert_eq!(cfg.factories.len(), 1);
    }
}
