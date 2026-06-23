export function generateReport(data, format = 'html') {
  const { summary, blockchain, backend, errors } = data;
  const timestamp = new Date().toISOString();

  if (format === 'md') {
    return `
# Aether E2E Test Report

**Date:** ${timestamp}
**Duration:** ${summary.totalDurationSeconds} s
**Status:** ${summary.success ? '✅ SUCCESS' : '❌ FAILED'}

## Blockchain (On-Chain)
- Initial balance: ${blockchain.initialBalance} ETH
- Final balance: ${blockchain.finalBalance} ETH
- Transactions: ${summary.totalTransactions}
- Total gas used: ${summary.totalGasUsed}
- Total gas cost: ${summary.totalCostEth.toFixed(6)} ETH

### Transactions
${blockchain.transactions.map((tx, i) =>
  `#### Tx ${i+1}
  - Hash: ${tx.hash}
  - Block: ${tx.blockNumber}
  - Gas: ${tx.gasUsed}
  - Gas price: ${tx.effectiveGasPrice} wei`
).join('\n')}

## Backend (Off-Chain)
- Requests: ${summary.backendRequestsCount}
- DB state: ${backend.dbState ? '✅ received' : '❌ not received'}
- S3 state: ${backend.s3State ? '✅ received' : '❌ not received'}

### Requests
${backend.requests.map(r => `- ${r.endpoint} → ${r.status}`).join('\n')}

## Errors
${errors.length ? errors.map(e => `- **${e.where}**: ${e.message}`).join('\n') : '✅ No errors'}

---
*Generated automatically by CI/CD.*
    `;
  }

  if (format === 'html') {
    return `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>Aether E2E Report</title>
<style>
body{font-family:sans-serif;padding:20px;background:#f5f5f5;}
.container{max-width:1200px;margin:auto;background:white;padding:30px;border-radius:8px;}
h1{color:#333;}
.success{color:green;}.error{color:red;}
table{border-collapse:collapse;width:100%;margin:10px 0;}
th,td{border:1px solid #ddd;padding:8px;text-align:left;}
th{background:#f0f0f0;}
</style>
</head>
<body>
<div class="container">
<h1>Aether E2E Test Report</h1>
<p><strong>Date:</strong> ${timestamp}</p>
<p><strong>Duration:</strong> ${summary.totalDurationSeconds} s</p>
<p><strong>Status:</strong> <span class="${summary.success ? 'success' : 'error'}">${summary.success ? '✅ SUCCESS' : '❌ FAILED'}</span></p>

<h2>Blockchain (On-Chain)</h2>
<ul>
<li>Initial balance: ${blockchain.initialBalance} ETH</li>
<li>Final balance: ${blockchain.finalBalance} ETH</li>
<li>Transactions: ${summary.totalTransactions}</li>
<li>Total gas: ${summary.totalGasUsed}</li>
<li>Gas cost: ${summary.totalCostEth.toFixed(6)} ETH</li>
</ul>

<h3>Transactions</h3>
<table><tr><th>#</th><th>Hash</th><th>Block</th><th>Gas</th><th>Gas Price</th></tr>
${blockchain.transactions.map((tx, i) =>
  `<tr><td>${i+1}</td><td><code>${tx.hash}</code></td><td>${tx.blockNumber}</td><td>${tx.gasUsed}</td><td>${tx.effectiveGasPrice}</td></tr>`
).join('')}
</table>

<h2>Backend (Off-Chain)</h2>
<p>Requests: ${summary.backendRequestsCount}</p>
<p>DB state: ${backend.dbState ? '✅ received' : '❌ not received'}</p>
<p>S3 state: ${backend.s3State ? '✅ received' : '❌ not received'}</p>

<h3>Requests</h3>
<table><tr><th>Endpoint</th><th>Status</th></tr>
${backend.requests.map(r => `<tr><td>${r.endpoint}</td><td>${r.status}</td></tr>`).join('')}
</table>

<h2>Errors</h2>
${errors.length ? `<ul>${errors.map(e => `<li><strong>${e.where}</strong>: ${e.message}</li>`).join('')}</ul>` : '<p>✅ No errors</p>'}

<hr>
<p><em>Generated automatically by CI/CD.</em></p>
</div>
</body>
</html>`;
  }
  return '';
}
