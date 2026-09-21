#!/usr/bin/env bash
# Upload the packaged microservice to Cumulocity and subscribe the tenant.
#
# Needs C8Y_BASEURL, C8Y_TENANT, C8Y_USER and C8Y_PASSWORD of a user allowed to
# manage applications. Idempotent: re-running uploads a new binary to the
# existing application, which the platform rolls out.
set -euo pipefail

cd "$(dirname "$0")"

manifest=cumulocity.json
name=$(sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -1)
version=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -1)
zipfile=${1:-$name-$version.zip}

: "${C8Y_BASEURL:?}" "${C8Y_TENANT:?}" "${C8Y_USER:?}" "${C8Y_PASSWORD:?}"
[ -r "$zipfile" ] || { echo "no $zipfile; run ./package-microservice.sh first" >&2; exit 1; }

base=${C8Y_BASEURL%/}
auth=(--user "$C8Y_TENANT/$C8Y_USER:$C8Y_PASSWORD")
api() { curl -sS "${auth[@]}" -H 'Accept: application/json' "$@"; }

echo ">> looking up application $name"
app=$(api "$base/application/applicationsByName/$name")
id=$(printf '%s' "$app" | sed -n 's/.*"id":"\([0-9]*\)".*/\1/p' | head -1)

if [ -z "$id" ]; then
  echo ">> creating application"
  created=$(api -X POST -H 'Content-Type: application/json' \
    -d "{\"name\":\"$name\",\"key\":\"$name-key\",\"contextPath\":\"$name\",\"type\":\"MICROSERVICE\"}" \
    "$base/application/applications")
  id=$(printf '%s' "$created" | sed -n 's/.*"id":"\([0-9]*\)".*/\1/p' | head -1)
  [ -n "$id" ] || { echo "create failed: $created" >&2; exit 1; }
fi
echo "   application id $id"

echo ">> uploading $zipfile ($(du -h "$zipfile" | cut -f1))"
code=$(curl -sS "${auth[@]}" -o /tmp/c8y-upload.json -w '%{http_code}' \
  -F "file=@$zipfile" "$base/application/applications/$id/binaries")
echo "   HTTP $code"
[ "$code" = 201 ] || { cat /tmp/c8y-upload.json >&2; echo; exit 1; }

echo ">> subscribing tenant $C8Y_TENANT"
code=$(curl -sS "${auth[@]}" -o /tmp/c8y-subscribe.json -w '%{http_code}' \
  -X POST -H 'Content-Type: application/json' \
  -d "{\"application\":{\"id\":\"$id\"}}" \
  "$base/tenant/tenants/$C8Y_TENANT/applications")
case "$code" in
  201) echo "   subscribed" ;;
  409) echo "   already subscribed" ;;
  *)   cat /tmp/c8y-subscribe.json >&2; echo; exit 1 ;;
esac

rm -f /tmp/c8y-upload.json /tmp/c8y-subscribe.json
echo ">> done: $base/service/$name/sse"
