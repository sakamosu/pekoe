-- A. レビュー面: 左にファイル一覧、右に差分。hunk 単位で承認・差し戻し・コメント・質問。
local client = require("boba.client")
local ui = require("boba.ui")
local sessions = require("boba.sessions")

local M = {}

local ns_cur = vim.api.nvim_create_namespace("boba_review_cursor")
local ns_virt = vim.api.nvim_create_namespace("boba_review_virt")

---@class BobaReviewState
---@field slug string
---@field data table|nil Review オブジェクト
---@field file_idx integer
---@field show_approved boolean
---@field line_info table<integer, {hunk: table, line: table|nil, hunk_idx: integer}>
---@field hunk_lnums integer[]
local R = nil
M._asked = {} -- hunk_id -> true(review.ask を投げたもの)

local function cfg()
  return require("boba").config
end

local function valid_win(w)
  return w and vim.api.nvim_win_is_valid(w)
end

local function valid_buf(b)
  return b and vim.api.nvim_buf_is_valid(b)
end

function M.state()
  return R
end

local function files()
  return (R and R.data and R.data.files) or {}
end

local function cur_file()
  return files()[R and R.file_idx or 1]
end

local function visible_hunks(file)
  local out = {}
  for _, h in ipairs((file and file.hunks) or {}) do
    if R.show_approved or not h.approved then
      table.insert(out, h)
    end
  end
  return out
end

local function count_approved(file)
  local n = 0
  for _, h in ipairs(file.hunks or {}) do
    if h.approved then
      n = n + 1
    end
  end
  return n
end

-- ---------------------------------------------------------------- 描画

local STATUS_HL = { A = "BobaOk", M = "BobaWarn", D = "BobaErr", R = "BobaWarn" }

local function render_list()
  if not valid_buf(R.list_buf) then
    return
  end
  local rows = {}
  for _, f in ipairs(files()) do
    local n = #(f.hunks or {})
    local m = count_approved(f)
    local all = n > 0 and m == n
    table.insert(rows, {
      { all and "✓ " or "● ", all and "BobaOk" or "BobaWarn" },
      { (f.status or "M") .. " ", STATUS_HL[f.status or "M"] or "BobaWarn" },
      { f.path, all and "BobaDim" or nil },
      { string.format("  %d hunks %d✓", n, m), "BobaDim" },
    })
  end
  if #rows == 0 then
    rows = { { { "  no changes", "BobaDim" } } }
  end
  ui.render_chunks(R.list_buf, rows)
  if valid_win(R.list_win) then
    local d = R.data or {}
    local test = ""
    if type(d.test) == "table" and d.test.status and d.test.status ~= "none" then
      test = d.test.status == "pass" and " %#BobaOk#✓test" or (d.test.status == "fail" and " %#BobaErr#✗test" or " %#BobaDim#…test")
    end
    -- 幅が足りないときは slug から先に切り、右端のキーヒントを残す(%<)
    vim.wo[R.list_win].winbar = string.format(
      "%%#BobaTitle# ± %%<%s %%#BobaDim#%s  %s/%s✓%s%%=%%#BobaKey#l %%#BobaDim#diff ",
      R.slug,
      (d.base_label or ""):gsub("%%", "%%%%"),
      tostring(d.approved or 0),
      tostring(d.total or 0),
      test
    )
    local n = math.max(1, math.min(R.file_idx, vim.api.nvim_buf_line_count(R.list_buf)))
    R.suppress_list_move = true
    pcall(vim.api.nvim_win_set_cursor, R.list_win, { n, 0 })
    R.suppress_list_move = false
  end
end

local function fmt_no(n)
  if not n or n == 0 then
    return ""
  end
  return tostring(n)
end

