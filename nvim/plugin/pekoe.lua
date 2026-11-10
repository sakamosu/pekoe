-- pekoe.nvim のコマンド定義。実体は lua/pekoe/ に遅延 require する。
if vim.g.loaded_pekoe then
  return
end
vim.g.loaded_pekoe = true

if vim.fn.has("nvim-0.10") ~= 1 then
  vim.notify("pekoe.nvim requires Neovim 0.10 or later", vim.log.levels.ERROR)
  return
end

local function pekoe()
  local b = require("pekoe")
  b.ensure()
  return b
end

local function cmd(name, fn, opts)
  vim.api.nvim_create_user_command(name, fn, opts or {})
end

local function slug_complete(arglead)
  local ok, sessions = pcall(require, "pekoe.sessions")
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

cmd("PekoeReview", function(a)
  pekoe()
  require("pekoe.review").open(a.args ~= "" and a.args or nil)
end, { nargs = "?", complete = slug_complete, desc = "pekoe: open the review" })

cmd("PekoeSend", function(a)
  pekoe()
  require("pekoe.review").send(a.args ~= "" and a.args or nil)
end, { nargs = "?", complete = slug_complete, desc = "pekoe: send all unsent comments" })

cmd("PekoeSessions", function()
  pekoe()
  require("pekoe.sessions").open_list()
end, { desc = "pekoe: list sessions (Enter sets the send target)" })

cmd("PekoeSendRange", function(a)
  pekoe()
  require("pekoe.send").range({ line1 = a.line1, line2 = a.line2 })
end, { range = true, desc = "pekoe: send the range to a session" })

cmd("PekoeNew", function(a)
  pekoe()
  require("pekoe.send").new_session({ line1 = a.line1, line2 = a.line2 })
end, { range = true, desc = "pekoe: start a new session with the range as its prompt" })

cmd("PekoeAnswer", function(a)
  pekoe()
  local map = { y = "allow", allow = "allow", n = "deny", deny = "deny", a = "always", A = "always", always = "always" }
  local ans = map[a.fargs[1] or ""]
  if not ans then
    vim.notify("pekoe: :PekoeAnswer y|n|always [slug]", vim.log.levels.ERROR)
    return
  end
  require("pekoe.approval").answer(ans, a.fargs[2])
end, { nargs = "+", desc = "pekoe: answer a pending permission" })

cmd("PekoeApproval", function()
  pekoe()
  require("pekoe.approval").focus()
end, { desc = "pekoe: focus the permission float" })

cmd("PekoeFollow", function(a)
  pekoe()
  if a.bang then
    require("pekoe.follow").stop()
  else
    require("pekoe.follow").start(a.args ~= "" and a.args or nil)
  end
end, { nargs = "?", bang = true, complete = slug_complete, desc = "pekoe: follow the agent's edits (! to stop)" })

cmd("PekoeFixDiagnostics", function(a)
  pekoe()
  require("pekoe.send").fix_diagnostics(a.bang, a.args ~= "" and a.args or nil)
end, { nargs = "?", bang = true, complete = slug_complete, desc = "pekoe: send LSP diagnostics (! for the workspace)" })

local group = vim.api.nvim_create_augroup("PekoePlugin", { clear = true })
local function on_enter()
  if vim.g.pekoe_no_auto_setup then
    return
  end
  local b = require("pekoe")
  b.ensure()
  b.maybe_auto_review()
end
if vim.v.vim_did_enter == 1 then
  vim.schedule(on_enter)
else
  vim.api.nvim_create_autocmd("VimEnter", { group = group, once = true, callback = on_enter })
end

cmd("PekoeRisks", function(a)
  pekoe()
  require("pekoe.risks").load(a.args ~= "" and a.args or nil)
end, { nargs = "?", complete = slug_complete, desc = "pekoe: load the gatekeeper's risks into quickfix" })
