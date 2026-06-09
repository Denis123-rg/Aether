//! Tests for WebSocket health gating of HTTP polling.

use aether_discovery::events::WsHealth;

#[test]
fn ws_health_starts_unhealthy() {
    let h = WsHealth::new();
    assert!(!h.is_healthy());
}

#[test]
fn ws_health_set_healthy_true() {
    let h = WsHealth::new();
    h.set_healthy(true);
    assert!(h.is_healthy());
}

#[test]
fn ws_health_set_healthy_false_after_true() {
    let h = WsHealth::new();
    h.set_healthy(true);
    h.set_healthy(false);
    assert!(!h.is_healthy());
}

#[test]
fn ws_health_clone_shares_state() {
    let h = WsHealth::new();
    let h2 = h.clone();
    h.set_healthy(true);
    assert!(h2.is_healthy());
}

#[test]
fn ws_health_default_is_unhealthy() {
    let h = WsHealth::default();
    assert!(!h.is_healthy());
}
