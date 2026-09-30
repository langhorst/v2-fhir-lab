#!/bin/sh
# Write each channel in this directory back from the engine, so edits made in
# the web administrator can be committed. Same settings as import.sh.
set -eu
cd "$(dirname "$0")"
api="${OIE_URL:-https://localhost:8443}/api"
auth="${OIE_USER:-admin}:${OIE_PASSWORD:-admin}"

call() { curl -fsSk -u "$auth" -H 'X-Requested-With: OpenAPI' "$@"; }

for f in *.xml; do
	id=$(sed -n 's:^  <id>\(.*\)</id>$:\1:p' "$f")
	call -H 'Accept: application/xml' "$api/channels/$id" -o "$f.tmp"
	mv "$f.tmp" "$f"
	echo "exported: $f"
done
