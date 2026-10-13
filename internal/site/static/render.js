// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages markup to HTML in the browser, for pages the static site
// doesn't render itself. It's a port of internal/render (inline.go and
// html.go) and must give exactly the same HTML; a test checks.

import { esc } from "./html.js";

export { esc };

const PALETTE = new Set(["pink", "hotpink", "purple", "lavender", "cyan", "mint", "yellow", "peach", "red", "blue", "green"]);
// Go's \s and unicode.IsPunct, for ASCII.
const S = "[\\t\\n\\f\\r ]";
const PUNCT = "!\"#%&'()*,-./:;?@[\\]_{}`<>*|";

const headingRE = new RegExp(`^${S}{0,3}(#{1,6})${S}+(.*?)${S}*#*${S}*$`);
const ruleRE = new RegExp(`^${S}{0,3}([-*_])(${S}*[-*_]){2,}${S}*$`);
const listRE = new RegExp(`^(${S}*)([-*+•]|\\d{1,3}[.)])${S}+(.*)$`);
const fenceRE = new RegExp(`^${S}*(\`\`\`+|~~~+)`);
const tableSep = new RegExp(`^${S}*\\|?[${S.slice(1, -1)}:|-]+\\|?${S}*$`);

// trim matches Go's strings.TrimSpace closely enough for markup.
const trim = (s) => s.replace(/^[\s\u0085]+|[\s\u0085]+$/g, "");

function indentWidth(s) {
  let n = 0;
  for (const c of s) {
    if (c === " ") n++;
    else if (c === "\t") n += 4;
    else return n;
  }
  return n;
}

function startsBlock(l) {
  const t = trim(l);
  return t === "" || headingRE.test(l) || ruleRE.test(l) || listRE.test(l) || fenceRE.test(l) || t.startsWith(">") || t.startsWith("|");
}

export function parseBlocks(markup) {
  const lines = markup.replaceAll("\r\n", "\n").split("\n");
  const out = [];
  for (let i = 0; i < lines.length;) {
    const l = lines[i], t = trim(l);
    let m;
    if (t === "") {
      if (!out.length || out[out.length - 1].kind !== "blank") out.push({ kind: "blank" });
      i++;
    } else if ((m = fenceRE.exec(l))) {
      const fence = m[1], ind = indentWidth(l);
      i++;
      const code = [];
      while (i < lines.length && !trim(lines[i]).startsWith(fence.slice(0, 3))) {
        let cl = lines[i].replaceAll("\t", "    ");
        const k = Math.min(ind, indentWidth(cl));
        if (k > 0) cl = cl.slice(k);
        code.push(cl.replace(/ +$/, ""));
        i++;
      }
      i++;
      out.push({ kind: "code", lines: code });
    } else if ((m = headingRE.exec(l))) {
      out.push({ kind: "heading", level: m[1].length, text: m[2] });
      i++;
    } else if (ruleRE.test(l)) {
      out.push({ kind: "rule" });
      i++;
    } else if (t.startsWith(">")) {
      const parts = [];
      while (i < lines.length && trim(lines[i]).startsWith(">")) {
        parts.push(trim(trim(lines[i]).replace(/^>+/, "")));
        i++;
      }
      out.push({ kind: "quote", text: parts.join(" ") });
    } else if (t.startsWith("|")) {
      const rows = [];
      while (i < lines.length && trim(lines[i]).startsWith("|")) {
        if (!tableSep.test(lines[i])) rows.push(trim(lines[i]));
        i++;
      }
      out.push({ kind: "table", lines: rows });
    } else if ((m = listRE.exec(l))) {
      const ind = indentWidth(m[1]);
      const marker = m[2][0] >= "0" && m[2][0] <= "9" ? m[2] : "";
      const text = [m[3]];
      i++;
      while (i < lines.length && !startsBlock(lines[i])) text.push(trim(lines[i++]));
      out.push({ kind: "list", level: Math.min(Math.floor(ind / 2), 6), marker, text: text.join(" ") });
    } else {
      const text = [t];
      i++;
      while (i < lines.length && !startsBlock(lines[i])) text.push(trim(lines[i++]));
      out.push({ kind: "para", text: text.join(" ") });
    }
  }
  return out;
}

