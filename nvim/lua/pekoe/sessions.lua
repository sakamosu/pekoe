-- セッション一覧・ピッカー・送信先(target)の管理。
local client = require("pekoe.client")
local ui = require("pekoe.ui")

local M = {}

M.cache = {} -- slug -> Session(イベントで更新)
M.target = nil -- :PekoeSessions で選んだ送信先

function M.update(s)
  if type(s) == "table" and s.slug then
    M.cache[s.slug] = s
  end
end

function M.remove(s)
  if type(s) == "table" and s.slug then
    M.cache[s.slug] = nil
  end
end

--- 既定の送信先: :PekoeSessions の選択 > $PEKOE_SESSION
function M.default_slug()
  if M.target and M.target ~= "" then
    return M.target
  end
  local env = vim.env.PEKOE_SESSION
  if env and env ~= "" then
    return env
  end
  return nil
end

function M.list(cb)
  client.call("session.list", {}, function(res)
    res = res or {}
    for _, s in ipairs(res) do
      M.update(s)
    end
    cb(res)
  end)
end

function M.format(s)
  local sym, _, word = ui.state(s.state)
  local extra = ""
  if s.state == "waiting" and s.wait_kind then
    extra = " (" .. s.wait_kind .. ")"
  end
  return string.format("%s %s  %s%s", sym, s.slug, word, extra)
end

--- vim.ui.select でセッションを選ぶ。cb(Session)。
function M.pick(cb, prompt)
  M.list(function(list)
    if #list == 0 then
      ui.notify("no sessions", vim.log.levels.WARN)
      return
    end
    vim.ui.select(list, {
      prompt = prompt or "pekoe session",
      format_item = M.format,
      kind = "pekoe_session",
    }, function(choice)
      if choice then
        cb(choice)
      end
    end)
  end)
end

--- slug を解決する(引数 > target/$PEKOE_SESSION > ピッカー)。cb(slug)。
function M.resolve(slug, cb, prompt)
  if slug and slug ~= "" then
    return cb(slug)
  end
  local d = M.default_slug()
  if d then
    return cb(d)
  end
  M.pick(function(s)
    cb(s.slug)
  end, prompt)
end

--- slug の Session を取得(キャッシュ優先)。cb(Session|nil)
function M.get(slug, cb)
  if M.cache[slug] then
    return cb(M.cache[slug])
  end
  client.request("session.get", { slug = slug }, function(err, s)
    if err then
      return cb(nil)
    end
    M.update(s)
    cb(s)
  end)
end

--- path を worktree からの相対にする(worktree 外なら絶対パス)。
local function real(p)
  p = vim.fn.fnamemodify(p, ":p")
  return vim.fs.normalize(vim.uv.fs_realpath(p) or p)
end

function M.relpath(abs, root)
  abs = real(abs)
  if root and root ~= "" then
    root = real(root):gsub("/$", "")
    if abs:sub(1, #root + 1) == root .. "/" then
      return abs:sub(#root + 2)
    end
  end
  return abs
end

-- :PekoeSessions のクイックリスト
function M.open_list()
  M.list(function(list)
    local rows = {}
    for _, s in ipairs(list) do
      local sym, hl, word = ui.state(s.state)
      local cur = (s.slug == M.default_slug()) and "*" or " "
      table.insert(rows, {
        { cur .. " ", "PekoeSecondary" },
        { sym .. " ", hl },
        { string.format("%-28s", s.slug) },
        { word, hl },
        { s.last_tool and ("  " .. s.last_tool) or "", "PekoeDim" },
      })
    end
    if #rows == 0 then
      rows = { { { "  no sessions", "PekoeDim" } } }
    end
    local buf, win = ui.float(rows, { title = "◆ Sessions", enter = true, min_width = 50 })
    vim.wo[win].cursorline = true
    vim.wo[win].winhighlight = vim.wo[win].winhighlight .. ",CursorLine:PekoeSelect"
    local o = { buffer = buf, nowait = true, silent = true }
    vim.keymap.set("n", "<CR>", function()
      local s = list[vim.api.nvim_win_get_cursor(win)[1]]
      ui.close_win(win)
      if s then
        M.target = s.slug
        ui.notify("send target: " .. s.slug)
      end
    end, o)
    vim.keymap.set("n", "r", function()
      local s = list[vim.api.nvim_win_get_cursor(win)[1]]
      ui.close_win(win)
      if s then
        require("pekoe.review").open(s.slug)
      end
    end, o)
    for _, k in ipairs({ "q", "<Esc>" }) do
      vim.keymap.set("n", k, function() ui.close_win(win) end, o)
    end
  end)
end

return M
