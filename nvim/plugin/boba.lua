-- boba.nvim のコマンド定義。実体は lua/boba/ に遅延 require する。
if vim.g.loaded_boba then
  return
end
vim.g.loaded_boba = true

if vim.fn.has("nvim-0.10") ~= 1 then
  vim.notify("boba.nvim は Neovim 0.10 以上が必要です", vim.log.levels.ERROR)
  return
end

local function boba()
  local b = require("boba")
  b.ensure()
  return b
end

local function cmd(name, fn, opts)
  vim.api.nvim_create_user_command(name, fn, opts or {})
end

local function slug_complete(arglead)
  local ok, sessions = pcall(require, "boba.sessions")
  if not ok then
    return {}
  end
  local out = {}
  for slug in pairs(sessions.cache) do
    if slug:sub(1, #arglead) == arglead then
      table.insert(out, slug)
    end
  end
  table.sort(out)
  return out
end

cmd("BobaReview", function(a)
  boba()
  require("boba.review").open(a.args ~= "" and a.args or nil)
end, { nargs = "?", complete = slug_complete, desc = "boba: レビュー面を開く" })

cmd("BobaSend", function(a)
  boba()
  require("boba.review").send(a.args ~= "" and a.args or nil)
end, { nargs = "?", complete = slug_complete, desc = "boba: 未送信コメントをまとめて送る" })

cmd("BobaSessions", function()
  boba()
  require("boba.sessions").open_list()
end, { desc = "boba: セッション一覧(Enter で送信先に)" })

cmd("BobaSendRange", function(a)
  boba()
  require("boba.send").range({ line1 = a.line1, line2 = a.line2 })
end, { range = true, desc = "boba: 範囲をセッションに送る" })

cmd("BobaNew", function(a)
  boba()
  require("boba.send").new_session({ line1 = a.line1, line2 = a.line2 })
end, { range = true, desc = "boba: 範囲を初期指示に新セッション" })

cmd("BobaAnswer", function(a)
  boba()
  local map = { y = "allow", allow = "allow", n = "deny", deny = "deny", a = "always", A = "always", always = "always" }
  local ans = map[a.fargs[1] or ""]
  if not ans then
    vim.notify("boba: :BobaAnswer y|n|always [slug]", vim.log.levels.ERROR)
    return
  end
  require("boba.approval").answer(ans, a.fargs[2])
end, { nargs = "+", desc = "boba: 許可待ちに答える" })

cmd("BobaFollow", function(a)
  boba()
  if a.bang then
    require("boba.follow").stop()
  else
    require("boba.follow").start(a.args ~= "" and a.args or nil)
  end
end, { nargs = "?", bang = true, complete = slug_complete, desc = "boba: エージェントの編集を追う(! で停止)" })

cmd("BobaFixDiagnostics", function(a)
  boba()
  require("boba.send").fix_diagnostics(a.bang, a.args ~= "" and a.args or nil)
end, { nargs = "?", bang = true, complete = slug_complete, desc = "boba: LSP 診断を送る(! でワークスペース)" })

local group = vim.api.nvim_create_augroup("BobaPlugin", { clear = true })
local function on_enter()
  if vim.g.boba_no_auto_setup then
    return
  end
  local b = require("boba")
  b.ensure()
  b.maybe_auto_review()
end
if vim.v.vim_did_enter == 1 then
  vim.schedule(on_enter)
else
  vim.api.nvim_create_autocmd("VimEnter", { group = group, once = true, callback = on_enter })
end

cmd("BobaRisks", function(a)
  boba()
  require("boba.risks").load(a.args ~= "" and a.args or nil)
end, { nargs = "?", complete = slug_complete, desc = "boba: 門番の指摘を quickfix に" })
