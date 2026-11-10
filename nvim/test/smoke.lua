-- ヘッドレスのスモークテスト。test/run.sh から呼ばれる(PEKOE_SOCK は偽デーモンを指す)。
local failures = 0
local function log(...)
  io.stdout:write(table.concat(vim.tbl_map(tostring, { ... }), " ") .. "\n")
end
local function check(cond, msg)
  if cond then
    log("ok   " .. msg)
  else
    failures = failures + 1
    log("FAIL " .. msg)
  end
  return cond
end
local function wait(ms, fn)
  return vim.wait(ms, fn, 10)
end
local function buf_text(buf)
  return table.concat(vim.api.nvim_buf_get_lines(buf, 0, -1, false), "\n")
end
local function find(hay, needle)
  return hay:find(needle, 1, true) ~= nil
end
local function daemon_log()
  local res = require("pekoe.client").request_sync("test.log", {})
  return res or {}
end
local function logged(method, pred)
  for _, e in ipairs(daemon_log()) do
    if e.method == method and (not pred or pred(e.params or {})) then
      return e.params or {}
    end
  end
end
local function virt_texts(buf)
  local out = {}
  for _, m in ipairs(vim.api.nvim_buf_get_extmarks(buf, -1, 0, -1, { details = true })) do
    local d = m[4]
    for _, c in ipairs(d.virt_text or {}) do
      table.insert(out, c[1])
    end
    for _, vl in ipairs(d.virt_lines or {}) do
      for _, c in ipairs(vl) do
        table.insert(out, c[1])
      end
    end
  end
  return table.concat(out, "\n")
end

