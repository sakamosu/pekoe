-- C. 許可待ちの承認フロート(右下)。フォーカスは奪わない。
-- <leader>ba(:PekoeApproval)で入って y / n / A / g / Esc。
--
-- pending_tool は pekoe の要約で、トークンなしの session.event でも書ける
-- (偽造できる)。だからフロートにはエージェントの画面の末尾(session.screen)を
-- 並べ、判断は画面で行う。answer には見せたツールを expect として付け、
-- pekoed の pending_tool が変わっていれば断られる(Web UI と同じ)。
local client = require("pekoe.client")
local ui = require("pekoe.ui")
local sessions = require("pekoe.sessions")

local M = {}

M.queue = {} -- 許可待ちの Session(古い順)
local cur = nil -- {slug, tool, buf, win, prev, screen, gen}
local gen = 0

M.screen_rows = 20 -- フロートに出す画面の行数

local function cfg()
  return require("pekoe").config
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

local function strip_ansi(l)
  l = l:gsub("\27%][^\7\27]*\7", ""):gsub("\27%][^\7\27]*\27\\", "")
  l = l:gsub("\27%[[%d;?]*[ -/]*[@-~]", ""):gsub("\27.", "")
  return (l:gsub("[%z\1-\8\11-\31\127]", ""))
end

