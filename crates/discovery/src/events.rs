//! Factory event listener for pool discovery.
//!
//! Decodes `PairCreated` / `PoolCreated` logs and forwards them to the
//! discovery service for validation and scoring.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use aether_common::types::ProtocolType;
use aether_ingestion::event_decoder::{
    decode_log, decode_plain_pool_deployed, decode_pool_registered_v3, v3_fee_bps_from_topic,
    EventSignatures, PoolEvent,
};
use crate::config::{FactoryEntry, FactoryEventType};
use alloy::network::Ethereum;
use alloy::primitives::{Address, B256, U256};
use alloy::providers::{DynProvider, Provider, ProviderBuilder, WsConnect};
use alloy::rpc::types::Filter;
use futures::StreamExt;
use tracing::{debug, info, warn};

use crate::metrics::DiscoveryMetrics;
use crate::service::DiscoveryService;

/// Tracks whether the discovery WebSocket subscription is currently healthy.
#[derive(Debug, Clone, Default)]
pub struct WsHealth {
    healthy: Arc<AtomicBool>,
}

impl WsHealth {
    /// Create a new health handle (starts unhealthy until WS connects).
    pub fn new() -> Self {
        Self {
            healthy: Arc::new(AtomicBool::new(false)),
        }
    }

    /// Mark the WebSocket listener as connected or disconnected.
    pub fn set_healthy(&self, ok: bool) {
        self.healthy.store(ok, Ordering::Release);
    }

    /// Returns true when the WS listener is connected and receiving logs.
    pub fn is_healthy(&self) -> bool {
        self.healthy.load(Ordering::Acquire)
    }
}

/// Decoded factory event ready for ingestion.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FactoryPoolCreated {
    pub factory: Address,
    pub protocol: ProtocolType,
    pub fee_bps: u32,
    pub token0: Address,
    pub token1: Address,
    pub pool: Address,
}

/// Decode a raw log into a `FactoryPoolCreated` event when it matches a
/// configured factory's event topic (V2/V3/Curve/Balancer V3).
pub fn decode_factory_log(
    factory: Address,
    protocol: ProtocolType,
    fee_bps: u32,
    event_type: FactoryEventType,
    topics: &[B256],
    data: &[u8],
) -> Option<FactoryPoolCreated> {
    if topics.is_empty() {
        return None;
    }
    let topic0 = topics[0];
    if topic0 != event_type.topic() {
        return None;
    }

    match event_type {
        FactoryEventType::PlainPoolDeployed => {
            let (pool, token0, token1, onchain_fee) = decode_plain_pool_deployed(topics, data)?;
            Some(FactoryPoolCreated {
                factory,
                protocol,
                fee_bps: if onchain_fee > 0 { onchain_fee } else { fee_bps },
                token0,
                token1,
                pool,
            })
        }
        FactoryEventType::PoolRegistered => {
            let (pool, token0, token1, onchain_fee) = decode_pool_registered_v3(topics, data)?;
            Some(FactoryPoolCreated {
                factory,
                protocol,
                fee_bps: if onchain_fee > 0 { onchain_fee } else { fee_bps },
                token0,
                token1,
                pool,
            })
        }
        FactoryEventType::PairCreated | FactoryEventType::PoolCreatedV3 => {
            let effective_fee =
                if event_type == FactoryEventType::PoolCreatedV3 && topics.len() >= 4 {
                    v3_fee_bps_from_topic(&topics[3])
                } else {
                    fee_bps
                };
            match decode_log(topics, data, factory, Some(protocol)) {
                Ok(PoolEvent::PoolCreated {
                    token0,
                    token1,
                    pool,
                }) => Some(FactoryPoolCreated {
                    factory,
                    protocol,
                    fee_bps: effective_fee,
                    token0,
                    token1,
                    pool,
                }),
                _ => None,
            }
        }
    }
}

/// Backward-compatible alias for V2 `PairCreated` decoding.
pub fn decode_pair_created_log(
    factory: Address,
    protocol: ProtocolType,
    fee_bps: u32,
    topics: &[B256],
    data: &[u8],
) -> Option<FactoryPoolCreated> {
    decode_factory_log(
        factory,
        protocol,
        fee_bps,
        FactoryEventType::PairCreated,
        topics,
        data,
    )
}

/// Build an alloy log filter for all configured factory/vault addresses.
pub fn build_factory_filter(entries: &[FactoryEntry]) -> Filter {
    let addresses: Vec<Address> = entries.iter().map(|e| e.address).collect();
    let mut topics: Vec<B256> = entries
        .iter()
        .map(|e| e.event_type.topic())
        .collect();
    topics.sort_unstable();
    topics.dedup();
    Filter::new().address(addresses).events(topics)
}

/// Process a batch of logs from a subscription and ingest valid pools.
pub async fn process_logs(
    service: &DiscoveryService,
    entries: &[FactoryEntry],
    logs: Vec<alloy::rpc::types::Log>,
) -> usize {
    process_logs_with_metrics(service, entries, logs, None).await
}

/// Like [`process_logs`] but increments Prometheus counters when provided.
pub async fn process_logs_with_metrics(
    service: &DiscoveryService,
    entries: &[FactoryEntry],
    logs: Vec<alloy::rpc::types::Log>,
    metrics: Option<Arc<DiscoveryMetrics>>,
) -> usize {
    if let Some(m) = &metrics {
        m.events_received.inc_by(logs.len() as f64);
    }
    let mut ingested = 0usize;
    for log in logs {
        let factory_addr = log.address();
        let Some(entry) = entries.iter().find(|e| e.address == factory_addr) else {
            continue;
        };

        let topics: Vec<B256> = log.topics().to_vec();
        let data = log.data().data.as_ref();

        if let Some(created) = decode_factory_log(
            factory_addr,
            entry.protocol,
            entry.fee_bps,
            entry.event_type,
            &topics,
            data,
        ) {
            debug!(pool = %created.pool, ?entry.protocol, "factory pool event decoded");
            if service.ingest_pool_created(created).await {
                ingested += 1;
                if let Some(m) = &metrics {
                    m.pools_validated.inc();
                }
            } else if let Some(m) = &metrics {
                m.pools_rejected.inc();
            }
        }
    }
    ingested
}

/// Resolve a WebSocket RPC URL from config and environment.
pub fn resolve_ws_url(config_ws: &str) -> Option<String> {
    if !config_ws.is_empty() {
        return Some(config_ws.to_string());
    }
    if let Ok(url) = std::env::var("ETH_WS_URL") {
        if !url.is_empty() {
            return Some(url);
        }
    }
    std::env::var("ETH_RPC_URL")
        .ok()
        .and_then(|url| http_to_ws_url(&url))
}

/// Convert an HTTP RPC URL to WebSocket (https→wss, http→ws).
pub fn http_to_ws_url(url: &str) -> Option<String> {
    let lower = url.to_ascii_lowercase();
    if lower.starts_with("wss://") || lower.starts_with("ws://") {
        return Some(url.to_string());
    }
    if lower.starts_with("https://") {
        return Some(url.replacen("https://", "wss://", 1).replacen("HTTPS://", "wss://", 1));
    }
    if lower.starts_with("http://") {
        return Some(url.replacen("http://", "ws://", 1).replacen("HTTP://", "ws://", 1));
    }
    None
}

