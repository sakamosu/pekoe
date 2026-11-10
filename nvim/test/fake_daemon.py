#!/usr/bin/env python3
"""pekoed の偽物。docs/api.md の unix socket (JSON Lines) を最小限だけ喋る。

使い方: fake_daemon.py <socket path>

- session.list / session.get / review.files などに固定データで答える
- subscribe した接続には 0.3 秒後に session.waiting(permission)を流す
- review.approve で状態を変えて review.updated を流す
- editor.open は購読者に editor.open イベントとして流す
- session.screen は fix-y の許可ダイアログの画面を返す
- session.answer は expect が今の pending_tool と違えば断る
- test.log で受け取ったリクエスト一覧(人間用トークンも)を返す(テスト用の拡張)
"""
import asyncio
import copy
import json
import os
import sys

SESSIONS = [
    {
        "slug": "feat-x", "branch": "feat/x", "repo": "app", "repo_id": "local_app",
        "path": "/tmp/pekoe-fake/feat-x", "agent": "claude", "state": "done",
        "last_tool": "Edit src/api.ts",
    },
    {
        "slug": "fix-y", "branch": "fix/y", "repo": "app", "repo_id": "local_app",
        "path": "/tmp/pekoe-fake/fix-y", "agent": "claude", "state": "waiting",
        "wait_kind": "permission",
        "pending_tool": {"name": "Bash", "input": "pnpm prisma migrate dev"},
    },
]

REVIEW = {
    "slug": "feat-x", "base": "abc123", "base_label": "main",
    "since": 0, "total": 3, "approved": 1,
    "files": [
        {"path": "src/api.ts", "status": "M", "hunks": [
            {"id": "h1", "header": "@@ -82,3 +82,4 @@", "old_start": 82, "new_start": 82,
             "lines": [
                 {"kind": " ", "old": 82, "new": 82, "text": "export async function rows() {"},
                 {"kind": "-", "old": 83, "new": 0, "text": "  return all()"},
                 {"kind": "+", "old": 0, "new": 83, "text": "  for await (const r of stream()) {"},
                 {"kind": "+", "old": 0, "new": 84, "text": "    yield r"},
                 {"kind": " ", "old": 84, "new": 85, "text": "}"},
             ],
             "approved": False, "rejected": False, "prompt": {"n": 6, "text": "stream the rows"}},
            {"id": "h2", "header": "@@ -120,2 +121,3 @@", "old_start": 120, "new_start": 121,
             "lines": [
                 {"kind": " ", "old": 120, "new": 121, "text": "const a = 1"},
                 {"kind": "+", "old": 0, "new": 122, "text": "const b = 2"},
             ],
             "approved": True, "rejected": False, "prompt": None},
        ]},
        {"path": "src/new.ts", "status": "A", "hunks": [
            {"id": "h3", "header": "@@ -0,0 +1,1 @@", "old_start": 0, "new_start": 1,
             "lines": [{"kind": "+", "old": 0, "new": 1, "text": "export const x = 1"}],
             "approved": False, "rejected": False, "prompt": {"n": 7, "text": "add x"}},
        ]},
    ],
    "comments": [
        {"id": 1, "path": "src/api.ts", "line": 83, "hunk_id": "h1", "text": "use a generator", "sent": False},
    ],
    "test": {"status": "pass", "summary": "12 passed"},
    "diffstat": {"added": 4, "deleted": 1, "files": 2},
}

state = {"review": copy.deepcopy(REVIEW), "log": [], "subs": []}


def find_hunk(hid):
    for f in state["review"]["files"]:
        for h in f["hunks"]:
            if h["id"] == hid:
                return h
    return None


def recount():
    r = state["review"]
    r["approved"] = sum(1 for f in r["files"] for h in f["hunks"] if h["approved"])


async def broadcast(event, data):
    line = (json.dumps({"event": event, "data": data}) + "\n").encode()
    for w, prefixes in list(state["subs"]):
        if any(p == "*" or event.startswith(p) for p in prefixes):
            try:
                w.write(line)
                await w.drain()
            except Exception:
                pass


