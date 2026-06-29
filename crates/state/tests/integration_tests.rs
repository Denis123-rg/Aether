//! Integration tests for the state crate.
//!
//! Tests token index, price graph, and concurrent access patterns
//! without requiring live network access.

use aether_common::types::{PoolId, ProtocolType};
use aether_state::price_graph::PriceGraph;
use aether_state::token_index::TokenIndex;
use alloy::primitives::{Address, U256};
use std::sync::Arc;

// ──────────────────────────────────────────────────────────────────────────────
// TokenIndex tests
// ──────────────────────────────────────────────────────────────────────────────

#[test]
fn test_token_index_get_or_insert() {
    let mut index = TokenIndex::new();
    let addr1: Address = "0x0000000000000000000000000000000000000001"
        .parse()
        .unwrap();
    let addr2: Address = "0x0000000000000000000000000000000000000002"
        .parse()
        .unwrap();

    let idx1 = index.get_or_insert(addr1);
    let idx2 = index.get_or_insert(addr2);
    let idx1_again = index.get_or_insert(addr1);

    assert_eq!(idx1, 0);
    assert_eq!(idx2, 1);
    assert_eq!(idx1_again, 0); // same index
    assert_eq!(index.len(), 2);
}

#[test]
fn test_token_index_get_index() {
    let mut index = TokenIndex::new();
    let addr: Address = "0x0000000000000000000000000000000000000001"
        .parse()
        .unwrap();

    assert!(index.get_index(&addr).is_none());
    index.get_or_insert(addr);
    assert_eq!(index.get_index(&addr), Some(0));
}

#[test]
fn test_token_index_get_address() {
    let mut index = TokenIndex::new();
    let addr: Address = "0x0000000000000000000000000000000000000001"
        .parse()
        .unwrap();

    assert!(index.get_address(0).is_none());
    index.get_or_insert(addr);
    assert_eq!(index.get_address(0), Some(&addr));
}

#[test]
fn test_token_index_empty() {
    let index = TokenIndex::new();
    assert_eq!(index.len(), 0);
}

#[test]
fn test_token_index_stability() {
    let mut index = TokenIndex::new();
    let addrs: Vec<Address> = (0..10)
        .map(|i| {
            let mut bytes = [0u8; 20];
            bytes[19] = i as u8;
            Address::from(bytes)
        })
        .collect();

    let indices: Vec<usize> = addrs.iter().map(|a| index.get_or_insert(*a)).collect();
    let indices2: Vec<usize> = addrs.iter().map(|a| index.get_or_insert(*a)).collect();
    assert_eq!(indices, indices2);
}

// ──────────────────────────────────────────────────────────────────────────────
// PriceGraph tests
// ──────────────────────────────────────────────────────────────────────────────

fn make_pool_id(id: u64) -> PoolId {
    let mut bytes = [0u8; 20];
    bytes[19] = id as u8;
    PoolId {
        address: Address::from(bytes),
        protocol: ProtocolType::UniswapV3,
    }
}

fn make_test_graph() -> (PriceGraph, TokenIndex) {
    let mut graph = PriceGraph::new(4);
    let mut index = TokenIndex::new();

    let weth: Address = "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"
        .parse()
        .unwrap();
    let usdc: Address = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
        .parse()
        .unwrap();
    let dai: Address = "0x6B175474E89094C44Da98b954EedeAC495271d0F"
        .parse()
        .unwrap();

    let weth_idx = index.get_or_insert(weth);
    let usdc_idx = index.get_or_insert(usdc);
    let dai_idx = index.get_or_insert(dai);

    // WETH -> USDC (1 WETH = 2000 USDC)
    graph.add_edge(
        weth_idx,
        usdc_idx,
        2000.0,
        make_pool_id(1),
        Address::ZERO,
        ProtocolType::UniswapV3,
        U256::from(1_000_000_000_000_000_000u64),
    );

    // USDC -> DAI (1 USDC = 1 DAI)
    graph.add_edge(
        usdc_idx,
        dai_idx,
        1.0,
        make_pool_id(2),
        Address::ZERO,
        ProtocolType::UniswapV2,
        U256::from(500_000_000_000_000_000u64),
    );

    // DAI -> WETH (4000 DAI = 1 WETH)
    graph.add_edge(
        dai_idx,
        weth_idx,
        1.0 / 4000.0,
        make_pool_id(3),
        Address::ZERO,
        ProtocolType::SushiSwap,
        U256::from(2_000_000_000_000_000_000u64),
    );

    (graph, index)
}

#[test]
fn test_price_graph_add_edge() {
    let (graph, _index) = make_test_graph();
    assert_eq!(graph.num_edges(), 3);
    assert_eq!(graph.num_vertices(), 4);
}