const isSpace = (c) => c === " " || c === "\t" || c === "\n";
const isWord = (c) => c === "_" || c.charCodeAt(0) >= 0x80 || /[A-Za-z0-9]/.test(c);
const isPunct = (c) => PUNCT.includes(c);

function runLen(s, i, c) {
  let n = 0;
  while (i + n < s.length && s[i + n] === c) n++;
  return n;
}

function findRun(s, i, c, n) {
  while (i < s.length) {
    let j = s.indexOf(c, i);
    if (j < 0) return -1;
    const l = runLen(s, j, c);
    if (l === n) return j;
    i = j + l;
  }
  return -1;
}

function skipCode(s, i) {
  const n = runLen(s, i, "`");
  const end = findRun(s, i + n, "`", n);
  return end >= 0 ? end + n : i + n;
}

function closes(s, i, delim) {
  while (i < s.length) {
    if (s[i] === "`") { i = skipCode(s, i); continue; }
    if (s.startsWith(delim, i) && i > 0 && !isSpace(s[i - 1])) return true;
    i++;
  }
  return false;
}

function closesSingle(s, i) {
  while (i < s.length) {
    if (s[i] === "`") { i = skipCode(s, i); continue; }
    if (s[i] === "*") {
      if (i + 1 < s.length && s[i + 1] === "*") { i += 2; continue; }
      if (!isSpace(s[i - 1]) && (i + 1 === s.length || !isWord(s[i + 1]))) return true;
    }
    i++;
  }
  return false;
}

function colorTag(s) {
  const end = s.indexOf(">");
  if (end < 2 || end > 12) return null;
  let inner = s.slice(1, end), closing = false;
  if (inner.startsWith("/")) { closing = true; inner = inner.slice(1); }
  inner = inner.toLowerCase();
  if (PALETTE.has(inner) || inner === "rainbow") return { name: inner, closing, n: end + 1 };
  return null;
}

// parseInline turns a paragraph into [{text, st: {bold, italic, code, color}}].
export function parseInline(s) {
  const out = [];
  let buf = "", bold = false, italic = false;
  const colors = [];
  const apply = () => ({ bold, italic, code: false, color: colors.length ? colors[colors.length - 1] : "" });
  let cur = apply();
  const flush = () => { if (buf) { out.push({ text: buf, st: cur }); buf = ""; } };
  const restyle = () => { flush(); cur = apply(); };
  for (let i = 0; i < s.length;) {
    const c = s[i];
    if (c === "\\" && i + 1 < s.length && isPunct(s[i + 1])) {
      buf += s[i + 1];
      i += 2;
      continue;
    }
    if (c === "`") {
      const n = runLen(s, i, "`");
      const end = findRun(s, i + n, "`", n);
      if (end >= 0) {
        let code = s.slice(i + n, end);
        if (code.length >= 2 && code[0] === " " && code[code.length - 1] === " " && trim(code) !== "") code = code.slice(1, -1);
        flush();
        out.push({ text: code, st: { ...apply(), code: true } });
        i = end + n;
        continue;
      }
      buf += "`".repeat(n);
      i += n;
      continue;
    }
    if (c === "*" && s.startsWith("**", i)) {
      if (bold && i > 0 && !isSpace(s[i - 1])) { bold = false; restyle(); i += 2; continue; }
      if (!bold && i + 2 < s.length && !isSpace(s[i + 2]) && closes(s, i + 2, "**")) { bold = true; restyle(); i += 2; continue; }
    } else if (c === "*") {
      if (italic && i > 0 && !isSpace(s[i - 1])) { italic = false; restyle(); i++; continue; }
      if (!italic && i + 1 < s.length && !isSpace(s[i + 1]) && (i === 0 || !isWord(s[i - 1])) && closesSingle(s, i + 1)) {
        italic = true; restyle(); i++; continue;
      }
    } else if (c === "<") {
      const tag = colorTag(s.slice(i));
      if (tag) {
        if (tag.closing) {
          for (let j = colors.length - 1; j >= 0; j--) if (colors[j] === tag.name) { colors.splice(j, 1); break; }
        } else colors.push(tag.name);
        restyle();
        i += tag.n;
        continue;
      }
    }
    const cp = s.codePointAt(i);
    const ch = String.fromCodePoint(cp);
    buf += ch;
    i += ch.length;
  }
  flush();
  return out;
}

