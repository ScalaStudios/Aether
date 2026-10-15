FROM debian:bookworm-slim
RUN groupadd -g 102 postfix && groupadd -g 103 postdrop \
 && useradd -r -u 100 -g postfix -d /var/spool/postfix -s /usr/sbin/nologin postfix \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends postfix postfix-pgsql postsrsd libsasl2-modules ca-certificates netbase \
 && rm -rf /var/lib/apt/lists/*
COPY main.cf master.cf submission_header_checks /etc/postfix/
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh
EXPOSE 25 465 587
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
