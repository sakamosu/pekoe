-- C. 許可待ちの承認フロート(右下)。y / n / A / g / Esc。
local client = require("boba.client")
local ui = require("boba.ui")
local sessions = require("boba.sessions")

local M = {}

M.queue = {} -- 許可待ちの Session(古い順)
local cur = nil -- {slug, buf, win, prev}

local function cfg()
  return require("boba").config
end

local function tool_input_str(input)
  if input == nil then
    return ""
  end
  if type(input) == "string" then
    return input
  end
  local ok, s = pcall(vim.json.encode, input)
  return ok and s or vim.inspect(input)
end

local function remove(slug)
  for i, s in ipairs(M.queue) do
    if s.slug == slug then
      table.remove(M.queue, i)
      return
    end
  end
end

function M.close()
  if not cur then
    return
  end
  local c = cur
  cur = nil
  local was_in = vim.api.nvim_get_current_win() == c.win
  ui.close_win(c.win)
  if was_in and c.prev and vim.api.nvim_win_is_valid(c.prev) then
    vim.api.nvim_set_current_win(c.prev)
  end
end

local show

local function show_next()
  M.close()
  if #M.queue > 0 then
    show(M.queue[1])
  end
end

--- 許可待ちに答える。answer: allow | deny | always
function M.answer(answer, slug)
  slug = slug or (cur and cur.slug) or (M.queue[1] and M.queue[1].slug)
  if not slug then
    ui.notify("許可待ちのセッションはありません", vim.log.levels.WARN)
    return
  end
  client.call("session.answer", { slug = slug, answer = answer }, function()
    ui.notify(string.format("%s → %s", slug, answer))
  end)
  remove(slug)
  show_next()
end

function M.go(slug)
  slug = slug or (cur and cur.slug)
  M.close()
  if not slug then
    return
  end
  sessions.target = slug
  if vim.env.BOBA_SESSION and vim.env.BOBA_SESSION ~= "" then
    ui.notify(slug .. ": boba の TUI に戻ってセッションを開いてください(:BobaSend で戻れます)")
  else
    ui.notify(slug .. ": ターミナルで `boba` を開くとこのセッションに答えられます(送信先に設定しました)")
  end
end

show = function(s)
  M.close()
  local km = cfg().keymaps.approval
  local pt = s.pending_tool or {}
  local rows = {
    { { "● ", "BobaWarn" }, { "Permission", "BobaWarn" }, { " · ", "BobaDim" }, { s.slug, "BobaTitle" } },
    "",
  }
  if pt.name then
    table.insert(rows, { { "  " .. pt.name, "BobaSecondary" } })
  end
  for _, l in ipairs(vim.split(tool_input_str(pt.input), "\n")) do
    if l ~= "" then
      table.insert(rows, "  " .. l)
    end
  end
  table.insert(rows, "")
  local hint = {}
  for _, p in ipairs({ { km.allow, "allow" }, { km.deny, "deny" }, { km.always, "always" }, { km.go, "go" }, { km.close, "close" } }) do
    if p[1] then
      table.insert(hint, { p[1] .. " ", "BobaKey" })
      table.insert(hint, { p[2] .. "  ", "BobaDim" })
    end
  end
  table.insert(rows, hint)
  if #M.queue > 1 then
    table.insert(rows, { { string.format("  (ほか %d 件待ち)", #M.queue - 1), "BobaDim" } })
  end

  local prev = vim.api.nvim_get_current_win()
  -- コマンドライン入力中はフォーカスを奪わない
  local mode = vim.api.nvim_get_mode().mode
  local enter = not mode:match("^c")
  if mode:match("^i") or mode:match("^R") then
    vim.cmd("stopinsert")
  end
  local buf, win = ui.float(rows, {
    anchor = "bottom-right",
    enter = enter,
    border_hl = "BobaWarn",
    min_width = 36,
    width = math.min(70, math.max(36, (function()
      local w = 0
      for _, r in ipairs(rows) do
        local t = type(r) == "string" and r or table.concat(vim.tbl_map(function(c) return c[1] end, r))
        w = math.max(w, vim.fn.strdisplaywidth(t))
      end
      return w + 2
    end)())),
    zindex = 80,
  })
  cur = { slug = s.slug, buf = buf, win = win, prev = prev }
  local o = { buffer = buf, nowait = true, silent = true }
  local function m(lhs, fn)
    if lhs and lhs ~= "" then
      vim.keymap.set("n", lhs, fn, o)
    end
  end
  m(km.allow, function() M.answer("allow", s.slug) end)
  m(km.deny, function() M.answer("deny", s.slug) end)
  m(km.always, function() M.answer("always", s.slug) end)
  m(km.go, function() M.go(s.slug) end)
  m(km.close, M.close)
  m("q", M.close)
end

function M.current()
  return cur
end

function M.on_waiting(s)
  if type(s) ~= "table" or not s.slug then
    return
  end
  sessions.update(s)
  if s.wait_kind ~= "permission" then
    return
  end
  remove(s.slug)
  table.insert(M.queue, s)
  if not cur or cur.slug == s.slug then
    show(s)
  end
end

function M.on_updated(s)
  if type(s) ~= "table" or not s.slug then
    return
  end
  if s.state ~= "waiting" then
    local had = false
    for _, q in ipairs(M.queue) do
      if q.slug == s.slug then
        had = true
      end
    end
    if had then
      remove(s.slug)
      if cur and cur.slug == s.slug then
        show_next()
      end
    end
  end
end

function M.setup_events()
  client.on("session.waiting", function(s)
    if cfg().approval then
      M.on_waiting(s)
    end
  end)
  client.on("session.updated", M.on_updated)
  client.on("session.removed", function(s)
    if type(s) == "table" then
      M.on_updated(vim.tbl_extend("force", s, { state = "removed" }))
    end
  end)
  client.on("session.done", M.on_updated)
end

return M