const plainInline = (s) => parseInline(s).map(g => g.text).join("");


const linkRE = new RegExp("(https?://[^\\t\\n\\f\\r <>()\"'`]*[^\\t\\n\\f\\r <>()\"'`.,;:!?])" +
  "|([A-Za-z_][A-Za-z0-9_.:+-]*)\\(([0-9][a-z0-9]*|n|l)\\)" +
  "|\\bRFC ?0*([1-9][0-9]{0,4})\\b", "g");

function splitRow(row) {
  row = trim(row);
  if (row.startsWith("|")) row = row.slice(1);
  if (row.endsWith("|")) row = row.slice(0, -1);
  return row.split("|").map(trim);
}

// renderHTML renders markup as HTML, returning {html, toc}; link(name,
// section) gives the URL for a reference like ls(1), or "" for none.
// makeLinkify gives a function that escapes text, turning URLs and man
// page references into links.
function makeLinkify(link) {
  return (s) => {
    let out = "", last = 0;
    for (const m of s.matchAll(linkRE)) {
      let href = "";
      if (m[1] !== undefined) href = m[1];
      else if (!link) href = "";
      else if (m[4] !== undefined) href = link(m[4], "rfc"); // RFC 9110
      else href = link(m[2], m[3]);
      if (!href) continue;
      out += esc(s.slice(last, m.index)) + `<a href="${esc(href)}">${esc(m[0])}</a>`;
      last = m.index + m[0].length;
    }
    return out + esc(s.slice(last));
  };
}

// makeIDs gives a function that makes headings' ids, unique on the page.
function makeIDs() {
  const ids = new Map();
  return (text) => {
    let s = "", dash = false;
    for (const r of text.toLowerCase()) {
      if (r.codePointAt(0) < 0x80 && /[a-z0-9]/i.test(r)) {
        if (dash && s) s += "-";
        s += r;
        dash = false;
      } else dash = true;
    }
    if (!s) s = "section";
    const n = (ids.get(s) || 0) + 1;
    ids.set(s, n);
    return n > 1 ? `${s}-${n}` : s;
  };
}

export function renderHTML(markup, { link } = {}) {
  let b = "";
  const lists = [], toc = [];
  const linkify = makeLinkify(link);
  const inline = (s) => {
    let out = "";
    for (const g of parseInline(s)) {
      const open = [], close = [];
      const add = (o, c) => { open.push(o); close.unshift(c); };
      if (g.st.color === "rainbow") add('<span class="rainbow">', "</span>");
      else if (g.st.color) add(`<span class="c-${g.st.color}">`, "</span>");
      if (g.st.bold) add("<strong>", "</strong>");
      if (g.st.italic) add("<em>", "</em>");
      if (g.st.code) add("<code>", "</code>");
      out += open.join("") + linkify(g.text) + close.join("");
    }
    return out;
  };
  const id = makeIDs();
  const closeLists = (level) => {
    while (lists.length && lists[lists.length - 1].level > level) {
      b += lists.pop().ordered ? "</li>\n</ol>\n" : "</li>\n</ul>\n";
    }
  };
  for (const bl of parseBlocks(markup)) {
    if (bl.kind === "blank") continue;
    if (bl.kind !== "list") closeLists(-1);
    switch (bl.kind) {
      case "heading": {
        const tag = bl.level === 1 ? "h2" : bl.level === 2 ? "h3" : "h4";
        const text = plainInline(bl.text);
        const hid = id(text);
        if (bl.level <= 2) toc.push({ id: hid, text: trim(text), level: bl.level });
        b += `<${tag} id="${hid}">${inline(bl.text)}</${tag}>\n`;
        break;
      }
      case "para": b += `<p>${inline(bl.text)}</p>\n`; break;
      case "quote": b += `<blockquote>${inline(bl.text)}</blockquote>\n`; break;
      case "rule": b += '<hr class="sparkle">\n'; break;
      case "code": b += "<pre><code>" + linkify(bl.lines.join("\n")) + "</code></pre>\n"; break;
      case "table":
        b += '<div class="table"><table>\n';
        bl.lines.forEach((row, i) => {
          const cell = i === 0 && bl.lines.length > 1 ? "th" : "td";
          b += "<tr>" + splitRow(row).map(c => `<${cell}>${inline(c)}</${cell}>`).join("") + "</tr>\n";
        });
        b += "</table></div>\n";
        break;
      case "list": {
        const ordered = bl.marker !== "";
        closeLists(bl.level);
        const top = lists.length - 1;
        if (top >= 0 && lists[top].level === bl.level && lists[top].ordered === ordered) {
          b += "</li>\n";
        } else {
          if (top >= 0 && lists[top].level === bl.level) closeLists(bl.level - 1);
          b += ordered ? "<ol>\n" : "<ul>\n";
          lists.push({ level: bl.level, ordered });
        }
        if (ordered) b += `<li value="${esc(bl.marker.replace(/[.)]+$/, ""))}">${inline(bl.text)}`;
        else b += `<li>${inline(bl.text)}`;
        break;
      }
    }
  }
  closeLists(-1);
  return { html: b, toc };
}

