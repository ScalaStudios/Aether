FROM debian:bookworm-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      dovecot-core dovecot-imapd dovecot-lmtpd dovecot-pgsql dovecot-sieve dovecot-managesieved msmtp openssl ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* /etc/dovecot/conf.d \
 && groupadd -g 5000 vmail && useradd -u 5000 -g vmail -d /var/vmail -s /usr/sbin/nologin vmail
COPY dovecot.conf dovecot-acl dovecot-acl-held /etc/dovecot/
COPY sieve/ /etc/dovecot/sieve/
COPY sieve-bin/ /usr/local/lib/aether-sieve/
COPY entrypoint.sh tls-init.sh aether-token-check.sh encrypt-mail.sh /usr/local/bin/
RUN for s in /etc/dovecot/sieve/*.sieve; do sievec "$s"; done && chmod 0755 /usr/local/bin/*.sh /usr/local/lib/aether-sieve/*
EXPOSE 143 993 24 12345 4190
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["dovecot", "-F"]
