#!/usr/bin/env bash
# Package the MCP server as a Cumulocity microservice ZIP.
#
# Cumulocity wants a `docker save` tarball plus the manifest. Building that
# needs no Docker daemon: the server is a static Go binary, so the image is one
# layer holding the binary and a CA bundle, and the archive format is just a
# tar of the layer, an image config and a manifest. That keeps the build
# reproducible and runnable anywhere Go is available.
set -euo pipefail

cd "$(dirname "$0")"

manifest=cumulocity.json
name=$(sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -1)
version=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -1)
out=${1:-$name-$version.zip}
ca=${SSL_CERT_FILE:-/etc/ssl/certs/ca-certificates.crt}

[ -n "$name" ] && [ -n "$version" ] || { echo "cannot read name/version from $manifest" >&2; exit 1; }
[ -r "$ca" ] || { echo "no CA bundle at $ca; set SSL_CERT_FILE" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo ">> building static linux/amd64 binary"
ldflags="-s -w -X github.com/skowalak/c8y-aiot-hackathon-two-day-orange/mcp/internal/server.Version=$version"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="$ldflags" -o "$work/rootfs/mcpd" .

install -Dm444 "$ca" "$work/rootfs/etc/ssl/certs/ca-certificates.crt"

# One deterministic layer. Timestamps and ownership are pinned so that an
# unchanged source tree produces a byte-identical image.
echo ">> assembling image layer"
tar --create --file "$work/layer.tar" \
    --directory "$work/rootfs" \
    --owner=0 --group=0 --numeric-owner --mtime=@0 --sort=name \
    --format=gnu .
diff_id=$(sha256sum "$work/layer.tar" | cut -d' ' -f1)

cat > "$work/config.json" <<JSON
{
  "architecture": "amd64",
  "os": "linux",
  "created": "1970-01-01T00:00:00Z",
  "config": {
    "Env": [
      "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
      "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
      "MCP_ADDR=:80",
      "APPLICATION_NAME=$name"
    ],
    "Entrypoint": ["/mcpd"],
    "ExposedPorts": {"80/tcp": {}},
    "WorkingDir": "/"
  },
  "rootfs": {"type": "layers", "diff_ids": ["sha256:$diff_id"]},
  "history": [{"created": "1970-01-01T00:00:00Z", "created_by": "package-microservice.sh"}]
}
JSON
config_sha=$(sha256sum "$work/config.json" | cut -d' ' -f1)

img=$work/image
mkdir -p "$img/$diff_id"
mv "$work/layer.tar" "$img/$diff_id/layer.tar"
mv "$work/config.json" "$img/$config_sha.json"
printf '1.0' > "$img/$diff_id/VERSION"
cat > "$img/manifest.json" <<JSON
[{"Config":"$config_sha.json","RepoTags":["$name:$version"],"Layers":["$diff_id/layer.tar"]}]
JSON
cat > "$img/repositories" <<JSON
{"$name":{"$version":"$diff_id"}}
JSON

echo ">> writing image.tar"
tar --create --file "$work/image.tar" --directory "$img" \
    --owner=0 --group=0 --numeric-owner --mtime=@0 --sort=name \
    manifest.json repositories "$config_sha.json" "$diff_id"

rm -f "$out"
zip --quiet --junk-paths "$out" "$manifest" "$work/image.tar"

echo ">> $out ($(du -h "$out" | cut -f1)) image $name:$version layer sha256:${diff_id:0:12}"
