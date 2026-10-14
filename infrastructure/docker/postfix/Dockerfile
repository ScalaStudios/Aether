FROM debian:bookworm-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends postfix postfix-pgsql libsasl2-modules ca-certificates netbase \
 && rm -rf /var/lib/apt/lists/*
COPY main.cf master.cf submission_header_checks /etc/postfix/
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh
EXPOSE 25 465 587
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
