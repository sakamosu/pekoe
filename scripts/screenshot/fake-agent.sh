#!/usr/bin/env bash
# fake-agent.sh — a fake coding agent for the README screenshot
# (scripts/screenshot/run.sh). driver.sh sets it as [agents.claude] in the
# sandbox. Per $PEKOE_SESSION it draws a plausible agent transcript and sends
# hook events with `pekoe event`, then idles. No real agent runs.
set -u
P=$(command -v pekoe)
sid="demo-$PEKOE_SESSION"
ev() { # ev <Hook> <json-fields>
	printf '{"session_id":"%s","cwd":"%s","hook_event_name":"%s"%s}' "$sid" "$PWD" "$1" "${2:+,$2}" | "$P" event "$1"
}
G=$'\e[38;2;81;215;94m'; D=$'\e[38;2;132;132;149m'; B=$'\e[1m'; R=$'\e[0m'; C=$'\e[38;2;17;205;237m'; Y=$'\e[38;2;237;200;18m'; M=$'\e[38;2;255;135;218m'
dot() { printf '%s⏺%s %s\n' "$1" "$R" "$2"; }
sub() { printf '  %s⎿%s  %s\n' "$D" "$R" "$1"; }
add() { printf '      %s%3d +%s%s\n' "$G" "$1" "$2" "$R"; }
prompt_box() {
	printf '\n%s──────────────────────────────────────────────────────────────────────%s\n' "$D" "$R"
	printf '%s>%s %s\n' "$B" "$R" "$1"
	printf '%s──────────────────────────────────────────────────────────────────────%s\n' "$D" "$R"
	printf '  %s? for shortcuts%s\n' "$D" "$R"
}
edit() { # edit <file> <lines...>
	mkdir -p "$(dirname "$1")"; f=$1; shift
	for l in "$@"; do printf '%s\n' "$l" >>"$f"; done
	ev PreToolUse "\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"$PWD/$f\"}"
	ev PostToolUse "\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"$PWD/$f\"}"
}
clear
ev SessionStart '"source":"startup"'

case "$PEKOE_SESSION" in
feat-csv-export)
	ask="Add a CSV export button to the orders page"
	printf '%s> %s%s\n\n' "$C" "$ask" "$R"
	ev UserPromptSubmit "\"prompt\":\"$ask\""
	dot "$R" "I'll add an export endpoint and a button on the orders list."
	echo
	dot "$G" "${B}Read${R}(src/orders/OrdersPage.tsx)"; sub "Read 84 lines"; echo
	edit src/api/orders.ts \
		'export async function exportOrdersCsv(q: OrderQuery) {' \
		'  const rows = await listOrders({ ...q, limit: 10_000 })' \
		'  return toCsv(rows, ["id", "customer", "total", "status"])' '}'
	dot "$G" "${B}Update${R}(src/api/orders.ts)"; sub "Updated src/api/orders.ts with 4 additions"
	add 41 'export async function exportOrdersCsv(q: OrderQuery) {'
	add 42 '  const rows = await listOrders({ ...q, limit: 10_000 })'
	add 43 '  return toCsv(rows, ["id", "customer", "total", "status"])'
	add 44 '}'
	echo
	edit src/orders/OrdersPage.tsx \
		'      <Button icon={<Download />} onClick={onExport}>' \
		'        Export CSV' '      </Button>'
	dot "$G" "${B}Update${R}(src/orders/OrdersPage.tsx)"; sub "Updated src/orders/OrdersPage.tsx with 3 additions"
	add 27 '      <Button icon={<Download />} onClick={onExport}>'
	add 28 '        Export CSV'
	add 29 '      </Button>'
	echo
	edit src/api/orders.test.ts 'test("exports orders as CSV", async () => {' '  expect(await exportOrdersCsv({})).toMatch(/^id,customer/)' '})'
	dot "$G" "${B}Write${R}(src/api/orders.test.ts)"; sub "Wrote 3 lines to src/api/orders.test.ts"; echo
	ev PreToolUse '"tool_name":"Bash","tool_input":{"command":"pnpm test src/api/orders.test.ts"}'
	dot "$Y" "${B}Bash${R}(pnpm test src/api/orders.test.ts)"; sub "${D}Running…${R}"
	printf '\n%s✶%s Running tests… %s(12s · esc to interrupt)%s\n' "$M" "$R" "$D" "$R"
	prompt_box ""
	;;
fix-cart-rounding)
	ask="Totals are off by a cent for some carts. Fix the rounding."
	printf '%s> %s%s\n\n' "$C" "$ask" "$R"
	ev UserPromptSubmit "\"prompt\":\"$ask\""
	edit src/cart/total.test.ts 'test("rounds half to even", () => {' '  expect(total([0.105, 0.105])).toBe(0.21)' '})'
	dot "$G" "${B}Grep${R}(\"toFixed\" in src/cart)"; sub "Found 3 matches"; echo
	ev PreToolUse "\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"src/api/orders.ts\"}"
	dot "$Y" "${B}Update${R}(src/api/orders.ts)"
	printf '\n %sDo you want to make this edit to orders.ts?%s\n' "$B" "$R"
	printf ' %s❯ 1. Yes%s\n   2. Yes, allow all edits during this session\n   3. No, and tell the agent what to do differently\n' "$C" "$R"
	ev Notification '"notification_type":"permission_prompt","message":"needs your permission to use Edit"'
	;;
feat-invoice-pdf)
	ask="Generate invoice PDFs from the order detail page"
	printf '%s> %s%s\n\n' "$C" "$ask" "$R"
	ev UserPromptSubmit "\"prompt\":\"$ask\""
	for i in $(seq 1 30); do edit src/invoices/pdf.ts "  // layout line $i"; done
	for i in $(seq 1 12); do edit src/invoices/InvoiceButton.tsx "  // ui line $i"; done
	dot "$R" "Invoices now render to PDF from the order page. Tests pass."
	prompt_box ""
	ev Stop '"last_assistant_message":"Invoices now render to PDF from the order page. Tests pass."'
	;;
*)
	prompt_box ""
	;;
esac
while :; do sleep 3600; done
