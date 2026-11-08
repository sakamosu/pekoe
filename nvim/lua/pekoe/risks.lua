-- 門番エージェント(review.risks)の結果を quickfix に流し込む。
-- レビューの代替ではなく「読む順序」を決める材料。
local client = require("pekoe.client")
local sessions = require("pekoe.sessions")

local M = {}

function M.load(slug)
  sessions.resolve(slug, function(s)
    sessions.get(s, function(sess)
      local root = sess and sess.path or vim.fn.getcwd()
      client.call("review.risks", { slug = s }, function(risks)
        local items = {}
        for _, r in ipairs(risks or {}) do
          local file = r.path
          if not file:match("^/") then
            file = root .. "/" .. file
          end
          table.insert(items, { filename = file, lnum = r.line or 1, text = r.text or "", type = "W" })
        end
        vim.fn.setqflist({}, " ", { title = "pekoe risks · " .. s, items = items })
        if #items == 0 then
          vim.notify("pekoe: " .. s .. " has no risks flagged")
        else
          vim.cmd("copen")
        end
      end)
    end)
  end, "risks for")
end

return M
