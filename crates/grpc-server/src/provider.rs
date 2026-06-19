use std::sync::Arc;
use std::time::Duration;
use tracing::{debug, error, info, trace, warn};

use alloy::primitives::{Address, B256};
use alloy::providers::{Provider, ProviderBuilder};
use alloy::rpc::types::Filter;
use futures::StreamExt;
use tokio::sync::RwLock;

use aether_common::types::NodeState;
use aether_ingestion::config::load_nodes_config;
use aether_ingestion::event_decoder;
use aether_ingestion::event_decoder::EventSignatures;
use aether_ingestion::node_pool::{NodeConfig, NodeConnection, NodePool, NodeType};
use aether_ingestion::subscription::{EventChannels, NewBlockEvent};

use crate::metrics::EngineMetrics;

/// Configuration for the RPC provider connection
#[derive(Debug, Clone)]
pub struct ProviderConfig {
    /// RPC endpoint URL (WS preferred, HTTP fallback)
    pub rpc_url: String,
    /// Optional path to nodes.yaml for multi-node pool configuration
    pub nodes_config_path: Option<String>,
    /// Pool addresses to monitor for events (empty = all)
    pub monitored_pools: Vec<Address>,
    /// Reconnect delay base (exponential backoff)
    pub reconnect_delay: Duration,
    /// Maximum reconnect attempts before giving up
    pub max_reconnect_attempts: u32,
    /// Health check interval
    pub health_check_interval: Duration,
}

impl Default for ProviderConfig {
    fn default() -> Self {
        Self {
            rpc_url: std::env::var("ETH_WS_URL")
                .or_else(|_| std::env::var("ETH_RPC_URL"))
                .unwrap_or_else(|_| "http://localhost:8545".to_string()),
            nodes_config_path: std::env::var("AETHER_NODES_CONFIG").ok(),
            monitored_pools: vec![],
            reconnect_delay: Duration::from_secs(1),
            max_reconnect_attempts: 10,
            health_check_interval: Duration::from_secs(30),
        }
    }
}

/// Infer the node transport type from the URL scheme.
fn infer_node_type(url: &str) -> NodeType {
    if url.starts_with("ws://") || url.starts_with("wss://") {
        NodeType::WebSocket
    } else if url.starts_with('/') || url.ends_with(".ipc") {
        NodeType::Ipc
    } else {
        NodeType::Http
    }
}

/// Pick the HTTP-fallback polling interval for a given RPC endpoint.
///
/// Mainnet RPC providers are 12 s/block, so polling once per second gives an
/// OK trade-off between latency and RPC cost. But the e2e replay setup runs
/// against a local Anvil fork that processes an entire historical block in
/// hundreds of milliseconds, and the 1 s cadence collapses all intra-block
/// state transitions into a single batched scrape — the detector never sees
/// per-tx state, so short-lived arb windows (post-exploit AAVE/WETH drain,
/// etc.) are missed by the pipeline even though the underlying txs replay
/// correctly.
///
/// Resolution order:
/// 1. `AETHER_HTTP_POLL_MS` env var (if set and parses as u64) — explicit
///    override for tests.
/// 2. Local endpoint heuristic: 127.0.0.1, localhost, or host.docker.internal
///    in the URL → 100 ms.
/// 3. Default: 1 000 ms (pre-existing behaviour for mainnet HTTP fallback).
fn resolve_http_poll_interval(url: &str) -> Duration {
    if let Ok(raw) = std::env::var("AETHER_HTTP_POLL_MS") {
        if let Ok(ms) = raw.trim().parse::<u64>() {
            return Duration::from_millis(ms);
        }
    }
    if is_local_rpc(url) {
        return Duration::from_millis(100);
    }
    Duration::from_secs(1)
}

fn is_local_rpc(url: &str) -> bool {
    let lower = url.to_ascii_lowercase();
    lower.contains("127.0.0.1")
        || lower.contains("localhost")
        || lower.contains("host.docker.internal")
        || lower.contains("[::1]")
}

/// RPC provider that bridges Ethereum events to the ingestion EventChannels.
///
/// Supports WebSocket (native subscriptions), IPC (native subscriptions),
/// and HTTP (polling fallback) transports. When configured with a
/// `nodes_config_path`, manages a pool of nodes with automatic failover.
pub struct RpcProvider {
    config: ProviderConfig,
    event_channels: Arc<EventChannels>,
    node_pool: NodePool,
    metrics: Arc<EngineMetrics>,
}

impl RpcProvider {
    /// Create a new `RpcProvider`.
    ///
    /// `metrics` is required at construction so the decode-failure counter
    /// is always wired up — forgetting to attach it would ship a dead
    /// counter that pegs at zero forever (indistinguishable from "no
    /// decode failures are happening") rather than surfacing as a missing
    /// time series that alerts can match on.
    ///
    /// If `config.nodes_config_path` is set, loads the multi-node pool
    /// from the YAML config file. Otherwise, creates a single-node pool
    /// from `config.rpc_url` with the transport type inferred from the
    /// URL scheme.
    pub fn new(
        config: ProviderConfig,
        event_channels: Arc<EventChannels>,
        metrics: Arc<EngineMetrics>,
    ) -> Self {
        let node_pool = match &config.nodes_config_path {
            Some(path) => match load_nodes_config(path) {
                Ok((configs, min_healthy)) => {
                    info!(
                        path = %path,
                        nodes = configs.len(),
                        min_healthy,
                        "Loaded node pool from config"
                    );
                    NodePool::new(configs, min_healthy)
                }
                Err(e) => {
                    warn!(
                        path = %path,
                        error = %e,
                        "Failed to load nodes config, falling back to rpc_url"
                    );
                    Self::single_node_pool(&config.rpc_url)
                }
            },
            None => Self::single_node_pool(&config.rpc_url),
        };

        Self {
            config,
            event_channels,
            node_pool,
            metrics,
        }
    }

    /// Build a single-node `NodePool` from a URL, inferring the transport type.
    fn single_node_pool(url: &str) -> NodePool {
        let node_type = infer_node_type(url);
        let node_config = NodeConfig {
            name: "default".to_string(),
            url: url.to_string(),
            node_type,
            priority: 0,
            max_retries: 5,
            health_check_interval: Duration::from_secs(30),
        };
        NodePool::new(vec![node_config], 1)
    }

