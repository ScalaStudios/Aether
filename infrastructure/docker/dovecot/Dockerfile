FROM debian:bookworm-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      dovecot-core dovecot-imapd dovecot-lmtpd dovecot-pgsql dovecot-sieve dovecot-managesieved openssl ca-certificates \
 && rm -rf /var/lib/apt/lists/* /etc/dovecot/conf.d \
 && groupadd -g 5000 vmail && useradd -u 5000 -g vmail -d /var/vmail -s /usr/sbin/nologin vmail
COPY dovecot.conf /etc/dovecot/dovecot.conf
COPY sieve/ /etc/dovecot/sieve/
COPY entrypoint.sh tls-init.sh /usr/local/bin/
RUN sievec /etc/dovecot/sieve/spam-to-junk.sieve && chmod 0755 /usr/local/bin/*.sh
EXPOSE 143 993 24 12345 4190
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
