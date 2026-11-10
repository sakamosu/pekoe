-- pekoe.nvim: pekoed に繋がるレビュー面と日常の小技。
local M = {}

M.defaults = {
  -- false にするとそのキーを割り当てない
  keymaps = {
    send_range = "<leader>bs", -- visual: 選択範囲を指示つきで送る
    new_session = "<leader>bn", -- visual: 選択を初期指示に新セッション
    approval_focus = "<leader>ba", -- normal: 許可待ちフロートに入る(フロートはフォーカスを奪わない)
    review = {
      next_hunk = "]h",
      prev_hunk = "[h",
      approve = "a",
      reject = "x",
      comment = "c",
      ask = "?",
      toggle_approved = "A",
      send = "S",
      refresh = "R",
      close = "q",
      open_file = "o",
      select = "<CR>",
    },
    approval = {
      allow = "y",
      deny = "n",
      always = "A",
      go = "g",
      close = "<Esc>",
    },
  },
  approval = true, -- 許可待ちフロートを出す
  editor_open = true, -- true: 自分宛て(PEKOE_SESSION / cwd 配下)だけ、"all": すべて、false: 無視
  auto_review = true, -- PEKOE_REVIEW=1 かつ PEKOE_SESSION があれば起動時に :PekoeReview
  list_width = 40,
}

M.config = vim.deepcopy(M.defaults)
M._setup_done = false
local events_done = false
local global_maps = {}

local function setup_keymaps()
  for _, m in ipairs(global_maps) do
    pcall(vim.keymap.del, m[1], m[2])
  end
  global_maps = {}
  local km = M.config.keymaps
  if km.send_range then
    vim.keymap.set("x", km.send_range, function()
      require("pekoe.send").range()
    end, { desc = "pekoe: send the selection to a session" })
    table.insert(global_maps, { "x", km.send_range })
  end
  if km.new_session then
    vim.keymap.set("x", km.new_session, function()
      require("pekoe.send").new_session()
    end, { desc = "pekoe: new session from the selection" })
    table.insert(global_maps, { "x", km.new_session })
  end
  if km.approval_focus then
    vim.keymap.set("n", km.approval_focus, function()
      require("pekoe.approval").focus()
    end, { desc = "pekoe: focus the permission float" })
    table.insert(global_maps, { "n", km.approval_focus })
  end
end

local function setup_events()
  if events_done then
    return
  end
  events_done = true
  local client = require("pekoe.client")
  local sessions = require("pekoe.sessions")
  client.on("session.created", sessions.update)
  client.on("session.updated", sessions.update)
  client.on("session.done", sessions.update)
  client.on("session.removed", sessions.remove)
  require("pekoe.review").setup_events()
  require("pekoe.approval").setup_events()
  require("pekoe.editor").setup_events()
end

function M.setup(opts)
  M.config = vim.tbl_deep_extend("force", vim.deepcopy(M.defaults), opts or {})
  M._setup_done = true
  local ui = require("pekoe.ui")
  ui.setup_highlights()
  vim.api.nvim_create_autocmd("ColorScheme", {
    group = vim.api.nvim_create_augroup("PekoeHighlights", { clear = true }),
    callback = ui.setup_highlights,
  })
  setup_keymaps()
  setup_events()
  require("pekoe.client").connect()
end

--- setup() が呼ばれていなければ既定値で初期化する。
function M.ensure()
  if not M._setup_done then
    M.setup({})
  end
end

-- 公開 API
function M.review(slug)
  M.ensure()
  require("pekoe.review").open(slug)
end

function M.request(method, params, cb)
  M.ensure()
  return require("pekoe.client").request(method, params, cb)
end

function M.on(pattern, fn)
  return require("pekoe.client").on(pattern, fn)
end

function M.target()
  return require("pekoe.sessions").default_slug()
end

function M.maybe_auto_review()
  if M.config.auto_review and vim.env.PEKOE_REVIEW == "1" and vim.env.PEKOE_SESSION and vim.env.PEKOE_SESSION ~= "" then
    require("pekoe.review").open(vim.env.PEKOE_SESSION)
  end
end

return M