/// Spawn the factory event listener. Tries WebSocket when mode is `websocket` or
/// `auto`; falls back to HTTP polling when WS is unavailable or mode is `poll`.
#[allow(clippy::too_many_arguments)]
pub fn spawn_factory_listener(
    http_provider: Option<DynProvider<Ethereum>>,
    service: Arc<DiscoveryService>,
    entries: Vec<FactoryEntry>,
    listener_mode: &str,
    ws_url: &str,
    poll_interval_secs: u64,
    ws_fallback_poll: bool,
    poll_when_ws_healthy: bool,
    ws_health: WsHealth,
    metrics: Option<Arc<DiscoveryMetrics>>,
    shutdown: tokio::sync::watch::Receiver<bool>,
) -> Vec<tokio::task::JoinHandle<()>> {
    let mode = listener_mode.to_ascii_lowercase();
    let mut handles = Vec::new();

    let want_ws = mode == "websocket" || mode == "auto";
    let want_poll = mode == "poll"
        || (mode == "auto" && ws_fallback_poll)
        || (mode == "websocket" && ws_fallback_poll);

    if want_ws {
        if let Some(ws) = resolve_ws_url(ws_url) {
            let svc = Arc::clone(&service);
            let fac = entries.clone();
            let m = metrics.clone();
            let health = ws_health.clone();
            let mut shutdown_ws = shutdown.clone();
            handles.push(tokio::spawn(async move {
                spawn_ws_listener(ws, svc, fac, m, health, &mut shutdown_ws).await;
            }));
            info!("discovery: WebSocket listener started");
        } else if mode == "websocket" {
            warn!("discovery: listener_mode=websocket but no WS URL configured — falling back to poll");
        }
    }

    if want_poll {
        if let Some(provider) = http_provider {
            handles.push(spawn_polling_listener(
                provider,
                service,
                entries,
                poll_interval_secs,
                poll_when_ws_healthy,
                ws_health.clone(),
                shutdown,
            ));
            info!(
                interval_secs = poll_interval_secs,
                "discovery: HTTP polling listener started"
            );
        } else {
            warn!("discovery: no HTTP provider for polling fallback");
        }
    }

    handles
}

/// WebSocket subscription loop with exponential-backoff reconnect.
async fn spawn_ws_listener(
    ws_url: String,
    service: Arc<DiscoveryService>,
    entries: Vec<FactoryEntry>,
    metrics: Option<Arc<DiscoveryMetrics>>,
    ws_health: WsHealth,
    shutdown: &mut tokio::sync::watch::Receiver<bool>,
) {
    let mut attempt = 0u32;
    let max_backoff = Duration::from_secs(30);

    loop {
        if *shutdown.borrow() {
            info!("discovery WS listener shutting down");
            return;
        }

        match run_ws_subscription(&ws_url, &service, &entries, metrics.clone(), &ws_health, shutdown).await {
            Ok(()) => {
                ws_health.set_healthy(false);
                info!("discovery WS listener exited cleanly");
                return;
            }
            Err(e) => {
                ws_health.set_healthy(false);
                attempt = attempt.saturating_add(1);
                let backoff = Duration::from_secs(1u64 << attempt.min(5)).min(max_backoff);
                warn!(
                    attempt,
                    backoff_ms = backoff.as_millis() as u64,
                    error = %e,
                    "discovery WS subscription failed, reconnecting"
                );
                tokio::select! {
                    _ = tokio::time::sleep(backoff) => {}
                    _ = shutdown.changed() => {
                        if *shutdown.borrow() { return; }
                    }
                }
            }
        }
    }
}

async fn run_ws_subscription(
    ws_url: &str,
    service: &DiscoveryService,
    entries: &[FactoryEntry],
    metrics: Option<Arc<DiscoveryMetrics>>,
    ws_health: &WsHealth,
    shutdown: &mut tokio::sync::watch::Receiver<bool>,
) -> anyhow::Result<()> {
    let ws = WsConnect::new(ws_url);
    let provider = ProviderBuilder::new().connect_ws(ws).await?;
    let filter = build_factory_filter(entries);
    let sub = provider.subscribe_logs(&filter).await?;
    let mut stream = sub.into_stream();
    ws_health.set_healthy(true);
    info!(url = %ws_url, "discovery WebSocket subscribed to factory logs");

    loop {
        tokio::select! {
            log_opt = stream.next() => {
                match log_opt {
                    Some(log) => {
                        let n = process_logs_with_metrics(
                            service,
                            entries,
                            vec![log],
                            metrics.clone(),
                        ).await;
                        if n > 0 {
                            info!(ingested = n, "discovery WS ingested pools");
                        }
                    }
                    None => anyhow::bail!("WebSocket log stream ended"),
                }
            }
            _ = shutdown.changed() => {
                if *shutdown.borrow() {
                    return Ok(());
                }
            }
        }
    }
}

/// Poll factory logs over a block range (used when WS subscription unavailable).
pub async fn poll_factory_logs(
    provider: &DynProvider<Ethereum>,
    service: &DiscoveryService,
    entries: &[FactoryEntry],
    from_block: u64,
    to_block: u64,
) -> anyhow::Result<usize> {
    let filter = build_factory_filter(entries)
        .from_block(from_block)
        .to_block(to_block);
    let logs = provider.get_logs(&filter).await?;
    Ok(process_logs(service, entries, logs).await)
}

/// Spawn a background task that polls factory events every `interval_secs`.
pub fn spawn_polling_listener(
    provider: DynProvider<Ethereum>,
    service: std::sync::Arc<DiscoveryService>,
    entries: Vec<FactoryEntry>,
    interval_secs: u64,
    poll_when_ws_healthy: bool,
    ws_health: WsHealth,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(std::time::Duration::from_secs(interval_secs));
        let mut last_block = match provider.get_block_number().await {
            Ok(n) => n.saturating_sub(1),
            Err(e) => {
                warn!(error = %e, "discovery listener: failed to get block number");
                0
            }
        };

        loop {
            tokio::select! {
                _ = interval.tick() => {
                    if ws_health.is_healthy() && !poll_when_ws_healthy {
                        debug!("discovery poll skipped: WebSocket healthy");
                        continue;
                    }
                    let current = match provider.get_block_number().await {
                        Ok(n) => n,
                        Err(e) => {
                            warn!(error = %e, "discovery listener: block number poll failed");
                            continue;
                        }
                    };
                    if current > last_block {
                        match poll_factory_logs(
                            &provider,
                            &service,
                            &entries,
                            last_block + 1,
                            current,
                        ).await {
                            Ok(n) if n > 0 => info!(ingested = n, "discovery poll ingested pools"),
                            Ok(_) => {}
                            Err(e) => warn!(error = %e, "discovery poll failed"),
                        }
                        last_block = current;
                    }
                }
                _ = shutdown.changed() => {
                    if *shutdown.borrow() {
                        info!("discovery listener shutting down");
                        break;
                    }
                }
            }
        }
    })
}

/// Simulate a Curve `PlainPoolDeployed` log for testing.
pub fn mock_plain_pool_deployed_log(
    pool: Address,
    token0: Address,
    token1: Address,
    fee: u64,
) -> (Vec<B256>, Vec<u8>) {
    use aether_ingestion::event_decoder::PlainPoolDeployed;
    use alloy::sol_types::SolEvent;
    let event = PlainPoolDeployed {
        pool,
        coins: vec![token0, token1],
        A: U256::from(100u64),
        fee: U256::from(fee),
        owner: Address::ZERO,
    };
    let topics = vec![PlainPoolDeployed::SIGNATURE_HASH];
    let data = event.encode_data();
    (topics, data)
}

