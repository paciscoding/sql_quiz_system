#!/usr/bin/env bash
#
# Generate the self-signed TLS certificate the nginx frontend serves.
# Stopgap TLS — see docs/superpowers/specs/2026-07-17-https-selfsigned-stopgap-design.md
#
#   ./scripts/gen-selfsigned-cert.sh <server-ip> [--force]
#
# Writes cert.pem + key.pem into CERT_DIR (default /opt/nginx/certs; override with
# CERT_DIR=...). Refuses to overwrite an existing cert.pem without --force, because
# regenerating invalidates the certificate exception every student's browser stored.
set -euo pipefail

CERT_DIR="${CERT_DIR:-/opt/nginx/certs}"
FORCE=0
IP=""
for arg in "$@"; do
  case "$arg" in
    --force) FORCE=1 ;;
    -*) echo "unknown option: $arg" >&2; exit 2 ;;
    *) IP="$arg" ;;
  esac
done

if [ -z "$IP" ]; then
  echo "usage: $0 <server-ip> [--force]" >&2
  exit 2
fi

CRT="$CERT_DIR/cert.pem"
KEY="$CERT_DIR/key.pem"

if [ -e "$CRT" ] && [ "$FORCE" -ne 1 ]; then
  echo "error: $CRT already exists; pass --force to overwrite." >&2
  echo "       (regenerating invalidates the cert exception every browser has stored)" >&2
  exit 1
fi

mkdir -p "$CERT_DIR"

# EC P-256, 825-day self-signed cert. The IP SAN is mandatory: modern browsers
# ignore the CN field and hard-fail with NO click-through if no SAN matches the host.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
  -keyout "$KEY" -out "$CRT" -days 825 \
  -subj "/CN=$IP" \
  -addext "subjectAltName=IP:$IP" \
  -addext "keyUsage=digitalSignature" \
  -addext "extendedKeyUsage=serverAuth"

chmod 600 "$KEY"
chmod 644 "$CRT"

echo ">> wrote $CRT and $KEY (SAN=IP:$IP, EC P-256, 825-day)"
openssl x509 -in "$CRT" -noout -subject -ext subjectAltName
