#!/usr/bin/env node

import { mkdirSync, writeFileSync } from "fs";
import { join } from "path";

const MODE = process.env.TEST_MODE || "quick";
const SEPOLIA_RPC = process.env.SEPOLIA_RPC_URL;
const PRIVATE_KEY = process.env.METAMASK_PRIVATE_KEY;
const WALLET_ADDRESS = process.env.METAMASK_ADDRESS;
const OUTPUT_DIR = process.env.REPORT_DIR || "/tmp/sepolia-report";
const CONTRACT_ADDRESS = process.env.CONTRACT_ADDRESS || "";

const isExtended = MODE === "extended";
const RUN_DURATION_MS = isExtended ? 7200000 : 900000;
const RUN_LABEL = isExtended ? "extended" : "quick";

const report = {
  workflow: "MetaMask Sepolia",
  mode: RUN_LABEL,
  startTime: new Date().toISOString(),
  finishTime: "",
  totalDuration: 0,
  testSummary: { total: 0, passed: 0, failed: 0, successPct: 0, failurePct: 0 },
  blockchain: {
    networkStatus: "",
    walletAddress: WALLET_ADDRESS,
    startingBalance: "0",
    endingBalance: "0",
    balanceDiff: "0",
    transactions: 0,
    confirmed: 0,
    failed: 0,
  },
  financial: {
    totalGasConsumed: "0",
    avgGasUsage: "0",
    maxGasUsage: "0",
    minGasUsage: "0",
    estimatedFees: "0",
    totalCost: "0",
  },
  performance: {
    avgRpcMs: 0,
    avgConfirmationMs: 0,
    peakMs: 0,
    slowestTx: 0,
    fastestTx: 0,
    throughputTpm: 0,
  },
  errors: [],
  overall: "PASSED",
  gasPerTx: [],
  rpcLatencies: [],
  confirmTimes: [],
};

function log(msg) {
  process.stdout.write(`[${new Date().toISOString()}] ${msg}\n`);
}

function logError(msg) {
  process.stderr.write(`[${new Date().toISOString()}] ERROR: ${msg}\n`);
}

function elapsed(start) {
  return Date.now() - start;
}

function result(name, success, detail) {
  report.testSummary.total++;
  if (success) {
    report.testSummary.passed++;
    log(`  ok ${name}`);
  } else {
    report.testSummary.failed++;
    log(`  FAIL ${name}: ${detail || "failed"}`);
    report.errors.push({ timestamp: new Date().toISOString(), operation: name, error: detail });
  }
}

