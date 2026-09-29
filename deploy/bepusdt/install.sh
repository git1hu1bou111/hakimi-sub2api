#!/usr/bin/env bash
set -euo pipefail

version="1.24.1"
archive="linux-amd64-BEpusdt.tar.gz"
release_url="https://github.com/v03413/BEpusdt/releases/download/v${version}/${archive}"
expected_sha256="6b74f8a774db8dcf0df11d72a3cb6b888bb3edda96211630e783aeef29293027"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if ss -H -lnt "sport = :18080" 2>/dev/null | grep -q .; then
    echo "ERROR: port 18080 is already in use" >&2
    exit 1
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

curl -fL --retry 3 --connect-timeout 10 -o "${work_dir}/${archive}" "$release_url"
actual_sha256="$(sha256sum "${work_dir}/${archive}" | awk '{print $1}')"
if [[ "$actual_sha256" != "$expected_sha256" ]]; then
    echo "ERROR: checksum mismatch: ${actual_sha256}" >&2
    exit 1
fi

tar -xzf "${work_dir}/${archive}" -C "$work_dir"
if ! getent passwd bepusdt >/dev/null; then
    useradd --system --home-dir /var/lib/bepusdt --shell /usr/sbin/nologin bepusdt
fi

install -d -o root -g root -m 0755 /opt/bepusdt
install -d -o bepusdt -g bepusdt -m 0750 /var/lib/bepusdt /var/log/bepusdt
install -o root -g root -m 0755 "${work_dir}/bepusdt" /opt/bepusdt/bepusdt
install -o root -g root -m 0644 "${script_dir}/bepusdt.service" /etc/systemd/system/bepusdt.service

printf '%s\n' \
    'LISTEN=127.0.0.1:18080' \
    'LOG=/var/log/bepusdt/' \
    'SQLITE=/var/lib/bepusdt/sqlite.db' \
    > /etc/bepusdt.env
chown root:root /etc/bepusdt.env
chmod 0640 /etc/bepusdt.env

systemctl daemon-reload
systemctl enable --now bepusdt.service
systemctl is-active --quiet bepusdt.service
curl -fsS --max-time 10 http://127.0.0.1:18080/ >/dev/null

echo "BEpusdt ${version} is running on 127.0.0.1:18080"
echo "Configure the wallet and API token in the server-side database or admin UI."