    /// Main provider loop with automatic failover across the node pool.
    ///
    /// Selects the best available node, connects using the appropriate
    /// transport, and runs the event loop. On failure, marks the node as
    /// degraded/failed and retries with the next best node.
    pub async fn run(&self, mut shutdown: tokio::sync::watch::Receiver<bool>) {
        info!("RpcProvider starting");

        let mut attempt = 0u32;

        loop {
            if *shutdown.borrow() {
                info!("RpcProvider shutting down before connection attempt");
                break;
            }

            match self.node_pool.best_node().await {
                Some(node) => {
                    let (node_type, node_url) = {
                        let n = node.read().await;
                        (n.config.node_type.clone(), n.config.url.clone())
                    };

                    info!(url = %node_url, transport = ?node_type, "Connecting to node");

                    let result = match node_type {
                        NodeType::WebSocket => {
                            self.connect_ws(&node_url, &node, &mut shutdown).await
                        }
                        NodeType::Ipc => {
                            self.connect_ipc(&node_url, &node, &mut shutdown).await
                        }
                        NodeType::Http => {
                            self.connect_http(&node_url, &node, &mut shutdown).await
                        }
                    };

                    match result {
                        Ok(()) => break, // Graceful shutdown
                        Err(e) => {
                            node.write().await.record_failure();
                            attempt += 1;
                            let delay = self.node_pool.backoff_delay(attempt);
                            warn!(
                                attempt,
                                delay_ms = delay.as_millis() as u64,
                                error = %e,
                                "Connection failed, reconnecting"
                            );
                            tokio::select! {
                                _ = tokio::time::sleep(delay) => {}
                                Ok(()) = shutdown.changed() => {
                                    if *shutdown.borrow() { break; }
                                }
                            }
                        }
                    }
                }
                None => {
                    // All nodes unhealthy
                    attempt += 1;
                    if attempt >= self.config.max_reconnect_attempts {
                        error!("All nodes failed, max reconnect attempts reached");
                        break;
                    }

                    let delay = self.node_pool.backoff_delay(attempt);
                    warn!(attempt, "All nodes unhealthy, waiting before retry");

                    // Reset all nodes to Connected so they can be retried
                    for node in self.node_pool.all_nodes() {
                        let mut n = node.write().await;
                        n.consecutive_failures = 0;
                        n.transition(NodeState::Connected);
                    }

                    tokio::select! {
                        _ = tokio::time::sleep(delay) => {}
                        Ok(()) = shutdown.changed() => {
                            if *shutdown.borrow() { break; }
                        }
                    }
                }
            }
        }

        info!("RpcProvider exited");
    }

    /// Connect via WebSocket and run native subscriptions.
    async fn connect_ws(
        &self,
        url: &str,
        node: &Arc<RwLock<NodeConnection>>,
        shutdown: &mut tokio::sync::watch::Receiver<bool>,
    ) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
        let ws_connect = alloy::providers::WsConnect::new(url);
        let provider = ProviderBuilder::new().connect_ws(ws_connect).await?;

        let initial_block = provider.get_block_number().await?;
        info!(block = initial_block, "WebSocket provider connected");
        node.write().await.record_success(0, initial_block);

