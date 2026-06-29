//! Integration tests for the ingestion crate.
//!
//! Tests event decoding, config parsing, mempool subscription config,
//! and subscription channel behavior without requiring live network access.

use aether_common::types::ProtocolType;
use aether_ingestion::config::{expand_env_vars, NodesFileConfig};
use aether_ingestion::event_decoder::PoolEvent;
use aether_ingestion::subscription::{EventChannels, NewBlockEvent, PendingTxEvent};
use alloy::primitives::{Address, B256, U256};
use std::sync::Arc;

// ──────────────────────────────────────────────────────────────────────────────
// Config parsing tests
// ──────────────────────────────────────────────────────────────────────────────

#[test]
fn test_expand_env_vars_known() {
    std::env::set_var("AETHER_TEST_RPC_URL", "https://example.com");
    let result = expand_env_vars("https://eth-mainnet.g.alchemy.com/v2/${AETHER_TEST_RPC_URL}");
    assert_eq!(
        result,
        "https://eth-mainnet.g.alchemy.com/v2/https://example.com"
    );
    std::env::remove_var("AETHER_TEST_RPC_URL");
}

#[test]
fn test_expand_env_vars_unknown_preserved() {
    let result = expand_env_vars("prefix_${NONEXISTENT_VAR_12345}_suffix");
    assert_eq!(result, "prefix_${NONEXISTENT_VAR_12345}_suffix");
}

#[test]
fn test_expand_env_vars_no_vars() {
    let input = "no variables here";
    let result = expand_env_vars(input);
    assert_eq!(result, input);
}

#[test]
fn test_expand_env_vars_multiple() {
    std::env::set_var("AETHER_TEST_HOST", "localhost");
    std::env::set_var("AETHER_TEST_PORT", "8545");
    let result = expand_env_vars("http://${AETHER_TEST_HOST}:${AETHER_TEST_PORT}");
    assert_eq!(result, "http://localhost:8545");
    std::env::remove_var("AETHER_TEST_HOST");
    std::env::remove_var("AETHER_TEST_PORT");
}

#[test]
fn test_nodes_config_parse() {
    let yaml = r#"
nodes:
  - name: "alchemy-ws"
    url: "wss://eth-mainnet.g.alchemy.com/v2/test"
    type: "websocket"
    priority: 1
  - name: "local-reth"
    url: "/tmp/reth.ipc"
    type: "ipc"
    priority: 0
min_healthy_nodes: 2
"#;
    let config: NodesFileConfig = serde_yml::from_str(yaml).unwrap();
    assert_eq!(config.nodes.len(), 2);
    assert_eq!(config.min_healthy_nodes, 2);
    assert_eq!(config.nodes[0].name, "alchemy-ws");
    assert_eq!(config.nodes[0].node_type, "websocket");
    assert_eq!(config.nodes[0].priority, 1);
    assert_eq!(config.nodes[1].name, "local-reth");
    assert_eq!(config.nodes[1].node_type, "ipc");
    assert_eq!(config.nodes[1].priority, 0);
}

#[test]
fn test_nodes_config_defaults() {
    let yaml = r#"
nodes:
  - name: "node1"
    url: "http://localhost:8545"
    type: "http"
"#;
    let config: NodesFileConfig = serde_yml::from_str(yaml).unwrap();
    assert_eq!(config.min_healthy_nodes, 1); // default
    assert_eq!(config.nodes[0].priority, 10); // default
}

// ──────────────────────────────────────────────────────────────────────────────
// EventChannels tests — broadcast behavior
// ──────────────────────────────────────────────────────────────────────────────

#[test]
fn test_event_channels_new_block_dispatch() {
    let channels = EventChannels::new();

    let event = NewBlockEvent {
        block_number: 12345,
        timestamp: 1700000000,
        base_fee: 20_000_000_000, // 20 gwei
        gas_limit: 30_000_000,
        tx_hashes: Arc::new(vec![]),
    };

    let mut rx = channels.subscribe_new_blocks();
    channels.dispatch_new_block(event.clone());
    let received = rx.try_recv().unwrap();
    assert_eq!(received.block_number, 12345);
    assert_eq!(received.timestamp, 1700000000);
    assert_eq!(received.base_fee, 20_000_000_000);
}

