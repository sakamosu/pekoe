-- boba.nvim: bobad に繋がるレビュー面と日常の小技。
local M = {}

M.defaults = {
  -- false にするとそのキーを割り当てない
  keymaps = {
    send_range = "<leader>bs", -- visual: 選択範囲を指示つきで送る
    new_session = "<leader>bn", -- visual: 選択を初期指示に新セッション
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
  editor_open = true, -- true: 自分宛て(BOBA_SESSION / cwd 配下)だけ、"all": すべて、false: 無視
  auto_review = true, -- BOBA_REVIEW=1 かつ BOBA_SESSION があれば起動時に :BobaReview
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
      require("boba.send").range()
    end, { desc = "boba: 選択範囲をセッションに送る" })
    table.insert(global_maps, { "x", km.send_range })
  end
  if km.new_session then
    vim.keymap.set("x", km.new_session, function()
      require("boba.send").new_session()
    end, { desc = "boba: 選択から新セッション" })
    table.insert(global_maps, { "x", km.new_session })
  end
end

local function setup_events()
  if events_done then
    return
  end
  events_done = true
  local client = require("boba.client")
  local sessions = require("boba.sessions")
  client.on("session.created", sessions.update)
  client.on("session.updated", sessions.update)
  client.on("session.done", sessions.update)
  client.on("session.removed", sessions.remove)
  require("boba.review").setup_events()
  require("boba.approval").setup_events()
  require("boba.editor").setup_events()
end

function M.setup(opts)
  M.config = vim.tbl_deep_extend("force", vim.deepcopy(M.defaults), opts or {})
  M._setup_done = true
  local ui = require("boba.ui")
  ui.setup_highlights()
  vim.api.nvim_create_autocmd("ColorScheme", {
    group = vim.api.nvim_create_augroup("BobaHighlights", { clear = true }),
    callback = ui.setup_highlights,
  })
  setup_keymaps()
  setup_events()
  require("boba.client").connect()
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
  require("boba.review").open(slug)
end

function M.request(method, params, cb)
  M.ensure()
  return require("boba.client").request(method, params, cb)
end

function M.on(pattern, fn)
  return require("boba.client").on(pattern, fn)
end

function M.target()
  return require("boba.sessions").default_slug()
end

function M.maybe_auto_review()
  if M.config.auto_review and vim.env.BOBA_REVIEW == "1" and vim.env.BOBA_SESSION and vim.env.BOBA_SESSION ~= "" then
    require("boba.review").open(vim.env.BOBA_SESSION)
  end
end

return M
