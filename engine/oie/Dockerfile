# syntax=docker/dockerfile:1
#
# Open Integration Engine with the Web Support extension baked in.
#
# The newest published image (openintegrationengine/engine:latest) is 4.5.2,
# and Web Support needs 4.6.0 or later. This builds the image the way
# upstream's engine-docker repository does (release tarball on a Temurin 17
# JRE), then unzips the extension into extensions/ at build time so it
# survives container re-creation without an install step.
#
# To upgrade, change a URL and its SHA-256 together; the build fails if a
# download doesn't match.

ARG JAVA_IMAGE=eclipse-temurin:17-jre-noble
# Only used for its entrypoint.sh, which upstream hasn't changed since 4.5.2.
ARG OIE_ENTRYPOINT_IMAGE=openintegrationengine/engine:4.5.2-ubuntu-jre

FROM ${OIE_ENTRYPOINT_IMAGE} AS upstream

FROM alpine:3.21 AS unpack
ARG OIE_URL=https://github.com/OpenIntegrationEngine/engine/releases/download/v4.6.0/oie_unix_4_6_0.tar.gz
ARG OIE_SHA256=7c82e79027e671277e1d78d0f7bbb1c53ddf1be476f1e80c2ddbfcf7855900ea
ARG WEBSUPPORT_URL=https://github.com/gibson9583/oie-web-support-plugin/releases/download/v1.0.5/websupport-1.0.5.zip
ARG WEBSUPPORT_SHA256=ee7952e42bd1282727ee77cad529c0676557b1dc615c03548bd6676373214755

WORKDIR /build
ADD --checksum=sha256:${OIE_SHA256} ${OIE_URL} oie.tar.gz
ADD --checksum=sha256:${WEBSUPPORT_SHA256} ${WEBSUPPORT_URL} websupport.zip

# Same trimming as upstream: the server image doesn't need the CLI.
RUN tar xzf oie.tar.gz \
 && mv oie engine \
 && rm -rf engine/cli-lib engine/mirth-cli-launcher.jar engine/oiecommand engine/oieserver.ps1 \
 && unzip -q websupport.zip -d engine/extensions \
 && mkdir -p engine/appdata engine/custom-extensions
COPY --from=upstream --chmod=755 /opt/engine/entrypoint.sh engine/entrypoint.sh

FROM ${JAVA_IMAGE}
# unzip is used by entrypoint.sh for custom-extensions/ and EXTENSIONS_DOWNLOAD.
# The published image runs as "engine" with uid 1000 (Temurin's "ubuntu" user,
# renamed); keeping that uid means existing appdata volumes stay writable.
RUN apt-get update \
 && apt-get install -y --no-install-recommends unzip \
 && rm -rf /var/lib/apt/lists/* \
 && usermod -l engine -d /opt/engine ubuntu \
 && groupmod -n engine ubuntu

COPY --from=unpack --chown=engine:engine /build/engine /opt/engine

VOLUME /opt/engine/appdata
WORKDIR /opt/engine
EXPOSE 8443
USER engine
ENTRYPOINT ["./entrypoint.sh"]
CMD ["./oieserver"]