async def handle_req(method, params, writer, token=None):
    state["log"].append({"method": method, "params": params, "token": token})
    if method == "subscribe":
        state["subs"].append((writer, params.get("events", ["*"])))

        async def later():
            await asyncio.sleep(0.3)
            await broadcast("session.waiting", SESSIONS[1])
        asyncio.ensure_future(later())
        return None
    if method == "session.list":
        return SESSIONS
    if method == "session.get":
        for s in SESSIONS:
            if s["slug"] == params.get("slug"):
                return s
        raise KeyError("no such session: %s" % params.get("slug"))
    if method == "review.files":
        if params.get("slug") != "feat-x":
            raise KeyError("no such session: %s" % params.get("slug"))
        r = copy.deepcopy(state["review"])
        if not params.get("show_approved"):
            for f in r["files"]:
                f["hunks"] = [h for h in f["hunks"] if not h["approved"]]
        return r
    if method in ("review.approve", "review.unapprove"):
        h = find_hunk(params.get("hunk_id"))
        if not h:
            raise KeyError("no such hunk")
        h["approved"] = method == "review.approve"
        recount()
        asyncio.ensure_future(broadcast("review.updated", {"slug": params["slug"]}))
        return None
    if method == "review.reject":
        h = find_hunk(params.get("hunk_id"))
        h["rejected"] = True
        return None
    if method == "review.comment":
        cid = len(state["review"]["comments"]) + 1
        state["review"]["comments"].append({**params, "id": cid, "sent": False})
        return {"id": cid}
    if method == "review.ask":
        async def answer():
            await asyncio.sleep(0.1)
            await broadcast("review.answer", {"slug": params["slug"], "hunk_id": params["hunk_id"],
                                              "text": "rows を全部メモリに載せないため"})
        asyncio.ensure_future(answer())
        return None
    if method == "review.flush":
        for c in state["review"]["comments"]:
            c["sent"] = True
        return None
    if method == "editor.open":
        asyncio.ensure_future(broadcast("editor.open", params))
        return None
    if method == "test.emit":
        asyncio.ensure_future(broadcast(params["event"], params["data"]))
        return None
    if method == "test.log":
        return state["log"]
    if method == "session.screen":
        if params.get("slug") != "fix-y":
            raise KeyError("no such session: %s" % params.get("slug"))
        return {"lines": ["\x1b[1mBash command\x1b[0m", "  pnpm prisma migrate dev",
                          "Do you want to proceed?", "\x1b[36m❯ 1. Yes\x1b[0m", "  2. No", "", ""],
                "width": 80, "height": 7}
    if method == "session.answer":
        exp = params.get("expect")
        if exp is not None:
            cur = next((x.get("pending_tool") for x in SESSIONS if x["slug"] == params.get("slug")), None)
            t = exp.get("tool")
            same = (t is None and cur is None) or (t is not None and cur is not None and
                                                   t.get("name") == cur.get("name") and t.get("input") == cur.get("input"))
            if not same:
                raise KeyError("the prompt changed; look again")
        return None
    if method in ("review.start", "review.end", "session.send", "session.new"):
        return None
    raise KeyError("unknown method: %s" % method)


async def client(reader, writer):
    try:
        while True:
            line = await reader.readline()
            if not line:
                break
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            rid = msg.get("id")
            try:
                res = await handle_req(msg.get("method"), msg.get("params") or {}, writer, msg.get("token"))
                out = {"id": rid, "result": res}
            except KeyError as e:
                out = {"id": rid, "error": str(e.args[0])}
            writer.write((json.dumps(out) + "\n").encode())
            await writer.drain()
    finally:
        state["subs"] = [s for s in state["subs"] if s[0] is not writer]
        writer.close()


async def main(path):
    if os.path.exists(path):
        os.unlink(path)
    server = await asyncio.start_unix_server(client, path=path)
    print("listening", path, flush=True)
    async with server:
        await server.serve_forever()


if __name__ == "__main__":
    asyncio.run(main(sys.argv[1]))
