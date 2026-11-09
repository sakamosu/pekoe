"""Turn a `tmux capture-pane -p -e` dump into a standalone HTML page that
looks like a terminal window (for the README screenshot, scripts/screenshot/run.sh).

    python3 ansi2html.py shot.ans shot.html
"""
import html, re, sys, unicodedata

BG, FG = "#151421", "#e0e0eb"
BASE16 = ["#151421", "#ff7172", "#51d75e", "#edc812", "#9bb7f2", "#ff87da", "#11cded", "#e0e0eb",
          "#848495", "#ff7172", "#51d75e", "#edc812", "#9bb7f2", "#ff87da", "#11cded", "#ffffff"]


def c256(n):
    if n < 16:
        return BASE16[n]
    if n < 232:
        n -= 16
        v = [0, 95, 135, 175, 215, 255]
        return "#%02x%02x%02x" % (v[n // 36], v[n // 6 % 6], v[n % 6])
    g = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (g, g, g)


def cells(t):
    # pin every non-ASCII glyph to its terminal cell width, so fallback fonts
    # cannot push the panel borders out of line
    out = []
    for ch in t:
        if ord(ch) < 128:
            out.append(html.escape(ch))
        else:
            w = 2 if unicodedata.east_asian_width(ch) in "WF" else 1
            out.append(f'<b class="c{w}">{html.escape(ch)}</b>')
    return "".join(out)


def render(text):
    rows = []
    for line in text.split("\n"):
        st = dict(fg=None, bg=None, b=False, d=False, i=False, u=False, r=False)
        out, pos = [], 0
        for m in re.finditer(r"\x1b\[([0-9;:]*)m", line):
            out.append((dict(st), line[pos:m.start()]))
            pos = m.end()
            ps = [int(x) if x else 0 for x in re.split("[;:]", m.group(1))] or [0]
            k = 0
            while k < len(ps):
                p = ps[k]
                if p == 0: st.update(fg=None, bg=None, b=False, d=False, i=False, u=False, r=False)
                elif p == 1: st["b"] = True
                elif p == 2: st["d"] = True
                elif p == 3: st["i"] = True
                elif p == 4: st["u"] = True
                elif p == 7: st["r"] = True
                elif p == 22: st["b"] = st["d"] = False
                elif p == 23: st["i"] = False
                elif p == 24: st["u"] = False
                elif p == 27: st["r"] = False
                elif 30 <= p <= 37: st["fg"] = BASE16[p - 30]
                elif 90 <= p <= 97: st["fg"] = BASE16[p - 90 + 8]
                elif 40 <= p <= 47: st["bg"] = BASE16[p - 40]
                elif 100 <= p <= 107: st["bg"] = BASE16[p - 100 + 8]
                elif p == 39: st["fg"] = None
                elif p == 49: st["bg"] = None
                elif p in (38, 48) and k + 1 < len(ps):
                    key = "fg" if p == 38 else "bg"
                    if ps[k + 1] == 5 and k + 2 < len(ps):
                        st[key] = c256(ps[k + 2]); k += 2
                    elif ps[k + 1] == 2 and k + 4 < len(ps):
                        st[key] = "#%02x%02x%02x" % tuple(ps[k + 2:k + 5]); k += 4
                k += 1
        out.append((dict(st), line[pos:]))
        spans = []
        for s, t in out:
            if not t:
                continue
            fg, bg = s["fg"] or FG, s["bg"]
            if s["r"]:
                fg, bg = (s["bg"] or BG), (s["fg"] or FG)
            css = [f"color:{fg}"]
            if bg: css.append(f"background:{bg}")
            if s["b"]: css.append("font-weight:700")
            if s["d"]: css.append("opacity:.6")
            if s["i"]: css.append("font-style:italic")
            if s["u"]: css.append("text-decoration:underline")
            spans.append(f'<span style="{";".join(css)}">{cells(t)}</span>')
        rows.append("<div>" + ("".join(spans) or " ") + "</div>")
    return "".join(rows)


src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read().rstrip("\n")
page = f"""<!doctype html><meta charset="utf-8">
<style>
html,body{{margin:0;background:transparent}}
.win{{display:inline-block;margin:28px;border-radius:12px;background:{BG};
  box-shadow:0 0 0 1px #3b3b4d,0 12px 32px rgba(20,16,50,.28);overflow:hidden}}
.bar{{height:30px;display:flex;align-items:center;gap:8px;padding:0 14px;background:#1e1d2c}}
.bar i{{width:12px;height:12px;border-radius:50%;display:block}}
.term{{padding:14px 18px 16px;font:14px/18px Menlo,"SF Mono",monospace;white-space:pre;font-variant-ligatures:none;color:{FG}}}
.term div{{height:18px}}
.c1,.c2{{display:inline-block;text-align:center;font-weight:inherit;overflow:visible}}
.c1{{width:1ch}}.c2{{width:2ch}}
</style>
<div class="win"><div class="bar"><i style="background:#ff5f57"></i><i style="background:#febc2e"></i><i style="background:#28c840"></i></div>
<div class="term">{render(text)}</div></div>"""
open(dst, "w", encoding="utf-8").write(page)