#[test]
fn test_event_channels_pending_tx_dispatch() {
    let channels = EventChannels::new();

    let event = PendingTxEvent {
        tx_hash: B256::ZERO,
        from: Address::ZERO,
        to: Some(Address::ZERO),
        value: U256::from(1_000_000_000_000_000_000u64), // 1 ETH
        input: vec![0xaa, 0xbb],
        gas_price: 30_000_000_000,
        first_seen_unix_nanos: 1700000000_000_000_000,
        raw_tx: vec![],
    };

    let mut rx = channels.subscribe_pending_txs();
    channels.dispatch_pending_tx(event.clone());
    let received = rx.try_recv().unwrap();
    assert_eq!(received.tx_hash, B256::ZERO);
    assert_eq!(received.value, U256::from(1_000_000_000_000_000_000u64));
    assert_eq!(received.input, vec![0xaa, 0xbb]);
}

#[test]
fn test_event_channels_pool_update_dispatch() {
    let channels = EventChannels::new();

    let event = PoolEvent::ReserveUpdate {
        pool: Address::ZERO,
        protocol: ProtocolType::UniswapV2,
        reserve0: U256::from(1000),
        reserve1: U256::from(2000),
    };

    let mut rx = channels.subscribe_pool_updates();
    channels.dispatch_pool_update(event);
    let received = rx.try_recv().unwrap();
    match received {
        PoolEvent::ReserveUpdate {
            pool,
            reserve0,
            reserve1,
            ..
        } => {
            assert_eq!(pool, Address::ZERO);
            assert_eq!(reserve0, U256::from(1000));
            assert_eq!(reserve1, U256::from(2000));
        }
        _ => panic!("expected PoolEvent::ReserveUpdate"),
    }
}

#[test]
fn test_event_channels_no_subscribers_no_panic() {
    let channels = EventChannels::new();
    // Dispatching with no subscribers should not panic (send returns Err)
    let _ = channels.dispatch_new_block(NewBlockEvent::default());
    let _ = channels.dispatch_pending_tx(PendingTxEvent::default());
}

#[test]
fn test_event_channels_multiple_subscribers() {
    let channels = EventChannels::new();
    let mut rx1 = channels.subscribe_new_blocks();
    let mut rx2 = channels.subscribe_new_blocks();

    let event = NewBlockEvent {
        block_number: 999,
        ..Default::default()
    };
    channels.dispatch_new_block(event);

    let r1 = rx1.try_recv().unwrap();
    let r2 = rx2.try_recv().unwrap();
    assert_eq!(r1.block_number, 999);
    assert_eq!(r2.block_number, 999);
}

#[test]
fn test_event_channels_subscriber_counts() {
    let channels = EventChannels::new();
    let _rx1 = channels.subscribe_new_blocks();
    let _rx2 = channels.subscribe_new_blocks();
    let _rx3 = channels.subscribe_pending_txs();

    let (pool, block, pending) = channels.subscriber_counts();
    assert_eq!(pool, 0);
    assert_eq!(block, 2);
    assert_eq!(pending, 1);
}

// ──────────────────────────────────────────────────────────────────────────────
// PendingTxEvent edge cases
// ──────────────────────────────────────────────────────────────────────────────

#[test]
fn test_pending_tx_event_zero_value() {
    let event = PendingTxEvent {
        value: U256::ZERO,
        ..Default::default()
    };
    assert_eq!(event.value, U256::ZERO);
}

#[test]
fn test_pending_tx_event_no_to_address() {
    let event = PendingTxEvent {
        to: None, // contract creation
        ..Default::default()
    };
    assert!(event.to.is_none());
}

#[test]
fn test_pending_tx_event_empty_input() {
    let event = PendingTxEvent {
        input: vec![],
        ..Default::default()
    };
    assert!(event.input.is_empty());
}

#[test]
fn test_new_block_event_default() {
    let event = NewBlockEvent::default();
    assert_eq!(event.block_number, 0);
    assert_eq!(event.timestamp, 0);
    assert_eq!(event.base_fee, 0);
    assert!(event.tx_hashes.is_empty());
}
