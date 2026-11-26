FROM rspamd/rspamd:4.3.1
USER root
COPY local.d/ /etc/rspamd/local.d/
COPY rspamd.local.lua /etc/rspamd/rspamd.local.lua
COPY entrypoint.sh /usr/local/bin/aether-rspamd-entrypoint.sh
RUN chmod 0755 /usr/local/bin/aether-rspamd-entrypoint.sh \
 && touch /etc/rspamd/local.d/redis.conf /etc/rspamd/local.d/antivirus.conf /etc/rspamd/local.d/dmarc.conf \
 && chown 11333:11333 /etc/rspamd/local.d/redis.conf /etc/rspamd/local.d/worker-controller.inc /etc/rspamd/local.d/antivirus.conf /etc/rspamd/local.d/dmarc.conf \
 && chmod 0600 /etc/rspamd/local.d/redis.conf /etc/rspamd/local.d/worker-controller.inc
USER 11333:11333
ENTRYPOINT ["/usr/local/bin/aether-rspamd-entrypoint.sh"]
