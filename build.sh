#!/bin/bash
set -e

# Private Hanzo modules resolve from git.hanzo.ai when GIT_AUTH_TOKEN is mounted
# (cloud /v1/build) or github.com when gh_token / GITHUB_TOKEN is mounted (CI),
# while GONOPROXY=none lets proxy.golang.org serve published modules verified
# against the committed go.sum.
export GOPRIVATE="github.com/hanzoai/*,github.com/hanzo-inc/*,github.com/hanzozt/*,github.com/zap-proto/*,github.com/luxfi/*,github.com/zooai/*,github.com/parsdao/*"
export GONOPROXY="none"
export GOPROXY="${GOPROXY:-https://proxy.golang.org|direct}"

n=0
if [ -s /run/secrets/GIT_AUTH_TOKEN ]; then
  tok="$(cat /run/secrets/GIT_AUTH_TOKEN)"
  for org in hanzoai hanzo-inc hanzozt zap-proto luxfi zooai parsdao; do
    export "GIT_CONFIG_KEY_${n}=url.https://x:${tok}@git.hanzo.ai/${org}/.insteadOf"
    export "GIT_CONFIG_VALUE_${n}=https://github.com/${org}/"
    n=$((n + 1))
  done
fi
if [ -s /run/secrets/gh_token ]; then
  export "GIT_CONFIG_KEY_${n}=url.https://x-access-token:$(cat /run/secrets/gh_token)@github.com/.insteadOf"
  export "GIT_CONFIG_VALUE_${n}=https://github.com/"
  n=$((n + 1))
elif [ -s /run/secrets/GITHUB_TOKEN ]; then
  export "GIT_CONFIG_KEY_${n}=url.https://x-access-token:$(cat /run/secrets/GITHUB_TOKEN)@github.com/.insteadOf"
  export "GIT_CONFIG_VALUE_${n}=https://github.com/"
  n=$((n + 1))
fi
if [ "$n" -gt 0 ]; then
  export GIT_CONFIG_COUNT="$n"
fi

# GOTOOLCHAIN is set by the Dockerfile (ENV GOTOOLCHAIN=auto in the BACK stage),
# which is where every other Hanzo Go builder sets it. Outside a container Go
# already defaults to auto, so this script needs no export of its own.

CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} go build -ldflags="-w -s" -o compute .
