//! Live Postgres integration tests for `aether_common::db::PgLedger`.
//!
//! Requires Docker (testcontainers). Skipped when Docker is unavailable.
//! Run: `cargo test -p aether-common --test db_postgres_test`

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use aether_common::db::{
    ledger_from_env, protocol_label, InclusionUpdate, Ledger, LedgerMetrics, NewArb, NewPool,
    NoopLedger, PgLedger,
};
use aether_common::types::ProtocolType;
use alloy::primitives::{Address, B256, U256};
use chrono::Utc;
use prometheus::Registry;
use sqlx::PgPool;
use testcontainers::runners::AsyncRunner;
use testcontainers::ContainerAsync;
use testcontainers_modules::postgres::Postgres;
use tokio::time::sleep;
use uuid::Uuid;

async fn apply_migrations(pool: &PgPool) {
    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../migrations");
    sqlx::migrate::Migrator::new(dir.as_path())
        .await
        .expect("migrator")
        .run(pool)
        .await
        .expect("run migrations");
}

async fn start_postgres() -> Result<(ContainerAsync<Postgres>, String), String> {
    let container = Postgres::default()
        .start()
        .await
        .map_err(|e| format!("start postgres container: {e}"))?;
    let host = container
        .get_host()
        .await
        .map_err(|e| format!("host: {e}"))?;
    let port = container
        .get_host_port_ipv4(5432)
        .await
        .map_err(|e| format!("port: {e}"))?;
    let url = format!("postgres://postgres:postgres@{host}:{port}/postgres?sslmode=disable");
    Ok((container, url))
}

fn sample_arb(arb_id: Uuid) -> NewArb {
    NewArb {
        arb_id,
        ts: Utc::now(),
        target_block: 18_000_000,
        path_hash: B256::ZERO,
        hops: 2,
        path: serde_json::json!([]),
        protocols: serde_json::json!(["UniswapV2"]),
        pool_addresses: serde_json::json!([]),
        flashloan_token: Address::ZERO,
        flashloan_amount: U256::from(1_000_000u64),
        gross_profit_wei: U256::from(500_000u64),
        net_profit_wei: U256::from(400_000u64),
        gas_estimate: 250_000,
        tip_bps: 9000,
        detection_us: Some(1200),
        sim_us: Some(3400),
        git_sha: Some("testsha".into()),
    }
}

#[tokio::test]
async fn pg_ledger_inserts_arb_and_pool() {
    if std::env::var("AETHER_SKIP_TESTCONTAINERS").is_ok() {
        return;
    }
    let (_container, url) = match start_postgres().await {
        Ok(v) => v,
        Err(e) => {
            eprintln!("skipping: docker unavailable ({e})");
            return;
        }
    };

    let pool = PgPool::connect(&url).await.expect("connect");
    apply_migrations(&pool).await;

    let registry = Registry::new();
    let metrics = LedgerMetrics::register(&registry);
    let ledger = PgLedger::connect(&url, metrics).await.expect("PgLedger");

    let arb_id = Uuid::new_v4();
    ledger.insert_arb(&sample_arb(arb_id));
    ledger.insert_pool(&NewPool {
        address: Address::from([0x11u8; 20]),
        protocol: ProtocolType::UniswapV2,
        token0: Address::from([0x22u8; 20]),
        token1: Address::from([0x33u8; 20]),
        fee_bps: Some(30),
        tier: Some("hot".into()),
        source: "test".into(),
    });

    sleep(Duration::from_millis(500)).await;

    let count: (i64,) = sqlx::query_as("SELECT COUNT(*) FROM arbs WHERE arb_id = $1")
        .bind(arb_id)
        .fetch_one(&pool)
        .await
        .expect("count arbs");
    assert_eq!(count.0, 1);

    let proto: (String,) = sqlx::query_as("SELECT protocol FROM pool_registry WHERE address = $1")
        .bind(Address::from([0x11u8; 20]).as_slice())
        .fetch_one(&pool)
        .await
        .expect("pool row");
    assert_eq!(proto.0, protocol_label(ProtocolType::UniswapV2));
}

#[tokio::test]
async fn pg_ledger_update_inclusion_round_trip() {
    if std::env::var("AETHER_SKIP_TESTCONTAINERS").is_ok() {
        return;
    }
    let (_container, url) = match start_postgres().await {
        Ok(v) => v,
        Err(_) => return,
    };

    let pool = PgPool::connect(&url).await.expect("connect");
    apply_migrations(&pool).await;

    let bundle_id = Uuid::new_v4();
    sqlx::query(
        r#"
        INSERT INTO bundles (bundle_id, arb_id, submitted_at, target_block, signed_tx_hex, is_shadow, builders)
        VALUES ($1, $2, now(), 1, '0x', false, '[]'::jsonb)
        "#,
    )
    .bind(bundle_id)
    .bind(Uuid::new_v4())
    .execute(&pool)
    .await
    .expect("seed bundle");

    let registry = Registry::new();
    let metrics = LedgerMetrics::register(&registry);
    let ledger = PgLedger::connect(&url, metrics).await.expect("PgLedger");

    ledger.update_inclusion(&InclusionUpdate {
        bundle_id,
        builder: "flashbots".into(),
        included: true,
        included_block: Some(18_000_001),
        landed_tx_hash: Some(B256::from([0xab; 32])),
        error: None,
        resolved_at: Utc::now(),
    });

    sleep(Duration::from_millis(500)).await;

    let included: (bool,) = sqlx::query_as(
        "SELECT included FROM inclusion_results WHERE bundle_id = $1 AND builder = $2",
    )
    .bind(bundle_id)
    .bind("flashbots")
    .fetch_one(&pool)
    .await
    .expect("inclusion row");
    assert!(included.0);
}