--- 画面の末尾 n 行(色なし、末尾の空行は落とす)
local function screen_tail(sc, n)
  local lines = {}
  for _, l in ipairs((type(sc) == "table" and sc.lines) or {}) do
    table.insert(lines, (strip_ansi(l):gsub("%s+$", "")))
  end
  while #lines > 0 and lines[#lines] == "" do
    table.remove(lines)
  end
  local from = math.max(1, #lines - n + 1)
  return vim.list_slice(lines, from, #lines)
end

local function remove(slug)
  for i, s in ipairs(M.queue) do
    if s.slug == slug then
      table.remove(M.queue, i)
      return
    end
  end
end

local function queued(slug)
  for _, s in ipairs(M.queue) do
    if s.slug == slug then
      return s
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
--- 見せたツール(フロートか待ち行列のもの)を expect に付ける。
function M.answer(answer, slug)
  slug = slug or (cur and cur.slug) or (M.queue[1] and M.queue[1].slug)
  if not slug then
    ui.notify("no sessions waiting for permission", vim.log.levels.WARN)
    return
  end
  local params = { slug = slug, answer = answer }
  local shown
  if cur and cur.slug == slug then
    shown = cur.tool
  elseif queued(slug) then
    shown = queued(slug).pending_tool
  end
  if (cur and cur.slug == slug) or queued(slug) then
    params.expect = { tool = shown or vim.NIL }
  end
  client.request("session.answer", params, function(err)
    if err then
      if err:find("prompt changed", 1, true) then
        ui.notify(slug .. ": the prompt changed; look again", vim.log.levels.WARN)
        client.request("session.get", { slug = slug }, function(e, s)
          if not e and type(s) == "table" then
            M.on_waiting(s)
          end
        end)
      else
        ui.notify("session.answer: " .. err, vim.log.levels.ERROR)
      end
      return
    end
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
  if vim.env.PEKOE_SESSION and vim.env.PEKOE_SESSION ~= "" then
    ui.notify(slug .. ": go back to the pekoe TUI and open the session (:PekoeSend takes you back)")
  else
    ui.notify(slug .. ": run `pekoe` in a terminal to answer this session (set as send target)")
  end
end

--- フロートに入る(<leader>ba / :PekoeApproval)。
function M.focus()
  if not cur or not vim.api.nvim_win_is_valid(cur.win) then
    if #M.queue > 0 then
      show(M.queue[1])
    end
  end
  if not cur or not vim.api.nvim_win_is_valid(cur.win) then
    ui.notify("no sessions waiting for permission", vim.log.levels.WARN)
    return
  end
  local w = vim.api.nvim_get_current_win()
  if w ~= cur.win then
    cur.prev = w
    vim.api.nvim_set_current_win(cur.win)
  end
end

local function build_rows(s, screen)
  local km = cfg().keymaps.approval
  local pt = s.pending_tool or {}
  local rows = {
    { { "● ", "PekoeWarn" }, { "Permission", "PekoeWarn" }, { " · ", "PekoeDim" }, { s.slug, "PekoeTitle" } },
    "",
  }
  -- pekoe の要約(フックから届いたもの。偽造できるので判断には使わない)
  if pt.name then
    table.insert(rows, { { "  " .. pt.name, "PekoeSecondary" }, { "  (pekoe's summary — decide from the screen)", "PekoeDim" } })
  end
  for _, l in ipairs(vim.split(tool_input_str(pt.input), "\n")) do
    if l ~= "" then
      table.insert(rows, "  " .. l)
    end
  end
  table.insert(rows, "")
  table.insert(rows, { { "── screen ──", "PekoeDim" } })
  if screen == nil then
    table.insert(rows, { { "  (loading the agent's screen…)", "PekoeDim" } })
  elseif screen == false then
    table.insert(rows, { { "  (could not read the screen: g to look at the session)", "PekoeWarn" } })
  else
    for _, l in ipairs(screen_tail(screen, M.screen_rows)) do
      table.insert(rows, l)
    end
  end
  table.insert(rows, "")
  local hint = {}
  local pairs_ = { { km.allow, "allow" }, { km.deny, "deny" }, { km.always, "always" }, { km.go, "go" }, { km.close, "close" } }
  if screen == nil or screen == false then
    pairs_ = { { km.deny, "deny" }, { km.go, "go" }, { km.close, "close" } }
  end
  for _, p in ipairs(pairs_) do
    if p[1] then
      table.insert(hint, { p[1] .. " ", "PekoeKey" })
      table.insert(hint, { p[2] .. "  ", "PekoeDim" })
    end
  end
  table.insert(rows, hint)
  local focus = cfg().keymaps.approval_focus
  table.insert(rows, { { "  " .. (focus and (focus .. " / ") or "") .. ":PekoeApproval to focus", "PekoeDim" } })
  if #M.queue > 1 then
    table.insert(rows, { { string.format("  (%d more waiting)", #M.queue - 1), "PekoeDim" } })
  end
  return rows
end

local function rows_width(rows)
  local w = 0
  for _, r in ipairs(rows) do
    local t = type(r) == "string" and r or table.concat(vim.tbl_map(function(c) return c[1] end, r))
    w = math.max(w, vim.fn.strdisplaywidth(t))
  end
  return math.min(math.max(36, w + 2), 90, math.max(36, vim.o.columns - 4))
end

--- 画面が届いたら描き直す。中身が変わったので、入っていれば外に出す
--- (読んでいない内容に y が当たらないように)。
local function redraw(c)
  if not c or cur ~= c or not vim.api.nvim_win_is_valid(c.win) then
    return
  end
  local rows = build_rows(c.session, c.screen)
  local lines = ui.render_chunks(c.buf, rows)
  local wc = vim.api.nvim_win_get_config(c.win)
  wc.width = rows_width(rows)
  wc.height = math.min(#lines, math.max(1, math.floor(vim.o.lines * 0.6)))
  pcall(vim.api.nvim_win_set_config, c.win, wc)
  if vim.api.nvim_get_current_win() == c.win and c.prev and vim.api.nvim_win_is_valid(c.prev) then
    vim.api.nvim_set_current_win(c.prev)
  end
end

show = function(s)
  M.close()
  local km = cfg().keymaps.approval
  local rows = build_rows(s, nil)
  local prev = vim.api.nvim_get_current_win()
  -- フォーカスは奪わない: 打鍵中の y が allow にならないように
  local buf, win = ui.float(rows, {
    anchor = "bottom-right",
    enter = false,
    border_hl = "PekoeWarn",
    min_width = 36,
    width = rows_width(rows),
    zindex = 80,
  })
  gen = gen + 1
  local c = { slug = s.slug, session = s, tool = s.pending_tool, buf = buf, win = win, prev = prev, gen = gen }
  cur = c
  local function m(lhs, fn, nowait)
    if lhs and lhs ~= "" then
      vim.keymap.set("n", lhs, fn, { buffer = buf, nowait = nowait, silent = true })
    end
  end
  -- allow / always は画面が出てから。nowait を付けないので、y で始まる
  -- 割り当て(yy など)の途中で発火しない
  local function needs_screen(fn)
    return function()
      if type(c.screen) ~= "table" then
        ui.notify(s.slug .. ": the screen is not shown; g to look at the session", vim.log.levels.WARN)
        return
      end
      fn()
    end
  end
  m(km.allow, needs_screen(function() M.answer("allow", s.slug) end), false)
  m(km.always, needs_screen(function() M.answer("always", s.slug) end), false)
  m(km.deny, function() M.answer("deny", s.slug) end, true)
  m(km.go, function() M.go(s.slug) end, true)
  m(km.close, M.close, true)
  m("q", M.close, true)
  client.request("session.screen", { slug = s.slug }, function(err, sc)
    if cur ~= c then
      return
    end
    c.screen = (not err and type(sc) == "table") and sc or false
    redraw(c)
  end)
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
