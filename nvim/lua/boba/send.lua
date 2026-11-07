-- B. 範囲送信 / 新セッション、E. 診断送信。
local client = require("boba.client")
local ui = require("boba.ui")
local sessions = require("boba.sessions")

local M = {}

--- visual 選択(または range)を {buf, l1, l2} で返し、visual モードを抜ける。
function M.get_range(opts)
  if opts and opts.line1 then
    return { buf = vim.api.nvim_get_current_buf(), l1 = opts.line1, l2 = opts.line2 }
  end
  local mode = vim.api.nvim_get_mode().mode
  local l1, l2
  if mode:match("^[vV\22]") then
    l1, l2 = vim.fn.line("v"), vim.fn.line(".")
    vim.api.nvim_feedkeys(vim.api.nvim_replace_termcodes("<Esc>", true, false, true), "nx", false)
  else
    l1, l2 = vim.fn.line("'<"), vim.fn.line("'>")
  end
  if l1 > l2 then
    l1, l2 = l2, l1
  end
  return { buf = vim.api.nvim_get_current_buf(), l1 = l1, l2 = l2 }
end

local function fence_for(code)
  local fence = "```"
  while code:find(fence, 1, true) do
    fence = fence .. "`"
  end
  return fence
end

--- "path:L40-72\n```lang\n<code>\n```\n<instruction>" を組み立てる。
---@param rng {buf: integer, l1: integer, l2: integer}
---@param root string|nil パスの基準(worktree)。外なら絶対パス
---@param instruction string|nil
function M.build_text(rng, root, instruction)
  local name = vim.api.nvim_buf_get_name(rng.buf)
  local path = name ~= "" and sessions.relpath(name, root) or "[No Name]"
  local loc = rng.l1 == rng.l2 and string.format("L%d", rng.l1) or string.format("L%d-%d", rng.l1, rng.l2)
  local lines = vim.api.nvim_buf_get_lines(rng.buf, rng.l1 - 1, rng.l2, false)
  local code = table.concat(lines, "\n")
  local fence = fence_for(code)
  local lang = vim.bo[rng.buf].filetype or ""
  local text = string.format("%s:%s\n%s%s\n%s\n%s", path, loc, fence, lang, code, fence)
  if instruction and instruction ~= "" then
    text = text .. "\n" .. instruction
  end
  return text
end

function M.send_text(slug, text, on_ok)
  client.call("session.send", { slug = slug, text = text, submit = true }, function(res)
    ui.notify("送信しました → " .. slug)
    if on_ok then
      on_ok(res)
    end
  end)
end

--- <leader>bs: セッションを選び、指示を打って送る。
function M.range(opts)
  local rng = M.get_range(opts)
  sessions.pick(function(s)
    ui.input({ title = string.format("> %s · L%d-%d に指示", s.slug, rng.l1, rng.l2) }, function(instr)
      if instr == nil then
        return
      end
      M.send_text(s.slug, M.build_text(rng, s.path, instr))
    end)
  end, "送信先セッション")
end

local function git_toplevel(dir)
  local out = vim.fn.systemlist({ "git", "-C", dir, "rev-parse", "--show-toplevel" })
  if vim.v.shell_error ~= 0 or not out[1] or out[1] == "" then
    return nil
  end
  return out[1]
end

--- <leader>bn: 選択を初期指示にして新しいセッションを作る。
function M.new_session(opts)
  local rng = M.get_range(opts)
  local name = vim.api.nvim_buf_get_name(rng.buf)
  local dir = name ~= "" and vim.fn.fnamemodify(name, ":p:h") or vim.fn.getcwd()
  local repo = git_toplevel(dir) or git_toplevel(vim.fn.getcwd())
  if not repo then
    ui.notify("git リポジトリの中ではありません", vim.log.levels.ERROR)
    return
  end
  vim.ui.input({ prompt = "新しいブランチ名: " }, function(branch)
    if not branch or vim.trim(branch) == "" then
      return
    end
    branch = vim.trim(branch)
    ui.input({ title = "> " .. branch .. " への指示" }, function(instr)
      if instr == nil then
        return
      end
      local text = M.build_text(rng, repo, instr)
      client.call("session.new", { repo = repo, branch = branch, prompt = text }, function(s)
        ui.notify("セッションを作成中: " .. ((s and s.slug) or branch))
      end)
    end)
  end)
end

-- ---------------------------------------------------------------- E. 診断

local SEV = { "error", "warning", "info", "hint" }

--- 診断を "path:line: severity: message" の行にする。
function M.format_diagnostics(diags, root)
  local out = {}
  table.sort(diags, function(a, b)
    if a.bufnr ~= b.bufnr then
      return a.bufnr < b.bufnr
    end
    if a.lnum ~= b.lnum then
      return a.lnum < b.lnum
    end
    return (a.severity or 9) < (b.severity or 9)
  end)
  for _, d in ipairs(diags) do
    local name = vim.api.nvim_buf_get_name(d.bufnr)
    local path = name ~= "" and sessions.relpath(name, root) or ("buffer " .. d.bufnr)
    local msg = (d.message or ""):gsub("\n", " ")
    if d.source and d.source ~= "" then
      msg = msg .. " [" .. d.source .. "]"
    end
    table.insert(out, string.format("%s:%d: %s: %s", path, d.lnum + 1, SEV[d.severity] or "info", msg))
  end
  return out
end

--- :BobaFixDiagnostics[!]
function M.fix_diagnostics(workspace, slug)
  local diags = vim.diagnostic.get(not workspace and vim.api.nvim_get_current_buf() or nil)
  if #diags == 0 then
    ui.notify("診断はありません")
    return
  end
  sessions.resolve(slug, function(s)
    sessions.get(s, function(sess)
      local lines = M.format_diagnostics(diags, sess and sess.path)
      local text = "次の LSP 診断を修正してください:\n" .. table.concat(lines, "\n")
      M.send_text(s, text)
    end)
  end, "診断の送信先")
end

return M
