FROM ghcr.io/hanzoai/guacd:1.5.4 as guacd

FROM --platform=$BUILDPLATFORM ghcr.io/hanzoai/node:18.19.0-alpine AS FRONT
WORKDIR /web
# alpine node build toolchain for any node-gyp native deps in `yarn install`
RUN apk add --no-cache python3 make g++ libc6-compat
COPY ./web .
RUN yarn install --frozen-lockfile --network-timeout 1000000 && yarn run build


FROM --platform=$BUILDPLATFORM ghcr.io/hanzoai/golang:1.26-alpine AS BACK
# go.mod pins the toolchain. The golang base image sets GOTOOLCHAIN=local,
# which turns a `go` directive newer than the image into a hard build
# failure instead of a download.
ENV GOTOOLCHAIN=auto
# build.sh is #!/bin/bash and fetches private modules over git (GOPRIVATE direct)
RUN apk add --no-cache bash git
WORKDIR /go/src/hanzo-visor
COPY . .

# Per SCALE_STANDARD.md §2 — every Go production Dockerfile that
# emits JSON to a client builds with GOEXPERIMENT=jsonv2. Verified
# -12% time / -23% allocs on the edge POST roundtrip vs encoding/json
# v1 (json_bench_test.go in hanzoai/zip).
ARG GO_EXPERIMENT=jsonv2
ENV GOEXPERIMENT=${GO_EXPERIMENT}

# buildx sets TARGETOS/TARGETARCH per target platform; build.sh compiles to it.
# Each arch is built on its native runner (amd64=evo, arm64=spark), so this is a
# native compile per platform — no QEMU.
ARG TARGETOS TARGETARCH

RUN chmod +x ./build.sh
RUN --mount=type=secret,id=gh_token ./build.sh


FROM ghcr.io/hanzoai/alpine:3.22 AS STANDARD
LABEL MAINTAINER="https://hanzo.ai/"
ARG USER=hanzo-visor

RUN sed -i 's/https/http/' /etc/apk/repositories
RUN apk add --update sudo
RUN apk add curl
RUN apk add ca-certificates && update-ca-certificates

RUN adduser -D $USER -u 1000 \
    && echo "$USER ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/$USER \
    && chmod 0440 /etc/sudoers.d/$USER \
    && mkdir logs \
    && chown -R $USER:$USER logs

USER 1000
WORKDIR /
COPY --from=BACK --chown=$USER:$USER /go/src/hanzo-visor/visor ./visor
COPY --from=BACK --chown=$USER:$USER /go/src/hanzo-visor/data ./data
COPY --from=BACK --chown=$USER:$USER /go/src/hanzo-visor/conf/app.conf ./conf/app.conf
COPY --from=FRONT --chown=$USER:$USER /web/build ./web/build

ENTRYPOINT ["/visor"]


FROM guacd AS ALLINONE
LABEL MAINTAINER="https://hanzo.ai/"

WORKDIR /

USER root
RUN apt-get update \
    && apt-get install -y      \
        mariadb-server         \
        mariadb-client         \
        ca-certificates        \
    && update-ca-certificates  \
    && rm -rf /var/lib/apt/lists/*

COPY --from=BACK /go/src/hanzo-visor/visor ./visor
COPY --from=BACK /go/src/hanzo-visor/data ./data
COPY --from=BACK /go/src/hanzo-visor/docker-entrypoint.sh /docker-entrypoint.sh
COPY --from=BACK /go/src/hanzo-visor/conf/app.conf ./conf/app.conf
COPY --from=FRONT /web/build ./web/build

EXPOSE 19000
ENTRYPOINT ["/bin/bash"]
CMD ["/docker-entrypoint.sh"]