#[test]
fn test_price_graph_edges_from() {
    let (graph, index) = make_test_graph();
    let weth: Address = "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"
        .parse()
        .unwrap();
    let weth_idx = index.get_index(&weth).unwrap();
    let edges = graph.edges_from(weth_idx);
    assert_eq!(edges.len(), 1); // WETH -> USDC
    let usdc: Address = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
        .parse()
        .unwrap();
    assert_eq!(edges[0].to, index.get_index(&usdc).unwrap());
}

#[test]
fn test_price_graph_all_edges() {
    let (graph, _index) = make_test_graph();
    let all = graph.all_edges();
    assert_eq!(all.len(), 3);
}

#[test]
fn test_price_graph_vertex_count() {
    let (graph, _index) = make_test_graph();
    assert_eq!(graph.num_vertices(), 4);
}

#[test]
fn test_price_graph_set_edge_filtered() {
    let (mut graph, index) = make_test_graph();
    let weth: Address = "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"
        .parse()
        .unwrap();
    let usdc: Address = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
        .parse()
        .unwrap();
    let weth_idx = index.get_index(&weth).unwrap();
    let usdc_idx = index.get_index(&usdc).unwrap();

    graph.set_edge_filtered(weth_idx, usdc_idx, make_pool_id(1), true);
    let edges = graph.edges_from(weth_idx);
    assert!(edges[0].filtered);
}

#[test]
fn test_price_graph_resize() {
    let mut graph = PriceGraph::new(2);
    assert_eq!(graph.num_vertices(), 2);
    graph.resize(10);
    assert_eq!(graph.num_vertices(), 10);
}

#[test]
fn test_price_graph_set_token_decimals() {
    let mut graph = PriceGraph::new(4);
    graph.set_token_decimals(0, 18);
    graph.set_token_decimals(1, 6);
    assert_eq!(graph.token_decimals(0), 18);
    assert_eq!(graph.token_decimals(1), 6);
    assert_eq!(graph.token_decimals(2), 18); // default
}

#[test]
fn test_price_graph_set_weth_vertex() {
    let mut graph = PriceGraph::new(4);
    graph.set_weth_vertex(2);
}

#[test]
fn test_price_graph_dirty_edges() {
    let (mut graph, _index) = make_test_graph();
    assert!(graph.has_dirty_edges());
    graph.clear_dirty();
    assert!(!graph.has_dirty_edges());
}

#[test]
fn test_price_graph_remove_pool_edges() {
    let (mut graph, _index) = make_test_graph();
    assert_eq!(graph.num_edges(), 3);
    graph.remove_pool_edges(&make_pool_id(1));
    assert_eq!(graph.num_edges(), 2);
}

#[test]
fn test_price_graph_clone_retaining_pools() {
    let (graph, _index) = make_test_graph();
    let allowed = vec![Address::ZERO].into_iter().collect();
    let cloned = graph.clone_retaining_pools(&allowed);
    assert_eq!(cloned.num_edges(), 3); // all have pool_address = ZERO
}

#[test]
fn test_price_graph_affected_vertices() {
    let (graph, _index) = make_test_graph();
    let affected = graph.affected_vertices();
    assert!(!affected.is_empty());
}

// ──────────────────────────────────────────────────────────────────────────────
// Concurrent access tests
// ──────────────────────────────────────────────────────────────────────────────

#[test]
fn test_token_index_concurrent_read() {
    let mut index = TokenIndex::new();
    let addr: Address = "0x0000000000000000000000000000000000000001"
        .parse()
        .unwrap();
    index.get_or_insert(addr);
    let index = Arc::new(index);

    let handles: Vec<_> = (0..8)
        .map(|_| {
            let idx = Arc::clone(&index);
            std::thread::spawn(move || {
                for _ in 0..1000 {
                    let _ = idx.get_index(&addr);
                    let _ = idx.get_address(0);
                }
            })
        })
        .collect();

    for h in handles {
        h.join().unwrap();
    }
}

#[test]
fn test_price_graph_concurrent_read() {
    let (graph, _index) = make_test_graph();
    let graph = Arc::new(graph);

    let handles: Vec<_> = (0..4)
        .map(|_| {
            let g = Arc::clone(&graph);
            std::thread::spawn(move || {
                for _ in 0..100 {
                    let _ = g.all_edges();
                    let _ = g.num_edges();
                    let _ = g.num_vertices();
                }
            })
        })
        .collect();

    for h in handles {
        h.join().unwrap();
    }
}

// ──────────────────────────────────────────────────────────────────────────────
// Edge cases
// ──────────────────────────────────────────────────────────────────────────────

#[test]
fn test_empty_graph() {
    let graph = PriceGraph::new(0);
    assert_eq!(graph.num_vertices(), 0);
    assert_eq!(graph.num_edges(), 0);
    assert!(!graph.has_dirty_edges());
}

#[test]
fn test_single_vertex_no_edges() {
    let graph = PriceGraph::new(1);
    assert_eq!(graph.num_vertices(), 1);
    assert_eq!(graph.num_edges(), 0);
    assert!(graph.edges_from(0).is_empty());
}
