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

local sender_lists_url = (os.getenv('AETHER_API_INTERNAL_URL') or 'http://api:8080') .. '/internal/sender-lists/check'

local function first_addr(list)
  return list and list[1] and list[1].addr or ''
end

local sender_lists = rspamd_config:register_symbol({
  name = 'AETHER_SENDER_LISTS',
  type = 'postfilter',
  callback = function(task)
    if task:get_user() or task:has_symbol('CLAM_VIRUS') then
      return
    end
    local recipients = {}
    for _, r in ipairs(task:get_recipients('smtp') or {}) do
      table.insert(recipients, r.addr)
    end
    if #recipients == 0 then
      return
    end
    local ip = task:get_from_ip()
    local dmarc_reject = task:has_symbol('DMARC_POLICY_REJECT')
    rspamd_http.request({
      task = task,
      url = sender_lists_url,
      method = 'post',
      mime_type = 'application/json',
      timeout = 3,
      body = ucl.to_format({
        from = first_addr(task:get_from('smtp')),
        headerFrom = first_addr(task:get_from('mime')),
        ip = ip and ip:is_valid() and ip:to_string() or '',
        recipients = recipients,
        spfPass = task:has_symbol('R_SPF_ALLOW'),
        dmarcPass = task:has_symbol('DMARC_POLICY_ALLOW'),
        dmarcReject = dmarc_reject,
      }, 'json-compact'),
      callback = function(err, code, body)
        if err or code ~= 200 then
          rspamd_logger.warnx(task, 'sender list check failed: %s %s', err, code)
          return
        end
        local parser = ucl.parser()
        if not parser:parse_string(body) then
          return
        end
        local action = parser:get_object().action
        if action == 'block' then
          task:insert_result('AETHER_SENDER_BLOCKED', 1.0)
          task:set_pre_result({ action = 'reject', message = 'Mail from this sender is blocked by the recipient\'s organization', module = 'aether' })
        elseif action == 'allow' and not task:has_pre_result() then
          task:insert_result('AETHER_SENDER_ALLOWED', 1.0)
          task:set_pre_result({ action = 'no action', module = 'aether' })
        end
      end,
    })
  end,
})

rspamd_config:register_symbol({ name = 'AETHER_SENDER_BLOCKED', type = 'virtual', parent = sender_lists, score = 0.0, description = 'Sender is on an Aether block list' })
rspamd_config:register_symbol({ name = 'AETHER_SENDER_ALLOWED', type = 'virtual', parent = sender_lists, score = 0.0, description = 'Sender is on an Aether allow list' })

rspamd_config:register_symbol({
  name = 'AETHER_FLOW_SCORE',
  score = 1.0,
  group = 'aether',
  callback = function(task)
    local score = tonumber(task:get_header('X-Aether-Flow-Score') or '')
    if score and score >= -50 and score <= 50 then
      task:insert_result('AETHER_FLOW_SCORE', score)
    end
  end,
})

local lua_redis = require 'lua_redis'
local aether_redis = lua_redis.parse_redis_server('aether')

rspamd_config:register_symbol({
  name = 'AETHER_OUTBOUND_SPAM',
  type = 'idempotent',
  flags = 'ignore_passthrough',
  callback = function(task)
    local user = task:get_user()
    if not aether_redis or not user or user == 'aether-sieve-relay' or task:has_symbol('AETHER_HEADER_FROM_MISMATCH') then
      return
    end
    local action = task:get_metric_action()
    if action ~= 'reject' and action ~= 'add header' and action ~= 'rewrite subject' then
      return
    end
    local key = 'aether:sending:spam:' .. user:lower() .. ':' .. math.floor(os.time() / 86400)
    lua_redis.redis_make_request(task, aether_redis, key, true, function(err)
      if not err then
        lua_redis.redis_make_request(task, aether_redis, key, true, function() end, 'EXPIRE', { key, '172800' })
      end
    end, 'INCR', { key })
  end,
})

