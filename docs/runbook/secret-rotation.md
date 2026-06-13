# Secret Rotation Runbook

Rotate credentials without downtime using a two-stage update: deploy the new secret alongside the old, verify, then revoke the old.

## AETHER_ADMIN_TOKEN

1. Generate: `openssl rand -hex 32`
2. Set new token in executor env / `deploy/docker/.env`
3. Update telebot `AETHER_ADMIN_TOKEN` to match
4. Restart executor + telebot (admin calls fail only during restart window)
5. Revoke knowledge of old token

## AETHER_BACKRUN_CONFIRM_TOKEN

Same procedure as admin token. Required for `POST /admin/backrun/promote`.

## AETHER_RESET_CONFIRM_TOKEN (optional)

If set, `/admin/reset` requires `X-Aether-Reset-Confirm` matching this value (or the admin token). Rotate together with admin token.

## Signer encryption key

1. On signer host: `aether-signer encrypt --new-key <path>`
2. Re-encrypt `signer.key.enc` with new passphrase
3. Deploy new `SIGNER_PASSPHRASE` to signer service only
4. Restart signer; executor reconnects via pooled UDS automatically
5. Securely delete old key material

## Builder API keys (Flashbots, Titan, Eden, rsync)

1. Issue new key with builder dashboard
2. Update `config/builders.yaml` `auth_key` for affected builder
3. `docker compose restart aether-go` (or rolling restart)
4. Verify `aether_executor_builder_submissions_total{result="success"}` for that builder
5. Revoke old key at builder

## Redis / Postgres passwords

1. Create new DB user/password or rotate Redis ACL
2. Update `REDIS_URL` / `DATABASE_URL` in `.env`
3. Rolling restart: executor → telebot → monitor
4. Confirm `redis_connected=1` and ledger writes succeed

## gRPC mTLS certificates

1. Generate new cert/key pair (same CA or new CA with overlap period)
2. Deploy server cert to Rust engine, client cert to Go executor
3. Set `GRPC_TLS_CERT`, `GRPC_TLS_KEY`, `GRPC_TLS_CA`
4. Restart both services within overlap window
5. Remove expired certs from trust stores

## Verification checklist

- [ ] Admin `/health` returns 200
- [ ] Telebot `/dashboard` updates
- [ ] Bundle submission succeeds (shadow mode OK)
- [ ] No `signer_error` or `401` in logs
