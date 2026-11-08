-- bobad の unix socket クライアント(JSON Lines)。
-- すべてのコールバックとイベントハンドラは vim.schedule 経由でメインループから呼ばれる。
local uv = vim.uv or vim.loop

local M = {}

local SUBSCRIBE = { "session.", "review.", "editor." }
local BACKOFF_MIN, BACKOFF_MAX = 500, 15000

local st = {
  pipe = nil,
  connected = false,
  connecting = false,
  started = false, -- 一度 connect を要求されたら再接続し続ける
  rbuf = "",
  next_id = 1,
  pending = {}, -- id -> callback(err, result)
  queue = {}, -- 未接続中に積まれた {line, id}
  handlers = {}, -- {pattern, fn}
  backoff = BACKOFF_MIN,
  timer = nil,
  warned = false,
}

---@return string
function M.socket_path()
  local sock = vim.env.BOBA_SOCK
  if sock and sock ~= "" then
    return sock
  end
  local home = vim.env.BOBA_HOME
  if home and home ~= "" then
    return home .. "/share/bobad.sock"
  end
  return vim.fn.expand("~/.local/share/boba/bobad.sock")
end

function M.is_connected()
  return st.connected
end

local function decode(line)
  local ok, obj = pcall(vim.json.decode, line, { luanil = { object = true, array = true } })
  if ok then
    return obj
  end
  return nil
end