local lua_mime = require "lua_mime"
local aether_auth_checks = {
  { 'spf', { { 'R_SPF_ALLOW', 'pass' }, { 'R_SPF_FAIL', 'fail' }, { 'R_SPF_SOFTFAIL', 'softfail' }, { 'R_SPF_NEUTRAL', 'neutral' }, { 'R_SPF_DNSFAIL', 'temperror' }, { 'R_SPF_PERMFAIL', 'permerror' } } },
  { 'dmarc', { { 'DMARC_POLICY_ALLOW', 'pass' }, { 'DMARC_POLICY_ALLOW_WITH_FAILURES', 'pass' }, { 'DMARC_POLICY_REJECT', 'fail' }, { 'DMARC_POLICY_QUARANTINE', 'fail' }, { 'DMARC_POLICY_SOFTFAIL', 'fail' }, { 'DMARC_BAD_POLICY', 'permerror' }, { 'DMARC_DNSFAIL', 'temperror' } } },
}
local aether_dkim_results = { allow = 'pass', reject = 'fail', tempfail = 'temperror', permerror = 'permerror', permfail = 'permerror', ['bad record'] = 'permerror' }

local function aether_domain(value)
  local domain = value and tostring(value):lower()
  if domain and #domain <= 253 and domain:match('^[a-z0-9][a-z0-9.-]*$') then
    return domain
  end
end

rspamd_config:register_symbol({
  name = 'AETHER_AUTHENTICATION',
  type = 'postfilter',
  callback = function(task)
    local user = task:get_user()
    local parts = { 'auth=' .. (user == 'aether-sieve-relay' and 'relay' or user and 'pass' or 'none') }
    for _, check in ipairs(aether_auth_checks) do
      local result = 'none'
      for _, candidate in ipairs(check[2]) do
        if task:has_symbol(candidate[1]) then
          result = candidate[2]
          break
        end
      end
      table.insert(parts, check[1] .. '=' .. result)
      if check[1] == 'spf' then
        local envelope = task:get_from('smtp')
        local domain = aether_domain(envelope and envelope[1] and envelope[1].domain ~= '' and envelope[1].domain or task:get_helo())
        if domain then
          table.insert(parts, 'spf.domain=' .. domain)
        end
      end
    end
    local dkim, dkim_domain = 'none', nil
    for _, res in ipairs(task:get_dkim_results() or {}) do
      local mapped = aether_dkim_results[res.result]
      if mapped and (dkim == 'none' or mapped == 'pass') and dkim ~= 'pass' then
        dkim, dkim_domain = mapped, aether_domain(res.domain)
      end
    end
    table.insert(parts, 'dkim=' .. dkim)
    if dkim_domain then
      table.insert(parts, 'dkim.domain=' .. dkim_domain)
    end
    lua_mime.modify_headers(task, {
      remove = { ['X-Aether-Authentication'] = 0 },
      add = { ['X-Aether-Authentication'] = { order = 1, value = table.concat(parts, ' ') } },
    })
  end,
})

local attachment_redis = lua_redis.parse_redis_server('aether_attachments')

local function attachment_extension(name)
  return (name:lower():gsub('[%s%.]+$', '')):match('%.([^%./\\]+)$')
end

local function blocked_attachment(part, blocked)
  local name = part:get_filename() or ''
  local ext = attachment_extension(name)
  if ext and blocked[ext] then
    return name
  end
  local detected = part:get_detected_ext()
  if detected and blocked[detected:lower()] then
    return name ~= '' and name or detected
  end
  if part:is_archive() then
    for _, file in ipairs(part:get_archive():get_files() or {}) do
      ext = attachment_extension(file)
      if ext and blocked[ext] then
        return name .. ' (' .. file .. ')'
      end
    end
  end
end

