-- 配色・状態記号・フロートの共通部品。
local M = {}

M.ns = vim.api.nvim_create_namespace("boba")

-- docs/ui-guidelines.md のカラートークン。default = true なのでユーザーが上書きできる。
function M.setup_highlights()
  local set = function(name, val)
    val.default = true
    vim.api.nvim_set_hl(0, name, val)
  end
  set("BobaPrimary", { fg = "#ff87da", ctermfg = 5 })
  set("BobaSecondary", { fg = "#11cded", ctermfg = 6 })
  set("BobaWarn", { fg = "#edc812", ctermfg = 3 })
  set("BobaOk", { fg = "#51d75e", ctermfg = 2 })
  set("BobaErr", { fg = "#ff7172", ctermfg = 1 })
  set("BobaDim", { fg = "#848495", ctermfg = 8 })
  set("BobaBorder", { fg = "#3b3b4d", ctermfg = 8 })
  set("BobaSelect", { bg = "#441e39", cterm = { reverse = true } })
  set("BobaKey", { fg = "#11cded", bold = true, ctermfg = 6 })
  -- 派生(役割へのリンク)
  set("BobaTitle", { fg = "#ff87da", bold = true, ctermfg = 5 })
  set("BobaFloatBorder", { link = "BobaPrimary" })
  set("BobaDiffAdd", { link = "BobaOk" })
  set("BobaDiffDelete", { link = "BobaErr" })
  set("BobaHunkHeader", { link = "BobaSecondary" })
  set("BobaPromptNote", { link = "BobaDim" })
  set("BobaComment", { link = "BobaSecondary" })
  set("BobaCommentSent", { link = "BobaDim" })
  set("BobaLineNr", { link = "BobaDim" })
end

-- state -> {symbol, hl, word}
local STATES = {
  creating = { "◌", "BobaDim", "creating" },
  running = { "●", "BobaOk", "run" },
  waiting = { "●", "BobaWarn", "wait" },
  idle = { "○", "BobaDim", "idle" },
  done = { "✓", "BobaSecondary", "done" },
  reviewing = { "●", "BobaSecondary", "review" },
  approved = { "✓", "BobaOk", "approved" },
  landed = { "✓", "BobaDim", "landed" },
  failed = { "✗", "BobaErr", "fail" },
}

---@return string symbol, string hl, string word
function M.state(state)
  local s = STATES[state or ""]
  if not s then
    return "○", "BobaDim", tostring(state or "?")
  end
  return s[1], s[2], s[3]
end

function M.scratch_buf(ft)
  local buf = vim.api.nvim_create_buf(false, true)
  vim.bo[buf].bufhidden = "wipe"
  vim.bo[buf].buftype = "nofile"
  vim.bo[buf].swapfile = false
  if ft then
    vim.bo[buf].filetype = ft
  end
  return buf
end