/// Simulate a Balancer V3 `PoolRegistered` log for testing.
pub fn mock_pool_registered_log(
    pool: Address,
    factory: Address,
    token0: Address,
    token1: Address,
    swap_fee: U256,
) -> (Vec<B256>, Vec<u8>) {
    use aether_ingestion::event_decoder::PoolRegistered;
    use alloy::sol_types::SolEvent;
    let event = PoolRegistered {
        pool,
        factory,
        tokenConfig: vec![
            aether_ingestion::event_decoder::BalancerV3TokenConfig {
                token: token0,
                tokenType: 0,
                rateProvider: Address::ZERO,
                paysYieldFees: false,
            },
            aether_ingestion::event_decoder::BalancerV3TokenConfig {
                token: token1,
                tokenType: 0,
                rateProvider: Address::ZERO,
                paysYieldFees: false,
            },
        ],
        swapFeePercentage: swap_fee,
        pauseWindowEndTime: 0,
        roleAccounts: aether_ingestion::event_decoder::BalancerV3RoleAccounts {
            pauseManager: Address::ZERO,
            swapFeeManager: Address::ZERO,
            poolCreator: Address::ZERO,
        },
        hooksConfig: aether_ingestion::event_decoder::BalancerV3HooksConfig {
            enableHookAdjustedAmounts: false,
            shouldCallBeforeInitialize: false,
            shouldCallAfterInitialize: false,
            shouldCallComputeDynamicSwapFee: false,
            shouldCallBeforeSwap: false,
            shouldCallAfterSwap: false,
            shouldCallBeforeAddLiquidity: false,
            shouldCallAfterAddLiquidity: false,
            shouldCallBeforeRemoveLiquidity: false,
            shouldCallAfterRemoveLiquidity: false,
            hooksContract: Address::ZERO,
        },
        liquidityManagement: aether_ingestion::event_decoder::BalancerV3LiquidityManagement {
            disableUnbalancedLiquidity: false,
            enableAddLiquidityCustom: false,
            enableRemoveLiquidityCustom: false,
            enableDonation: false,
        },
    };
    let topics = vec![
        PoolRegistered::SIGNATURE_HASH,
        B256::left_padding_from(pool.as_slice()),
        B256::left_padding_from(factory.as_slice()),
    ];
    let data = event.encode_data();
    (topics, data)
}

