FROM golang:1.26.9-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -o /out/serverdesk ./cmd/serverdesk

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl openssl openssh-client util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 serverdesk \
    && useradd --uid 10001 --gid 10001 --home-dir /var/lib/serverdesk --shell /usr/sbin/nologin serverdesk \
    && install -d -o serverdesk -g serverdesk -m 0700 /var/lib/serverdesk
COPY --from=build /out/serverdesk /usr/local/bin/serverdesk
COPY config.example.json NOTICE THIRD_PARTY_NOTICES.md /usr/local/share/serverdesk/
COPY --chmod=755 deploy/docker/entrypoint.sh /usr/local/bin/serverdesk-entrypoint
COPY --chmod=755 deploy/docker/healthcheck.sh /usr/local/bin/serverdesk-healthcheck
ENV HOME=/var/lib/serverdesk \
    SERVERDESK_CREDENTIALS_STORE=/var/lib/serverdesk/credentials
USER 10001:10001
WORKDIR /var/lib/serverdesk
EXPOSE 6005/tcp
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD ["serverdesk-healthcheck"]
ENTRYPOINT ["serverdesk-entrypoint"]
CMD ["serverdesk"]