// The original, reflowed: a port of internal/render/original.go, which
// must give exactly the same HTML; a test checks.

const FILL_WIDTH = 79;
const BULLETS = new Set(["•", "·", "◦", "‣", "∙", "*", "-", "+", "o"]);
const orderedTag = /^\(?[0-9]{1,3}[.)]$/;
const isLetter = (r) => /\p{L}/u.test(r);
const isLetterOrDigit = (r) => /[\p{L}\p{Nd}]/u.test(r);
const runes = (s) => Array.from(s);
const stripControls = (s) => s.replace(/[\x00-\x08\x0b-\x1f\x7f-\x9f]/g, "");
const fieldSep = /[\t\n\f\r ]{2,}/;

function splitFields(s) {
  s = trim(stripControls(s || ""));
  return s ? s.split(fieldSep) : [];
}

function headFields(s) {
  let f = splitFields(s);
  if (f.length >= 2 && f[0] === f[f.length - 1]) f = f.slice(1, -1);
  return f;
}

function origLines(body) {
  body = body.replaceAll("\r\n", "\n");
  if (body.endsWith("\n")) body = body.slice(0, -1);
  if (!body) return [];
  return body.split("\n").map(l => {
    l = l.replaceAll("\t", "        ").replace(/ +$/, "");
    const t = l.replace(/^ +/, "");
    return { ind: l.length - t.length, text: t };
  });
}

function baseIndent(lines) {
  let base = -1, least = -1;
  for (const l of lines) {
    if (!l.text) continue;
    if (least < 0 || l.ind < least) least = l.ind;
    if (l.ind >= 4 && (base < 0 || l.ind < base)) base = l.ind;
  }
  return base < 0 ? Math.max(least, 0) : base;
}

function tagIndents(lines) {
  const out = new Set();
  for (let i = 0; i + 1 < lines.length; i++) {
    if (lines[i].text && lines[i + 1].text && lines[i + 1].ind > lines[i].ind) out.add(lines[i + 1].ind);
  }
  return out;
}

function tagLike(t) {
  if (!runes(t).some(isLetterOrDigit)) return false;
  return !"{[(,;\\=".includes(t[t.length - 1]);
}

function textLike(t) {
  const rs = runes(t);
  if (!rs.length) return false;
  if (!isLetter(rs[0]) && !"\"'([".includes(rs[0])) {
    const f = trim(t).split(/\s+/);
    if (rs[0] !== "-" || f.length < 3 || !/^\p{Ll}+$/u.test(f[1])) return false;
  }
  let odd = 0;
  for (const r of rs) if ("{}[]<>|=$#;/\\_*&~^`@%".includes(r)) odd++;
  return odd * 10 <= rs.length;
}

function regionEnd(lines, start, ind) {
  let end = start;
  for (let k = start; k < lines.length; k++) {
    if (!lines[k].text) continue;
    if (lines[k].ind < ind) break;
    end = k + 1;
  }
  return end;
}

