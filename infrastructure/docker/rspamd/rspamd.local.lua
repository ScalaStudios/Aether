rspamd_config:register_symbol({
  name = 'AETHER_HEADER_FROM_MISMATCH',
  type = 'prefilter',
  priority = 10,
  callback = function(task)
    if not task:get_user() then
      return
    end
    local envelope = task:get_from('smtp')
    local header = task:get_from('mime')
    local env_addr = envelope and envelope[1] and envelope[1].addr or ''
    local hdr_addr = header and #header == 1 and header[1].addr or ''
    if env_addr == '' or hdr_addr:lower() ~= env_addr:lower() then
      task:insert_result('AETHER_HEADER_FROM_MISMATCH', 1.0, hdr_addr)
      task:set_pre_result({ action = 'reject', message = 'The From header must match the address you are sending as', module = 'aether' })
    end
  end,
})
