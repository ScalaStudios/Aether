FROM debian:bookworm-slim AS flatcurve-builder
ARG FLATCURVE_VERSION=1.0.5
ARG FLATCURVE_SHA256=d9a67228257b02abb1885dc79bc24dcfb1b010bb849e39f86bc8d10d6eecdb18
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential autoconf automake libtool pkg-config dovecot-dev libxapian-dev curl ca-certificates \
 && curl -fsSL "https://github.com/slusarz/dovecot-fts-flatcurve/archive/refs/tags/v${FLATCURVE_VERSION}.tar.gz" -o /tmp/flatcurve.tar.gz \
 && echo "${FLATCURVE_SHA256}  /tmp/flatcurve.tar.gz" | sha256sum -c - \
 && mkdir -p /tmp/flatcurve \
 && tar -xzf /tmp/flatcurve.tar.gz -C /tmp/flatcurve --strip-components=1 \
 && cd /tmp/flatcurve \
 && ./autogen.sh \
 && ./configure --with-dovecot=/usr/lib/dovecot \
 && make -j"$(nproc)" \
 && make install DESTDIR=/out \
 && find /out \( -name '*.a' -o -name '*.la' \) -delete \
 && rm -rf /tmp/flatcurve /tmp/flatcurve.tar.gz /var/lib/apt/lists/*

FROM debian:bookworm-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      dovecot-core dovecot-imapd dovecot-lmtpd dovecot-pgsql dovecot-sieve dovecot-managesieved libxapian30 msmtp openssl ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* /etc/dovecot/conf.d \
 && groupadd -g 5000 vmail && useradd -u 5000 -g vmail -d /var/vmail -s /usr/sbin/nologin vmail
COPY --from=flatcurve-builder /out/usr/lib/dovecot/modules/ /usr/lib/dovecot/modules/
COPY dovecot.conf dovecot-acl dovecot-acl-held /etc/dovecot/
COPY sieve/ /etc/dovecot/sieve/
COPY sieve-bin/ /usr/local/lib/aether-sieve/
COPY entrypoint.sh tls-init.sh aether-token-check.sh encrypt-mail.sh /usr/local/bin/
RUN for s in /etc/dovecot/sieve/*.sieve; do sievec "$s"; done && chmod 0755 /usr/local/bin/*.sh /usr/local/lib/aether-sieve/*
EXPOSE 143 993 24 12345 4190
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["dovecot", "-F"]