function isProse(run, width) {
  for (let k = 0; k < run.length; k++) {
    const l = run[k];
    if (l.text.includes("   ")) return false;
    if (k + 1 < run.length) {
      if ("{};\\".includes(l.text[l.text.length - 1])) return false;
      const next = run[k + 1].text.split(" ")[0];
      if (l.ind + runes(l.text).length + 1 + runes(next).length <= width) return false;
    }
  }
  return run.length === 1 ? textLike(run[0].text) : true;
}

function isText(run) {
  return run.every((l, k) => !l.text.includes("   ") && textLike(l.text) && (k === run.length - 1 || trim(l.text).split(/\s+/).length >= 3));
}

function joinProse(run) {
  let b = "";
  run.forEach((l, k) => {
    if (k > 0) {
      const prev = run[k - 1].text;
      const pr = runes(prev.endsWith("-") ? prev.slice(0, -1) : prev).pop() || "";
      const nr = runes(l.text)[0] || "";
      if (!(prev.endsWith("-") && prev.length >= 2 && isLetter(pr) && isLetter(nr))) b += " ";
    }
    b += l.text;
  });
  return b;
}

function mergeCode(nodes) {
  const out = [];
  for (const n of nodes) {
    const last = out[out.length - 1];
    if (last && n.kind === "code" && last.kind === "code") {
      last.lines = [...last.lines, ...(n.gap ? [{ ind: 0, text: "" }] : []), ...n.lines];
      continue;
    }
    out.push(n);
  }
  return out;
}

function codeNodes(lines) {
  const out = [];
  for (let i = 0; i < lines.length;) {
    if (!lines[i].text) { i++; continue; }
    let j = i;
    while (j < lines.length && lines[j].text) j++;
    out.push({ kind: "code", lines: lines.slice(i, j), gap: i > 0 });
    i = j;
  }
  return out;
}

function tagAt(tagInds, lines, i) {
  const l = lines[i], rs = runes(l.text);
  const next = lines[i + 1];
  if (next && next.text && next.ind > l.ind) {
    const col = next.ind, c = col - l.ind;
    if (c >= 2 && rs.length > c && rs[c] !== " " && rs[c - 1] === " " && trim(rs.slice(0, c).join(""))) {
      return { tag: trim(rs.slice(0, c).join("")), desc: rs.slice(c).join(""), col };
    }
    if (rs.length <= 60 && tagLike(l.text)) return { tag: l.text, desc: "", col };
    return null;
  }
  if (next && next.text && next.ind === l.ind) return null;
  if (rs.length >= 3 && BULLETS.has(rs[0]) && rs[1] === " ") {
    let c = 1;
    while (c < rs.length && rs[c] === " ") c++;
    if (c < rs.length) return { tag: rs[0], desc: rs.slice(c).join(""), col: l.ind + c };
  }
  for (let c = 1; c + 1 < rs.length && c <= 24; c++) {
    if (rs[c] !== " ") continue;
    let e = c;
    while (e < rs.length && rs[e] === " ") e++;
    const tag = rs.slice(0, c).join("");
    if (e < rs.length && (e === 7 || tagInds.has(l.ind + e)) && (e - c >= 2 || rs[0] === "-") && tagLike(tag)) {
      return { tag, desc: rs.slice(e).join(""), col: l.ind + e };
    }
    break;
  }
  return null;
}

