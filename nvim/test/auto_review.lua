-- PEKOE_REVIEW=1 での自動 :PekoeReview と、デーモン後起動からの再接続を確かめる。
local function out(s) io.stdout:write(s .. "\n") end
local function run()
local ok = vim.wait(8000, function()
  local R = require("pekoe.review").state()
  return R and R.data ~= nil and vim.api.nvim_buf_line_count(R.list_buf) == 2
end, 50)
out((ok and "ok   " or "FAIL ") .. "auto review opened after reconnect")
out(ok and "ALL PASSED" or "1 FAILURE(S)")
vim.cmd(ok and "qa!" or "cquit 1")
end
-- -c は VimEnter より前に走るので、VimEnter の後に確かめる
vim.api.nvim_create_autocmd("VimEnter", { once = true, callback = function() vim.schedule(run) end })
