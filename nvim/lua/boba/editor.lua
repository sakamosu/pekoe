-- F. 逆方向: editor.open イベント({path, line, note, slug})でその行に飛び、メモをフロートに出す。
local ui = require("boba.ui")
local sessions = require("boba.sessions")

local M = {}

local note_win = nil

local function under(path, root)
  if not root or root == "" then
    return false
  end
  root = vim.fs.normalize(vim.uv.fs_realpath(root) or root):gsub("/$", "")
  path = vim.fs.normalize(vim.uv.fs_realpath(path) or path)
  return path == root or path:sub(1, #root + 1) == root .. "/"
end

--- このイベントをこの nvim が扱うべきか。
--- $BOBA_SESSION がある(boba が起動した nvim)ならその slug のものだけ、
--- それ以外は path が cwd 配下か slug なしのもの。
function M.should_handle(d, abs)
  local mode = require("boba").config.editor_open
  if mode == false then
    return false
  end
  if mode == "all" then
    return true
  end
  local env = vim.env.BOBA_SESSION
  if env and env ~= "" and d.slug and d.slug ~= "" then
    return d.slug == env
  end
  if not d.slug or d.slug == "" then
    return true
  end
  return under(abs, vim.fn.getcwd())
end

local function resolve_path(d)
  local p = d.path or ""
  if p:sub(1, 1) == "/" then
    return vim.fs.normalize(p)
  end
  local s = d.slug and sessions.cache[d.slug]
  local root = (s and s.path) or vim.fn.getcwd()
  return vim.fs.normalize(root .. "/" .. p)
end

function M.show_note(note)
  ui.close_win(note_win)
  note_win = nil
  if not note or note == "" then
    return
  end
  local rows = {}
  for _, l in ipairs(vim.split(note, "\n")) do
    table.insert(rows, l)
  end
  local _, win = ui.float(rows, {
    anchor = "cursor",
    enter = false,
    title = "✎ agent",
    width = math.min(70, math.max(20, (function()
      local w = 0
      for _, r in ipairs(rows) do
        w = math.max(w, vim.fn.strdisplaywidth(r))
      end
      return w + 1
    end)())),
    border_hl = "BobaSecondary",
  })
  note_win = win
  -- カーソルが動いたら閉じる(開いた直後の移動は無視)
  vim.defer_fn(function()
    vim.api.nvim_create_autocmd({ "CursorMoved", "CursorMovedI", "BufLeave" }, {
      once = true,
      callback = function()
        if note_win == win then
          ui.close_win(win)
          note_win = nil
        end
      end,
    })
  end, 50)
end

function M.note_win()
  return note_win
end

function M.open(d)
  if type(d) ~= "table" or not d.path then
    return
  end
  local abs = resolve_path(d)
  if not M.should_handle(d, abs) then
    return
  end
  local ok = pcall(vim.cmd, "edit " .. vim.fn.fnameescape(abs))
  if not ok then
    vim.cmd("split " .. vim.fn.fnameescape(abs))
  end
  local line = tonumber(d.line) or 1
  pcall(vim.api.nvim_win_set_cursor, 0, { math.max(1, math.min(line, vim.api.nvim_buf_line_count(0))), 0 })
  vim.cmd("normal! zz")
  M.show_note(d.note)
end

function M.setup_events()
  require("boba.client").on("editor.open", M.open)
end

return M
