FROM golang:1.27-bookworm AS postfix-api
WORKDIR /src
COPY --from=aether-source go.mod go.sum ./
COPY --from=aether-source internal/mail/queue ./internal/mail/queue
COPY --from=aether-source cmd/aether-postfix-api ./cmd/aether-postfix-api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aether-postfix-api ./cmd/aether-postfix-api

FROM debian:bookworm-slim
RUN groupadd -g 102 postfix && groupadd -g 103 postdrop \
 && useradd -r -u 100 -g postfix -d /var/spool/postfix -s /usr/sbin/nologin postfix \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends postfix postfix-pgsql postsrsd libsasl2-modules ca-certificates netbase \
 && rm -rf /var/lib/apt/lists/*
COPY main.cf master.cf submission_header_checks trace_header_checks trace_milter_header_checks /etc/postfix/
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh
COPY --from=postfix-api /out/aether-postfix-api /usr/local/bin/aether-postfix-api
EXPOSE 25 465 587
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
