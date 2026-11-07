-- D. :BobaFollow <slug> — エージェントが編集したファイルを split に開き、変更箇所へ飛ぶ。
local client = require("boba.client")
local ui = require("boba.ui")
local sessions = require("boba.sessions")

local M = {}

local F = nil -- {slug, root, last_tool, pending = {path, before}, win, unsub, timer}

local function read_lines(path)
  local ok, lines = pcall(vim.fn.readfile, path)
  return ok and lines or nil
end

--- last_tool("Edit src/api.ts" など)から編集対象のパスを取り出す。
function M.parse_tool(last_tool)
  if type(last_tool) ~= "string" then
    return nil
  end
  local tool, rest = last_tool:match("^(%a+)%s+(.+)$")
  if not tool then
    return nil
  end
  if tool == "Edit" or tool == "Write" or tool == "MultiEdit" or tool == "NotebookEdit" then
    return vim.trim(rest)
  end
  return nil
end

--- before → after で最初に変わった行(after 側、1 始まり)。
function M.first_changed_line(before, after)
  if not before then
    return nil
  end
  local a = table.concat(before, "\n") .. "\n"
  local b = table.concat(after or {}, "\n") .. "\n"
  local ok, idx = pcall(vim.diff, a, b, { result_type = "indices" })
  if not ok or type(idx) ~= "table" or not idx[1] then
    return nil
  end
  local h = idx[1]
  return math.max(1, h[4] > 0 and h[3] or h[3] + 1)
end

local function git_first_line(root, path)
  local out = vim.fn.systemlist({ "git", "-C", root, "diff", "-U0", "--", path })
  if vim.v.shell_error ~= 0 then
    return nil
  end
  for _, l in ipairs(out) do
    local n = l:match("^@@ %-%d+,?%d* %+(%d+)")
    if n then
      return math.max(1, tonumber(n))
    end
  end
  return nil
end

local function abs_path(p)
  if p:sub(1, 1) == "/" then
    return p
  end
  return (F.root or vim.fn.getcwd()) .. "/" .. p
end

local function show(pending)
  if not F then
    return
  end
  local path = pending.path
  local after = read_lines(path)
  local line = M.first_changed_line(pending.before, after) or git_first_line(F.root or vim.fn.getcwd(), path) or 1
  -- 既にその file を表示している窓があればそこ、なければ follow 用 split を使い回す
  local target
  for _, w in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
    if vim.fs.normalize(vim.api.nvim_buf_get_name(vim.api.nvim_win_get_buf(w))) == vim.fs.normalize(path) then
      target = w
      break
    end
  end
  local prev = vim.api.nvim_get_current_win()
  if target then
    vim.api.nvim_set_current_win(target)
    vim.cmd("checktime")
  elseif F.win and vim.api.nvim_win_is_valid(F.win) then
    vim.api.nvim_set_current_win(F.win)
    vim.cmd("edit " .. vim.fn.fnameescape(path))
  else
    vim.cmd("split " .. vim.fn.fnameescape(path))
    F.win = vim.api.nvim_get_current_win()
  end
  local win = vim.api.nvim_get_current_win()
  pcall(vim.api.nvim_win_set_cursor, win, { math.min(line, vim.api.nvim_buf_line_count(0)), 0 })
  vim.cmd("normal! zz")
  if prev ~= win and vim.api.nvim_win_is_valid(prev) then
    vim.api.nvim_set_current_win(prev)
  end
end

local function flush_pending()
  if F and F.pending then
    local p = F.pending
    F.pending = nil
    if F.timer then
      F.timer:stop()
    end
    show(p)
  end
end

local function on_updated(s)
  if not F or type(s) ~= "table" or s.slug ~= F.slug then
    return
  end
  if s.path then
    F.root = s.path
  end
  if s.last_tool == F.last_tool then
    return
  end
  F.last_tool = s.last_tool
  -- 前のツールが終わった合図として、保留中の編集を表示する
  flush_pending()
  local p = M.parse_tool(s.last_tool)
  if not p then
    return
  end
  local path = abs_path(p)
  -- last_tool は PreToolUse 時点。編集前の中身を控え、少し待ってから差分を取る
  F.pending = { path = path, before = read_lines(path) }
  F.timer = F.timer or vim.uv.new_timer()
  F.timer:stop()
  F.timer:start(1500, 0, vim.schedule_wrap(flush_pending))
end

function M.start(slug)
  M.stop(true)
  sessions.resolve(slug, function(s)
    F = { slug = s }
    sessions.get(s, function(sess)
      if F and F.slug == s and sess then
        F.root = sess.path
        F.last_tool = sess.last_tool
      end
    end)
    F.unsub = client.on("session.updated", on_updated)
    ui.notify("follow: " .. s .. "(:BobaFollow! で停止)")
  end, "follow するセッション")
end

function M.stop(quiet)
  if not F then
    return
  end
  if F.unsub then
    F.unsub()
  end
  if F.timer then
    F.timer:stop()
    F.timer:close()
  end
  local slug = F.slug
  F = nil
  if not quiet then
    ui.notify("follow 停止: " .. slug)
  end
end

function M.active()
  return F and F.slug
end

return M
