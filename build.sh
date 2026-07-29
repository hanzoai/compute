#!/bin/bash
set -e

# Private Hanzo modules (e.g. github.com/hanzoai/commerce/metering) live in
# private repos; go-mod's direct git fetch is authenticated with the BuildKit
# BuildKit secret gh_token, mounted at /run/secrets/gh_token by the Dockerfile
# (RUN --mount=type=secret,id=gh_token). GOPRIVATE routes those modules
# direct-to-git (never through a public proxy).
export GOPRIVATE="github.com/hanzoai,github.com/lux-private,github.com/zooai,github.com/zenlm,github.com/parsdao,github.com/adnexustech"
if [ -f /run/secrets/gh_token ]; then
  git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/"
fi
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"

CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} go build -ldflags="-w -s" -o visor .