function parseOriginal(tagInds, lines, ind, top, indented, width) {
  const out = [];
  const add = (i, n) => { n.gap = i > 0 && !lines[i - 1].text; out.push(n); };
  for (let i = 0; i < lines.length;) {
    const l = lines[i];
    if (!l.text) { i++; continue; }
    if (top && l.ind < ind) { add(i, { kind: "sub", text: l.text }); i++; continue; }
    const t = tagAt(tagInds, lines, i);
    if (t) {
      const end = regionEnd(lines, i + 1, t.col);
      let sub = lines.slice(i + 1, end);
      if (t.desc) sub = [{ ind: t.col, text: t.desc }, ...sub];
      const kind = BULLETS.has(t.tag) ? "ul" : orderedTag.test(t.tag) ? "ol" : "dl";
      const item = { tag: t.tag, body: parseOriginal(tagInds, sub, t.col, false, false, width) };
      const last = out[out.length - 1];
      if (last && last.kind === kind) last.items.push(item);
      else add(i, { kind, items: [item] });
      i = end;
      continue;
    }
    if (l.ind > ind) {
      const end = regionEnd(lines, i, l.ind);
      const sub = parseOriginal(tagInds, lines.slice(i, end), l.ind, false, true, width);
      if (sub.length) sub[0].gap = i > 0 && !lines[i - 1].text;
      out.push(...sub);
      i = end;
      continue;
    }
    let j = i + 1;
    while (j < lines.length && lines[j].text && lines[j].ind === ind && !tagAt(tagInds, lines, j)) j++;
    const run = lines.slice(i, j);
    if (indented && run.length === 1 && !run[0].text.endsWith(".")) add(i, { kind: "code", lines: run });
    else if (isProse(run, width)) add(i, { kind: "para", text: joinProse(run) });
    else if (isText(run)) add(i, { kind: "para", text: run.map(l => l.text).join("\n") });
    else add(i, { kind: "code", lines: run });
    i = j;
  }
  return mergeCode(out);
}

// renderOriginalHTML renders a text variant (header, footer and sections)
// as HTML that reflows, returning {html, toc}, as renderHTML does; indent
// and width are as HTMLOptions' Indent and Width.
export function renderOriginalHTML(doc, { link, indent = 0, width = 0, titleBlock = false } = {}) {
  let b = "";
  const toc = [], linkify = makeLinkify(link), id = makeIDs();
  const head = headFields(doc.header);
  if (head.length) b += `<p class="manhead">${esc(head.join(" · "))}</p>\n`;
  const nodes = (ns) => {
    for (const n of ns) {
      switch (n.kind) {
        case "para": b += `<p>${linkify(n.text).replaceAll("\n", "<br>\n")}</p>\n`; break;
        case "sub": {
          const hid = id(n.text);
          toc.push({ id: hid, text: n.text, level: 2 });
          b += `<h3 id="${hid}">${esc(n.text)}</h3>\n`;
          break;
        }
        case "code": {
          let least = -1;
          for (const l of n.lines) if (l.text && (least < 0 || l.ind < least)) least = l.ind;
          const text = n.lines.map(l => l.text ? " ".repeat(l.ind - least) + l.text : "").join("\n");
          b += `<pre><code>${linkify(text)}</code></pre>\n`;
          break;
        }
        case "dl":
          b += "<dl>\n";
          for (const it of n.items) {
            b += `<dt>${linkify(it.tag)}</dt>\n<dd>\n`;
            nodes(it.body);
            b += "</dd>\n";
          }
          b += "</dl>\n";
          break;
        case "ul": case "ol":
          b += `<${n.kind}>\n`;
          for (const it of n.items) {
            b += n.kind === "ol" ? `<li value="${esc(it.tag.replace(/^[().]+|[().]+$/g, ""))}">\n` : "<li>\n";
            nodes(it.body);
            b += "</li>\n";
          }
          b += `</${n.kind}>\n`;
          break;
      }
    }
  };
  (doc.sections || []).forEach((s, i) => {
    const heading = trim(stripControls(s.heading || ""));
    const lines = origLines(stripControls(s.body || ""));
    if (heading) {
      const hid = id(heading);
      toc.push({ id: hid, text: heading, level: 1 });
      b += `<h2 id="${hid}">${esc(heading)}</h2>\n`;
    }
    nodes(heading.toUpperCase() === "SYNOPSIS" || (titleBlock && !heading && i === 0) ? mergeCode(codeNodes(lines))
      : parseOriginal(tagIndents(lines), lines, indent > 0 ? indent : baseIndent(lines), true, false, width > 0 ? width : FILL_WIDTH));
  });
  const foot = splitFields(doc.footer);
  if (foot.length) b += `<p class="manhead">${esc(foot.join(" · "))}</p>\n`;
  return { html: b, toc };
}