local function comment_target_lnum(c, file)
  -- 1. 該当行 2. hunk ヘッダ 3. 先頭
  local header
  for lnum, info in pairs(R.line_info) do
    local l = info.line
    if l and ((l.new or 0) == c.line or ((l.new or 0) == 0 and (l.old or 0) == c.line)) then
      if not c.hunk_id or c.hunk_id == info.hunk.id then
        return lnum
      end
    end
    if not l and c.hunk_id and info.hunk.id == c.hunk_id then
      header = lnum
    end
  end
  return header or 1
end

local function render_diff(restore)
  if not valid_buf(R.diff_buf) then
    return
  end
  local file = cur_file()
  local buf = R.diff_buf
  R.line_info, R.hunk_lnums = {}, {}
  local lines, hls, signs, notes = {}, {}, {}, {}
  if not file then
    lines = { "", "  nothing to review" }
  else
    local hunks = visible_hunks(file)
    for hi, h in ipairs(hunks) do
      local suffix = ""
      if h.approved then
        suffix = "  ✓ approved"
      elseif h.rejected then
        suffix = "  ✗ rejected"
      end
      table.insert(lines, (h.header or "@@") .. suffix)
      local ln = #lines
      R.line_info[ln] = { hunk = h, line = nil, hunk_idx = hi }
      table.insert(R.hunk_lnums, ln)
      table.insert(hls, { ln, 0, #(h.header or "@@"), "BobaHunkHeader" })
      if h.approved then
        table.insert(hls, { ln, #(h.header or "@@"), -1, "BobaOk" })
      elseif h.rejected then
        table.insert(hls, { ln, #(h.header or "@@"), -1, "BobaErr" })
      end
      if type(h.prompt) == "table" and h.prompt.n then
        table.insert(notes, { ln, string.format("prompt #%s: %s", h.prompt.n, h.prompt.text or "") })
      end
      for _, l in ipairs(h.lines or {}) do
        local kind = l.kind or " "
        local prefix = string.format("%4s %4s ", fmt_no(l.old), fmt_no(l.new))
        table.insert(lines, prefix .. kind .. (l.text or ""))
        local lnum = #lines
        R.line_info[lnum] = { hunk = h, line = l, hunk_idx = hi }
        table.insert(hls, { lnum, 0, #prefix, "BobaLineNr" })
        if kind == "+" then
          table.insert(hls, { lnum, #prefix, -1, "BobaDiffAdd" })
          table.insert(signs, { lnum, "BobaOk" })
        elseif kind == "-" then
          table.insert(hls, { lnum, #prefix, -1, "BobaDiffDelete" })
          table.insert(signs, { lnum, "BobaErr" })
        end
      end
      table.insert(lines, "")
    end
    if #hunks == 0 then
      local n = #(file.hunks or {})
      if n > 0 then
        lines = { "", string.format("  ✓ all %d hunks approved (%s to show)", n, cfg().keymaps.review.toggle_approved or "A") }
      else
        lines = { "", "  (no diff to show)" }
      end
    end
  end

  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.bo[buf].modifiable = false
  vim.api.nvim_buf_clear_namespace(buf, ui.ns, 0, -1)
  vim.api.nvim_buf_clear_namespace(buf, ns_virt, 0, -1)
  for _, h in ipairs(hls) do
    local text = lines[h[1]] or ""
    local ec = h[3] == -1 and #text or h[3]
    if ec > h[2] then
      vim.api.nvim_buf_set_extmark(buf, ui.ns, h[1] - 1, h[2], { end_col = ec, hl_group = h[4] })
    end
  end
  for _, s in ipairs(signs) do
    vim.api.nvim_buf_set_extmark(buf, ui.ns, s[1] - 1, 0, { sign_text = "▎", sign_hl_group = s[2] })
  end
  for _, n in ipairs(notes) do
    vim.api.nvim_buf_set_extmark(buf, ns_virt, n[1] - 1, 0, {
      virt_text = { { "  " .. n[2], "BobaPromptNote" } },
      virt_text_pos = "eol",
    })
  end
  -- コメント(未送信は secondary、送信済みは dim)
  if file then
    for _, c in ipairs((R.data and R.data.comments) or {}) do
      if c.path == file.path then
        local lnum = comment_target_lnum(c, file)
        if lnum <= #lines then
          local sent = c.sent == true
          local hl = sent and "BobaCommentSent" or "BobaComment"
          local vl = {}
          for i, t in ipairs(vim.split(c.text or "", "\n")) do
            table.insert(vl, {
              { i == 1 and ("          ✎ " .. (sent and "✓ " or "● ")) or "              ", hl },
              { t, hl },
            })
          end
          vim.api.nvim_buf_set_extmark(buf, ns_virt, lnum - 1, 0, { virt_lines = vl })
        end
      end
    end
  end

  if valid_win(R.diff_win) then
    local km = cfg().keymaps.review
    local hint = function(k, w)
      return k and string.format("%%#BobaKey#%s %%#BobaDim#%s ", k, w) or ""
    end
    -- h は先頭に固定し、幅が足りないときはパスから先に切る(%<)
    vim.wo[R.diff_win].winbar = string.format(
      " %s%%#BobaTitle#%%<%s %%=%s%s%s%s%s%s%s",
      hint("h", "files"),
      file and file.path:gsub("%%", "%%%%") or "",
      hint(km.next_hunk, "next"),
      hint(km.approve, "approve"),
      hint(km.reject, "reject"),
      hint(km.comment, "comment"),
      hint(km.ask, "ask"),
      hint(km.toggle_approved, R.show_approved and "hide✓" or "show✓"),
      hint(km.send, "send")
    )
    -- カーソル復元
    local target = 1
    restore = restore or {}
    local found = false
    if restore.hunk_id then
      for _, ln in ipairs(R.hunk_lnums) do
        if R.line_info[ln].hunk.id == restore.hunk_id then
          target = ln + (restore.offset or 0)
          found = true
          break
        end
      end
    end
    if not found and #R.hunk_lnums > 0 then
      local idx = math.max(1, math.min(restore.idx or 1, #R.hunk_lnums))
      target = R.hunk_lnums[idx]
    end
    target = math.max(1, math.min(target, #lines))
    pcall(vim.api.nvim_win_set_cursor, R.diff_win, { target, 0 })
    M.update_cursor_mark()
  end
end

function M.update_cursor_mark()
  if not (R and valid_buf(R.diff_buf)) then
    return
  end
  vim.api.nvim_buf_clear_namespace(R.diff_buf, ns_cur, 0, -1)
  local _, _, ln = M.current_hunk()
  if ln then
    vim.api.nvim_buf_set_extmark(R.diff_buf, ns_cur, ln - 1, 0, { sign_text = "▶", sign_hl_group = "BobaPrimary", priority = 20 })
  end
end

-- ---------------------------------------------------------------- 取得

local function fetch(cb)
  local slug = R.slug
  client.request("review.files", { slug = slug, show_approved = true }, function(err, res)
    if not R or R.slug ~= slug then
      return
    end
    if err then
      -- 未接続なら繋がった時点(boba.connected)で取り直すので黙る
      if not err:find("not connected", 1, true) then
        ui.notify("review.files: " .. err, vim.log.levels.ERROR)
      end
      return
    end
    R.data = res or {}
    if cb then
      cb()
    end
  end)
end

local function snapshot_cursor()
  if not (R and valid_win(R.diff_win)) then
    return {}
  end
  local h, idx, ln = M.current_hunk()
  if not h then
    return {}
  end
  local cur = vim.api.nvim_win_get_cursor(R.diff_win)[1]
  return { hunk_id = h.id, offset = cur - ln, idx = idx, path = (cur_file() or {}).path }
end

local function find_file_idx(path)
  for i, f in ipairs(files()) do
    if f.path == path then
      return i
    end
  end
end

local function next_file_with_hunks(from)
  local fs = files()
  for k = 1, #fs do
    local i = ((from - 1 + k) % #fs) + 1
    if #visible_hunks(fs[i]) > 0 then
      return i
    end
  end
end

--- 再取得して再描画する。opts.restore でカーソル位置、opts.advance_if_empty で空になったら次のファイルへ。
function M.refresh(opts)
  if not R then
    return
  end
  opts = opts or {}
  local restore = opts.restore or snapshot_cursor()
  local path = (cur_file() or {}).path
  fetch(function()
    R.file_idx = (path and find_file_idx(path)) or math.min(R.file_idx, math.max(1, #files()))
    if opts.advance_if_empty and #visible_hunks(cur_file()) == 0 then
      local nxt = next_file_with_hunks(R.file_idx)
      if nxt then
        R.file_idx = nxt
        restore = { idx = 1 }
      end
    end
    render_list()
    render_diff(restore)
    if opts.cb then
      opts.cb()
    end
  end)
end

local refresh_timer
local function refresh_debounced()
  if refresh_timer then
    refresh_timer:stop()
  else
    refresh_timer = vim.uv.new_timer()
  end
  refresh_timer:start(80, 0, vim.schedule_wrap(function()
    M.refresh()
  end))
end

-- ---------------------------------------------------------------- 操作

--- カーソル位置の hunk。返り値: hunk, 表示上の index, ヘッダ行番号
function M.current_hunk()
  if not (R and valid_win(R.diff_win)) or #R.hunk_lnums == 0 then
    return nil
  end
  local cur = vim.api.nvim_win_get_cursor(R.diff_win)[1]
  local best = 1
  for i, ln in ipairs(R.hunk_lnums) do
    if ln <= cur then
      best = i
    end
  end
  local ln = R.hunk_lnums[best]
  return R.line_info[ln].hunk, best, ln
end

function M.select_file(idx, opts)
  if not R or not files()[idx] then
    return
  end
  opts = opts or {}
  if idx ~= R.file_idx or opts.force then
    R.file_idx = idx
    render_list()
    render_diff(opts.restore or { idx = 1 })
  end
  if opts.focus and valid_win(R.diff_win) then
    vim.api.nvim_set_current_win(R.diff_win)
  end
end

function M.move_hunk(dir)
  if not R then
    return
  end
  local _, idx = M.current_hunk()
  local cur = valid_win(R.diff_win) and vim.api.nvim_win_get_cursor(R.diff_win)[1] or 1
  if idx then
    local ln = R.hunk_lnums[idx]
    local target
    if dir > 0 then
      target = R.hunk_lnums[idx + 1]
    elseif cur > ln then
      target = ln
    else
      target = R.hunk_lnums[idx - 1]
    end
    if target then
      vim.api.nvim_win_set_cursor(R.diff_win, { target, 0 })
      vim.cmd("normal! zt")
      M.update_cursor_mark()
      return
    end
  end
  -- ファイルの端: 隣のファイルへ
  local fs = files()
  local i = R.file_idx
  for _ = 1, #fs do
    i = ((i - 1 + dir) % #fs) + 1
    if #visible_hunks(fs[i]) > 0 then
      M.select_file(i, { force = true, restore = { idx = dir > 0 and 1 or math.huge } })
      return
    end
  end
end

local function with_hunk(fn)
  local h, idx = M.current_hunk()
  if not h then
    ui.notify("no hunk here", vim.log.levels.WARN)
    return
  end
  fn(h, idx)
end

function M.approve()
  with_hunk(function(h, idx)
    local method = h.approved and "review.unapprove" or "review.approve"
    client.call(method, { slug = R.slug, hunk_id = h.id }, function()
      local restore = { idx = (R.show_approved and not h.approved) and idx + 1 or idx }
      M.refresh({ restore = restore, advance_if_empty = true })
    end)
  end)
end

function M.reject()
  with_hunk(function(h, idx)
    ui.input({ title = "✗ send back (why? optional)" }, function(text)
      if text == nil then
        return
      end
      local p = { slug = R.slug, hunk_id = h.id }
      if text ~= "" then
        p.text = text
      end
      client.call("review.reject", p, function()
        M.refresh({ restore = { hunk_id = h.id, idx = idx } })
      end)
    end)
  end)
end

function M.comment()
  if not (R and valid_win(R.diff_win)) then
    return
  end
  local cur = vim.api.nvim_win_get_cursor(R.diff_win)[1]
  local info = R.line_info[cur]
  local file = cur_file()
  if not info or not file then
    ui.notify("no line to comment on", vim.log.levels.WARN)
    return
  end
  local line
  if info.line then
    local l = info.line
    line = (l.new and l.new > 0) and l.new or l.old
  else
    line = info.hunk.new_start or info.hunk.old_start
  end
  ui.input({ title = string.format("✎ %s:%s", file.path, tostring(line)) }, function(text)
    if not text or text == "" then
      return
    end
    client.call("review.comment", {
      slug = R.slug,
      path = file.path,
      line = line,
      hunk_id = info.hunk.id,
      text = text,
    }, function()
      M.refresh()
    end)
  end)
end

function M.ask()
  with_hunk(function(h)
    ui.input({ title = "? ask the agent (empty: default question)" }, function(text)
      if text == nil then
        return
      end
      local p = { slug = R.slug, hunk_id = h.id }
      if text ~= "" then
        p.question = text
      end
      M._asked[h.id] = true
      client.call("review.ask", p, function()
        ui.notify("asked the agent. waiting for the answer…")
      end)
    end)
  end)
end

function M.show_answer(d)
  if type(d) ~= "table" then
    return
  end
  local mine = (R and d.slug == R.slug) or (d.hunk_id and M._asked[d.hunk_id])
  if not mine then
    return
  end
  if d.hunk_id then
    M._asked[d.hunk_id] = nil
  end
  local rows = {}
  for _, t in ipairs(vim.split(d.text or "", "\n")) do
    table.insert(rows, t)
  end
  local buf, win = ui.float(rows, {
    title = "? answer · " .. (d.slug or ""),
    enter = true,
    width = math.min(80, math.floor(vim.o.columns * 0.7)),
    ft = "markdown",
  })
  for _, k in ipairs({ "q", "<Esc>" }) do
    vim.keymap.set("n", k, function() ui.close_win(win) end, { buffer = buf, nowait = true })
  end
end

function M.toggle_approved()
  if not R then
    return
  end
  local snap = snapshot_cursor()
  R.show_approved = not R.show_approved
  render_list()
  render_diff(snap)
end

function M._quit()
  local ok, err = pcall(vim.cmd, "qa")
  if not ok then
    ui.notify("cannot finish: " .. tostring(err), vim.log.levels.WARN)
  end
end

function M.send(slug)
  slug = slug or (R and R.slug) or sessions.default_slug()
  if not slug then
    ui.notify("no session to send to", vim.log.levels.WARN)
    return
  end
  client.call("review.flush", { slug = slug }, function()
    ui.notify("comments sent → " .. slug)
    if vim.env.BOBA_SESSION and vim.env.BOBA_SESSION ~= "" then
      M._quit()
    elseif R and R.slug == slug then
      M.refresh()
    end
  end)
end

function M.open_file()
  if not R then
    return
  end
  local file = cur_file()
  if not file then
    return
  end
  local line = 1
  if valid_win(R.diff_win) then
    local info = R.line_info[vim.api.nvim_win_get_cursor(R.diff_win)[1]]
    if info then
      line = (info.line and info.line.new and info.line.new > 0) and info.line.new or (info.hunk.new_start or 1)
    end
  end
  sessions.get(R.slug, function(s)
    local root = (s and s.path) or vim.fn.getcwd()
    local p = file.path:sub(1, 1) == "/" and file.path or (root .. "/" .. file.path)
    vim.cmd("tabedit " .. vim.fn.fnameescape(p))
    pcall(vim.api.nvim_win_set_cursor, 0, { line, 0 })
  end)
end

function M.close()
  if not R then
    return
  end
  local r = R
  R = nil
  -- 未送信コメントが無ければ reviewing を抜ける
  client.request("review.end", { slug = r.slug }, function() end)
  if #vim.api.nvim_list_tabpages() > 1 and r.tab and vim.api.nvim_tabpage_is_valid(r.tab) then
    vim.api.nvim_set_current_tabpage(r.tab)
    pcall(vim.cmd, "tabclose")
  else
    ui.close_win(r.list_win)
  end
  for _, b in ipairs({ r.list_buf, r.diff_buf }) do
    if valid_buf(b) then
      pcall(vim.api.nvim_buf_delete, b, { force = true })
    end
  end
end

-- ---------------------------------------------------------------- レイアウト

local function map(buf, lhs, fn, desc)
  if lhs and lhs ~= "" then
    vim.keymap.set("n", lhs, fn, { buffer = buf, nowait = true, silent = true, desc = desc })
  end
end

local function common_maps(buf)
  local km = cfg().keymaps.review
  map(buf, km.toggle_approved, M.toggle_approved, "boba: show/hide approved hunks")
  map(buf, km.send, function() M.send() end, "boba: send comments")
  map(buf, km.refresh, function() M.refresh() end, "boba: refresh")
  map(buf, km.close, M.close, "boba: close the review")
  map(buf, km.next_hunk, function() M.move_hunk(1) end, "boba: next hunk")
  map(buf, km.prev_hunk, function() M.move_hunk(-1) end, "boba: previous hunk")
end

local function set_name(buf, name)
  local old = vim.fn.bufnr(name)
  if old ~= -1 and old ~= buf then
    pcall(vim.api.nvim_buf_delete, old, { force = true })
  end
  pcall(vim.api.nvim_buf_set_name, buf, name)
end

local function create_layout(slug)
  local cur = vim.api.nvim_get_current_buf()
  local reuse = vim.api.nvim_buf_get_name(cur) == ""
    and not vim.bo[cur].modified
    and vim.api.nvim_buf_line_count(cur) <= 1
    and vim.api.nvim_buf_get_lines(cur, 0, 1, false)[1] == ""
    and #vim.api.nvim_tabpage_list_wins(0) == 1
  if not reuse then
    vim.cmd("tabnew")
  end
  R.tab = vim.api.nvim_get_current_tabpage()
  R.diff_win = vim.api.nvim_get_current_win()

  R.diff_buf = vim.api.nvim_create_buf(false, true)
  vim.bo[R.diff_buf].buftype = "nofile"
  vim.bo[R.diff_buf].bufhidden = "hide"
  vim.bo[R.diff_buf].swapfile = false
  vim.bo[R.diff_buf].filetype = "boba-review"
  set_name(R.diff_buf, "boba://review/" .. slug .. "/diff")
  vim.api.nvim_win_set_buf(R.diff_win, R.diff_buf)

  vim.cmd("topleft vsplit")
  R.list_win = vim.api.nvim_get_current_win()
  R.list_buf = vim.api.nvim_create_buf(false, true)
  vim.bo[R.list_buf].buftype = "nofile"
  vim.bo[R.list_buf].bufhidden = "hide"
  vim.bo[R.list_buf].swapfile = false
  vim.bo[R.list_buf].filetype = "boba-review-files"
  set_name(R.list_buf, "boba://review/" .. slug .. "/files")
  vim.api.nvim_win_set_buf(R.list_win, R.list_buf)
  vim.api.nvim_win_set_width(R.list_win, cfg().list_width)

  for _, w in ipairs({ R.list_win, R.diff_win }) do
    vim.wo[w].number = false
    vim.wo[w].relativenumber = false
    vim.wo[w].wrap = false
    vim.wo[w].cursorline = true
    vim.wo[w].foldenable = false
    vim.wo[w].list = false
    vim.wo[w].winhighlight = "CursorLine:BobaSelect"
  end
  vim.wo[R.list_win].signcolumn = "no"
  vim.wo[R.list_win].winfixwidth = true
  vim.wo[R.diff_win].signcolumn = "yes:1"

  -- ファイル一覧のキー
  local km = cfg().keymaps.review
  map(R.list_buf, km.select or "<CR>", function()
    M.select_file(vim.api.nvim_win_get_cursor(R.list_win)[1], { focus = true })
  end, "boba: open the file")
  map(R.list_buf, "l", function()
    M.select_file(vim.api.nvim_win_get_cursor(R.list_win)[1], { focus = true })
  end, "boba: open the file")
  common_maps(R.list_buf)

  -- 差分のキー
  map(R.diff_buf, km.approve, M.approve, "boba: approve the hunk")
  map(R.diff_buf, km.reject, M.reject, "boba: send back the hunk")
  map(R.diff_buf, km.comment, M.comment, "boba: comment on the line")
  map(R.diff_buf, km.ask, M.ask, "boba: ask the agent")
  map(R.diff_buf, km.open_file, M.open_file, "boba: open the real file")
  map(R.diff_buf, "h", function()
    if valid_win(R.list_win) then
      vim.api.nvim_set_current_win(R.list_win)
    end
  end, "boba: go to the file list")
  common_maps(R.diff_buf)

  local group = vim.api.nvim_create_augroup("BobaReviewLayout", { clear = true })
  vim.api.nvim_create_autocmd("CursorMoved", {
    group = group,
    buffer = R.list_buf,
    callback = function()
      if R and not R.suppress_list_move and valid_win(R.list_win) then
        M.select_file(vim.api.nvim_win_get_cursor(R.list_win)[1])
      end
    end,
  })
  vim.api.nvim_create_autocmd("CursorMoved", {
    group = group,
    buffer = R.diff_buf,
    callback = M.update_cursor_mark,
  })
  vim.api.nvim_create_autocmd("BufWipeout", {
    group = group,
    buffer = R.diff_buf,
    callback = function()
      R = nil
    end,
  })
end

--- :BobaReview [slug]
function M.open(slug, cb)
  sessions.resolve(slug, function(s)
    if R and valid_win(R.list_win) and valid_win(R.diff_win) then
      R.slug = s
      R.data = nil
      R.file_idx = 1
      if R.tab and vim.api.nvim_tabpage_is_valid(R.tab) then
        vim.api.nvim_set_current_tabpage(R.tab)
      end
    else
      R = { slug = s, file_idx = 1, show_approved = false, line_info = {}, hunk_lnums = {} }
      create_layout(s)
    end
    -- reviewing に遷移(失敗しても表示は続ける)
    client.request("review.start", { slug = s }, function() end)
    fetch(function()
      local first = next_file_with_hunks(0) or 1
      R.file_idx = first
      render_list()
      render_diff({ idx = 1 })
      if valid_win(R.diff_win) then
        vim.api.nvim_set_current_win(R.diff_win)
      end
      if cb then
        cb()
      end
    end)
  end, "session to review")
end

function M.setup_events()
  -- レビューを開いたまま nvim を終了した場合も reviewing を抜ける
  vim.api.nvim_create_autocmd("VimLeavePre", {
    group = vim.api.nvim_create_augroup("BobaReviewLeave", { clear = true }),
    callback = function()
      if R and R.slug then
        client.request_sync("review.end", { slug = R.slug }, 500)
      end
    end,
  })
  client.on("review.updated", function(d)
    if R and type(d) == "table" and d.slug == R.slug then
      refresh_debounced()
    end
  end)
  client.on("review.answer", M.show_answer)
  -- デーモン再起動・起動前に開いた場合は繋がった時点で取り直す
  client.on("boba.connected", function()
    if R then
      M.refresh()
    end
  end)
end

return M
