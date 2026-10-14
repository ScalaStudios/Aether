FROM rspamd/rspamd:latest
USER root
COPY local.d/ /etc/rspamd/local.d/
COPY entrypoint.sh /usr/local/bin/aether-rspamd-entrypoint.sh
RUN chmod 0755 /usr/local/bin/aether-rspamd-entrypoint.sh \
 && touch /etc/rspamd/local.d/redis.conf \
 && chown 11333:11333 /etc/rspamd/local.d/redis.conf \
 && chmod 0600 /etc/rspamd/local.d/redis.conf
USER 11333:11333
ENTRYPOINT ["/usr/local/bin/aether-rspamd-entrypoint.sh"]