--- 行ごとの {text, hl} 片(chunks)を描く。chunks は { {text, hl?}, ... } の配列の配列。
function M.render_chunks(buf, rows)
  local lines, marks = {}, {}
  for i, row in ipairs(rows) do
    if type(row) == "string" then
      lines[i] = row
    else
      local parts, col = {}, 0
      for _, c in ipairs(row) do
        local t = c[1] or ""
        if c[2] then
          table.insert(marks, { i - 1, col, col + #t, c[2] })
        end
        table.insert(parts, t)
        col = col + #t
      end
      lines[i] = table.concat(parts)
    end
  end
  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.bo[buf].modifiable = false
  vim.api.nvim_buf_clear_namespace(buf, M.ns, 0, -1)
  for _, m in ipairs(marks) do
    vim.api.nvim_buf_set_extmark(buf, M.ns, m[1], m[2], { end_col = m[3], hl_group = m[4] })
  end
  return lines
end

local function rows_width(lines, min)
  local w = min or 20
  for _, l in ipairs(lines) do
    w = math.max(w, vim.fn.strdisplaywidth(l))
  end
  return math.min(w, math.floor(vim.o.columns * 0.8))
end

--- フロートを開く。
---@param rows table 行(string か chunks)
---@param opts table {title, enter, anchor = "center"|"cursor"|"bottom-right", width, border_hl, ft}
---@return integer buf, integer win
function M.float(rows, opts)
  opts = opts or {}
  local buf = M.scratch_buf(opts.ft)
  local lines = M.render_chunks(buf, rows)
  local width = opts.width or rows_width(lines, opts.min_width)
  local height = math.min(#lines, math.max(1, math.floor(vim.o.lines * 0.6)))
  local cfg = {
    style = "minimal",
    border = "rounded",
    width = width,
    height = height,
    focusable = true,
    zindex = opts.zindex or 60,
  }
  if opts.title then
    cfg.title = { { " " .. opts.title .. " ", opts.title_hl or "BobaTitle" } }
    cfg.title_pos = "left"
  end
  local anchor = opts.anchor or "center"
  if anchor == "cursor" then
    cfg.relative = "cursor"
    cfg.row = 1
    cfg.col = 0
  elseif anchor == "bottom-right" then
    cfg.relative = "editor"
    cfg.anchor = "SE"
    cfg.row = vim.o.lines - vim.o.cmdheight - 1
    cfg.col = vim.o.columns - 1
  else
    cfg.relative = "editor"
    cfg.row = math.floor((vim.o.lines - height) / 2) - 1
    cfg.col = math.floor((vim.o.columns - width) / 2)
  end
  local win = vim.api.nvim_open_win(buf, opts.enter == true, cfg)
  vim.wo[win].winhighlight = "FloatBorder:" .. (opts.border_hl or "BobaFloatBorder") .. ",NormalFloat:Normal"
  vim.wo[win].wrap = true
  return buf, win
end

function M.close_win(win)
  if win and vim.api.nvim_win_is_valid(win) then
    pcall(vim.api.nvim_win_close, win, true)
  end
end

--- フローティング入力。<CR> で確定(挿入・ノーマル両方)、<C-j> で改行、ノーマルの <Esc>/q で取消。
---@param opts table {title, default}
---@param cb fun(text: string|nil)
function M.input(opts, cb)
  opts = opts or {}
  local prev = vim.api.nvim_get_current_win()
  local buf = M.scratch_buf("markdown")
  local width = math.min(math.max(50, vim.fn.strdisplaywidth(opts.title or "") + 6), vim.o.columns - 4)
  local win = vim.api.nvim_open_win(buf, true, {
    relative = opts.relative or "cursor",
    row = 1,
    col = 0,
    width = width,
    height = opts.height or 3,
    style = "minimal",
    border = "rounded",
    title = { { " " .. (opts.title or "boba") .. " ", "BobaTitle" } },
    title_pos = "left",
    footer = { { " ⏎ ", "BobaKey" }, { "send ", "BobaDim" }, { " ^j ", "BobaKey" }, { "newline ", "BobaDim" }, { " esc ", "BobaKey" }, { "cancel ", "BobaDim" } },
    footer_pos = "right",
    zindex = 70,
  })
  vim.wo[win].winhighlight = "FloatBorder:BobaFloatBorder,NormalFloat:Normal"
  vim.wo[win].wrap = true
  if opts.default and opts.default ~= "" then
    vim.api.nvim_buf_set_lines(buf, 0, -1, false, vim.split(opts.default, "\n"))
  end
  local finished = false
  local function finish(text)
    if finished then
      return
    end
    finished = true
    vim.cmd("stopinsert")
    M.close_win(win)
    if vim.api.nvim_win_is_valid(prev) then
      vim.api.nvim_set_current_win(prev)
    end
    vim.schedule(function()
      cb(text)
    end)
  end
  local function submit()
    local lines = vim.api.nvim_buf_get_lines(buf, 0, -1, false)
    finish(vim.trim(table.concat(lines, "\n")))
  end
  local o = { buffer = buf, nowait = true, silent = true }
  vim.keymap.set({ "i", "n" }, "<CR>", submit, o)
  vim.keymap.set("i", "<C-j>", "<CR>", { buffer = buf, noremap = true })
  vim.keymap.set("n", "<Esc>", function() finish(nil) end, o)
  vim.keymap.set("n", "q", function() finish(nil) end, o)
  vim.keymap.set("i", "<C-c>", function() finish(nil) end, o)
  vim.api.nvim_create_autocmd("WinLeave", {
    buffer = buf,
    once = true,
    callback = function()
      finish(nil)
    end,
  })
  vim.cmd("startinsert!")
  return buf, win
end

function M.notify(msg, level)
  vim.notify("boba: " .. msg, level or vim.log.levels.INFO)
end

return M
