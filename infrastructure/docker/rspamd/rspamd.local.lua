rspamd_config:register_symbol({
  name = 'AETHER_HEADER_FROM_MISMATCH',
  type = 'prefilter',
  priority = 10,
  callback = function(task)
    local user = task:get_user()
    if not user or user == 'aether-sieve-relay' then
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

local rspamd_http = require 'rspamd_http'
local rspamd_logger = require 'rspamd_logger'
local upstream_list = require 'rspamd_upstream_list'
local ucl = require 'ucl'

local quarantine_score = 10
local quarantine_api

rspamd_config:register_symbol({
  name = 'AETHER_QUARANTINE',
  type = 'postfilter',
  priority = 10,
  flags = 'empty,ignore_passthrough',
  callback = function(task)
    if task:get_user() then
      return
    end
    local action = task:get_metric_action()
    local score = task:get_metric_score()[1]
    if action ~= 'reject' and not ((action == 'add header' or action == 'rewrite subject') and score >= quarantine_score) then
      return
    end
    quarantine_api = quarantine_api or upstream_list.create(rspamd_config, 'api:8080', 8080)
    local upstream = quarantine_api and quarantine_api:get_upstream_round_robin()
    if not upstream then
      quarantine_api = nil
      rspamd_logger.warnx(task, 'quarantine skipped: the API address could not be resolved')
      return
    end
    local symbols = {}
    for _, s in ipairs(task:get_symbols_all()) do
      local options = {}
      for i, o in ipairs(s.options or {}) do
        if i > 3 then
          break
        end
        options[i] = tostring(o):sub(1, 200)
      end
      table.insert(symbols, { name = s.name, score = s.score, options = #options > 0 and options or nil })
    end
    local rcpt = {}
    for _, r in ipairs(task:get_recipients('smtp') or {}) do
      table.insert(rcpt, r.addr)
    end
    local from = task:get_from('smtp')
    local scan = {
      action = action,
      score = score,
      from = from and from[1] and from[1].addr or '',
      rcpt = rcpt,
      symbols = #symbols > 0 and symbols or nil,
    }
    rspamd_http.request({
      task = task,
      url = 'http://' .. upstream:get_addr():to_string(true) .. '/internal/quarantine',
      body = task:get_content(),
      mime_type = 'message/rfc822',
      headers = { ['X-Aether-Scan'] = ucl.to_format(scan, 'json-compact') },
      timeout = 5,
      callback = function(err, code)
        if err then
          upstream:fail()
          quarantine_api = nil
          rspamd_logger.warnx(task, 'quarantine failed, keeping action %s: %s', action, err)
        elseif code == 201 then
          upstream:ok()
          task:set_pre_result({ action = 'discard', message = 'Held in quarantine', module = 'aether', priority = 3 })
        elseif code ~= 204 then
          upstream:ok()
          rspamd_logger.warnx(task, 'quarantine refused with HTTP %s, keeping action %s', code, action)
        end
      end,
    })
  end,
})