local function event_matches(pattern, name)
  if pattern == "*" or pattern == name then
    return true
  end
  return pattern:sub(-1) == "." and name:sub(1, #pattern) == pattern
end

local function dispatch_event(name, data)
  for _, h in ipairs(st.handlers) do
    if event_matches(h.pattern, name) then
      local ok, err = pcall(h.fn, data, name)
      if not ok then
        vim.notify("boba: event handler error (" .. name .. "): " .. tostring(err), vim.log.levels.ERROR)
      end
    end
  end
end

local function handle_line(line)
  if line == "" then
    return
  end
  local msg = decode(line)
  if type(msg) ~= "table" then
    return
  end
  if msg.event then
    dispatch_event(msg.event, msg.data)
  elseif msg.id ~= nil then
    local cb = st.pending[msg.id]
    st.pending[msg.id] = nil
    if cb then
      local err = msg.error
      if err ~= nil and type(err) ~= "string" then
        err = type(err) == "table" and (err.message or vim.inspect(err)) or tostring(err)
      end
      cb(err, msg.result)
    end
  end
end

local function on_data(chunk)
  st.rbuf = st.rbuf .. chunk
  while true do
    local nl = st.rbuf:find("\n", 1, true)
    if not nl then
      break
    end
    local line = st.rbuf:sub(1, nl - 1)
    st.rbuf = st.rbuf:sub(nl + 1)
    handle_line(line)
  end
end

local function fail_all(reason)
  local pending = st.pending
  st.pending = {}
  local queue = st.queue
  st.queue = {}
  for _, cb in pairs(pending) do
    cb(reason, nil)
  end
  for _, q in ipairs(queue) do
    local cb = q.cb
    if cb then
      cb(reason, nil)
    end
  end
end

local schedule_reconnect

local function on_disconnect(reason)
  if st.pipe and not st.pipe:is_closing() then
    st.pipe:close()
  end
  st.pipe = nil
  local was = st.connected
  st.connected = false
  st.connecting = false
  st.rbuf = ""
  fail_all(reason or "boba: disconnected")
  if was then
    dispatch_event("boba.disconnected", {})
  end
  schedule_reconnect()
end

local function raw_write(line)
  if st.pipe then
    st.pipe:write(line)
  end
end

local function on_connected()
  st.connected = true
  st.connecting = false
  st.backoff = BACKOFF_MIN
  st.warned = false
  st.pipe:read_start(function(err, chunk)
    if err or not chunk then
      vim.schedule(function()
        on_disconnect(err and ("boba: read error: " .. err) or "boba: daemon closed connection")
      end)
      return
    end
    vim.schedule(function()
      on_data(chunk)
    end)
  end)
  -- subscribe を最初に送り、未接続中に積んだリクエストを流す
  M.request("subscribe", { events = SUBSCRIBE })
  local queue = st.queue
  st.queue = {}
  for _, q in ipairs(queue) do
    if q.cb then
      st.pending[q.id] = q.cb
    end
    raw_write(q.line)
  end
  dispatch_event("boba.connected", {})
end

function M.connect()
  st.started = true
  if st.connected or st.connecting then
    return
  end
  st.connecting = true
  local pipe = uv.new_pipe(false)
  st.pipe = pipe
  local path = M.socket_path()
  pipe:connect(path, function(err)
    vim.schedule(function()
      if st.pipe ~= pipe then
        return
      end
      if err then
        if not st.warned and #st.queue > 0 then
          st.warned = true
          vim.notify("boba: cannot connect to bobad (" .. path .. "): " .. err, vim.log.levels.WARN)
        end
        on_disconnect("boba: not connected (" .. err .. ")")
        return
      end
      on_connected()
    end)
  end)
end

schedule_reconnect = function()
  if not st.started or st.timer then
    return
  end
  local delay = st.backoff
  st.backoff = math.min(st.backoff * 2, BACKOFF_MAX)
  st.timer = uv.new_timer()
  st.timer:start(delay, 0, function()
    vim.schedule(function()
      if st.timer then
        st.timer:close()
        st.timer = nil
      end
      M.connect()
    end)
  end)
end

function M.disconnect()
  st.started = false
  if st.timer then
    st.timer:stop()
    st.timer:close()
    st.timer = nil
  end
  if st.pipe then
    on_disconnect("boba: disconnected by client")
  end
end

--- リクエストを送る。cb(err, result) はメインループで呼ばれる。
---@param method string
---@param params table|nil
---@param cb fun(err: string|nil, result: any)|nil
function M.request(method, params, cb)
  local id = st.next_id
  st.next_id = id + 1
  if params == nil or (type(params) == "table" and next(params) == nil) then
    params = vim.empty_dict()
  end
  local line = vim.json.encode({ id = id, method = method, params = params }) .. "\n"
  local wrapped = cb and function(err, res)
    local ok, e = pcall(cb, err, res)
    if not ok then
      vim.notify("boba: callback error (" .. method .. "): " .. tostring(e), vim.log.levels.ERROR)
    end
  end or nil
  if st.connected then
    if wrapped then
      st.pending[id] = wrapped
    end
    raw_write(line)
  else
    table.insert(st.queue, { id = id, line = line, cb = wrapped })
    M.connect()
  end
  return id
end

--- エラーを通知するだけの簡易版。成功時は on_ok(result)。
function M.call(method, params, on_ok)
  return M.request(method, params, function(err, res)
    if err then
      vim.notify("boba: " .. method .. ": " .. err, vim.log.levels.ERROR)
      return
    end
    if on_ok then
      on_ok(res)
    end
  end)
end

--- 同期版(テスト・コマンド用)。
---@return any result, string|nil err
function M.request_sync(method, params, timeout)
  local done, res, err = false, nil, nil
  M.request(method, params, function(e, r)
    done, res, err = true, r, e
  end)
  local ok = vim.wait(timeout or 3000, function()
    return done
  end, 10)
  if not ok then
    return nil, "timeout"
  end
  return res, err
end

--- イベントハンドラを登録する。pattern は完全一致、"." で終われば前方一致、"*" はすべて。
--- 返り値の関数を呼ぶと解除。
function M.on(pattern, fn)
  local h = { pattern = pattern, fn = fn }
  table.insert(st.handlers, h)
  return function()
    for i, x in ipairs(st.handlers) do
      if x == h then
        table.remove(st.handlers, i)
        return
      end
    end
  end
end

return M
