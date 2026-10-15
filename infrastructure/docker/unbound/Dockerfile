FROM alpine:3.22
RUN apk add --no-cache unbound dns-root-hints dnssec-root drill \
  && install -d -o unbound -g unbound /var/lib/unbound \
  && install -o unbound -g unbound -m 644 /usr/share/dnssec-root/trusted-key.key /var/lib/unbound/root.key
COPY unbound.conf /etc/unbound/unbound.conf
RUN unbound-checkconf /etc/unbound/unbound.conf
EXPOSE 53/udp 53/tcp
HEALTHCHECK --interval=15s --timeout=5s --retries=5 CMD drill @127.0.0.1 localhost >/dev/null || exit 1
ENTRYPOINT ["unbound", "-d", "-c", "/etc/unbound/unbound.conf"]
