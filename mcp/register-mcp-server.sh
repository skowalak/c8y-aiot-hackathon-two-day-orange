#!/usr/bin/env bash
# Register the deployed microservice as an MCP server with the AI Agent Manager.
#
# Needs C8Y_BASEURL, C8Y_TENANT, C8Y_USER and C8Y_PASSWORD. The `ai` application
# has to be subscribed on the tenant. Idempotent: an existing registration of
# the same name is replaced via PUT.
#
#   ./register-mcp-server.sh          register, then list the discovered tools
#   ./register-mcp-server.sh --test   connect and enumerate tools, store nothing
set -euo pipefail

cd "$(dirname "$0")"

manifest=cumulocity.json
service=$(sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -1)

# The name the agent builder shows. Kept free of the deployment suffix.
name=${MCP_REGISTRATION_NAME:-diagnostic-agent}
description=${MCP_REGISTRATION_DESCRIPTION:-Automated root cause and diagnostics reporting for Cumulocity devices.}

: "${C8Y_BASEURL:?}" "${C8Y_TENANT:?}" "${C8Y_USER:?}" "${C8Y_PASSWORD:?}"

base=${C8Y_BASEURL%/}
ai=$base/service/ai/mcp/servers
auth=(--user "$C8Y_TENANT/$C8Y_USER:$C8Y_PASSWORD")
api() { curl -sS "${auth[@]}" -H 'Accept: application/json' "$@"; }

payload=$(cat <<JSON
{"name":"$name",
 "description":"$description",
 "url":"$base/service/$service/sse",
 "type":"sse",
 "sendAuthentication":true}
JSON
)

post() {
  curl -sS "${auth[@]}" -o /tmp/c8y-mcp.json -w '%{http_code}' \
    -X "$1" -H 'Content-Type: application/json' -d "$payload" "$2"
}

# Tool names open an object in the "tools" array. The echoed "source" block
# repeats the server name under the same key, so mask it out first.
tools() {
  sed 's/"source":{"name"/"source":{"_name"/g' "$1" |
    grep -o '{"name":"[^"]*"' | sed 's/.*:"/   /;s/"$//'
}

if [ "${1:-}" = "--test" ]; then
  echo ">> testing $base/service/$service/sse"
  code=$(post POST "$ai/test")
  [ "$code" = 200 ] || [ "$code" = 201 ] ||
    { cat /tmp/c8y-mcp.json >&2; echo; exit 1; }
  echo ">> tools"
  tools /tmp/c8y-mcp.json
  rm -f /tmp/c8y-mcp.json
  exit 0
fi

echo ">> registering MCP server $name -> /service/$service/sse"
code=$(post POST "$ai")
# A duplicate name or URL comes back as 500 with "already exists" in the body,
# not as 409, so the message has to be matched.
case "$code" in
  200|201) echo "   created" ;;
  *)       grep -q 'already exists' /tmp/c8y-mcp.json ||
             { cat /tmp/c8y-mcp.json >&2; echo; exit 1; }
           echo "   exists, updating"
           code=$(post PUT "$ai/$name")
           case "$code" in
             200|201|204) echo "   updated" ;;
             *) cat /tmp/c8y-mcp.json >&2; echo; exit 1 ;;
           esac ;;
esac

echo ">> tools"
api "$ai/$name/tools" >/tmp/c8y-mcp.json
tools /tmp/c8y-mcp.json
rm -f /tmp/c8y-mcp.json
echo ">> done"
