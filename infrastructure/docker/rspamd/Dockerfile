FROM rspamd/rspamd:latest
USER root
COPY local.d/ /etc/rspamd/local.d/
COPY rspamd.local.lua /etc/rspamd/rspamd.local.lua
COPY entrypoint.sh /usr/local/bin/aether-rspamd-entrypoint.sh
RUN chmod 0755 /usr/local/bin/aether-rspamd-entrypoint.sh \
 && touch /etc/rspamd/local.d/redis.conf \
 && chown 11333:11333 /etc/rspamd/local.d/redis.conf /etc/rspamd/local.d/worker-controller.inc \
 && chmod 0600 /etc/rspamd/local.d/redis.conf /etc/rspamd/local.d/worker-controller.inc
USER 11333:11333
ENTRYPOINT ["/usr/local/bin/aether-rspamd-entrypoint.sh"]