        self.run_subscription_loop(provider, node, shutdown).await
    }

    /// Connect via IPC (Unix domain socket) and run native subscriptions.
    async fn connect_ipc(
        &self,
        path: &str,
        node: &Arc<RwLock<NodeConnection>>,
        shutdown: &mut tokio::sync::watch::Receiver<bool>,
    ) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
        let ipc_connect = alloy::providers::IpcConnect::new(path.to_string());
        let provider = ProviderBuilder::new().connect_ipc(ipc_connect).await?;

        let initial_block = provider.get_block_number().await?;
        info!(block = initial_block, "IPC provider connected");
        node.write().await.record_success(0, initial_block);

        self.run_subscription_loop(provider, node, shutdown).await
    }

    /// Shared subscription loop for push-based transports (WS and IPC).
    ///
    /// Subscribes to `newHeads` and DEX event logs, dispatching events
    /// through `EventChannels` as they arrive.
    async fn run_subscription_loop<P>(
        &self,
        provider: P,
        node: &Arc<RwLock<NodeConnection>>,
        shutdown: &mut tokio::sync::watch::Receiver<bool>,
    ) -> Result<(), Box<dyn std::error::Error + Send + Sync>>
    where
        P: Provider + Clone + 'static,
    {
        let block_sub = provider.subscribe_blocks().await?;
        let mut block_stream = block_sub.into_stream();

        // When monitored_pools is non-empty, scope the filter to those addresses only.
        // When empty, receive events from all contracts (pool discovery mode).
        let mut log_filter = Filter::new().event_signature(self.event_topics());
        if !self.config.monitored_pools.is_empty() {
            log_filter = log_filter.address(self.config.monitored_pools.clone());
        }
        let log_sub = provider.subscribe_logs(&log_filter).await?;
        let mut log_stream = log_sub.into_stream();

        info!("Subscriptions active (newHeads + logs)");

        loop {
            tokio::select! {
                block_opt = block_stream.next() => {
                    match block_opt {
                        Some(block) => {
                            let number = block.inner.number;
                            let timestamp = block.inner.timestamp;
                            let base_fee = block.inner.base_fee_per_gas.unwrap_or(0) as u128;
                            let gas_limit = block.inner.gas_limit;
                            debug!(block = number, "Block received via subscription");
                            // newHeads only carries header fields; fetch the
                            // block (hashes-only — Full would inflate the
                            // payload by 100×+) so the mempool first-seen
                            // tracker has a tx list to diff against. A
                            // failed fetch dispatches with an empty list —
                            // the inclusion-latency histogram quietly skips
                            // that block rather than crashing the loop.
                            let tx_hashes = match provider
                                .get_block_by_number(alloy::eips::BlockNumberOrTag::Number(number))
                                .await
                            {
                                Ok(Some(b)) => {
                                    std::sync::Arc::new(b.transactions.hashes().collect())
                                }
                                _ => std::sync::Arc::new(Vec::new()),
                            };
                            self.dispatch_block(number, timestamp, base_fee, gas_limit, tx_hashes);
                            node.write().await.record_success(0, number);
                        }
                        None => {
                            return Err("Block subscription stream ended".into());
                        }
                    }
                }
                log_opt = log_stream.next() => {
                    match log_opt {
                        Some(log) => {
                            self.process_single_log(&log);
                        }
                        None => {
                            return Err("Log subscription stream ended".into());
                        }
                    }
                }
                Ok(()) = shutdown.changed() => {
                    if *shutdown.borrow() {
                        return Ok(());
                    }
                }
            }
        }
    }

    /// Connect via HTTP and run the polling-based event loop.
    ///
    /// HTTP does not support native subscriptions, so this falls back to
    /// polling `eth_getBlockByNumber` and `eth_getLogs`. Polling cadence is
    /// adaptive:
    ///
    /// * Local endpoints (127.0.0.1 / localhost / host.docker.internal) —
    ///   the e2e replay setup, where Anvil processes an entire mainnet block
    ///   of txs in hundreds of milliseconds — poll every 100 ms. Real
    ///   mainnet blocks are 12 s so this faster cadence is wasted there, but
    ///   on a fork it recovers per-tx detection precision without adding a
    ///   separate ingestion path. Also overridable via the
    ///   `AETHER_HTTP_POLL_MS` env var.
    /// * Everything else polls once per second, matching pre-existing
    ///   mainnet-HTTP fallback behaviour.
    async fn connect_http(
        &self,
        url: &str,
        node: &Arc<RwLock<NodeConnection>>,
        shutdown: &mut tokio::sync::watch::Receiver<bool>,
    ) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
        let poll_interval = resolve_http_poll_interval(url);
        warn!(
            poll_ms = poll_interval.as_millis(),
            "HTTP transport detected -- falling back to polling mode"
        );

        let parsed_url: url::Url = url.parse()?;
        let provider = ProviderBuilder::new().connect_http(parsed_url);

        let initial_block = provider.get_block_number().await?;
        info!(block = initial_block, "HTTP provider connected (polling mode)");
        node.write().await.record_success(0, initial_block);
        let mut last_block = initial_block;
        let event_topics = self.event_topics();

        loop {
            tokio::select! {
                _ = tokio::time::sleep(poll_interval) => {
                    let current_block = match provider.get_block_number().await {
                        Ok(n) => n,
                        Err(e) => {
                            warn!(error = %e, "Failed to get block number");
                            continue;
                        }
                    };

                    if current_block <= last_block {
                        continue;
                    }

                    debug!(block = current_block, prev = last_block, "New block detected");

                    let block_opt = match provider.get_block_by_number(
                        alloy::eips::BlockNumberOrTag::Number(current_block),
                    ).await {
                        Ok(b) => b,
                        Err(e) => {
                            warn!(error = %e, block = current_block, "Failed to get block");
                            continue;
                        }
                    };

                    if let Some(block) = block_opt {
                        let timestamp = block.header.timestamp;
                        let base_fee = block.header.base_fee_per_gas.unwrap_or(0) as u128;
                        let gas_limit = block.header.gas_limit;
                        // Tx hashes already in the polled block payload —
                        // free for the mempool first-seen tracker.
                        let tx_hashes =
                            std::sync::Arc::new(block.transactions.hashes().collect());

                        self.dispatch_block(
                            current_block, timestamp, base_fee, gas_limit, tx_hashes,
                        );
                        node.write().await.record_success(0, current_block);

                        // Fetch logs for all blocks since last_block to avoid
                        // dropping events when multiple blocks arrive between polls.
                        // When monitored_pools is non-empty, scope to those addresses only.
                        let mut filter = Filter::new()
                            .from_block(last_block + 1)
                            .to_block(current_block)
                            .event_signature(event_topics.clone());
                        if !self.config.monitored_pools.is_empty() {
                            filter = filter.address(self.config.monitored_pools.clone());
                        }

                        match provider.get_logs(&filter).await {
                            Ok(logs) => {
                                if !logs.is_empty() {
                                    debug!(
                                        count = logs.len(),
                                        block = current_block,
                                        "Processing DEX event logs"
                                    );
                                    let decoded_logs: Vec<(Address, Vec<B256>, Vec<u8>)> = logs
                                        .iter()
                                        .map(|log| {
                                            (
                                                log.address(),
                                                log.topics().to_vec(),
                                                log.data().data.to_vec(),
                                            )
                                        })
                                        .collect();
                                    self.process_logs(&decoded_logs);
                                }
                            }
                            Err(e) => {
                                warn!(
                                    error = %e,
                                    block = current_block,
                                    "Failed to get logs"
                                );
                            }
                        }
                    }

                    last_block = current_block;
                }
                Ok(()) = shutdown.changed() => {
                    if *shutdown.borrow() {
                        return Ok(());
                    }
                }
            }
        }
    }

    /// Known DEX event topic signatures for log filtering.
    fn event_topics(&self) -> Vec<B256> {
        vec![
            EventSignatures::sync_topic(),
            EventSignatures::swap_v2_topic(),
            EventSignatures::swap_v3_topic(),
            EventSignatures::token_exchange_topic(),
            EventSignatures::pair_created_topic(),
        ]
    }

    /// Decode and dispatch a single log received from a subscription stream.
    /// Borrows directly from the log to avoid heap allocations on the hot path.
    fn process_single_log(&self, log: &alloy::rpc::types::Log) {
        let address = log.address();
        let topics = log.topics();
        let data = &log.data().data;
        match event_decoder::decode_log(topics, data, address, None) {
            Ok(event) => self.event_channels.dispatch_pool_update(event),
            Err(reason) => self.record_decode_failure(address, topics, reason),
        }
    }

    /// Dispatch a new block event to the event channels.
    ///
    /// `tx_hashes` should hold the block's confirmed tx hash list when
    /// available — the mempool first-seen tracker uses it to compute
    /// inclusion latency. Pass `Arc::new(Vec::new())` when the source
    /// can't provide the list (e.g. some WS notifications).
    pub fn dispatch_block(
        &self,
        number: u64,
        timestamp: u64,
        base_fee: u128,
        gas_limit: u64,
        tx_hashes: std::sync::Arc<Vec<B256>>,
    ) {
        self.event_channels.dispatch_new_block(NewBlockEvent {
            block_number: number,
            timestamp,
            base_fee,
            gas_limit,
            tx_hashes,
        });
    }

    /// Process raw logs from a block and dispatch decoded pool events.
    pub fn process_logs(&self, logs: &[(Address, Vec<B256>, Vec<u8>)]) {
        for (address, topics, data) in logs {
            match event_decoder::decode_log(topics, data, *address, None) {
                Ok(event) => self.event_channels.dispatch_pool_update(event),
                Err(reason) => self.record_decode_failure(*address, topics, reason),
            }
        }
    }

    /// Surface a decoder drop to operators. Bumps
    /// `aether_decode_errors_total{reason="..."}` (the primary ops signal —
    /// a labelled counter wired to alerting) and emits a `trace!` with the
    /// offending pool address, first topic, and reason for triage.
    ///
    /// The per-event log is deliberately `trace!`, not `warn!`: in discovery
    /// mode (`monitored_pools = []`) every unmatched log on mainnet — tens
    /// of thousands per block — lands here as `unknown_topic`, and a `warn!`
    /// would swamp Loki. Operators should watch the per-reason counter;
    /// `malformed_payload` / `insufficient_topics` spikes are the real
    /// data-integrity signals worth paging on.
    ///
    /// Called from the hot path, so it must be cheap — the counter is a
    /// single atomic increment and `trace!` is compiled to a tiny level
    /// check at the disabled level.
    fn record_decode_failure(
        &self,
        address: Address,
        topics: &[B256],
        reason: event_decoder::DecodeReason,
    ) {
        self.metrics.inc_decode_errors(reason.as_str());
        let topic0 = topics.first().copied().unwrap_or_default();
        trace!(
            pool = %address,
            %topic0,
            reason = reason.as_str(),
            "Event decoder drop"
        );
    }

    /// Get the configured RPC URL.
    #[allow(dead_code)]
    pub fn rpc_url(&self) -> &str {
        &self.config.rpc_url
    }

    /// Check if the provider is configured (has a non-empty URL).
    #[allow(dead_code)]
    pub fn is_configured(&self) -> bool {
        !self.config.rpc_url.is_empty()
    }

    /// Get a reference to the underlying node pool.
    #[allow(dead_code)]
    pub fn node_pool(&self) -> &NodePool {
        &self.node_pool
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy::primitives::U256;
    use std::sync::Mutex;

    static ENV_LOCK: Mutex<()> = Mutex::new(());

    fn test_metrics() -> Arc<EngineMetrics> {
        Arc::new(EngineMetrics::new())
    }

    #[test]
    fn test_provider_config_default() {
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        assert_eq!(config.rpc_url, "http://localhost:8545");
        assert!(config.monitored_pools.is_empty());
        assert_eq!(config.reconnect_delay, Duration::from_secs(1));
        assert_eq!(config.max_reconnect_attempts, 10);
    }

    #[test]
    fn test_is_local_rpc() {
        assert!(is_local_rpc("http://127.0.0.1:8545"));
        assert!(is_local_rpc("http://localhost:8547"));
        assert!(is_local_rpc("http://host.docker.internal:8547"));
        assert!(is_local_rpc("http://[::1]:8547"));
        assert!(is_local_rpc("HTTP://LOCALHOST:8547"));
        assert!(!is_local_rpc("https://eth-mainnet.g.alchemy.com/v2/KEY"));
        assert!(!is_local_rpc("wss://ethereum.publicnode.com"));
    }

    #[test]
    fn test_is_local_rpc_empty_string() {
        assert!(!is_local_rpc(""));
    }

    #[test]
    fn test_is_local_rpc_partial_match() {
        assert!(is_local_rpc("http://not-localhost.evil.com")); // contains "localhost" substring
        assert!(!is_local_rpc("http://127.0.0.2:8545"));
        assert!(!is_local_rpc("http://example.com:8545"));
    }

    #[test]
    fn test_resolve_http_poll_interval_defaults() {
        let _guard = ENV_LOCK.lock().unwrap();
        std::env::remove_var("AETHER_HTTP_POLL_MS");
        assert_eq!(
            resolve_http_poll_interval("http://127.0.0.1:8547"),
            Duration::from_millis(100),
        );
        assert_eq!(
            resolve_http_poll_interval("https://eth-mainnet.g.alchemy.com/v2/KEY"),
            Duration::from_secs(1),
        );
    }

    #[test]
    fn test_resolve_http_poll_interval_env_override() {
        let _guard = ENV_LOCK.lock().unwrap();
        std::env::remove_var("AETHER_HTTP_POLL_MS");
        std::env::set_var("AETHER_HTTP_POLL_MS", "250");
        assert_eq!(
            resolve_http_poll_interval("https://eth-mainnet.g.alchemy.com/v2/KEY"),
            Duration::from_millis(250),
        );
        assert_eq!(
            resolve_http_poll_interval("http://127.0.0.1:8547"),
            Duration::from_millis(250),
        );
        std::env::remove_var("AETHER_HTTP_POLL_MS");

        std::env::set_var("AETHER_HTTP_POLL_MS", "nonsense");
        assert_eq!(
            resolve_http_poll_interval("https://eth-mainnet.g.alchemy.com/v2/KEY"),
            Duration::from_secs(1),
        );
        std::env::remove_var("AETHER_HTTP_POLL_MS");
    }

    #[test]
    fn test_resolve_http_poll_interval_env_zero() {
        let _guard = ENV_LOCK.lock().unwrap();
        std::env::set_var("AETHER_HTTP_POLL_MS", "0");
        assert_eq!(
            resolve_http_poll_interval("https://example.com"),
            Duration::from_millis(0),
        );
        std::env::remove_var("AETHER_HTTP_POLL_MS");
    }

    #[test]
    fn test_resolve_http_poll_interval_env_large() {
        let _guard = ENV_LOCK.lock().unwrap();
        std::env::set_var("AETHER_HTTP_POLL_MS", "5000");
        assert_eq!(
            resolve_http_poll_interval("https://example.com"),
            Duration::from_millis(5000),
        );
        std::env::remove_var("AETHER_HTTP_POLL_MS");
    }

    #[test]
    fn test_provider_config_nodes_config_path_defaults_to_env() {
        std::env::remove_var("AETHER_NODES_CONFIG");
        let config = ProviderConfig::default();
        assert!(config.nodes_config_path.is_none());
    }

    #[test]
    fn test_provider_creation() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "ws://localhost:8546".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());
        assert_eq!(provider.rpc_url(), "ws://localhost:8546");
        assert!(provider.is_configured());
    }

    #[test]
    fn test_provider_not_configured_with_empty_url() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: String::new(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());
        assert!(!provider.is_configured());
    }

    #[test]
    fn test_dispatch_block() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_new_blocks();

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        provider.dispatch_block(
            18_000_000,
            1_700_000_000,
            30_000_000_000,
            30_000_000,
            std::sync::Arc::new(Vec::new()),
        );

        let event = rx.try_recv().expect("should receive block event");
        assert_eq!(event.block_number, 18_000_000);
        assert_eq!(event.timestamp, 1_700_000_000);
        assert_eq!(event.base_fee, 30_000_000_000);
        assert_eq!(event.gas_limit, 30_000_000);
    }

    #[test]
    fn test_dispatch_block_with_tx_hashes() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_new_blocks();

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        let hashes = vec![B256::repeat_byte(0x01), B256::repeat_byte(0x02)];
        provider.dispatch_block(1, 2, 3, 4, std::sync::Arc::new(hashes.clone()));

        let event = rx.try_recv().expect("should receive block event");
        assert_eq!(event.tx_hashes.len(), 2);
        assert_eq!(event.tx_hashes[0], B256::repeat_byte(0x01));
    }

    #[test]
    fn test_process_logs_sync_event() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_pool_updates();

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        let pool_addr = Address::repeat_byte(0xAA);
        let topics = vec![EventSignatures::sync_topic()];

        let reserve0 = U256::from(1_000_000_000_000_000_000u64);
        let reserve1 = U256::from(2_000_000_000u64);
        let mut data = Vec::new();
        data.extend_from_slice(&reserve0.to_be_bytes::<32>());
        data.extend_from_slice(&reserve1.to_be_bytes::<32>());

        provider.process_logs(&[(pool_addr, topics, data)]);

        let event = rx.try_recv().expect("should receive pool event");
        match event {
            aether_ingestion::event_decoder::PoolEvent::ReserveUpdate { pool, .. } => {
                assert_eq!(pool, pool_addr);
            }
            other => panic!("Expected ReserveUpdate, got {:?}", other),
        }
    }

    #[test]
    fn test_process_logs_unknown_event_ignored() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_pool_updates();

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        let unknown_topic = B256::repeat_byte(0xFF);
        provider.process_logs(&[(Address::ZERO, vec![unknown_topic], vec![0u8; 64])]);

        assert!(rx.try_recv().is_err());
    }

    #[test]
    fn test_process_logs_decode_failure_increments_counter() {
        let channels = Arc::new(EventChannels::new());
        let metrics = Arc::new(EngineMetrics::new());

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, Arc::clone(&metrics));

        let unknown_topic = B256::repeat_byte(0xFF);
        provider.process_logs(&[(
            Address::ZERO,
            vec![unknown_topic],
            vec![0u8; 64],
        )]);

        let rendered = String::from_utf8(metrics.render()).expect("metrics utf-8");
        assert!(
            rendered.contains(r#"aether_decode_errors_total{reason="unknown_topic"} 1"#),
            "expected unknown_topic counter at 1, got: {rendered}"
        );

        provider.process_logs(&[(
            Address::ZERO,
            vec![unknown_topic],
            vec![0u8; 64],
        )]);
        let rendered = String::from_utf8(metrics.render()).expect("metrics utf-8");
        assert!(
            rendered.contains(r#"aether_decode_errors_total{reason="unknown_topic"} 2"#),
            "expected unknown_topic counter at 2, got: {rendered}"
        );
    }

    #[test]
    fn test_process_logs_malformed_payload_reason_label() {
        let channels = Arc::new(EventChannels::new());
        let metrics = Arc::new(EngineMetrics::new());

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, Arc::clone(&metrics));

        provider.process_logs(&[(
            Address::ZERO,
            vec![EventSignatures::sync_topic()],
            vec![0u8; 32],
        )]);

        let rendered = String::from_utf8(metrics.render()).expect("metrics utf-8");
        assert!(
            rendered.contains(r#"aether_decode_errors_total{reason="malformed_payload"} 1"#),
            "expected malformed_payload counter at 1, got: {rendered}"
        );
        assert!(
            !rendered.contains(r#"aether_decode_errors_total{reason="unknown_topic"} 1"#),
        );
    }

    #[test]
    fn test_process_logs_insufficient_topics_reason_label() {
        let channels = Arc::new(EventChannels::new());
        let metrics = Arc::new(EngineMetrics::new());

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, Arc::clone(&metrics));

        provider.process_logs(&[(
            Address::ZERO,
            vec![EventSignatures::pair_created_topic(), B256::ZERO],
            vec![0u8; 64],
        )]);

        let rendered = String::from_utf8(metrics.render()).expect("metrics utf-8");
        assert!(
            rendered.contains(r#"aether_decode_errors_total{reason="insufficient_topics"} 1"#),
            "expected insufficient_topics counter at 1, got: {rendered}"
        );
    }

    #[tokio::test]
    async fn test_provider_run_with_shutdown() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            max_reconnect_attempts: 5,
            reconnect_delay: Duration::from_millis(200),
            ..ProviderConfig::default()
        };
        let provider = Arc::new(RpcProvider::new(config, channels, test_metrics()));

        let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);

        let provider_clone = Arc::clone(&provider);
        let handle = tokio::spawn(async move {
            provider_clone.run(shutdown_rx).await;
        });

        tokio::time::sleep(Duration::from_millis(50)).await;
        let _ = shutdown_tx.send(true);

        tokio::time::timeout(Duration::from_secs(5), handle)
            .await
            .expect("provider should shut down within timeout")
            .expect("provider task should not panic");
    }

    #[test]
    fn test_process_logs_multiple() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_pool_updates();

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        let pool1 = Address::repeat_byte(0x01);
        let pool2 = Address::repeat_byte(0x02);
        let sync_topic = EventSignatures::sync_topic();

        let mut data = Vec::new();
        let r = U256::from(1000u64);
        data.extend_from_slice(&r.to_be_bytes::<32>());
        data.extend_from_slice(&r.to_be_bytes::<32>());

        provider.process_logs(&[
            (pool1, vec![sync_topic], data.clone()),
            (pool2, vec![sync_topic], data),
        ]);

        let e1 = rx.try_recv().expect("should receive first event");
        let e2 = rx.try_recv().expect("should receive second event");

        match (e1, e2) {
            (
                aether_ingestion::event_decoder::PoolEvent::ReserveUpdate { pool: p1, .. },
                aether_ingestion::event_decoder::PoolEvent::ReserveUpdate { pool: p2, .. },
            ) => {
                assert_eq!(p1, pool1);
                assert_eq!(p2, pool2);
            }
            _ => panic!("Expected two ReserveUpdate events"),
        }
    }

    // ── Transport inference tests ──

    #[test]
    fn test_infer_node_type_websocket() {
        assert_eq!(infer_node_type("ws://localhost:8546"), NodeType::WebSocket);
        assert_eq!(infer_node_type("wss://eth-mainnet.g.alchemy.com/v2/key"), NodeType::WebSocket);
    }

    #[test]
    fn test_infer_node_type_ipc() {
        assert_eq!(infer_node_type("/tmp/reth.ipc"), NodeType::Ipc);
        assert_eq!(infer_node_type("/var/run/geth.ipc"), NodeType::Ipc);
        assert_eq!(infer_node_type("path/to/node.ipc"), NodeType::Ipc);
    }

    #[test]
    fn test_infer_node_type_http() {
        assert_eq!(infer_node_type("http://localhost:8545"), NodeType::Http);
        assert_eq!(infer_node_type("https://mainnet.infura.io/v3/key"), NodeType::Http);
    }

    #[test]
    fn test_infer_node_type_unknown_defaults_to_http() {
        assert_eq!(infer_node_type("some-random-string"), NodeType::Http);
    }

    #[test]
    fn test_infer_node_type_empty_string() {
        assert_eq!(infer_node_type(""), NodeType::Http);
    }

    // ── Node pool construction tests ──

    #[test]
    fn test_single_node_pool_from_ws_url() {
        let pool = RpcProvider::single_node_pool("ws://localhost:8546");
        assert_eq!(pool.all_nodes().len(), 1);
    }

    #[test]
    fn test_single_node_pool_from_http_url() {
        let pool = RpcProvider::single_node_pool("http://localhost:8545");
        assert_eq!(pool.all_nodes().len(), 1);
    }

    #[test]
    fn test_single_node_pool_from_ipc_path() {
        let pool = RpcProvider::single_node_pool("/tmp/reth.ipc");
        assert_eq!(pool.all_nodes().len(), 1);
    }

    #[test]
    fn test_single_node_pool_ipc_no_ext() {
        let pool = RpcProvider::single_node_pool("/var/run/node.ipc");
        assert_eq!(pool.all_nodes().len(), 1);
    }

    #[test]
    fn test_single_node_pool_ws_secure() {
        let pool = RpcProvider::single_node_pool("wss://eth-mainnet.g.alchemy.com/v2/key");
        assert_eq!(pool.all_nodes().len(), 1);
    }

    #[tokio::test]
    async fn test_provider_with_nodes_config_file() {
        use std::io::Write;

        let dir = std::env::temp_dir().join("aether_provider_test");
        std::fs::create_dir_all(&dir).expect("create temp dir");
        let path = dir.join("nodes.yaml");

        let yaml = r#"
nodes:
  - name: "ws-primary"
    url: "wss://example.com"
    type: "websocket"
    priority: 1
  - name: "ipc-local"
    url: "/tmp/reth.ipc"
    type: "ipc"
    priority: 0
  - name: "http-fallback"
    url: "http://localhost:8545"
    type: "http"
    priority: 2
min_healthy_nodes: 1
"#;
        let mut f = std::fs::File::create(&path).expect("create temp file");
        f.write_all(yaml.as_bytes()).expect("write temp file");

        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            nodes_config_path: Some(path.to_str().expect("valid path").to_string()),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());

        assert_eq!(provider.node_pool().all_nodes().len(), 3);

        let best = provider.node_pool().best_node().await.expect("should have best node");
        let best_read = best.read().await;
        assert_eq!(best_read.config.name, "ipc-local");

        std::fs::remove_file(&path).ok();
        std::fs::remove_dir(&dir).ok();
    }

    #[test]
    fn test_provider_falls_back_on_invalid_config_path() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            nodes_config_path: Some("/nonexistent/path/nodes.yaml".to_string()),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());
        assert_eq!(provider.node_pool().all_nodes().len(), 1);
    }

    #[test]
    fn test_event_topics_returns_known_signatures() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());

        let topics = provider.event_topics();
        assert_eq!(topics.len(), 5);
        assert_eq!(topics[0], EventSignatures::sync_topic());
        assert_eq!(topics[1], EventSignatures::swap_v2_topic());
        assert_eq!(topics[2], EventSignatures::swap_v3_topic());
        assert_eq!(topics[3], EventSignatures::token_exchange_topic());
        assert_eq!(topics[4], EventSignatures::pair_created_topic());
    }

    #[test]
    fn test_provider_node_pool_accessor() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());
        let pool = provider.node_pool();
        assert_eq!(pool.all_nodes().len(), 1);
    }

    #[test]
    fn test_provider_rpc_url_accessor() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "ws://remote.host:8546".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());
        assert_eq!(provider.rpc_url(), "ws://remote.host:8546");
    }

    #[test]
    fn test_provider_is_configured_various_urls() {
        let channels = Arc::new(EventChannels::new());

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels.clone(), test_metrics());
        assert!(provider.is_configured());

        let config = ProviderConfig {
            rpc_url: "".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());
        assert!(!provider.is_configured());
    }

    #[test]
    fn test_process_logs_empty_logs() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_pool_updates();

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        provider.process_logs(&[]);
        assert!(rx.try_recv().is_err());
    }

    #[test]
    fn test_dispatch_block_zero_values() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_new_blocks();

        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        provider.dispatch_block(0, 0, 0, 0, std::sync::Arc::new(Vec::new()));
        let event = rx.try_recv().expect("should receive block event");
        assert_eq!(event.block_number, 0);
        assert_eq!(event.timestamp, 0);
    }

    #[tokio::test]
    async fn test_provider_run_shutdown_before_attempt() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            max_reconnect_attempts: 10,
            reconnect_delay: Duration::from_millis(100),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());

        let (_shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(true); // Already true.
        provider.run(shutdown_rx).await;
        // Should exit immediately without attempting connection.
    }

    #[test]
    fn test_provider_config_all_fields() {
        let config = ProviderConfig {
            rpc_url: "ws://test:8546".to_string(),
            nodes_config_path: Some("/tmp/nodes.yaml".to_string()),
            monitored_pools: vec![Address::repeat_byte(0x01)],
            reconnect_delay: Duration::from_secs(5),
            max_reconnect_attempts: 20,
            health_check_interval: Duration::from_secs(60),
        };
        assert_eq!(config.rpc_url, "ws://test:8546");
        assert_eq!(config.nodes_config_path, Some("/tmp/nodes.yaml".to_string()));
        assert_eq!(config.monitored_pools.len(), 1);
        assert_eq!(config.reconnect_delay, Duration::from_secs(5));
        assert_eq!(config.max_reconnect_attempts, 20);
        assert_eq!(config.health_check_interval, Duration::from_secs(60));
    }

    #[test]
    fn test_provider_config_clone() {
        let config = ProviderConfig {
            rpc_url: "ws://test:8546".to_string(),
            nodes_config_path: None,
            monitored_pools: vec![],
            reconnect_delay: Duration::from_secs(1),
            max_reconnect_attempts: 10,
            health_check_interval: Duration::from_secs(30),
        };
        let cloned = config.clone();
        assert_eq!(cloned.rpc_url, config.rpc_url);
        assert_eq!(cloned.max_reconnect_attempts, config.max_reconnect_attempts);
    }

    #[test]
    fn test_provider_config_debug() {
        let config = ProviderConfig::default();
        let debug_str = format!("{:?}", config);
        assert!(debug_str.contains("ProviderConfig"));
    }

    #[test]
    fn test_provider_config_default_from_eth_ws_url() {
        let _guard = ENV_LOCK.lock().unwrap();
        std::env::set_var("ETH_WS_URL", "ws://custom:8546");
        std::env::remove_var("ETH_RPC_URL");
        let config = ProviderConfig::default();
        assert_eq!(config.rpc_url, "ws://custom:8546");
        std::env::remove_var("ETH_WS_URL");
    }

    #[test]
    fn test_provider_config_is_cloneable() {
        let config = ProviderConfig {
            rpc_url: "ws://clone-test:8546".to_string(),
            monitored_pools: vec![Address::repeat_byte(0x01), Address::repeat_byte(0x02)],
            ..ProviderConfig::default()
        };
        let cloned = config.clone();
        assert_eq!(cloned.rpc_url, "ws://clone-test:8546");
        assert_eq!(cloned.monitored_pools.len(), 2);
    }

    #[test]
    fn test_provider_node_type_debug() {
        let ws = NodeType::WebSocket;
        let ipc = NodeType::Ipc;
        let http = NodeType::Http;
        assert!(format!("{:?}", ws).contains("WebSocket"));
        assert!(format!("{:?}", ipc).contains("Ipc"));
        assert!(format!("{:?}", http).contains("Http"));
    }

    #[test]
    fn test_process_logs_mixed_valid_and_invalid() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_pool_updates();
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        let pool1 = Address::repeat_byte(0x01);
        let pool2 = Address::repeat_byte(0x02);

        let mut sync_data = Vec::new();
        let r = U256::from(2000u64);
        sync_data.extend_from_slice(&r.to_be_bytes::<32>());
        sync_data.extend_from_slice(&r.to_be_bytes::<32>());

        let unknown_topic = B256::repeat_byte(0xFF);
        provider.process_logs(&[
            (pool1, vec![EventSignatures::sync_topic()], sync_data),
            (pool2, vec![unknown_topic], vec![0u8; 64]),
        ]);

        let e1 = rx.try_recv().expect("should receive valid event");
        match e1 {
            aether_ingestion::event_decoder::PoolEvent::ReserveUpdate { pool, .. } => {
                assert_eq!(pool, pool1);
            }
            other => panic!("Expected ReserveUpdate, got {:?}", other),
        }
    }

    #[test]
    fn test_infer_node_type_case_insensitive() {
        assert_eq!(infer_node_type("ws://localhost:8546"), NodeType::WebSocket);
        assert_eq!(infer_node_type("wss://example.com"), NodeType::WebSocket);
        assert_eq!(infer_node_type("https://example.com"), NodeType::Http);
        assert_eq!(infer_node_type("http://example.com"), NodeType::Http);
    }

    #[test]
    fn test_is_local_rpc_ipv6_loopback() {
        assert!(is_local_rpc("http://[::1]:8545"));
        assert!(is_local_rpc("HTTP://[::1]:8545"));
    }

    #[test]
    fn test_is_local_rpc_docker_internal() {
        assert!(is_local_rpc("http://host.docker.internal:8545"));
        assert!(is_local_rpc("HTTP://HOST.DOCKER.INTERNAL:8545"));
    }

    #[test]
    fn test_is_local_rpc_non_local() {
        assert!(!is_local_rpc("https://mainnet.infura.io/v3/abc"));
        assert!(!is_local_rpc("wss://eth-mainnet.alchemyapi.io/v2/key"));
        assert!(!is_local_rpc("http://10.0.0.1:8545"));
        assert!(!is_local_rpc("http://192.168.1.1:8545"));
    }

    #[test]
    fn test_process_logs_pair_created_event() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_pool_updates();
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        let pair_created_topic = EventSignatures::pair_created_topic();
        let token0 = Address::repeat_byte(0x01);
        let token1 = Address::repeat_byte(0x02);
        let pool_addr = Address::repeat_byte(0x03);

        let mut data = vec![0u8; 64];
        data[12..32].copy_from_slice(pool_addr.as_slice());

        let mut topics = vec![pair_created_topic];
        topics.push(token0.into_word());
        topics.push(token1.into_word());

        provider.process_logs(&[(Address::ZERO, topics, data)]);

        let event = rx.try_recv().expect("should receive PoolCreated event");
        match event {
            aether_ingestion::event_decoder::PoolEvent::PoolCreated { token0: t0, pool: _, .. } => {
                assert_eq!(t0, token0);
            }
            other => panic!("Expected PoolCreated, got {:?}", other),
        }
    }

    #[test]
    fn test_process_logs_swap_v2_event() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_pool_updates();
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        let swap_v2_topic = EventSignatures::swap_v2_topic();
        let pool_addr = Address::repeat_byte(0xAA);
        let sender = Address::repeat_byte(0xBB);
        let to = Address::repeat_byte(0xCC);

        let data = vec![0u8; 128];

        let mut topics = vec![swap_v2_topic];
        topics.push(sender.into_word());
        topics.push(to.into_word());

        provider.process_logs(&[(pool_addr, topics, data)]);

        let event = rx.try_recv().expect("should receive V2Swap event");
        match event {
            aether_ingestion::event_decoder::PoolEvent::V2Swap { pool, .. } => {
                assert_eq!(pool, pool_addr);
            }
            other => panic!("Expected V2Swap, got {:?}", other),
        }
    }

    #[test]
    fn test_record_decode_failure_insufficient_topics() {
        let channels = Arc::new(EventChannels::new());
        let metrics = Arc::new(EngineMetrics::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, Arc::clone(&metrics));

        provider.record_decode_failure(
            Address::repeat_byte(0xFF),
            &[B256::repeat_byte(0x01)],
            event_decoder::DecodeReason::InsufficientTopics,
        );

        let rendered = String::from_utf8(metrics.render()).expect("metrics utf-8");
        assert!(
            rendered.contains(r#"aether_decode_errors_total{reason="insufficient_topics"} 1"#),
        );
    }

    #[test]
    fn test_record_decode_failure_malformed_payload() {
        let channels = Arc::new(EventChannels::new());
        let metrics = Arc::new(EngineMetrics::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, Arc::clone(&metrics));

        provider.record_decode_failure(
            Address::repeat_byte(0xFF),
            &[B256::repeat_byte(0x01)],
            event_decoder::DecodeReason::MalformedPayload,
        );

        let rendered = String::from_utf8(metrics.render()).expect("metrics utf-8");
        assert!(
            rendered.contains(r#"aether_decode_errors_total{reason="malformed_payload"} 1"#),
        );
    }

    #[test]
    fn test_provider_config_default_rpc_url_fallback() {
        let _guard = ENV_LOCK.lock().unwrap();
        std::env::remove_var("ETH_WS_URL");
        std::env::remove_var("ETH_RPC_URL");
        let config = ProviderConfig::default();
        assert_eq!(config.rpc_url, "http://localhost:8545");
    }

    #[test]
    fn test_provider_config_default_rpc_url_from_eth_rpc_url() {
        let _guard = ENV_LOCK.lock().unwrap();
        std::env::remove_var("ETH_WS_URL");
        std::env::set_var("ETH_RPC_URL", "http://rpc.example.com:8545");
        let config = ProviderConfig::default();
        assert_eq!(config.rpc_url, "http://rpc.example.com:8545");
        std::env::remove_var("ETH_RPC_URL");
    }

    #[test]
    fn test_dispatch_block_large_values() {
        let channels = Arc::new(EventChannels::new());
        let mut rx = channels.subscribe_new_blocks();
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, Arc::clone(&channels), test_metrics());

        provider.dispatch_block(
            u64::MAX,
            u64::MAX,
            u128::MAX,
            u64::MAX,
            std::sync::Arc::new(Vec::new()),
        );

        let event = rx.try_recv().expect("should receive block event");
        assert_eq!(event.block_number, u64::MAX);
        assert_eq!(event.timestamp, u64::MAX);
        assert_eq!(event.base_fee, u128::MAX);
        assert_eq!(event.gas_limit, u64::MAX);
    }

    #[test]
    fn test_event_topics_count() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());
        let topics = provider.event_topics();
        assert_eq!(topics.len(), 5);
        assert!(topics.contains(&EventSignatures::sync_topic()));
        assert!(topics.contains(&EventSignatures::swap_v2_topic()));
        assert!(topics.contains(&EventSignatures::swap_v3_topic()));
        assert!(topics.contains(&EventSignatures::token_exchange_topic()));
        assert!(topics.contains(&EventSignatures::pair_created_topic()));
    }

    #[test]
    fn test_provider_config_health_check_interval() {
        let config = ProviderConfig {
            health_check_interval: Duration::from_secs(120),
            ..ProviderConfig::default()
        };
        assert_eq!(config.health_check_interval, Duration::from_secs(120));
    }

    #[test]
    fn test_provider_config_max_reconnect_attempts() {
        let config = ProviderConfig {
            max_reconnect_attempts: 25,
            ..ProviderConfig::default()
        };
        assert_eq!(config.max_reconnect_attempts, 25);
    }

    #[test]
    fn test_provider_config_reconnect_delay() {
        let config = ProviderConfig {
            reconnect_delay: Duration::from_millis(500),
            ..ProviderConfig::default()
        };
        assert_eq!(config.reconnect_delay, Duration::from_millis(500));
    }

    #[test]
    fn test_process_logs_insufficient_topics_data_only() {
        let channels = Arc::new(EventChannels::new());
        let metrics = Arc::new(EngineMetrics::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, Arc::clone(&metrics));

        provider.process_logs(&[(
            Address::ZERO,
            vec![],
            vec![0u8; 64],
        )]);

        let rendered = String::from_utf8(metrics.render()).expect("metrics utf-8");
        assert!(
            rendered.contains("aether_decode_errors_total"),
        );
    }

    #[test]
    fn test_provider_node_pool_all_nodes() {
        let channels = Arc::new(EventChannels::new());
        let config = ProviderConfig {
            rpc_url: "http://localhost:8545".to_string(),
            ..ProviderConfig::default()
        };
        let provider = RpcProvider::new(config, channels, test_metrics());
        let nodes = provider.node_pool().all_nodes();
        assert_eq!(nodes.len(), 1);
    }
}
