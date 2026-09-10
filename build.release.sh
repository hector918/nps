#!/usr/bin/env bash
#
# Build the release artifacts, stamped with the release tag.
#
# This builds the handful of platforms this fleet actually runs, not the
# thirty-three of build.assets.sh. Add to PLATFORMS if that changes:
#
#   PLATFORMS="linux/amd64 linux/arm64 linux/arm" ./build.release.sh v0.26.11
#
# Asset names match the old scheme (linux_amd64_client.tar.gz) because both
# deploy/update.sh and the client's own updater derive the asset name from
# GOOS and GOARCH that way.
set -euo pipefail

TAG=${1:-dev}
OUT=${OUT:-dist}
PLATFORMS=${PLATFORMS:-"linux/amd64 linux/arm64"}

# VERSION is only ever displayed and reported to the server, never compared,
# so the tag can go in verbatim. GetVersion, which the server does compare
# byte for byte, is deliberately left alone.
LDFLAGS="-s -w -X ehang.io/nps/lib/version.VERSION=${TAG}"

rm -rf "$OUT"
mkdir -p "$OUT"

for platform in $PLATFORMS; do
    goos=${platform%%/*}
    goarch=${platform##*/}
    label="${goos}_${goarch}"

    echo "==> ${label} client"
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
        go build -trimpath -ldflags "$LDFLAGS" -o npc ./cmd/npc/npc.go
    tar -czf "${OUT}/${label}_client.tar.gz" npc conf/npc.conf conf/multi_account.conf
    rm -f npc

    echo "==> ${label} server"
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
        go build -trimpath -ldflags "$LDFLAGS" -o nps ./cmd/nps/nps.go
    tar -czf "${OUT}/${label}_server.tar.gz" nps \
        conf/nps.conf conf/tasks.json conf/clients.json conf/hosts.json \
        conf/server.key conf/server.pem web/views web/static
    rm -f nps
done

# The checksum file is the one thing a curl-driven update can actually verify,
# so every asset has to be in it.
(cd "$OUT" && sha256sum ./*.tar.gz | sed 's|\./||' > sha256sums.txt)

echo
echo "==> ${OUT}/sha256sums.txt"
cat "${OUT}/sha256sums.txt"