local function without_parts(task, removed)
  local nl = ({ cr = '\r', lf = '\n' })[task:get_newlines_type()] or '\r\n'
  local out, open = {}, {}
  task:headers_foreach(function(_, hdr)
    out[#out + 1] = hdr.raw:gsub('\r?\n?$', '') .. nl
  end, { full = true })
  out[#out + 1] = nl
  for i, part in ipairs(task:get_parts()) do
    local enclosing = part:get_enclosing_boundary()
    if enclosing then
      while #open > 0 and open[#open] ~= enclosing do
        out[#out + 1] = '--' .. table.remove(open) .. '--' .. nl
      end
      out[#out + 1] = '--' .. enclosing .. nl
    end
    if removed[i] then
      out[#out + 1] = 'Content-Type: text/plain; charset=utf-8' .. nl .. 'Content-Transfer-Encoding: 8bit' .. nl .. nl
      out[#out + 1] = string.format('The attachment "%s" was removed because your organization blocks this file type.', removed[i]) .. nl
    elseif i > 1 then
      out[#out + 1] = tostring(part:get_raw_headers()) .. nl
      if part:is_multipart() then
        open[#open + 1] = part:get_boundary()
      elseif not part:is_message() then
        out[#out + 1] = tostring(part:get_raw_content()) .. nl
      end
    elseif part:is_multipart() then
      open[#open + 1] = part:get_boundary()
    else
      out[#out + 1] = tostring(part:get_raw_content())
    end
  end
  while #open > 0 do
    out[#out + 1] = '--' .. table.remove(open) .. '--' .. nl
  end
  return table.concat(out)
end

rspamd_config:register_symbol({
  name = 'AETHER_BLOCKED_ATTACHMENT',
  type = 'prefilter',
  priority = 5,
  callback = function(task)
    local user = task:get_user()
    if not attachment_redis or user == 'aether-sieve-relay' then
      return
    end
    local candidates = {}
    for i, part in ipairs(task:get_parts()) do
      if not part:is_multipart() and not part:is_message() and not (part:is_text() and not part:is_attachment()) then
        candidates[i] = part
      end
    end
    if not next(candidates) then
      return
    end
    local sender = (task:get_from('smtp') or {})[1] or {}
    local sender_addr, sender_domain = (sender.addr or ''):lower(), (sender.domain or ''):lower()
    local domains, seen = {}, {}
    for _, addr in ipairs(user and { sender } or task:get_recipients('smtp') or {}) do
      local domain = (addr.domain or ''):lower()
      if domain ~= '' and not seen[domain] then
        seen[domain] = true
        domains[#domains + 1] = domain
      end
    end
    if #domains == 0 then
      return
    end
    local args = { 'AETHER_ATTACHMENT_POLICY' }
    for _, domain in ipairs(domains) do
      args[#args + 1] = domain
    end
    lua_redis.redis_make_request(task, attachment_redis, nil, false, function(err, data)
      if err or type(data) ~= 'table' then
        return
      end
      local removed, names, reject = {}, {}, false
      for _, raw in ipairs(data) do
        local parser = ucl.parser()
        local policy = type(raw) == 'string' and parser:parse_string(raw) and parser:get_object()
        local rules = policy and policy[user and 'outbound' or 'inbound']
        local exempt = {}
        for _, entry in ipairs(policy and policy.exemptSenders or {}) do
          exempt[entry] = true
        end
        if rules and rules.action ~= 'off' and not exempt[sender_addr] and not exempt['@' .. sender_domain] then
          local blocked = {}
          for _, ext in ipairs(rules.extensions or {}) do
            blocked[ext] = true
          end
          for i, part in pairs(candidates) do
            local name = blocked_attachment(part, blocked)
            if name then
              if not removed[i] then
                removed[i] = (name:gsub('%c', ''))
                names[#names + 1] = removed[i]
              end
              reject = reject or rules.action == 'reject' or not part:get_enclosing_boundary()
            end
          end
        end
      end
      if #names == 0 then
        return
      end
      task:insert_result('AETHER_BLOCKED_ATTACHMENT', 1.0, names)
      if reject then
        task:set_pre_result({ action = 'reject', message = string.format('Message rejected: the attachment "%s" is a blocked file type', names[1]), module = 'aether' })
      else
        task:set_message(without_parts(task, removed))
      end
    end, 'HMGET', args)
  end,
})