async function main() {
  log(`Starting Sepolia tests (${RUN_LABEL} mode, ${RUN_DURATION_MS / 1000}s limit)`);
  log(`Wallet: ${WALLET_ADDRESS || "not provided"}`);
  log(`RPC: ${SEPOLIA_RPC ? SEPOLIA_RPC.substring(0, 30) + "..." : "not provided"}`);

  if (!SEPOLIA_RPC) { logError("SEPOLIA_RPC_URL not set"); report.overall = "FAILED"; return; }
  if (!PRIVATE_KEY) { logError("METAMASK_PRIVATE_KEY not set"); report.overall = "FAILED"; return; }

  const { ethers } = await import("ethers");

  // Network Connection
  log("\n[Stage] Connecting to Sepolia...");
  let provider, wallet;
  try {
    provider = new ethers.JsonRpcProvider(SEPOLIA_RPC);
    const net = await provider.getNetwork();
    const chainId = Number(net.chainId);
    log(`  Network: ${net.name} (chainId: ${chainId})`);
    const ok = chainId === 11155111;
    report.blockchain.networkStatus = ok ? "connected" : `wrong chain: ${chainId}`;
    result("Sepolia connection", ok, `expected 11155111, got ${chainId}`);
    if (!ok) { report.overall = "FAILED"; return; }
  } catch (e) {
    report.blockchain.networkStatus = "failed";
    result("Sepolia connection", false, e.message);
    report.overall = "FAILED";
    return;
  }

  // Wallet Initialization
  log("\n[Stage] Initializing wallet...");
  try {
    wallet = new ethers.Wallet(PRIVATE_KEY, provider);
    const addr = await wallet.getAddress();
    if (WALLET_ADDRESS && addr.toLowerCase() !== WALLET_ADDRESS.toLowerCase()) {
      logError(`  Address mismatch: derived ${addr}, expected ${WALLET_ADDRESS}`);
      result("Wallet init", false, "address mismatch");
      report.overall = "FAILED";
      return;
    }
    report.blockchain.walletAddress = addr;
    result("Wallet init", true);
  } catch (e) {
    result("Wallet init", false, e.message);
    report.overall = "FAILED";
    return;
  }

  // Balance Check
  log("\n[Stage] Checking balance...");
  let startBalance;
  try {
    startBalance = await provider.getBalance(wallet.address);
    report.blockchain.startingBalance = ethers.formatEther(startBalance);
    log(`  Balance: ${report.blockchain.startingBalance} ETH`);
    result("Balance check", true);
  } catch (e) {
    result("Balance check", false, e.message);
    startBalance = 0n;
  }

  // Contract Interaction
  log("\n[Stage] Contract interaction...");
  const hasContract = CONTRACT_ADDRESS && ethers.isAddress(CONTRACT_ADDRESS);
  if (hasContract) {
    try {
      const code = await provider.getCode(CONTRACT_ADDRESS);
      result("Contract check", code !== "0x", code !== "0x" ? "deployed" : "no bytecode");
    } catch (e) {
      result("Contract check", false, e.message);
    }
  } else {
    log("  Skipping contract — no address provided");
  }

  // Blockchain State
  log("\n[Stage] Reading blockchain state...");
  try {
    const block = await provider.getBlock("latest");
    result("Latest block", !!block, block ? `#${block.number}` : "null");
    if (block) log(`  Block #${block.number} gasUsed: ${ethers.formatUnits(block.gasUsed, "wei")}`);
  } catch (e) {
    result("Latest block", false, e.message);
  }
  try {
    const feeData = await provider.getFeeData();
    const gp = feeData.gasPrice ? ethers.formatUnits(feeData.gasPrice, "gwei") : "unknown";
    log(`  Gas price: ${gp} gwei`);
    result("Fee data", true);
  } catch (e) {
    result("Fee data", false, e.message);
  }

  // Transaction Execution
  log(`\n[Stage] Executing transactions (${RUN_LABEL} mode)...`);
  const txCount = isExtended ? 50 : 10;
  let confirmed = 0, failedWallet = 0;
  let totalGas = 0n, minGas = 999999999999n, maxGas = 0n, totalCost = 0n;
  let maxConfirmMs = 0, minConfirmMs = 999999, totalConfirmMs = 0;

  for (let i = 0; i < txCount; i++) {
    const t0 = Date.now();
    try {
      const tx = await wallet.sendTransaction({ to: wallet.address, value: 0n });
      report.blockchain.transactions++;
      result(`Tx ${i + 1}/${txCount} sent`, true, tx.hash.substring(0, 18));

      const receipt = await tx.wait();
      report.blockchain.confirmed++;
      confirmed++;
      const gasUsed = receipt.gasUsed || 0n;
      const gPrice = receipt.gasPrice || 0n;
      const cost = gasUsed * gPrice;
      totalGas += gasUsed;
      totalCost += cost;
      if (gasUsed < minGas) minGas = gasUsed;
      if (gasUsed > maxGas) maxGas = gasUsed;
      const ct = elapsed(t0);
      if (ct > maxConfirmMs) maxConfirmMs = ct;
      if (ct < minConfirmMs) minConfirmMs = ct;
      totalConfirmMs += ct;
      report.gasPerTx.push(Number(gasUsed));
      report.confirmTimes.push(ct);
      log(`    confirmed ${ct}ms | gas: ${gasUsed} | cost: ${ethers.formatEther(cost)} ETH`);
    } catch (e) {
      failedWallet++;
      report.blockchain.failed++;
      result(`Tx ${i + 1}`, false, e.message);
      report.errors.push({ timestamp: new Date().toISOString(), operation: `tx_${i + 1}`, error: e.message });
    }
    if (elapsed(0) > RUN_DURATION_MS * 0.9) {
      log("  Approaching time limit, stopping early...");
      break;
    }
  }

  // Final Balance
  log("\n[Stage] Final balance...");
  try {
    const endBalance = await provider.getBalance(wallet.address);
    report.blockchain.endingBalance = ethers.formatEther(endBalance);
    report.blockchain.balanceDiff = ethers.formatEther(endBalance - startBalance);
    log(`  End: ${report.blockchain.endingBalance} ETH | delta: ${report.blockchain.balanceDiff} ETH`);
    result("Final balance", true);
  } catch (e) {
    result("Final balance", false, e.message);
  }

  // Compute Metrics
  log("\n[Stage] Computing metrics...");
  report.finishTime = new Date().toISOString();
  report.totalDuration = elapsed(0);
  report.testSummary.successPct = report.testSummary.total > 0
    ? Number((report.testSummary.passed / report.testSummary.total * 100).toFixed(2)) : 0;
  report.testSummary.failurePct = Number((100 - report.testSummary.successPct).toFixed(2));

  report.financial.totalGasConsumed = totalGas.toString();
  report.financial.avgGasUsage = report.gasPerTx.length > 0
    ? String(Math.round(report.gasPerTx.reduce((a, b) => a + b, 0) / report.gasPerTx.length)) : "0";
  report.financial.maxGasUsage = maxGas.toString();
  report.financial.minGasUsage = minGas === 999999999999n ? "0" : minGas.toString();
  report.financial.totalCost = ethers.formatEther(totalCost);
  report.financial.estimatedFees = ethers.formatEther(totalCost);

  report.performance.avgConfirmationMs = confirmed > 0 ? Math.round(totalConfirmMs / confirmed) : 0;
  report.performance.slowestTx = maxConfirmMs;
  report.performance.fastestTx = minConfirmMs === 999999 ? 0 : minConfirmMs;
  report.performance.throughputTpm = report.blockchain.transactions > 0 && report.totalDuration > 0
    ? Number((report.blockchain.transactions / (report.totalDuration / 60000)).toFixed(2)) : 0;

  if (report.testSummary.failed > 0 && report.testSummary.passed > 0) {
    report.overall = "PASSED_WITH_WARNINGS";
  } else if (report.testSummary.failed > 0) {
    report.overall = "FAILED";
  }

  // Generate Reports
  log("\n[Stage] Generating reports...");
  mkdirSync(OUTPUT_DIR, { recursive: true });

  writeFileSync(join(OUTPUT_DIR, "report.json"), JSON.stringify(report, null, 2));
  log("  report.json");

  const statusLine = report.overall === "PASSED" ? "PASSED"
    : report.overall === "PASSED_WITH_WARNINGS" ? "PASSED WITH WARNINGS" : "FAILED";

  const md = [
    `# MetaMask Sepolia Test Report`,
    ``,
    `| Field | Value |`,
    `|---|---|`,
    `| Mode | ${report.mode} |`,
    `| Start | ${report.startTime} |`,
    `| Finish | ${report.finishTime} |`,
    `| Duration | ${(report.totalDuration / 1000).toFixed(1)}s |`,
    `| Overall | ${statusLine} |`,
    ``,
    `## Test Summary`,
    `| Metric | Value |`,
    `|---|---|`,
    `| Total | ${report.testSummary.total} |`,
    `| Passed | ${report.testSummary.passed} |`,
    `| Failed | ${report.testSummary.failed} |`,
    `| Success Rate | ${report.testSummary.successPct}% |`,
    ``,
    `## Blockchain`,
    `| Metric | Value |`,
    `|---|---|`,
    `| Network | ${report.blockchain.networkStatus} |`,
    `| Wallet | \`${report.blockchain.walletAddress}\` |`,
    `| Start Balance | ${report.blockchain.startingBalance} ETH |`,
    `| End Balance | ${report.blockchain.endingBalance} ETH |`,
    `| Delta | ${report.blockchain.balanceDiff} ETH |`,
    `| Txs | ${report.blockchain.transactions} |`,
    `| Confirmed | ${report.blockchain.confirmed} |`,
    `| Failed Txs | ${report.blockchain.failed} |`,
    ``,
    `## Financial`,
    `| Metric | Value |`,
    `|---|---|`,
    `| Total Gas | ${report.financial.totalGasConsumed} |`,
    `| Avg Gas/Tx | ${report.financial.avgGasUsage} |`,
    `| Max Gas | ${report.financial.maxGasUsage} |`,
    `| Min Gas | ${report.financial.minGasUsage} |`,
    `| Total Cost | ${report.financial.totalCost} ETH |`,
    ``,
    `## Performance`,
    `| Metric | Value |`,
    `|---|---|`,
    `| Avg Confirmation | ${report.performance.avgConfirmationMs} ms |`,
    `| Slowest Tx | ${report.performance.slowestTx} ms |`,
    `| Fastest Tx | ${report.performance.fastestTx} ms |`,
    `| Throughput | ${report.performance.throughputTpm} tx/min |`,
    ``,
  ];
  if (report.errors.length > 0) {
    md.push(`## Errors`, ``);
    md.push(`| Timestamp | Operation | Error |`);
    md.push(`|---|---|---|`);
    for (const e of report.errors) {
      md.push(`| ${e.timestamp} | ${e.operation} | ${(e.error || "").substring(0, 100)} |`);
    }
    md.push(``);
  }
  writeFileSync(join(OUTPUT_DIR, "report.md"), md.join("\n"));
  log("  report.md");

  const statusClass = report.overall === "PASSED" ? "passed"
    : report.overall === "PASSED_WITH_WARNINGS" ? "warning" : "failed";
  const statusEmoji = report.overall === "PASSED" ? "&#9989;"
    : report.overall === "PASSED_WITH_WARNINGS" ? "&#9888;" : "&#10060;";

  const errorsHtml = report.errors.length > 0
    ? `<h2>Errors</h2><table><tr><th>Timestamp</th><th>Operation</th><th>Error</th></tr>${report.errors.map(e => `<tr><td>${e.timestamp}</td><td>${e.operation}</td><td>${(e.error || "").substring(0, 100)}</td></tr>`).join("")}</table>`
    : "";

  const html = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>MetaMask Sepolia Report</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:960px;margin:40px auto;padding:0 20px;background:#0d1117;color:#c9d1d9}
h1{color:#58a6ff}h2{color:#f0883e;border-bottom:1px solid #30363d;padding-bottom:4px}
table{border-collapse:collapse;width:100%;margin:12px 0}
th,td{text-align:left;padding:8px 12px;border:1px solid #30363d}
th{background:#161b22;color:#8b949e}
.status{font-size:1.4em;padding:12px 20px;border-radius:6px;display:inline-block}
.passed{background:#1b3a2d;color:#3fb950;border:1px solid #3fb950}
.warning{background:#3d2e00;color:#d29922;border:1px solid #d29922}
.failed{background:#3d1117;color:#f85149;border:1px solid #f85149}
.footer{margin-top:40px;color:#484f58;font-size:0.85em}
</style></head>
<body>
<h1>MetaMask Sepolia Test Report</h1>
<p><strong>Mode:</strong> ${report.mode} | <strong>Duration:</strong> ${(report.totalDuration / 1000).toFixed(1)}s</p>
<p class="status ${statusClass}">${statusEmoji} ${statusLine}</p>
<h2>Test Summary</h2>
<table><tr><th>Total</th><th>Passed</th><th>Failed</th><th>Success %</th></tr>
<tr><td>${report.testSummary.total}</td><td>${report.testSummary.passed}</td><td>${report.testSummary.failed}</td><td>${report.testSummary.successPct}%</td></tr></table>
<h2>Blockchain</h2>
<table><tr><th>Metric</th><th>Value</th></tr>
<tr><td>Network</td><td>${report.blockchain.networkStatus}</td></tr>
<tr><td>Wallet</td><td><code>${report.blockchain.walletAddress}</code></td></tr>
<tr><td>Start Balance</td><td>${report.blockchain.startingBalance} ETH</td></tr>
<tr><td>End Balance</td><td>${report.blockchain.endingBalance} ETH</td></tr>
<tr><td>Delta</td><td>${report.blockchain.balanceDiff} ETH</td></tr>
<tr><td>Transactions</td><td>${report.blockchain.transactions}</td></tr>
<tr><td>Confirmed</td><td>${report.blockchain.confirmed}</td></tr>
<tr><td>Failed</td><td>${report.blockchain.failed}</td></tr></table>
<h2>Financial</h2>
<table><tr><th>Metric</th><th>Value</th></tr>
<tr><td>Total Gas</td><td>${report.financial.totalGasConsumed}</td></tr>
<tr><td>Avg Gas/Tx</td><td>${report.financial.avgGasUsage}</td></tr>
<tr><td>Max Gas</td><td>${report.financial.maxGasUsage}</td></tr>
<tr><td>Min Gas</td><td>${report.financial.minGasUsage}</td></tr>
<tr><td>Total Cost</td><td>${report.financial.totalCost} ETH</td></tr></table>
<h2>Performance</h2>
<table><tr><th>Metric</th><th>Value</th></tr>
<tr><td>Avg Confirmation</td><td>${report.performance.avgConfirmationMs} ms</td></tr>
<tr><td>Slowest Tx</td><td>${report.performance.slowestTx} ms</td></tr>
<tr><td>Fastest Tx</td><td>${report.performance.fastestTx} ms</td></tr>
<tr><td>Throughput</td><td>${report.performance.throughputTpm} tx/min</td></tr></table>
${errorsHtml}
<div class="footer">Generated by Aether MetaMask Sepolia CI/CD — ${report.finishTime}</div>
</body></html>`;
  writeFileSync(join(OUTPUT_DIR, "report.html"), html);
  log("  report.html");

  const durationSec = (report.totalDuration / 1000).toFixed(1);
  log(`\n${"=".repeat(60)}`);
  log(`  Overall: ${report.overall}`);
  log(`  Duration: ${durationSec}s`);
  log(`  Tests: ${report.testSummary.passed}/${report.testSummary.total} passed`);
  log(`  Txs: ${report.blockchain.confirmed}/${report.blockchain.transactions} confirmed`);
  log(`  Cost: ${report.financial.totalCost} ETH`);
  log(`${"=".repeat(60)}`);

  if (report.overall === "FAILED") process.exit(1);
}

main().catch(e => { logError("Fatal: " + e.message); process.exit(1); });
