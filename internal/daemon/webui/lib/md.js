// A naive markdown of our own for the agent's words (tapi chat's bubbles):
// headings, lists, checkboxes, inline code, bold, fenced code. Built as
// elements through textContent only: no HTML, nothing becomes a link.

import { el, txt } from "./dom.js";

// mdSpans: a line's inline parts, as data (no HTML is ever built):
// {code} for `code`, {bold: [parts]} for **bold**, {text} for the rest.
// Bold opens on ** with no blank after it and closes on the next ** with
// no blank before it; neither may sit inside code (code inside bold is
// fine). An unclosed ** stays as written.
export function mdSpans(s) {
  const codes = [];
  const cre = /`([^`\n]+)`/g;
  let m;
  while ((m = cre.exec(s))) codes.push([m.index, cre.lastIndex]);
  const codeAt = (i) => codes.find(([a, b]) => i >= a && i < b);
  const plain = (a, b) => {
    const out = [];
    let at = a;
    for (const [x, y] of codes) {
      if (x < a || y > b) continue;
      if (x > at) out.push({ text: s.slice(at, x) });
      out.push({ code: s.slice(x + 1, y - 1) });
      at = y;
    }
    if (at < b) out.push({ text: s.slice(at, b) });
    return out;
  };
  const out = [];
  let at = 0, i = 0;
  while ((i = s.indexOf("**", i)) >= 0) {
    const c = codeAt(i) || codeAt(i + 1);
    if (c) {
      i = c[1];
      continue;
    }
    if (!/\S/.test(s[i + 2] || "")) {
      i += 2;
      continue;
    }
    let j = i + 3, close = -1;
    while ((j = s.indexOf("**", j)) >= 0) {
      const d = codeAt(j) || codeAt(j + 1);
      if (d) {
        j = d[1];
        continue;
      }
      if (/\S/.test(s[j - 1])) {
        close = j;
        break;
      }
      j++;
    }
    if (close < 0) {
      i += 2;
      continue;
    }
    out.push(...plain(at, i), { bold: plain(i + 2, close) });
    at = i = close + 2;
  }
  out.push(...plain(at, s.length));
  return out;
}

// mdInline: a line's parts as elements, every word through textContent
export function mdInline(parent, s) {
  const put = (to, parts) => {
    for (const p of parts) {
      if (p.bold) put(to.appendChild(el("strong")), p.bold);
      else if (p.code !== undefined) to.append(el("code", null, p.code));
      else to.append(txt(p.text));
    }
  };
  put(parent, mdSpans(s));
}

// mdLines: a naive markdown, one element per line. Headings, list markers
// and checkboxes (dim, monospace), inline code, bold, fenced code
// (monospace, as written). Nothing becomes a link.
export function mdLines(lines) {
  const frag = document.createDocumentFragment();
  let fence = null;
  for (const ln of lines) {
    if (/^\s*```/.test(ln)) {
      if (fence) fence = null;
      else frag.append((fence = el("pre", "fence")));
      continue;
    }
    if (fence) {
      fence.append(txt((fence.childNodes.length ? "\n" : "") + ln));
      continue;
    }
    const h = ln.match(/^ {0,3}#{1,6}\s+(.*)$/);
    if (h) {
      const e = el("div", "h");
      mdInline(e, h[1].replace(/\s+#+\s*$/, ""));
      frag.append(e);
      continue;
    }
    const line = el("div", "l");
    const m = ln.match(/^(\s*)([-*+]|\d{1,3}[.)])\s+(\[[ xX]\]\s+)?(.*)$/);
    if (m) {
      if (m[1]) line.append(txt(m[1]));
      const num = /\d/.test(m[2]);
      const mark = m[3] ? (num ? m[2] + " " : "- ") + m[3].trim() : num ? m[2] : "•";
      line.append(el("span", "cb", mark), txt(" "));
      mdInline(line, m[4]);
    } else {
      mdInline(line, ln);
    }
    frag.append(line);
  }
  // a fence opened on the last line shown (folded) has nothing in it yet
  for (const f of frag.querySelectorAll(".fence")) if (!f.childNodes.length) f.remove();
  return frag;
}

