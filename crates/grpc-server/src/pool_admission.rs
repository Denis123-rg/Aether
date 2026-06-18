//! Pool-admission gate for hot-token candidates.
//!
//! Turns a frequently-traded candidate pair (surfaced by [`crate::hot_token`])
//! into registered, arbitrageable pools — but only after it clears a safety
//! gate. The admission flow:
//!
//! ```text
//! hot_tokens.candidates()                         (C1 — ranked new pairs)
//!   └─► enrich: factory getPair/getPool + reserves (integration layer)
//!         └─► fee-on-transfer / honeypot screen     (C2 — fee_on_transfer)
//!               └─► evaluate(venues, fot, cfg)       (this module — pure)
//!                     └─► append_admitted_pools()    (this module — writes TOML)
//!                           └─► ControlService.ReloadConfig / bootstrap_pools
//! ```
//!
//! `ReloadConfig` re-reads the whole `pools.toml` and re-registers every entry
//! (idempotent), so admission is simply: **append new `[[pools]]` blocks, then
//! reload**. This module owns the two pieces that are pure and unit-testable —
//! the admit/reject *decision* and the dedup-aware TOML *writer*. The
//! enrichment (factory lookups, reserves, the buy→sell round-trip) and the
//! reload trigger are the surrounding integration layer.
//!
//! Safety stance: the gate **fails closed**. A token is admitted only when the
//! fee-on-transfer screen is `Clean` AND at least `min_venues` venues clear the
//! liquidity floor. Anything else is rejected with a reason.

use std::collections::HashSet;
use std::fs::OpenOptions;
use std::io::Write;
use std::path::Path;

use aether_common::types::ProtocolType;
use aether_simulator::fee_on_transfer::FotVerdict;
use alloy::primitives::Address;

/// Thresholds governing admission.
#[derive(Clone, Debug)]
pub struct AdmissionConfig {
    /// Minimum distinct venues (above the liquidity floor) a pair must trade on
    /// before any of its pools are admitted. Atomic arbitrage needs a price gap
    /// between ≥2 venues, so this is 2 by default.
    pub min_venues: usize,
    /// Per-venue USD liquidity floor.
    pub min_liquidity_usd: f64,
    /// Reject unless the fee-on-transfer screen returned `Clean`.
    pub require_fot_clean: bool,
    /// Tier assigned to admitted pools (`"warm"` keeps new tokens off the
    /// every-block hot path until they prove out).
    pub tier: String,
}

impl Default for AdmissionConfig {
    fn default() -> Self {
        Self {
            min_venues: 2,
            min_liquidity_usd: 25_000.0,
            require_fot_clean: true,
            tier: "warm".to_string(),
        }
    }
}

/// A discovered venue for a candidate pair, enriched with on-chain facts by the
/// integration layer (factory lookup + reserves). Input to [`evaluate`].
#[derive(Clone, Debug, PartialEq)]
pub struct CandidateVenue {
    pub protocol: ProtocolType,
    pub address: Address,
    pub token0: Address,
    pub token1: Address,
    pub fee_bps: u32,
    pub tick_spacing: Option<i32>,
    pub liquidity_usd: f64,
}

impl CandidateVenue {
    fn to_entry(&self, tier: &str) -> PoolEntryToml {
        PoolEntryToml {
            protocol: self.protocol,
            address: self.address,
            token0: self.token0,
            token1: self.token1,
            fee_bps: self.fee_bps,
            tick_spacing: self.tick_spacing,
            tier: tier.to_string(),
        }
    }
}

/// A fully-resolved pool ready to be written as a `[[pools]]` block.
#[derive(Clone, Debug, PartialEq)]
pub struct PoolEntryToml {
    pub protocol: ProtocolType,
    pub address: Address,
    pub token0: Address,
    pub token1: Address,
    pub fee_bps: u32,
    pub tick_spacing: Option<i32>,
    pub tier: String,
}

/// Result of the admission decision.
#[derive(Clone, Debug, PartialEq)]
pub enum AdmissionOutcome {
    /// Admit these pool entries (one per qualifying venue).
    Admit(Vec<PoolEntryToml>),
    /// Reject the candidate, with a human-readable reason.
    Reject { reason: String },
}

