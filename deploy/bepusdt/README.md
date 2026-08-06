# Hakimi BEpusdt deployment

This directory contains the non-secret deployment configuration for the
Hakimi USDT-BEP20 payment gateway.

## Security boundary

Never commit any of the following:

- wallet addresses used by production;
- BEpusdt API tokens or Sub2API provider keys;
- administrator credentials;
- `sqlite.db`, backups, SSH keys, or RPC credentials.

Those values must remain in the production database or a secret manager.

## Install

Run `install.sh` as root on the payment host. It installs the pinned BEpusdt
release, verifies its SHA-256 checksum, creates a restricted service account,
and binds the service to `127.0.0.1:18080`. It does not expose the service or
change DNS.

After configuring the wallet and API token, verify locally:

```bash
systemctl is-active bepusdt.service
curl -fsS http://127.0.0.1:18080/ >/dev/null
```

Create a signed test order and confirm that its checkout page contains the
expected network and wallet address. Remove that unpaid test order afterward.

## Publish without stopping Sub2API

1. Back up `/var/lib/bepusdt/sqlite.db` and the current Nginx configuration.
2. Install `pay.biuapi.com.nginx` only after its certificate exists.
3. Run `nginx -t`; do not reload when validation fails.
4. Reload Nginx with `systemctl reload nginx`. Do not restart Sub2API.
5. Point `pay.biuapi.com` to the payment host and verify `/submit.php`, the
   checkout page, order query, callback crediting, and the Sub2API health page.
6. Keep the old DNS value and Nginx backup available for immediate rollback.

The Nginx template keeps BEpusdt private and exposes it only through HTTPS.
