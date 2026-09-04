# One directory in an empty image: the static binary and the files it reads.
# Nothing else is present to run, so nothing else can be run.

FROM --platform=$BUILDPLATFORM ghcr.io/hanzoai/node:18.19.0-alpine AS front
WORKDIR /web
RUN apk add --no-cache python3 make g++ libc6-compat
COPY ./web .
RUN yarn install --frozen-lockfile --network-timeout 1000000 && yarn run build

FROM --platform=$BUILDPLATFORM ghcr.io/hanzoai/golang:1.26-alpine AS back
ENV GOTOOLCHAIN=auto
RUN apk add --no-cache bash git ca-certificates tzdata
WORKDIR /go/src/hanzo-visor
COPY . .
ARG GO_EXPERIMENT=jsonv2
ENV GOEXPERIMENT=${GO_EXPERIMENT}
ARG TARGETOS TARGETARCH
RUN chmod +x ./build.sh
RUN --mount=type=secret,id=gh_token ./build.sh
# The runtime directories, owned by the runtime user, made here because an
# empty image has no mkdir and no chown.
RUN mkdir -p /out/logs /out/conf /out/web \
    && cp visor /out/visor \
    && cp -r data /out/data \
    && cp conf/app.conf /out/conf/app.conf \
    && chown -R 1000:1000 /out

FROM scratch
COPY --from=back /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=back /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=back /out/ /
COPY --from=front --chown=1000:1000 /web/build /web/build
USER 1000:1000
WORKDIR /
ENTRYPOINT ["/visor"]