/// The `protocol` string `pools.toml` / `bootstrap_pools` expects.
pub fn protocol_toml_str(p: ProtocolType) -> &'static str {
    match p {
        ProtocolType::UniswapV2 => "uniswap_v2",
        ProtocolType::UniswapV3 => "uniswap_v3",
        ProtocolType::SushiSwap => "sushiswap",
        ProtocolType::Curve => "curve",
        ProtocolType::BalancerV2 => "balancer_v2",
        ProtocolType::BalancerV3 => "balancer_v3",
        ProtocolType::BancorV3 => "bancor_v3",
    }
}

/// Decide whether a candidate's venues should be admitted. Pure — all on-chain
/// facts are already baked into `venues` and `fot`.
pub fn evaluate(
    venues: &[CandidateVenue],
    fot: &FotVerdict,
    cfg: &AdmissionConfig,
) -> AdmissionOutcome {
    if cfg.require_fot_clean && !fot.is_admissible() {
        return AdmissionOutcome::Reject {
            reason: format!("fee-on-transfer screen not clean: {fot:?}"),
        };
    }
    let qualified: Vec<&CandidateVenue> = venues
        .iter()
        .filter(|v| v.liquidity_usd >= cfg.min_liquidity_usd)
        .collect();
    if qualified.len() < cfg.min_venues {
        return AdmissionOutcome::Reject {
            reason: format!(
                "{} venue(s) above ${:.0} liquidity; need {}",
                qualified.len(),
                cfg.min_liquidity_usd,
                cfg.min_venues
            ),
        };
    }
    let entries = qualified.iter().map(|v| v.to_entry(&cfg.tier)).collect();
    AdmissionOutcome::Admit(entries)
}

/// Render one pool as a `pools.toml` `[[pools]]` block (trailing newline). The
/// field order and `protocol` strings match what `bootstrap_pools` parses.
pub fn format_pool_entry(e: &PoolEntryToml) -> String {
    let mut s = String::new();
    s.push_str("[[pools]]\n");
    s.push_str(&format!("protocol = \"{}\"\n", protocol_toml_str(e.protocol)));
    s.push_str(&format!("address = \"{}\"\n", e.address.to_checksum(None)));
    s.push_str(&format!("token0 = \"{}\"\n", e.token0.to_checksum(None)));
    s.push_str(&format!("token1 = \"{}\"\n", e.token1.to_checksum(None)));
    s.push_str(&format!("fee_bps = {}\n", e.fee_bps));
    s.push_str(&format!("tier = \"{}\"\n", e.tier));
    if let Some(ts) = e.tick_spacing {
        s.push_str(&format!("tick_spacing = {ts}\n"));
    }
    s
}

/// Lower-cased set of pool `address = "..."` values already present in the file
/// (empty if the file is missing/unreadable). Used to keep admission idempotent.
fn read_existing_addresses(path: &Path) -> HashSet<String> {
    let mut set = HashSet::new();
    if let Ok(contents) = std::fs::read_to_string(path) {
        for line in contents.lines() {
            let t = line.trim();
            if let Some(rest) = t.strip_prefix("address") {
                if let Some(open) = rest.find('"') {
                    if let Some(len) = rest[open + 1..].find('"') {
                        set.insert(rest[open + 1..open + 1 + len].to_lowercase());
                    }
                }
            }
        }
    }
    set
}