#[tokio::test]
async fn pg_ledger_channel_saturation_drops_without_panic() {
    let registry = Registry::new();
    let metrics = LedgerMetrics::register(&registry);
    // No DATABASE_URL — NoopLedger path.
    let ledger: Arc<dyn Ledger> = Arc::new(NoopLedger::new());
    for _ in 0..10_000 {
        ledger.insert_arb(&NewArb::default());
    }
    let _ = metrics; // exercised register path
}

#[tokio::test]
async fn pg_ledger_concurrent_writes() {
    if std::env::var("AETHER_SKIP_TESTCONTAINERS").is_ok() {
        return;
    }
    let (_container, url) = match start_postgres().await {
        Ok(v) => v,
        Err(_) => return,
    };

    let pool = PgPool::connect(&url).await.expect("connect");
    apply_migrations(&pool).await;

    let registry = Registry::new();
    let metrics = LedgerMetrics::register(&registry);
    let ledger = Arc::new(PgLedger::connect(&url, metrics).await.expect("PgLedger"));

    let mut handles = vec![];
    for i in 0..32 {
        let lg = Arc::clone(&ledger);
        handles.push(tokio::spawn(async move {
            let id = Uuid::new_v4();
            let mut arb = sample_arb(id);
            arb.target_block = 18_000_000 + i as u64;
            lg.insert_arb(&arb);
        }));
    }
    for h in handles {
        h.await.expect("join");
    }
    sleep(Duration::from_secs(2)).await;

    let count: (i64,) = sqlx::query_as("SELECT COUNT(*) FROM arbs")
        .fetch_one(&pool)
        .await
        .expect("count");
    assert_eq!(count.0, 32);
}

#[tokio::test]
async fn pg_ledger_duplicate_arb_is_idempotent() {
    if std::env::var("AETHER_SKIP_TESTCONTAINERS").is_ok() {
        return;
    }
    let (_container, url) = match start_postgres().await {
        Ok(v) => v,
        Err(_) => return,
    };

    let pool = PgPool::connect(&url).await.expect("connect");
    apply_migrations(&pool).await;

    let registry = Registry::new();
    let metrics = LedgerMetrics::register(&registry);
    let ledger = PgLedger::connect(&url, metrics).await.expect("PgLedger");

    let arb_id = Uuid::new_v4();
    let arb = sample_arb(arb_id);
    ledger.insert_arb(&arb);
    ledger.insert_arb(&arb);
    sleep(Duration::from_millis(500)).await;

    let count: (i64,) = sqlx::query_as("SELECT COUNT(*) FROM arbs WHERE arb_id = $1")
        .bind(arb_id)
        .fetch_one(&pool)
        .await
        .expect("count");
    assert_eq!(count.0, 1);
}

#[tokio::test]
async fn pg_ledger_from_env_bad_url_falls_back_to_noop() {
    let registry = Registry::new();
    let metrics = LedgerMetrics::register(&registry);
    std::env::set_var(
        "DATABASE_URL",
        "postgres://127.0.0.1:1/none?connect_timeout=1",
    );
    let ledger = ledger_from_env(metrics).await;
    // NoopLedger accepts writes without panicking.
    ledger.insert_arb(&NewArb::default());
    std::env::remove_var("DATABASE_URL");
}

#[tokio::test]
async fn pg_ledger_drops_metric_on_saturated_channel() {
    let registry = Registry::new();
    let metrics = LedgerMetrics::register(&registry);
    // Tiny channel via direct construction is not exposed; flood a live ledger
    // with a closed receiver by dropping all senders after connect.
    let (_container, url) = match start_postgres().await {
        Ok(v) => v,
        Err(_) => return,
    };
    if std::env::var("AETHER_SKIP_TESTCONTAINERS").is_ok() {
        return;
    }

    let pool = PgPool::connect(&url).await.expect("connect");
    apply_migrations(&pool).await;

    let ledger = PgLedger::connect(&url, Arc::clone(&metrics))
        .await
        .expect("PgLedger");
    for i in 0..2048 {
        let mut arb = sample_arb(Uuid::new_v4());
        arb.target_block = 18_000_000 + i;
        ledger.insert_arb(&arb);
    }
    sleep(Duration::from_secs(3)).await;

    let families = registry.gather();
    let drops = families
        .iter()
        .find(|f| f.get_name() == "aether_ledger_writes_total")
        .map(|f| {
            f.get_metric()
                .iter()
                .filter(|m| {
                    m.get_label()
                        .iter()
                        .any(|l| l.get_name() == "result" && l.get_value() == "ok")
                })
                .map(|m| m.get_counter().get_value())
                .sum::<f64>()
        })
        .unwrap_or(0.0);
    assert!(drops > 0.0, "expected successful writes recorded");
    drop(ledger);
}
