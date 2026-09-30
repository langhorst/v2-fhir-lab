#!/bin/sh
# Import (or update) every channel export in this directory, then deploy it.
#
#   engine/oie/channels/import.sh
#
# OIE_URL defaults to https://localhost:8443; OIE_USER / OIE_PASSWORD default to
# admin / admin, so set OIE_PASSWORD once you've changed it.
set -eu
cd "$(dirname "$0")"
api="${OIE_URL:-https://localhost:8443}/api"
auth="${OIE_USER:-admin}:${OIE_PASSWORD:-admin}"

# -k because the engine serves a self-signed certificate.
call() { curl -fsSk -u "$auth" -H 'X-Requested-With: OpenAPI' "$@"; }

for f in *.xml; do
	id=$(sed -n 's:^  <id>\(.*\)</id>$:\1:p' "$f")
	name=$(sed -n 's:^  <name>\(.*\)</name>$:\1:p' "$f")
	call -X PUT -H 'Content-Type: application/xml' --data-binary @"$f" "$api/channels/$id?override=true" >/dev/null
	call -X POST "$api/channels/$id/_deploy" >/dev/null
	echo "deployed: $name ($id)"
done