/// Append admitted pool entries to `pools.toml`, skipping any address already
/// present in the file or earlier in `entries` (so admission is idempotent and
/// re-running the gate never duplicates a pool). Returns how many were written.
///
/// Caller must trigger a config reload afterwards (`ControlService.ReloadConfig`
/// / `AetherEngine::bootstrap_pools`) for the appended pools to take effect.
pub fn append_admitted_pools(path: &Path, entries: &[PoolEntryToml]) -> std::io::Result<usize> {
    let mut seen = read_existing_addresses(path);
    let mut blocks = String::new();
    let mut appended = 0usize;
    for e in entries {
        let key = e.address.to_checksum(None).to_lowercase();
        if !seen.insert(key) {
            continue; // already in the file or earlier in this batch
        }
        blocks.push('\n');
        blocks.push_str(&format_pool_entry(e));
        appended += 1;
    }
    if appended > 0 {
        let mut f = OpenOptions::new().create(true).append(true).open(path)?;
        writeln!(
            f,
            "\n# ---------------------------------------------------------------------------"
        )?;
        writeln!(f, "# Auto-discovered pools (hot-token admission gate)")?;
        writeln!(
            f,
            "# ---------------------------------------------------------------------------"
        )?;
        f.write_all(blocks.as_bytes())?;
    }
    Ok(appended)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn addr(b: u8) -> Address {
        Address::repeat_byte(b)
    }

    fn venue(protocol: ProtocolType, a: u8, liq: f64) -> CandidateVenue {
        CandidateVenue {
            protocol,
            address: addr(a),
            token0: addr(0xAA),
            token1: addr(0xBB),
            fee_bps: 30,
            tick_spacing: None,
            liquidity_usd: liq,
        }
    }

    fn cfg() -> AdmissionConfig {
        AdmissionConfig {
            min_venues: 2,
            min_liquidity_usd: 25_000.0,
            require_fot_clean: true,
            tier: "warm".into(),
        }
    }

    #[test]
    fn rejects_when_fot_not_clean() {
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100_000.0),
            venue(ProtocolType::SushiSwap, 2, 100_000.0),
        ];
        let out = evaluate(&venues, &FotVerdict::FeeOnTransfer { tax_bps: 500 }, &cfg());
        assert!(matches!(out, AdmissionOutcome::Reject { .. }));
    }

    #[test]
    fn rejects_when_too_few_qualified_venues() {
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100_000.0),
            venue(ProtocolType::SushiSwap, 2, 1_000.0),
        ];
        let out = evaluate(&venues, &FotVerdict::Clean { observed_tax_bps: 0 }, &cfg());
        assert!(matches!(out, AdmissionOutcome::Reject { .. }));
    }

    #[test]
    fn admits_two_qualified_clean_venues() {
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100_000.0),
            venue(ProtocolType::SushiSwap, 2, 50_000.0),
        ];
        let out = evaluate(&venues, &FotVerdict::Clean { observed_tax_bps: 0 }, &cfg());
        match out {
            AdmissionOutcome::Admit(entries) => {
                assert_eq!(entries.len(), 2);
                assert_eq!(entries[0].tier, "warm");
            }
            other => panic!("expected admit, got {other:?}"),
        }
    }

    #[test]
    fn format_v2_entry_matches_schema() {
        let e = PoolEntryToml {
            protocol: ProtocolType::UniswapV2,
            address: addr(0x11),
            token0: addr(0xAA),
            token1: addr(0xBB),
            fee_bps: 30,
            tick_spacing: None,
            tier: "warm".into(),
        };
        let toml = format_pool_entry(&e);
        assert!(toml.contains("protocol = \"uniswap_v2\""));
        assert!(toml.contains("fee_bps = 30"));
        assert!(toml.contains("tier = \"warm\""));
        assert!(!toml.contains("tick_spacing"));
        #[derive(serde::Deserialize)]
        struct P {
            pools: Vec<Entry>,
        }
        #[derive(serde::Deserialize)]
        #[allow(dead_code)]
        struct Entry {
            protocol: String,
            address: String,
            token0: String,
            token1: String,
            fee_bps: u32,
        }
        let parsed: P = toml::from_str(&toml).expect("admitted entry must parse");
        assert_eq!(parsed.pools.len(), 1);
        assert_eq!(parsed.pools[0].protocol, "uniswap_v2");
        assert_eq!(parsed.pools[0].fee_bps, 30);
    }

    #[test]
    fn format_v3_entry_includes_tick_spacing() {
        let e = PoolEntryToml {
            protocol: ProtocolType::UniswapV3,
            address: addr(0x11),
            token0: addr(0xAA),
            token1: addr(0xBB),
            fee_bps: 5,
            tick_spacing: Some(10),
            tier: "warm".into(),
        };
        let toml = format_pool_entry(&e);
        assert!(toml.contains("protocol = \"uniswap_v3\""));
        assert!(toml.contains("tick_spacing = 10"));
    }

    #[test]
    fn append_dedups_against_file_and_batch() {
        let tmp = tempfile::NamedTempFile::new().unwrap();
        let existing = addr(0x11);
        std::fs::write(
            tmp.path(),
            format!(
                "[[pools]]\nprotocol = \"uniswap_v2\"\naddress = \"{}\"\ntoken0 = \"{}\"\ntoken1 = \"{}\"\nfee_bps = 30\ntier = \"hot\"\n",
                existing.to_checksum(None).to_lowercase(),
                addr(0xAA).to_checksum(None),
                addr(0xBB).to_checksum(None),
            ),
        )
        .unwrap();

        let entries = vec![
            PoolEntryToml {
                protocol: ProtocolType::UniswapV2,
                address: existing,
                token0: addr(0xAA),
                token1: addr(0xBB),
                fee_bps: 30,
                tick_spacing: None,
                tier: "warm".into(),
            },
            PoolEntryToml {
                protocol: ProtocolType::SushiSwap,
                address: addr(0x22),
                token0: addr(0xAA),
                token1: addr(0xBB),
                fee_bps: 30,
                tick_spacing: None,
                tier: "warm".into(),
            },
            PoolEntryToml {
                protocol: ProtocolType::SushiSwap,
                address: addr(0x22),
                token0: addr(0xAA),
                token1: addr(0xBB),
                fee_bps: 30,
                tick_spacing: None,
                tier: "warm".into(),
            },
        ];

        let n = append_admitted_pools(tmp.path(), &entries).unwrap();
        assert_eq!(n, 1, "only the one genuinely-new pool should be appended");

        let n2 = append_admitted_pools(tmp.path(), &entries).unwrap();
        assert_eq!(n2, 0);

        let final_contents = std::fs::read_to_string(tmp.path()).unwrap();
        assert_eq!(
            final_contents.matches("[[pools]]").count(),
            2,
            "file should hold exactly the original + one admitted pool"
        );
    }

    #[test]
    fn protocol_toml_str_all_variants() {
        assert_eq!(protocol_toml_str(ProtocolType::UniswapV2), "uniswap_v2");
        assert_eq!(protocol_toml_str(ProtocolType::UniswapV3), "uniswap_v3");
        assert_eq!(protocol_toml_str(ProtocolType::SushiSwap), "sushiswap");
        assert_eq!(protocol_toml_str(ProtocolType::Curve), "curve");
        assert_eq!(protocol_toml_str(ProtocolType::BalancerV2), "balancer_v2");
        assert_eq!(protocol_toml_str(ProtocolType::BalancerV3), "balancer_v3");
        assert_eq!(protocol_toml_str(ProtocolType::BancorV3), "bancor_v3");
    }

    #[test]
    fn admission_config_default_values() {
        let d = AdmissionConfig::default();
        assert_eq!(d.min_venues, 2);
        assert_eq!(d.min_liquidity_usd, 25_000.0);
        assert!(d.require_fot_clean);
        assert_eq!(d.tier, "warm");
    }

    #[test]
    fn evaluate_fot_not_clean_honeypot() {
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100_000.0),
            venue(ProtocolType::SushiSwap, 2, 100_000.0),
        ];
        let out = evaluate(&venues, &FotVerdict::Honeypot { reason: "transfer failed".into() }, &cfg());
        assert!(matches!(out, AdmissionOutcome::Reject { .. }));
    }

    #[test]
    fn evaluate_fot_not_clean_inconclusive() {
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100_000.0),
            venue(ProtocolType::SushiSwap, 2, 100_000.0),
        ];
        let out = evaluate(
            &venues,
            &FotVerdict::Inconclusive { reason: "slot not found".into() },
            &cfg(),
        );
        assert!(matches!(out, AdmissionOutcome::Reject { .. }));
    }

    #[test]
    fn evaluate_fot_clean_bypasses_requirement() {
        let mut c = cfg();
        c.require_fot_clean = false;
        // Only 1 venue above threshold — would normally reject, but let's
        // give it 2 venues both below the liquidity floor to test the
        // liquidity path separately.
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100_000.0),
            venue(ProtocolType::SushiSwap, 2, 100_000.0),
        ];
        let out = evaluate(&venues, &FotVerdict::FeeOnTransfer { tax_bps: 100 }, &c);
        // fot_clean=false means FOT check is skipped; both venues are above
        // the liquidity floor so we should get Admit.
        assert!(matches!(out, AdmissionOutcome::Admit(_)));
    }

    #[test]
    fn evaluate_zero_venues() {
        let out = evaluate(&[], &FotVerdict::Clean { observed_tax_bps: 0 }, &cfg());
        match out {
            AdmissionOutcome::Reject { reason } => {
                assert!(reason.contains("0 venue"));
            }
            other => panic!("expected reject, got {other:?}"),
        }
    }

    #[test]
    fn evaluate_all_venues_below_liquidity_floor() {
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100.0),
            venue(ProtocolType::SushiSwap, 2, 200.0),
            venue(ProtocolType::Curve, 3, 50.0),
        ];
        let out = evaluate(&venues, &FotVerdict::Clean { observed_tax_bps: 0 }, &cfg());
        match out {
            AdmissionOutcome::Reject { reason } => {
                assert!(reason.contains("0 venue"));
                assert!(reason.contains("25000"));
            }
            other => panic!("expected reject, got {other:?}"),
        }
    }

    #[test]
    fn evaluate_exactly_min_venues_just_above_floor() {
        let mut c = cfg();
        c.min_venues = 1;
        c.min_liquidity_usd = 10_000.0;
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 10_000.0),
            venue(ProtocolType::SushiSwap, 2, 5_000.0),
        ];
        let out = evaluate(&venues, &FotVerdict::Clean { observed_tax_bps: 0 }, &c);
        // Only 1 venue exactly at the floor qualifies.
        match out {
            AdmissionOutcome::Admit(entries) => assert_eq!(entries.len(), 1),
            other => panic!("expected admit with 1 entry, got {other:?}"),
        }
    }

    #[test]
    fn candidate_venue_to_entry_preserves_fields() {
        let v = CandidateVenue {
            protocol: ProtocolType::Curve,
            address: addr(0x42),
            token0: addr(0x01),
            token1: addr(0x02),
            fee_bps: 400,
            tick_spacing: None,
            liquidity_usd: 50_000.0,
        };
        let entry = v.to_entry("hot");
        assert_eq!(entry.protocol, ProtocolType::Curve);
        assert_eq!(entry.address, addr(0x42));
        assert_eq!(entry.token0, addr(0x01));
        assert_eq!(entry.token1, addr(0x02));
        assert_eq!(entry.fee_bps, 400);
        assert_eq!(entry.tier, "hot");
        assert!(entry.tick_spacing.is_none());
    }

    #[test]
    fn candidate_venue_to_entry_with_tick_spacing() {
        let v = CandidateVenue {
            protocol: ProtocolType::UniswapV3,
            address: addr(0x55),
            token0: addr(0x03),
            token1: addr(0x04),
            fee_bps: 5,
            tick_spacing: Some(60),
            liquidity_usd: 100_000.0,
        };
        let entry = v.to_entry("cold");
        assert_eq!(entry.tick_spacing, Some(60));
        assert_eq!(entry.tier, "cold");
    }

    #[test]
    fn format_entry_all_protocols() {
        for proto in [
            ProtocolType::UniswapV2,
            ProtocolType::UniswapV3,
            ProtocolType::SushiSwap,
            ProtocolType::Curve,
            ProtocolType::BalancerV2,
            ProtocolType::BalancerV3,
            ProtocolType::BancorV3,
        ] {
            let e = PoolEntryToml {
                protocol: proto,
                address: addr(0x11),
                token0: addr(0xAA),
                token1: addr(0xBB),
                fee_bps: 30,
                tick_spacing: None,
                tier: "warm".into(),
            };
            let toml = format_pool_entry(&e);
            assert!(
                toml.contains(&format!("protocol = \"{}\"", protocol_toml_str(proto))),
                "missing protocol for {proto:?}"
            );
            assert!(toml.contains("[[pools]]"));
        }
    }

    #[test]
    fn format_pool_entry_starts_with_pools_block() {
        let e = PoolEntryToml {
            protocol: ProtocolType::UniswapV2,
            address: addr(0x01),
            token0: addr(0xAA),
            token1: addr(0xBB),
            fee_bps: 30,
            tick_spacing: None,
            tier: "hot".into(),
        };
        let toml = format_pool_entry(&e);
        assert!(toml.starts_with("[[pools]]\n"));
    }

    #[test]
    fn append_to_new_file_creates_and_writes() {
        let tmp = tempfile::NamedTempFile::new().unwrap();
        let path = tmp.path().with_extension("toml");
        let entries = vec![
            PoolEntryToml {
                protocol: ProtocolType::UniswapV2,
                address: addr(0x01),
                token0: addr(0xAA),
                token1: addr(0xBB),
                fee_bps: 30,
                tick_spacing: None,
                tier: "warm".into(),
            },
            PoolEntryToml {
                protocol: ProtocolType::SushiSwap,
                address: addr(0x02),
                token0: addr(0xAA),
                token1: addr(0xBB),
                fee_bps: 30,
                tick_spacing: None,
                tier: "warm".into(),
            },
        ];
        let n = append_admitted_pools(&path, &entries).unwrap();
        assert_eq!(n, 2);
        let contents = std::fs::read_to_string(&path).unwrap();
        assert_eq!(contents.matches("[[pools]]").count(), 2);
        assert!(contents.contains("Auto-discovered pools"));
    }

    #[test]
    fn append_empty_entries_noop() {
        let tmp = tempfile::NamedTempFile::new().unwrap();
        let original = std::fs::read_to_string(tmp.path()).unwrap();
        let n = append_admitted_pools(tmp.path(), &[]).unwrap();
        assert_eq!(n, 0);
        assert_eq!(std::fs::read_to_string(tmp.path()).unwrap(), original);
    }

    #[test]
    fn read_existing_addresses_nonexistent_file() {
        let set = read_existing_addresses(Path::new("/tmp/nonexistent_pools_file_aether_test.toml"));
        assert!(set.is_empty());
    }

    #[test]
    fn read_existing_addresses_malformed_lines() {
        let tmp = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(
            tmp.path(),
            "random text\nno quotes here\naddress = \"0xABC\"\naddress no quotes\n",
        )
        .unwrap();
        let set = read_existing_addresses(tmp.path());
        assert_eq!(set.len(), 1);
        assert!(set.contains("0xabc"));
    }

    #[test]
    fn read_existing_addresses_empty_file() {
        let tmp = tempfile::NamedTempFile::new().unwrap();
        std::fs::write(tmp.path(), "").unwrap();
        let set = read_existing_addresses(tmp.path());
        assert!(set.is_empty());
    }

    #[test]
    fn evaluate_fot_clean_observed_tax_bps_preserved() {
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100_000.0),
            venue(ProtocolType::SushiSwap, 2, 100_000.0),
        ];
        let fot = FotVerdict::Clean { observed_tax_bps: 15 };
        let out = evaluate(&venues, &fot, &cfg());
        assert!(matches!(out, AdmissionOutcome::Admit(_)));
    }

    #[test]
    fn evaluate_fot_fee_on_transfer_tax_bps_in_reject_reason() {
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 100_000.0),
            venue(ProtocolType::SushiSwap, 2, 100_000.0),
        ];
        let out = evaluate(
            &venues,
            &FotVerdict::FeeOnTransfer { tax_bps: 300 },
            &cfg(),
        );
        match out {
            AdmissionOutcome::Reject { reason } => {
                assert!(reason.contains("fee-on-transfer"));
            }
            other => panic!("expected reject, got {other:?}"),
        }
    }

    #[test]
    fn evaluate_venues_with_varied_liquidity() {
        let mut c = cfg();
        c.min_venues = 3;
        c.min_liquidity_usd = 10_000.0;
        let venues = vec![
            venue(ProtocolType::UniswapV2, 1, 50_000.0),
            venue(ProtocolType::SushiSwap, 2, 30_000.0),
            venue(ProtocolType::Curve, 3, 10_000.0),
            venue(ProtocolType::BalancerV2, 4, 5_000.0),
        ];
        let out = evaluate(&venues, &FotVerdict::Clean { observed_tax_bps: 0 }, &c);
        match out {
            AdmissionOutcome::Admit(entries) => assert_eq!(entries.len(), 3),
            other => panic!("expected admit with 3, got {other:?}"),
        }
    }

    #[test]
    fn pool_entry_toml_partial_eq() {
        let a = PoolEntryToml {
            protocol: ProtocolType::UniswapV2,
            address: addr(0x01),
            token0: addr(0xAA),
            token1: addr(0xBB),
            fee_bps: 30,
            tick_spacing: None,
            tier: "warm".into(),
        };
        let b = a.clone();
        assert_eq!(a, b);
    }

    #[test]
    fn admission_outcome_clone() {
        let admit = AdmissionOutcome::Admit(vec![PoolEntryToml {
            protocol: ProtocolType::UniswapV2,
            address: addr(0x01),
            token0: addr(0xAA),
            token1: addr(0xBB),
            fee_bps: 30,
            tick_spacing: None,
            tier: "warm".into(),
        }]);
        let _cloned = admit.clone();
    }
}