local ok, err = xpcall(function()
  vim.o.columns, vim.o.lines = 160, 50
  vim.env.PEKOE_SESSION = nil
  vim.cmd("runtime plugin/pekoe.lua")
  require("pekoe").setup({})
  local client = require("pekoe.client")
  local review = require("pekoe.review")
  local approval = require("pekoe.approval")

  -- 接続と session.list
  local list, e = client.request_sync("session.list", {})
  check(e == nil and type(list) == "table" and #list == 2, "session.list returns 2 sessions")
  check(client.is_connected(), "connected to " .. client.socket_path())
  check(logged("subscribe", function(p)
    return vim.deep_equal(p.events, { "session.", "review.", "editor." })
  end) ~= nil, "auto subscribe with session./review./editor.")

  -- review.files
  local r = client.request_sync("review.files", { slug = "feat-x", show_approved = true })
  check(r and r.files and #r.files == 2, "review.files returns 2 files")
  local _, e2 = client.request_sync("review.files", { slug = "nope" })
  check(e2 and find(e2, "no such session"), "error responses are surfaced")

  -- C. 許可待ちフロート(subscribe 後 0.3 秒で session.waiting が来る)
  local function answers()
    local out = {}
    for _, e in ipairs(daemon_log()) do
      if e.method == "session.answer" then
        table.insert(out, e)
      end
    end
    return out
  end
  local home_win = vim.api.nvim_get_current_win()
  vim.api.nvim_buf_set_lines(0, 0, -1, false, { "some text", "more" })
  check(wait(2000, function() return approval.current() ~= nil end), "approval float shown on session.waiting")
  local c = approval.current()
  if c then
    check(vim.api.nvim_get_current_win() == home_win, "approval float does not take the focus")
    check(wait(1000, function() return find(buf_text(c.buf), "Do you want to proceed?") end), "approval float shows the agent's screen")
    local t = buf_text(c.buf)
    check(find(t, "Permission · fix-y"), "approval float title")
    check(find(t, "Bash") and find(t, "pnpm prisma migrate dev"), "approval float shows pending tool")
    check(not find(t, "\27"), "screen is shown without escape sequences")
    -- 打鍵中の yy は自分のバッファの yank のまま
    vim.api.nvim_feedkeys("yy", "x", false)
    vim.wait(200)
    check(#answers() == 0, "yy in the editor does not answer the prompt")
    check(approval.current() ~= nil, "float stays after typing elsewhere")
    vim.api.nvim_feedkeys(vim.api.nvim_replace_termcodes("<leader>ba", true, false, true), "x", false)
    check(vim.api.nvim_get_current_win() == c.win, "<leader>ba focuses the float")
    vim.api.nvim_feedkeys("y", "x", false)
    check(wait(1000, function() return #answers() == 1 end), "y sends session.answer")
    local a = answers()[1] or {}
    local p = a.params or {}
    check(p.slug == "fix-y" and p.answer == "allow", "session.answer allow for fix-y")
    check(type(p.expect) == "table" and type(p.expect.tool) == "table" and p.expect.tool.name == "Bash"
      and p.expect.tool.input == "pnpm prisma migrate dev", "session.answer carries the shown tool as expect")
    check(a.token == "test-human-token", "session.answer carries the human token (got " .. tostring(a.token) .. ")")
    check(approval.current() == nil, "approval float closed after answer")
    check(vim.api.nvim_get_current_win() == home_win, "focus goes back after answer")

    -- 偽の pending_tool(フックは偽造できる): expect が合わず断られ、本物で出し直す
    client.request_sync("test.emit", { event = "session.waiting", data = {
      slug = "fix-y", state = "waiting", wait_kind = "permission",
      pending_tool = { name = "Read", input = "README.md" } } })
    check(wait(1000, function()
      local x = approval.current()
      return x and x.tool and x.tool.name == "Read" and find(buf_text(x.buf), "Do you want to proceed?")
    end), "forged prompt shown alongside the real screen")
    approval.focus()
    vim.api.nvim_feedkeys("y", "x", false)
    check(wait(1000, function() return #answers() == 2 end), "second answer sent")
    check(wait(1000, function()
      local x = approval.current()
      return x and x.tool and x.tool.name == "Bash"
    end), "a changed prompt is refused and the float shows the daemon's tool again")
    check(vim.api.nvim_get_current_win() ~= (approval.current() or {}).win, "re-shown float is not focused")
    approval.close()
  end

  -- A. レビュー
  vim.cmd("PekoeReview feat-x")
  check(wait(2000, function()
    local R = review.state()
    return R and R.data ~= nil and vim.api.nvim_buf_line_count(R.list_buf) == 2
  end), "PekoeReview opened")
  local R = review.state()
  local lt = buf_text(R.list_buf)
  check(find(lt, "● M src/api.ts  2 hunks 1✓"), "file list line for src/api.ts: " .. vim.split(lt, "\n")[1])
  check(find(lt, "● A src/new.ts  1 hunks 0✓"), "file list line for src/new.ts")
  check(vim.api.nvim_get_current_win() == R.diff_win, "diff window focused")
  local dt = buf_text(R.diff_buf)
  check(find(dt, "@@ -82,3 +82,4 @@"), "diff shows hunk header")
  check(find(dt, "  83      -  return all()"), "diff shows - line with old number")
  check(find(dt, "       83 +  for await (const r of stream()) {"), "diff shows + line with new number")
  check(not find(dt, "@@ -120,2 +121,3 @@"), "approved hunk hidden by default")
  local vt = virt_texts(R.diff_buf)
  check(find(vt, "prompt #6: stream the rows"), "prompt annotation virtual text")
  check(find(vt, "use a generator"), "unsent comment virtual line")
  local signs = 0
  for _, m in ipairs(vim.api.nvim_buf_get_extmarks(R.diff_buf, -1, 0, -1, { details = true })) do
    if m[4].sign_text and vim.trim(m[4].sign_text) == "▎" then
      signs = signs + 1
    end
  end
  check(signs == 3, "gutter signs on +/- lines (" .. signs .. ")")

  -- winbar のキーヒント(幅が足りなくても h / l は残る)
  local function winbar(win, width)
    return vim.api.nvim_eval_statusline(vim.wo[win].winbar, { winid = win, maxwidth = width, use_winbar = true }).str
  end
  local dw = winbar(R.diff_win, 200)
  check(find(dw, "h files") and find(dw, "src/api.ts") and find(dw, "S send"), "diff winbar shows h hint: " .. dw)
  dw = winbar(R.diff_win, 50)
  check(vim.startswith(dw, " h files") and find(dw, "S send"), "narrow diff winbar keeps h and tail hints: " .. dw)
  local lw = winbar(R.list_win, 40)
  check(find(lw, "l diff"), "list winbar shows l hint: " .. lw)
  check(find(winbar(R.list_win, 20), "l diff"), "narrow list winbar keeps l hint")

  review.toggle_approved()
  check(find(buf_text(R.diff_buf), "@@ -120,2 +121,3 @@  ✓ approved"), "A shows approved hunk")
  review.toggle_approved()
  check(not find(buf_text(R.diff_buf), "@@ -120,2 +121,3 @@"), "A hides approved hunk again")

  -- ]h / [h
  vim.api.nvim_set_current_win(R.diff_win)
  vim.api.nvim_win_set_cursor(R.diff_win, { 1, 0 })
  review.move_hunk(1)
  check((R.file_idx == 2), "]h at last hunk moves to next file")
  review.move_hunk(-1)
  check(R.file_idx == 1, "[h moves back to previous file")

  -- c: コメント(フローティング入力)
  vim.api.nvim_win_set_cursor(R.diff_win, { 4, 0 }) -- "+ for await" 行
  review.comment()
  vim.api.nvim_buf_set_lines(0, 0, -1, false, { "looks good" })
  vim.api.nvim_feedkeys(vim.api.nvim_replace_termcodes("<CR>", true, false, true), "x", false)
  check(wait(1000, function()
    return logged("review.comment", function(p)
      return p.path == "src/api.ts" and p.line == 83 and p.hunk_id == "h1" and p.text == "looks good"
    end) ~= nil
  end), "c sends review.comment {path,line,hunk_id,text}")
  check(wait(1000, function() return find(virt_texts(R.diff_buf), "looks good") end), "new comment shown as virtual line")

  -- ?: 意図を聞く → review.answer をフロートに
  vim.api.nvim_set_current_win(R.diff_win)
  vim.api.nvim_win_set_cursor(R.diff_win, { 1, 0 })
  review.ask()
  vim.api.nvim_feedkeys(vim.api.nvim_replace_termcodes("<CR>", true, false, true), "x", false)
  check(wait(1000, function() return logged("review.ask", function(p) return p.hunk_id == "h1" end) ~= nil end), "? sends review.ask")
  local answered = wait(1500, function()
    local w = vim.api.nvim_get_current_win()
    local cfgw = vim.api.nvim_win_get_config(w)
    return cfgw.relative ~= "" and find(buf_text(vim.api.nvim_win_get_buf(w)), "メモリに載せない")
  end)
  check(answered, "review.answer shown in float")
  if answered then
    vim.api.nvim_feedkeys("q", "x", false)
  end

  -- a: 承認 → review.updated → 再描画、空になれば次のファイルへ
  vim.api.nvim_set_current_win(R.diff_win)
  vim.api.nvim_win_set_cursor(R.diff_win, { 1, 0 })
  review.approve()
  check(wait(1500, function()
    return find(buf_text(R.list_buf), "✓ M src/api.ts  2 hunks 2✓")
  end), "a approves hunk and list shows ✓")
  check(wait(500, function() return R.file_idx == 2 end), "after approving last hunk moves to next file")

  -- F. editor.open
  local tmp = vim.fn.tempname() .. ".txt"
  local lines = {}
  for i = 1, 20 do
    lines[i] = "line " .. i
  end
  vim.fn.writefile(lines, tmp)
  client.request_sync("editor.open", { path = tmp, line = 7, note = "ここを見て" })
  check(wait(1500, function()
    return vim.fn.resolve(vim.api.nvim_buf_get_name(0)) == vim.fn.resolve(tmp)
  end), "editor.open edits path")
  check(vim.api.nvim_win_get_cursor(0)[1] == 7, "editor.open jumps to line (" .. vim.api.nvim_win_get_cursor(0)[1] .. ")")
  local nw = require("pekoe.editor").note_win()
  check(nw and vim.api.nvim_win_is_valid(nw) and find(buf_text(vim.api.nvim_win_get_buf(nw)), "ここを見て"), "editor.open note float")

  -- B. 範囲送信のテキスト
  local send = require("pekoe.send")
  vim.bo.filetype = "text"
  local text = send.build_text({ buf = 0, l1 = 2, l2 = 3 }, vim.fn.fnamemodify(tmp, ":h"), "直して")
  local base = vim.fn.fnamemodify(tmp, ":t")
  check(text == base .. ":L2-3\n```text\nline 2\nline 3\n```\n直して", "build_text format: " .. vim.inspect(text))
  check(vim.startswith(send.build_text({ buf = 0, l1 = 1, l2 = 1 }, "/nonexistent"), vim.fn.resolve(tmp) .. ":L1"), "path absolute outside worktree")
  send.send_text("fix-y", text)
  check(wait(1000, function()
    return logged("session.send", function(p) return p.slug == "fix-y" and p.text == text and p.submit == true end) ~= nil
  end), "session.send {slug,text,submit}")

  -- E. 診断の整形
  local dns = vim.api.nvim_create_namespace("pekoe_test")
  vim.diagnostic.set(dns, 0, { { lnum = 4, col = 0, severity = vim.diagnostic.severity.ERROR, message = "bad thing", source = "ts" } })
  local dl = send.format_diagnostics(vim.diagnostic.get(0), vim.fn.fnamemodify(tmp, ":h"))
  check(dl[1] == base .. ":5: error: bad thing [ts]", "diagnostic format: " .. tostring(dl[1]))

  -- D. follow の補助
  local follow = require("pekoe.follow")
  check(follow.parse_tool("Edit src/api.ts") == "src/api.ts" and follow.parse_tool("Bash ls") == nil, "follow parses last_tool")
  check(follow.first_changed_line({ "a", "b", "c" }, { "a", "B", "c" }) == 2, "follow first changed line")

  -- :PekoeSessions
  vim.cmd("PekoeSessions")
  check(wait(1000, function()
    local w = vim.api.nvim_get_current_win()
    return vim.api.nvim_win_get_config(w).relative ~= "" and find(buf_text(0), "fix-y") and find(buf_text(0), "wait")
  end), ":PekoeSessions lists sessions with state word")
  vim.api.nvim_win_set_cursor(0, { 2, 0 })
  vim.api.nvim_feedkeys(vim.api.nvim_replace_termcodes("<CR>", true, false, true), "x", false)
  check(require("pekoe.sessions").target == "fix-y", "<CR> in :PekoeSessions sets target")

  -- D. :PekoeFollow(イベントで編集を追う)
  local ftmp = vim.fn.tempname() .. ".lua"
  vim.fn.writefile({ "a", "b", "c", "d" }, ftmp)
  vim.cmd("PekoeFollow feat-x")
  client.request_sync("test.emit", { event = "session.updated", data = { slug = "feat-x", state = "running", last_tool = "Edit " .. ftmp } })
  wait(200, function() return false end)
  vim.fn.writefile({ "a", "b", "C", "d" }, ftmp)
  client.request_sync("test.emit", { event = "session.updated", data = { slug = "feat-x", state = "running", last_tool = "Bash ls" } })
  local fwin
  check(wait(1500, function()
    for _, w in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
      if vim.fn.resolve(vim.api.nvim_buf_get_name(vim.api.nvim_win_get_buf(w))) == vim.fn.resolve(ftmp) then
        fwin = w
        return true
      end
    end
  end), ":PekoeFollow opens edited file in split")
  check(fwin and vim.api.nvim_win_get_cursor(fwin)[1] == 3, ":PekoeFollow jumps to first changed line")
  vim.cmd("PekoeFollow!")
  check(follow.active() == nil, ":PekoeFollow! stops")

  -- 接続先は Go の paths.Socket と同じ規則($XDG_DATA_HOME も見る)
  do
    local saved = { vim.env.PEKOE_SOCK, vim.env.PEKOE_HOME, vim.env.XDG_DATA_HOME }
    vim.env.PEKOE_SOCK, vim.env.PEKOE_HOME, vim.env.XDG_DATA_HOME = nil, nil, "/x/data"
    local got = client.socket_path()
    vim.env.PEKOE_SOCK, vim.env.PEKOE_HOME, vim.env.XDG_DATA_HOME = saved[1], saved[2], saved[3]
    check(got == "/x/data/pekoe/pekoed.sock", "socket under $XDG_DATA_HOME: " .. got)
  end


  -- S / :PekoeSend: flush → PEKOE_SESSION があれば終了
  local quit = false
  review._quit = function() quit = true end
  vim.env.PEKOE_SESSION = "feat-x"
  vim.cmd("PekoeSend")
  check(wait(1000, function() return quit end), ":PekoeSend flushes then quits when PEKOE_SESSION set")
  check(logged("review.flush", function(p) return p.slug == "feat-x" end) ~= nil, "review.flush sent")
end, debug.traceback)

if not ok then
  failures = failures + 1
  log("ERROR " .. tostring(err))
end
log(failures == 0 and "ALL PASSED" or (failures .. " FAILURE(S)"))
io.stdout:flush()
if failures > 0 then
  vim.cmd("cquit 1")
else
  vim.cmd("qa!")
end