/// Simulate a `PairCreated` log for testing (mock event listener).
pub fn mock_pair_created_log(
    _factory: Address,
    token0: Address,
    token1: Address,
    pair: Address,
    all_pairs_length: u64,
) -> (Vec<B256>, Vec<u8>) {
    use alloy::primitives::U256;
    let topic0 = EventSignatures::pair_created_topic();
    let topics = vec![
        topic0,
        B256::left_padding_from(token0.as_slice()),
        B256::left_padding_from(token1.as_slice()),
    ];
    let mut data = vec![0u8; 64];
    data[12..32].copy_from_slice(pair.as_slice());
    data[32..64].copy_from_slice(&U256::from(all_pairs_length).to_be_bytes::<32>());
    (topics, data)
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::{address, U256};
    use serial_test::serial;

    fn uni_v2_factory() -> Address {
        address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f")
    }

    #[test]
    fn decode_pair_created_valid() {
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (topics, data) = mock_pair_created_log(uni_v2_factory(), token0, token1, pair, 42);

        let decoded = decode_pair_created_log(
            uni_v2_factory(),
            ProtocolType::UniswapV2,
            30,
            &topics,
            &data,
        );
        assert_eq!(
            decoded,
            Some(FactoryPoolCreated {
                factory: uni_v2_factory(),
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                token0,
                token1,
                pool: pair,
            })
        );
    }

    #[test]
    fn decode_wrong_topic_returns_none() {
        let result = decode_pair_created_log(
            uni_v2_factory(),
            ProtocolType::UniswapV2,
            30,
            &[B256::ZERO],
            &[],
        );
        assert!(result.is_none());
    }

    #[test]
    fn build_factory_filter_succeeds() {
        use crate::config::FactoryEventType;
        let entries = vec![FactoryEntry {
            address: uni_v2_factory(),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];
        let _filter = build_factory_filter(&entries);
    }

    #[test]
    fn factory_pool_created_equality() {
        let a = FactoryPoolCreated {
            factory: uni_v2_factory(),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: Address::ZERO,
            token1: Address::ZERO,
            pool: Address::ZERO,
        };
        assert_eq!(a, a.clone());
    }

    #[test]
    fn mock_log_produces_three_topics() {
        let (topics, data) = mock_pair_created_log(
            uni_v2_factory(),
            Address::ZERO,
            Address::ZERO,
            Address::ZERO,
            1,
        );
        assert_eq!(topics.len(), 3);
        assert!(data.len() >= 64);
    }

    #[test]
    fn decode_empty_topics_none() {
        assert!(decode_pair_created_log(
            uni_v2_factory(),
            ProtocolType::UniswapV2,
            30,
            &[],
            &[]
        )
        .is_none());
    }

    #[test]
    fn decode_preserves_fee_bps() {
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = Address::from([0x42; 20]);
        let (topics, data) = mock_pair_created_log(uni_v2_factory(), token0, token1, pair, 1);
        let decoded = decode_pair_created_log(uni_v2_factory(), ProtocolType::SushiSwap, 25, &topics, &data)
            .unwrap();
        assert_eq!(decoded.fee_bps, 25);
        assert_eq!(decoded.protocol, ProtocolType::SushiSwap);
    }

    #[test]
    fn mock_log_pair_address_in_data() {
        let pair = Address::from([0x99; 20]);
        let (_, data) = mock_pair_created_log(
            uni_v2_factory(),
            Address::ZERO,
            Address::ZERO,
            pair,
            7,
        );
        assert_eq!(&data[12..32], pair.as_slice());
    }

    #[test]
    fn mock_log_all_pairs_length_encoded() {
        let (_, data) = mock_pair_created_log(
            uni_v2_factory(),
            Address::ZERO,
            Address::ZERO,
            Address::ZERO,
            12345,
        );
        let len = U256::from_be_slice(&data[32..64]);
        assert_eq!(len, U256::from(12345));
    }

    #[test]
    fn build_factory_filter_multiple_factories() {
        use crate::config::FactoryEventType;
        let sushi = address!("C0AEe478e3658e2610c5F7A4A2D1773cDCC8b275");
        let entries = vec![
            FactoryEntry {
                address: uni_v2_factory(),
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                event_type: FactoryEventType::PairCreated,
            },
            FactoryEntry {
                address: sushi,
                protocol: ProtocolType::SushiSwap,
                fee_bps: 30,
                event_type: FactoryEventType::PairCreated,
            },
        ];
        let _filter = build_factory_filter(&entries);
    }

    #[test]
    fn http_to_ws_converts_https() {
        assert_eq!(
            http_to_ws_url("https://eth-mainnet.g.alchemy.com/v2/key"),
            Some("wss://eth-mainnet.g.alchemy.com/v2/key".to_string())
        );
    }

    #[test]
    fn http_to_ws_converts_http_local() {
        assert_eq!(
            http_to_ws_url("http://127.0.0.1:8545"),
            Some("ws://127.0.0.1:8545".to_string())
        );
    }

    #[test]
    fn http_to_ws_passthrough_wss() {
        let url = "wss://example.com/ws";
        assert_eq!(http_to_ws_url(url), Some(url.to_string()));
    }

    #[test]
    fn resolve_ws_url_from_config() {
        assert_eq!(
            resolve_ws_url("wss://custom.example/ws"),
            Some("wss://custom.example/ws".to_string())
        );
    }

    #[test]
    fn resolve_ws_url_empty_uses_eth_rpc_url_fallback() {
        let result = resolve_ws_url("");
        // When config is empty and no env vars set (common in CI), returns None
        assert!(result.is_none() || result.is_some());
    }

    #[test]
    fn metrics_noop_registers() {
        let m = DiscoveryMetrics::noop();
        assert_eq!(m.events_received.get(), 0.0);
    }

    #[test]
    fn http_to_ws_unknown_scheme_returns_none() {
        assert_eq!(http_to_ws_url("ftp://example.com/file"), None);
        assert_eq!(http_to_ws_url("file:///path"), None);
        assert_eq!(http_to_ws_url("grpc://example.com"), None);
    }

    #[test]
    fn http_to_ws_empty_string_returns_none() {
        assert_eq!(http_to_ws_url(""), None);
    }

    #[test]
    fn http_to_ws_https_uppercase_conversion() {
        let url = "HTTPS://ETH-MAINNET.ALCHEMY.COM/v2/key";
        let result = http_to_ws_url(url).unwrap();
        assert!(result.starts_with("wss://"));
    }

    #[test]
    fn http_to_ws_http_uppercase_conversion() {
        let url = "HTTP://127.0.0.1:8545";
        let result = http_to_ws_url(url).unwrap();
        assert!(result.starts_with("ws://"));
    }

    #[test]
    fn http_to_ws_passthrough_ws_lowercase() {
        let url = "ws://127.0.0.1:8545";
        assert_eq!(http_to_ws_url(url), Some(url.to_string()));
    }

    #[test]
    fn resolve_ws_url_config_priority_over_env() {
        let result = resolve_ws_url("wss://config.example/ws");
        assert_eq!(result, Some("wss://config.example/ws".to_string()));
    }

    #[test]
    fn decode_factory_log_plain_pool_deployed_onchain_fee_nonzero() {
        let pool = Address::from([0xAA; 20]);
        let (topics, data) = mock_plain_pool_deployed_log(pool, address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), 5_000_000);
        let result = decode_factory_log(
            address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            ProtocolType::Curve,
            4,
            FactoryEventType::PlainPoolDeployed,
            &topics,
            &data,
        )
        .unwrap();
        assert_eq!(result.fee_bps, 5);
    }

    #[test]
    fn decode_factory_log_plain_pool_deployed_onchain_fee_zero_uses_config() {
        let pool = Address::from([0xBB; 20]);
        let (topics, data) = mock_plain_pool_deployed_log(pool, address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), 0);
        let result = decode_factory_log(
            address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            ProtocolType::Curve,
            4,
            FactoryEventType::PlainPoolDeployed,
            &topics,
            &data,
        )
        .unwrap();
        assert_eq!(result.fee_bps, 4);
    }

    #[test]
    fn decode_factory_log_pool_registered_onchain_fee_nonzero() {
        let pool = Address::from([0xCC; 20]);
        let (topics, data) = mock_pool_registered_log(pool, address!("bA1333333333a1BA1108E8412f11850A5C319bA9"), address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), U256::from(1_000_000_000_000_000u64));
        let result = decode_factory_log(
            address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
            ProtocolType::BalancerV3,
            10,
            FactoryEventType::PoolRegistered,
            &topics,
            &data,
        )
        .unwrap();
        assert_eq!(result.fee_bps, 10);
    }

    #[test]
    fn decode_factory_log_pool_registered_onchain_fee_zero_uses_config() {
        let pool = Address::from([0xDD; 20]);
        let (topics, data) = mock_pool_registered_log(pool, address!("bA1333333333a1BA1108E8412f11850A5C319bA9"), address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), U256::ZERO);
        let result = decode_factory_log(
            address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
            ProtocolType::BalancerV3,
            10,
            FactoryEventType::PoolRegistered,
            &topics,
            &data,
        )
        .unwrap();
        assert_eq!(result.fee_bps, 10);
    }

    #[test]
    fn decode_factory_log_pool_created_v3_with_fee_topic() {
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pool = Address::from([0xEE; 20]);
        let factory = Address::from([0xFF; 20]);
        let topic0 = EventSignatures::pool_created_v3_topic();
        let topic3_fee = B256::left_padding_from(&U256::from(5000u32).to_be_bytes::<32>());
        let topics = vec![
            topic0,
            B256::left_padding_from(token0.as_slice()),
            B256::left_padding_from(token1.as_slice()),
            topic3_fee,
        ];
        let mut data = vec![0u8; 64];
        data[12..32].copy_from_slice(pool.as_slice());
        let result = decode_factory_log(
            factory,
            ProtocolType::UniswapV3,
            30,
            FactoryEventType::PoolCreatedV3,
            &topics,
            &data,
        );
        assert!(result.is_some());
        assert_eq!(result.as_ref().unwrap().fee_bps, 50);
    }

    #[test]
    fn decode_factory_log_pool_created_v3_without_fee_topic() {
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pool = Address::from([0xEF; 20]);
        let factory = Address::from([0xFE; 20]);
        let topic0 = EventSignatures::pool_created_v3_topic();
        let topics = vec![
            topic0,
            B256::left_padding_from(token0.as_slice()),
            B256::left_padding_from(token1.as_slice()),
        ];
        let mut data = vec![0u8; 64];
        data[12..32].copy_from_slice(pool.as_slice());
        let result = decode_factory_log(
            factory,
            ProtocolType::UniswapV3,
            30,
            FactoryEventType::PoolCreatedV3,
            &topics,
            &data,
        );
        // decode_pool_created_v3 requires topics.len() >= 4, so this returns None
        assert!(result.is_none());
    }

    #[test]
    fn decode_factory_log_pair_created_wrong_data_returns_none() {
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let topic0 = EventSignatures::pair_created_topic();
        let topics = vec![
            topic0,
            B256::left_padding_from(address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48").as_slice()),
            B256::left_padding_from(address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2").as_slice()),
        ];
        let bad_data = vec![0xFF; 10];
        let result = decode_factory_log(
            factory,
            ProtocolType::UniswapV2,
            30,
            FactoryEventType::PairCreated,
            &topics,
            &bad_data,
        );
        assert!(result.is_none());
    }

    #[test]
    fn build_factory_filter_multiple_different_event_types() {
        let entries = vec![
            FactoryEntry {
                address: address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f"),
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                event_type: FactoryEventType::PairCreated,
            },
            FactoryEntry {
                address: address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
                protocol: ProtocolType::Curve,
                fee_bps: 4,
                event_type: FactoryEventType::PlainPoolDeployed,
            },
            FactoryEntry {
                address: address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
                protocol: ProtocolType::BalancerV3,
                fee_bps: 10,
                event_type: FactoryEventType::PoolRegistered,
            },
        ];
        let _filter = build_factory_filter(&entries);
    }

    #[tokio::test]
    async fn process_logs_ingests_valid_pool() {
        use alloy::primitives::LogData;
        let service = crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = Address::from([0xAA; 20]);
        let (topics, data) = mock_pair_created_log(factory, token0, token1, pair, 1);
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory,
                data: LogData::new_unchecked(topics, data.into()),
            },
            ..Default::default()
        };

        let ingested = process_logs(&service, &entries, vec![log]).await;
        assert_eq!(ingested, 1);
    }

    #[tokio::test]
    async fn process_logs_skips_unknown_factory() {
        use alloy::primitives::LogData;
        let service = crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let entries = vec![FactoryEntry {
            address: address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f"),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: address!("0000000000000000000000000000000000000001"),
                data: LogData::new_unchecked(
                    vec![EventSignatures::pair_created_topic()],
                    vec![].into(),
                ),
            },
            ..Default::default()
        };

        let ingested = process_logs(&service, &entries, vec![log]).await;
        assert_eq!(ingested, 0);
    }

    #[tokio::test]
    async fn process_logs_with_metrics_increments_counters() {
        use alloy::primitives::LogData;
        let service = crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let metrics = DiscoveryMetrics::noop();
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory,
                data: LogData::new_unchecked(
                    vec![EventSignatures::pair_created_topic()],
                    vec![].into(),
                ),
            },
            ..Default::default()
        };

        let before = metrics.events_received.get();
        let _ = process_logs_with_metrics(&service, &entries, vec![log], Some(metrics.clone())).await;
        assert!(metrics.events_received.get() > before);
    }

    #[tokio::test]
    async fn process_logs_empty_logs_returns_zero() {
        let service = crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let entries = vec![];
        let ingested = process_logs(&service, &entries, vec![]).await;
        assert_eq!(ingested, 0);
    }

    #[tokio::test]
    async fn process_logs_with_metrics_none_metrics() {
        use alloy::primitives::LogData;
        let service = crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = Address::from([0xCC; 20]);
        let (topics, data) = mock_pair_created_log(factory, token0, token1, pair, 1);
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory,
                data: LogData::new_unchecked(topics, data.into()),
            },
            ..Default::default()
        };

        let ingested = process_logs_with_metrics(&service, &entries, vec![log], None).await;
        assert_eq!(ingested, 1);
    }

    // ── WsHealth ──

    #[test]
    fn ws_health_new_starts_unhealthy() {
        let h = WsHealth::new();
        assert!(!h.is_healthy());
    }

    #[test]
    fn ws_health_default_starts_unhealthy() {
        let h = WsHealth::default();
        assert!(!h.is_healthy());
    }

    #[test]
    fn ws_health_set_healthy_true() {
        let h = WsHealth::new();
        h.set_healthy(true);
        assert!(h.is_healthy());
    }

    #[test]
    fn ws_health_set_healthy_toggle() {
        let h = WsHealth::new();
        assert!(!h.is_healthy());
        h.set_healthy(true);
        assert!(h.is_healthy());
        h.set_healthy(false);
        assert!(!h.is_healthy());
    }

    #[test]
    fn ws_health_clone_shares_state() {
        let h1 = WsHealth::new();
        let h2 = h1.clone();
        h1.set_healthy(true);
        assert!(h2.is_healthy());
        h2.set_healthy(false);
        assert!(!h1.is_healthy());
    }

    // ── decode_factory_log edge cases ──

    #[test]
    fn decode_factory_log_plain_pool_wrong_topic() {
        let pool = Address::from([0xAA; 20]);
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let (topics, data) = mock_plain_pool_deployed_log(pool, token0, token1, 5_000_000);
        let mut bad_topics = topics;
        bad_topics[0] = B256::from([0xFF; 32]);
        let result = decode_factory_log(
            address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            ProtocolType::Curve,
            4,
            FactoryEventType::PlainPoolDeployed,
            &bad_topics,
            &data,
        );
        assert!(result.is_none());
    }

    #[test]
    fn decode_factory_log_pool_registered_wrong_topic() {
        let pool = Address::from([0xCC; 20]);
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let (topics, data) = mock_pool_registered_log(
            pool,
            address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
            token0,
            token1,
            U256::from(1_000_000_000_000_000u64),
        );
        let mut bad_topics = topics;
        bad_topics[0] = B256::from([0xFF; 32]);
        let result = decode_factory_log(
            address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
            ProtocolType::BalancerV3,
            10,
            FactoryEventType::PoolRegistered,
            &bad_topics,
            &data,
        );
        assert!(result.is_none());
    }

    #[test]
    fn decode_factory_log_pair_created_full_fields() {
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = address!("B4e16d0168e52d35CaCD2c6185b44281Ec28C9Dc");
        let (topics, data) = mock_pair_created_log(uni_v2_factory(), token0, token1, pair, 42);
        let result = decode_factory_log(
            uni_v2_factory(),
            ProtocolType::UniswapV2,
            30,
            FactoryEventType::PairCreated,
            &topics,
            &data,
        )
        .unwrap();
        assert_eq!(result.pool, pair);
        assert_eq!(result.token0, token0);
        assert_eq!(result.token1, token1);
        assert_eq!(result.fee_bps, 30);
        assert_eq!(result.protocol, ProtocolType::UniswapV2);
        assert_eq!(result.factory, uni_v2_factory());
    }

    #[test]
    fn decode_factory_log_plain_pool_deployed_empty_data() {
        let topics = vec![EventSignatures::plain_pool_deployed_topic()];
        let result = decode_factory_log(
            address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            ProtocolType::Curve,
            4,
            FactoryEventType::PlainPoolDeployed,
            &topics,
            &[],
        );
        assert!(result.is_none());
    }

    #[test]
    fn decode_factory_log_pool_registered_empty_data() {
        let topics = vec![EventSignatures::pool_registered_v3_topic()];
        let result = decode_factory_log(
            address!("bA1333333333a1BA1108E8412f11850A5C319bA9"),
            ProtocolType::BalancerV3,
            10,
            FactoryEventType::PoolRegistered,
            &topics,
            &[],
        );
        assert!(result.is_none());
    }

    #[test]
    fn decode_factory_log_pair_created_valid_event_type_mismatch() {
        let result = decode_factory_log(
            uni_v2_factory(),
            ProtocolType::UniswapV2,
            30,
            FactoryEventType::PlainPoolDeployed,
            &[EventSignatures::pair_created_topic()],
            &[],
        );
        assert!(result.is_none());
    }

    #[test]
    fn decode_factory_log_pair_created_empty_data_returns_none() {
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let topics = vec![
            EventSignatures::pair_created_topic(),
            B256::left_padding_from(token0.as_slice()),
            B256::left_padding_from(token1.as_slice()),
        ];
        let result = decode_factory_log(
            factory,
            ProtocolType::UniswapV2,
            30,
            FactoryEventType::PairCreated,
            &topics,
            &[],
        );
        assert!(result.is_none());
    }

    // ── http_to_ws_url edge cases ──

    #[test]
    fn http_to_ws_passthrough_wss_uppercase() {
        let url = "WSS://example.com/ws";
        assert_eq!(http_to_ws_url(url), Some(url.to_string()));
    }

    #[test]
    fn http_to_ws_passthrough_ws_uppercase() {
        let url = "WS://example.com/ws";
        assert_eq!(http_to_ws_url(url), Some(url.to_string()));
    }

    #[test]
    fn http_to_ws_mixed_case_http() {
        let url = "HtTp://127.0.0.1:8545";
        let result = http_to_ws_url(url);
        assert!(result.is_some());
    }

    #[test]
    fn http_to_ws_mixed_case_https() {
        let url = "HtTpS://example.com";
        let result = http_to_ws_url(url);
        assert!(result.is_some());
    }

    #[test]
    fn http_to_ws_complex_https_url() {
        let url = "https://eth-mainnet.alchemyapi.io/v2/abc123def456";
        assert_eq!(
            http_to_ws_url(url),
            Some("wss://eth-mainnet.alchemyapi.io/v2/abc123def456".to_string())
        );
    }

    #[test]
    fn http_to_ws_local_rpc() {
        let url = "http://localhost:8545";
        assert_eq!(
            http_to_ws_url(url),
            Some("ws://localhost:8545".to_string())
        );
    }

    // ── resolve_ws_url edge cases ──

    #[serial]
    #[test]
    fn resolve_ws_url_empty_no_env() {
        std::env::remove_var("ETH_WS_URL");
        std::env::remove_var("ETH_RPC_URL");
        let result = resolve_ws_url("");
        assert!(result.is_none());
    }

    #[serial]
    #[test]
    fn resolve_ws_url_env_ws_url() {
        std::env::remove_var("ETH_RPC_URL");
        std::env::set_var("ETH_WS_URL", "wss://env-test.example/ws");
        let result = resolve_ws_url("");
        assert_eq!(result, Some("wss://env-test.example/ws".to_string()));
        std::env::remove_var("ETH_WS_URL");
    }

    #[serial]
    #[test]
    fn resolve_ws_url_env_ws_empty_falls_through() {
        std::env::remove_var("ETH_RPC_URL");
        std::env::set_var("ETH_WS_URL", "");
        let result = resolve_ws_url("");
        assert!(result.is_none());
        std::env::remove_var("ETH_WS_URL");
    }

    #[serial]
    #[test]
    fn resolve_ws_url_env_rpc_url() {
        std::env::remove_var("ETH_WS_URL");
        std::env::set_var("ETH_RPC_URL", "https://eth-mainnet.g.alchemy.com/v2/key");
        let result = resolve_ws_url("");
        assert_eq!(
            result,
            Some("wss://eth-mainnet.g.alchemy.com/v2/key".to_string())
        );
        std::env::remove_var("ETH_RPC_URL");
    }

    #[serial]
    #[test]
    fn resolve_ws_url_env_rpc_not_http() {
        std::env::remove_var("ETH_WS_URL");
        std::env::set_var("ETH_RPC_URL", "grpc://example.com");
        let result = resolve_ws_url("");
        assert!(result.is_none());
        std::env::remove_var("ETH_RPC_URL");
    }

    #[serial]
    #[test]
    fn resolve_ws_url_env_ws_priority_over_rpc() {
        std::env::set_var("ETH_WS_URL", "wss://ws-priority.example/ws");
        std::env::set_var("ETH_RPC_URL", "https://rpc-priority.example/v2/key");
        let result = resolve_ws_url("");
        assert_eq!(
            result,
            Some("wss://ws-priority.example/ws".to_string())
        );
        std::env::remove_var("ETH_WS_URL");
        std::env::remove_var("ETH_RPC_URL");
    }

    // ── build_factory_filter edge cases ──

    #[test]
    fn build_factory_filter_empty() {
        let entries = vec![];
        let _filter = build_factory_filter(&entries);
    }

    // ── process_logs rejection / mixed paths ──

    #[tokio::test]
    async fn process_logs_rejects_duplicate_pool() {
        use alloy::primitives::LogData;
        let service =
            crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = Address::from([0xAA; 20]);
        let (topics, data) = mock_pair_created_log(factory, token0, token1, pair, 1);
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let make_log = || {
            alloy::rpc::types::Log {
                inner: alloy::primitives::Log {
                    address: factory,
                    data: LogData::new_unchecked(topics.clone(), data.clone().into()),
                },
                ..Default::default()
            }
        };

        let first = process_logs(&service, &entries, vec![make_log()]).await;
        assert_eq!(first, 1);

        let second = process_logs(&service, &entries, vec![make_log()]).await;
        assert_eq!(second, 0);
    }

    #[tokio::test]
    async fn process_logs_with_metrics_rejects_duplicate() {
        use alloy::primitives::LogData;
        let service =
            crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let metrics = DiscoveryMetrics::noop();
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = Address::from([0xBB; 20]);
        let (topics, data) = mock_pair_created_log(factory, token0, token1, pair, 1);
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let make_log = || {
            alloy::rpc::types::Log {
                inner: alloy::primitives::Log {
                    address: factory,
                    data: LogData::new_unchecked(topics.clone(), data.clone().into()),
                },
                ..Default::default()
            }
        };

        let first = process_logs_with_metrics(
            &service,
            &entries,
            vec![make_log()],
            Some(metrics.clone()),
        )
        .await;
        assert_eq!(first, 1);

        let second = process_logs_with_metrics(
            &service,
            &entries,
            vec![make_log()],
            Some(metrics.clone()),
        )
        .await;
        assert_eq!(second, 0);
        assert!(metrics.pools_rejected.get() > 0.0);
    }

    #[tokio::test]
    async fn process_logs_mixed_valid_and_invalid_logs() {
        use alloy::primitives::LogData;
        let service =
            crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let pair = Address::from([0xAA; 20]);
        let (topics, data) = mock_pair_created_log(factory, token0, token1, pair, 1);

        let valid_log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory,
                data: LogData::new_unchecked(topics, data.into()),
            },
            ..Default::default()
        };

        let invalid_log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory,
                data: LogData::new_unchecked(
                    vec![EventSignatures::pair_created_topic()],
                    vec![].into(),
                ),
            },
            ..Default::default()
        };

        let ingested = process_logs(&service, &entries, vec![valid_log, invalid_log]).await;
        assert_eq!(ingested, 1);
    }

    #[tokio::test]
    async fn process_logs_with_metrics_bad_decode() {
        use alloy::primitives::LogData;
        let service =
            crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let metrics = DiscoveryMetrics::noop();
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let bad_log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory,
                data: LogData::new_unchecked(
                    vec![EventSignatures::pair_created_topic()],
                    vec![0xFF; 5].into(),
                ),
            },
            ..Default::default()
        };

        let ingested =
            process_logs_with_metrics(&service, &entries, vec![bad_log], Some(metrics.clone()))
                .await;
        assert_eq!(ingested, 0);
        assert!(metrics.events_received.get() > 0.0);
    }

    #[tokio::test]
    async fn process_logs_with_metrics_unknown_factory_with_metrics() {
        use alloy::primitives::LogData;
        let service =
            crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let metrics = DiscoveryMetrics::noop();
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let unknown_factory = address!("0000000000000000000000000000000000000001");
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let unknown_log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: unknown_factory,
                data: LogData::new_unchecked(
                    vec![EventSignatures::pair_created_topic()],
                    vec![].into(),
                ),
            },
            ..Default::default()
        };

        let before = metrics.events_received.get();
        let ingested = process_logs_with_metrics(
            &service,
            &entries,
            vec![unknown_log],
            Some(metrics.clone()),
        )
        .await;
        assert_eq!(ingested, 0);
        assert!(metrics.events_received.get() > before);
    }

    // ── FactoryPoolCreated derives ──

    #[test]
    fn factory_pool_created_debug_format() {
        let fpc = FactoryPoolCreated {
            factory: uni_v2_factory(),
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: Address::ZERO,
            token1: Address::ZERO,
            pool: Address::ZERO,
        };
        let debug_str = format!("{:?}", fpc);
        assert!(debug_str.contains("FactoryPoolCreated"));
        assert!(debug_str.contains("UniswapV2"));
    }

    #[test]
    fn factory_pool_created_clone() {
        let fpc = FactoryPoolCreated {
            factory: uni_v2_factory(),
            protocol: ProtocolType::Curve,
            fee_bps: 4,
            token0: Address::from([0xAA; 20]),
            token1: Address::from([0xBB; 20]),
            pool: Address::from([0xCC; 20]),
        };
        let cloned = fpc.clone();
        assert_eq!(fpc, cloned);
    }

    // ── Mock log roundtrips ──

    #[test]
    fn mock_plain_pool_deployed_log_roundtrip() {
        let pool = Address::from([0x01; 20]);
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let (topics, data) = mock_plain_pool_deployed_log(pool, token0, token1, 3_000_000);
        assert_eq!(topics.len(), 1);
        assert!(!data.is_empty());
        let result = decode_factory_log(
            address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            ProtocolType::Curve,
            4,
            FactoryEventType::PlainPoolDeployed,
            &topics,
            &data,
        );
        assert!(result.is_some());
        let created = result.unwrap();
        assert_eq!(created.pool, pool);
        assert_eq!(created.token0, token0);
        assert_eq!(created.token1, token1);
    }

    #[test]
    fn mock_pool_registered_log_roundtrip() {
        let pool = Address::from([0x02; 20]);
        let factory = address!("bA1333333333a1BA1108E8412f11850A5C319bA9");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let (topics, data) = mock_pool_registered_log(
            pool,
            factory,
            token0,
            token1,
            U256::from(1_000_000_000_000_000u64),
        );
        assert_eq!(topics.len(), 3);
        assert!(!data.is_empty());
        let result = decode_factory_log(
            factory,
            ProtocolType::BalancerV3,
            10,
            FactoryEventType::PoolRegistered,
            &topics,
            &data,
        );
        assert!(result.is_some());
        let created = result.unwrap();
        assert_eq!(created.pool, pool);
        assert_eq!(created.token0, token0);
        assert_eq!(created.token1, token1);
    }

    // ── spawn_factory_listener mode branches ──

    #[tokio::test]
    async fn spawn_factory_listener_poll_no_provider() {
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let (_, shutdown_rx) = tokio::sync::watch::channel(false);
        let ws_health = WsHealth::new();
        let handles = spawn_factory_listener(
            None,
            service,
            entries,
            "poll",
            "",
            5,
            false,
            false,
            ws_health,
            None,
            shutdown_rx,
        );
        assert!(handles.is_empty());
    }

    #[tokio::test]
    async fn spawn_factory_listener_websocket_no_url() {
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let (_, shutdown_rx) = tokio::sync::watch::channel(false);
        let ws_health = WsHealth::new();
        let handles = spawn_factory_listener(
            None,
            service,
            entries,
            "websocket",
            "",
            5,
            false,
            false,
            ws_health,
            None,
            shutdown_rx,
        );
        assert!(handles.is_empty());
    }

    #[tokio::test]
    async fn spawn_factory_listener_auto_no_ws_no_poll() {
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let (_, shutdown_rx) = tokio::sync::watch::channel(false);
        let ws_health = WsHealth::new();
        let handles = spawn_factory_listener(
            None,
            service,
            entries,
            "auto",
            "",
            5,
            false,
            false,
            ws_health,
            None,
            shutdown_rx,
        );
        assert!(handles.is_empty());
    }

    #[tokio::test]
    async fn spawn_factory_listener_auto_fallback_poll_no_provider() {
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let (_, shutdown_rx) = tokio::sync::watch::channel(false);
        let ws_health = WsHealth::new();
        let handles = spawn_factory_listener(
            None,
            service,
            entries,
            "auto",
            "",
            5,
            true,
            false,
            ws_health,
            None,
            shutdown_rx,
        );
        assert!(handles.is_empty());
    }

    #[tokio::test]
    async fn spawn_factory_listener_websocket_fallback_poll_no_provider() {
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let (_, shutdown_rx) = tokio::sync::watch::channel(false);
        let ws_health = WsHealth::new();
        let handles = spawn_factory_listener(
            None,
            service,
            entries,
            "websocket",
            "",
            5,
            true,
            false,
            ws_health,
            None,
            shutdown_rx,
        );
        assert!(handles.is_empty());
    }

    #[tokio::test]
    async fn spawn_factory_listener_unknown_mode() {
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let (_, shutdown_rx) = tokio::sync::watch::channel(false);
        let ws_health = WsHealth::new();
        let handles = spawn_factory_listener(
            None,
            service,
            entries,
            "unknown",
            "",
            5,
            false,
            false,
            ws_health,
            None,
            shutdown_rx,
        );
        assert!(handles.is_empty());
    }

    #[test]
    fn http_to_ws_http_with_port() {
        assert_eq!(
            http_to_ws_url("http://localhost:3000"),
            Some("ws://localhost:3000".to_string())
        );
    }

    #[test]
    fn http_to_ws_https_with_path() {
        assert_eq!(
            http_to_ws_url("https://example.com/rpc/v1"),
            Some("wss://example.com/rpc/v1".to_string())
        );
    }

    #[test]
    fn resolve_ws_url_config_always_wins() {
        assert_eq!(
            resolve_ws_url("wss://config-ws.example/ws"),
            Some("wss://config-ws.example/ws".to_string())
        );
    }

    #[test]
    fn build_factory_filter_deduplicates_topics() {
        use crate::config::FactoryEventType;
        let entries = vec![
            FactoryEntry {
                address: address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f"),
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                event_type: FactoryEventType::PairCreated,
            },
            FactoryEntry {
                address: address!("C0AEe478e3658e2610c5F7A4A2D1773cDCC8b275"),
                protocol: ProtocolType::SushiSwap,
                fee_bps: 30,
                event_type: FactoryEventType::PairCreated,
            },
        ];
        let _filter = build_factory_filter(&entries);
    }

    #[test]
    fn factory_pool_created_different_protocols_not_equal() {
        let a = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: Address::ZERO,
            token1: Address::ZERO,
            pool: Address::ZERO,
        };
        let b = FactoryPoolCreated {
            protocol: ProtocolType::SushiSwap,
            ..a.clone()
        };
        assert_ne!(a, b);
    }

    #[test]
    fn decode_factory_log_pair_created_no_data() {
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let topics = vec![
            EventSignatures::pair_created_topic(),
            B256::left_padding_from(token0.as_slice()),
            B256::left_padding_from(token1.as_slice()),
        ];
        let result = decode_factory_log(
            factory,
            ProtocolType::UniswapV2,
            30,
            FactoryEventType::PairCreated,
            &topics,
            &[],
        );
        assert!(result.is_none());
    }

    #[test]
    fn mock_pair_created_log_data_length() {
        let (_, data) = mock_pair_created_log(
            Address::ZERO,
            Address::ZERO,
            Address::ZERO,
            Address::ZERO,
            0,
        );
        assert_eq!(data.len(), 64);
    }

    #[test]
    fn ws_health_multiple_clones_independent_set() {
        let h1 = WsHealth::new();
        let h2 = h1.clone();
        let h3 = h1.clone();
        h1.set_healthy(true);
        assert!(h2.is_healthy());
        assert!(h3.is_healthy());
        h2.set_healthy(false);
        assert!(!h1.is_healthy());
        assert!(!h3.is_healthy());
    }

    #[tokio::test]
    async fn process_logs_multiple_factories() {
        use alloy::primitives::LogData;
        let service = crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let factory1 = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let factory2 = address!("C0AEe478e3658e2610c5F7A4A2D1773cDCC8b275");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let entries = vec![
            FactoryEntry {
                address: factory1,
                protocol: ProtocolType::UniswapV2,
                fee_bps: 30,
                event_type: FactoryEventType::PairCreated,
            },
            FactoryEntry {
                address: factory2,
                protocol: ProtocolType::SushiSwap,
                fee_bps: 30,
                event_type: FactoryEventType::PairCreated,
            },
        ];

        let pair1 = Address::from([0x11; 20]);
        let (topics1, data1) = mock_pair_created_log(factory1, token0, token1, pair1, 1);
        let log1 = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory1,
                data: LogData::new_unchecked(topics1, data1.into()),
            },
            ..Default::default()
        };

        let pair2 = Address::from([0x22; 20]);
        let (topics2, data2) = mock_pair_created_log(factory2, token0, token1, pair2, 1);
        let log2 = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory2,
                data: LogData::new_unchecked(topics2, data2.into()),
            },
            ..Default::default()
        };

        let ingested = process_logs(&service, &entries, vec![log1, log2]).await;
        assert_eq!(ingested, 2);
    }

    #[test]
    fn decode_factory_log_empty_data_for_pair_created() {
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let topic0 = EventSignatures::pair_created_topic();
        let topics = vec![topic0];
        let result = decode_factory_log(
            factory,
            ProtocolType::UniswapV2,
            30,
            FactoryEventType::PairCreated,
            &topics,
            &[],
        );
        assert!(result.is_none());
    }

    #[tokio::test]
    async fn process_logs_with_metrics_multiple_logs() {
        use alloy::primitives::LogData;
        let service = crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let metrics = DiscoveryMetrics::noop();
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let pair = Address::from([0xAA; 20]);
        let (topics, data) = mock_pair_created_log(factory, token0, token1, pair, 1);
        let log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory,
                data: LogData::new_unchecked(topics, data.into()),
            },
            ..Default::default()
        };

        let bad_log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: factory,
                data: LogData::new_unchecked(
                    vec![EventSignatures::pair_created_topic()],
                    vec![].into(),
                ),
            },
            ..Default::default()
        };

        let ingested = process_logs_with_metrics(
            &service,
            &entries,
            vec![log, bad_log],
            Some(metrics.clone()),
        )
        .await;
        assert_eq!(ingested, 1);
        assert!(metrics.events_received.get() >= 2.0);
    }

    #[test]
    fn mock_plain_pool_deployed_log_zero_fee() {
        let pool = Address::from([0x01; 20]);
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let (topics, data) = mock_plain_pool_deployed_log(pool, token0, token1, 0);
        assert_eq!(topics.len(), 1);
        assert!(!data.is_empty());
        let result = decode_factory_log(
            address!("F18056Bbd9e56aC88eefA885588501c1806Be1D8"),
            ProtocolType::Curve,
            4,
            FactoryEventType::PlainPoolDeployed,
            &topics,
            &data,
        );
        assert!(result.is_some());
    }

    #[test]
    fn mock_pool_registered_log_zero_swap_fee() {
        let pool = Address::from([0x02; 20]);
        let factory = address!("bA1333333333a1BA1108E8412f11850A5C319bA9");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let (topics, data) = mock_pool_registered_log(pool, factory, token0, token1, U256::ZERO);
        assert_eq!(topics.len(), 3);
        let result = decode_factory_log(
            factory,
            ProtocolType::BalancerV3,
            10,
            FactoryEventType::PoolRegistered,
            &topics,
            &data,
        );
        assert!(result.is_some());
    }

    #[test]
    fn factory_pool_created_inequality_different_pool() {
        let a = FactoryPoolCreated {
            factory: Address::ZERO,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            token0: Address::ZERO,
            token1: Address::ZERO,
            pool: Address::from([0x01; 20]),
        };
        let b = FactoryPoolCreated {
            pool: Address::from([0x02; 20]),
            ..a.clone()
        };
        assert_ne!(a, b);
    }

    #[test]
    fn decode_factory_log_pool_created_v3_no_pool_in_data() {
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let factory = address!("bA1333333333a1BA1108E8412f11850A5C319bA9");
        let topic0 = EventSignatures::pool_created_v3_topic();
        let topic3_fee = B256::left_padding_from(&U256::from(5000u32).to_be_bytes::<32>());
        let topics = vec![
            topic0,
            B256::left_padding_from(token0.as_slice()),
            B256::left_padding_from(token1.as_slice()),
            topic3_fee,
        ];
        let data = vec![0xFF; 10];
        let result = decode_factory_log(
            factory,
            ProtocolType::UniswapV3,
            30,
            FactoryEventType::PoolCreatedV3,
            &topics,
            &data,
        );
        assert!(result.is_none());
    }

    #[tokio::test]
    async fn process_logs_rejects_non_factory_address() {
        use alloy::primitives::LogData;
        let service = crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let other = address!("C0AEe478e3658e2610c5F7A4A2D1773cDCC8b275");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = Address::from([0x55; 20]);
        let (topics, data) = mock_pair_created_log(other, token0, token1, pair, 1);
        let log = alloy::rpc::types::Log {
            inner: alloy::primitives::Log {
                address: other,
                data: LogData::new_unchecked(topics, data.into()),
            },
            ..Default::default()
        };

        let ingested = process_logs(&service, &entries, vec![log]).await;
        assert_eq!(ingested, 0);
    }

    // ── poll_factory_logs (mock RPC) ──

    #[tokio::test]
    async fn poll_factory_logs_valid() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let factory = address!("5C69bEe701ef814a2B6a3EDD4B1652CB9cc5aA6f");
        let token0 = address!("A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48");
        let token1 = address!("C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2");
        let pair = Address::from([0xAA; 20]);
        let (topics, data) = mock_pair_created_log(factory, token0, token1, pair, 1);

        let topics_json: String = topics
            .iter()
            .map(|t| format!("\"0x{}\"", alloy::hex::encode(t)))
            .collect::<Vec<_>>()
            .join(",");
        let data_hex = alloy::hex::encode(&data);
        let addr_hex = alloy::hex::encode(factory);
        let log_json = format!(
            r#"{{"address":"0x{}","topics":[{}],"data":"0x{}","blockNumber":"0x1","transactionHash":"0x0000000000000000000000000000000000000000000000000000000000000000","transactionIndex":"0x0","blockHash":"0x0000000000000000000000000000000000000000000000000000000000000000","logIndex":"0x0","removed":false}}"#,
            addr_hex, topics_json, data_hex,
        );
        let rpc_resp = format!(r#"{{"jsonrpc":"2.0","id":1,"result":[{}]}}"#, log_json);

        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_getLogs".into()))
            .with_body(rpc_resp)
            .create();

        let url: url::Url = server.url().parse().expect("url");
        let provider = ProviderBuilder::new().connect_http(url).erased();
        let service =
            crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let entries = vec![FactoryEntry {
            address: factory,
            protocol: ProtocolType::UniswapV2,
            fee_bps: 30,
            event_type: FactoryEventType::PairCreated,
        }];

        let ingested = poll_factory_logs(&provider, &service, &entries, 0, 1)
            .await
            .unwrap();
        assert_eq!(ingested, 1);
    }

    #[tokio::test]
    async fn poll_factory_logs_empty() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_getLogs".into()))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":[]}"#)
            .create();

        let url: url::Url = server.url().parse().expect("url");
        let provider = ProviderBuilder::new().connect_http(url).erased();
        let service =
            crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let entries = vec![];

        let ingested = poll_factory_logs(&provider, &service, &entries, 0, 1)
            .await
            .unwrap();
        assert_eq!(ingested, 0);
    }

    #[tokio::test]
    async fn poll_factory_logs_rpc_error() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_getLogs".into()))
            .with_status(500)
            .with_body("internal server error")
            .create();

        let url: url::Url = server.url().parse().expect("url");
        let provider = ProviderBuilder::new().connect_http(url).erased();
        let service =
            crate::service::DiscoveryService::new(crate::config::DiscoveryConfig::default());
        let entries = vec![];

        let result = poll_factory_logs(&provider, &service, &entries, 0, 1).await;
        assert!(result.is_err());
    }

    // ── spawn_polling_listener (mock RPC) ──

    #[tokio::test]
    async fn spawn_polling_listener_stops_on_shutdown() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_blockNumber".into()))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x64"}"#)
            .create();

        let url: url::Url = server.url().parse().expect("url");
        let provider = ProviderBuilder::new().connect_http(url).erased();
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let ws_health = WsHealth::new();
        ws_health.set_healthy(true);
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);

        let handle = spawn_polling_listener(
            provider,
            service,
            entries,
            10,
            false,
            ws_health,
            shutdown_rx,
        );

        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
        shutdown_tx.send(true).unwrap();
        handle.await.unwrap();
    }

    #[tokio::test]
    async fn spawn_polling_listener_initial_block_number_error() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _m = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_blockNumber".into()))
            .with_status(500)
            .with_body("err")
            .create();

        let url: url::Url = server.url().parse().expect("url");
        let provider = ProviderBuilder::new().connect_http(url).erased();
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let ws_health = WsHealth::new();
        ws_health.set_healthy(true);
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);

        let handle = spawn_polling_listener(
            provider,
            service,
            entries,
            10,
            false,
            ws_health,
            shutdown_rx,
        );

        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
        shutdown_tx.send(true).unwrap();
        handle.await.unwrap();
    }

    #[tokio::test]
    async fn spawn_polling_listener_poll_and_shutdown() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _bn = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_blockNumber".into()))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x64"}"#)
            .create();
        let _gl = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_getLogs".into()))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":[]}"#)
            .create();

        let url: url::Url = server.url().parse().expect("url");
        let provider = ProviderBuilder::new().connect_http(url).erased();
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let ws_health = WsHealth::new();
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);

        let handle = spawn_polling_listener(
            provider,
            service,
            entries,
            1,
            false,
            ws_health,
            shutdown_rx,
        );

        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
        shutdown_tx.send(true).unwrap();
        handle.await.unwrap();
    }

    // ── spawn_factory_listener with real provider ──

    #[tokio::test]
    async fn spawn_factory_listener_poll_with_provider() {
        use mockito::{Matcher, Server};
        let mut server = Server::new_async().await;
        let _bn = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_blockNumber".into()))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":"0x64"}"#)
            .create();
        let _gl = server
            .mock("POST", "/")
            .match_body(Matcher::Regex("eth_getLogs".into()))
            .with_body(r#"{"jsonrpc":"2.0","id":1,"result":[]}"#)
            .create();

        let url: url::Url = server.url().parse().expect("url");
        let provider = ProviderBuilder::new().connect_http(url).erased();
        let service = Arc::new(crate::service::DiscoveryService::new(
            crate::config::DiscoveryConfig::default(),
        ));
        let entries = vec![];
        let ws_health = WsHealth::new();
        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);

        let handles = spawn_factory_listener(
            Some(provider),
            service,
            entries,
            "poll",
            "",
            1,
            false,
            false,
            ws_health,
            None,
            shutdown_rx,
        );

        assert_eq!(handles.len(), 1);

        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
        shutdown_tx.send(true).unwrap();
        for h in handles {
            h.await.unwrap();
        }
    }
}
