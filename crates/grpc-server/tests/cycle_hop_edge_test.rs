//! Tests for cycle hop edge selection (H2 — filtered flag respected).

use aether_common::types::{PoolId, ProtocolType};
use aether_grpc_server::cycle_gating::select_best_edge_for_hop;
use aether_state::price_graph::PriceEdge;
use alloy::primitives::{Address, U256};

fn edge(from: usize, to: usize, weight: f64, filtered: bool) -> PriceEdge {
    PriceEdge {
        from,
        to,
        weight,
        pool_id: PoolId {
            address: Address::from([from as u8; 20]),
            protocol: ProtocolType::UniswapV2,
        },
        pool_address: Address::from([from as u8; 20]),
        protocol: ProtocolType::UniswapV2,
        liquidity: U256::from(1_000u64),
        reserve_in: 1.0,
        reserve_out: 1.0,
        filtered,
    }
}

#[test]
fn filtered_edge_not_selected() {
    let edges = vec![edge(0, 1, 0.1, true), edge(0, 1, 0.5, false)];
    let best = select_best_edge_for_hop(&edges, 1).expect("unfiltered edge");
    assert!((best.weight - 0.5).abs() < f64::EPSILON);
    assert!(!best.filtered);
}

#[test]
fn all_filtered_returns_none() {
    let edges = vec![edge(0, 1, 0.1, true), edge(0, 1, 0.2, true)];
    assert!(select_best_edge_for_hop(&edges, 1).is_none());
}

#[test]
fn picks_minimum_weight_among_unfiltered() {
    let edges = vec![
        edge(0, 1, 0.3, false),
        edge(0, 1, 0.1, false),
        edge(0, 1, 0.05, true),
    ];
    let best = select_best_edge_for_hop(&edges, 1).unwrap();
    assert!((best.weight - 0.1).abs() < f64::EPSILON);
}

#[test]
fn wrong_destination_skipped() {
    let edges = vec![edge(0, 2, 0.1, false)];
    assert!(select_best_edge_for_hop(&edges, 1).is_none());
}

#[test]
fn single_unfiltered_edge_selected() {
    let edges = vec![edge(0, 1, 0.42, false)];
    let best = select_best_edge_for_hop(&edges, 1).unwrap();
    assert!((best.weight - 0.42).abs() < f64::EPSILON);
}

#[test]
fn mixed_filtered_unfiltered_only_considers_unfiltered() {
    let edges = vec![
        edge(0, 1, -1.0, true),
        edge(0, 1, 0.8, false),
        edge(0, 1, -2.0, true),
    ];
    let best = select_best_edge_for_hop(&edges, 1).unwrap();
    assert!((best.weight - 0.8).abs() < f64::EPSILON);
}

#[test]
fn empty_edge_list_returns_none() {
    let edges: Vec<PriceEdge> = vec![];
    assert!(select_best_edge_for_hop(&edges, 1).is_none());
}

#[test]
fn filtered_lowest_weight_ignored_for_better_unfiltered() {
    let edges = vec![edge(0, 1, 0.01, true), edge(0, 1, 0.99, false)];
    let best = select_best_edge_for_hop(&edges, 1).unwrap();
    assert!((best.weight - 0.99).abs() < f64::EPSILON);
}
