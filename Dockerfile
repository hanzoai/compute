FROM casbin/guacd:1.5.4 as guacd
FROM node:18.19.0 AS FRONT
WORKDIR /web
COPY ./web .
RUN yarn install --frozen-lockfile --network-timeout 1000000 && yarn run build


FROM golang:1.23 AS BACK
WORKDIR /go/src/hanzo-visor
COPY . .
RUN chmod +x ./build.sh
RUN ./build.sh


FROM alpine:latest AS STANDARD
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
