// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
//
// girlpages markup to HTML in the browser, for pages the static site
// doesn't render itself. It's a port of internal/render (inline.go and
// html.go) and must give exactly the same HTML; a test checks.

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

// esc matches Go's html.EscapeString.
export function esc(s) {
  return s.replace(/[&'<>"]/g, c => ({ "&": "&amp;", "'": "&#39;", "<": "&lt;", ">": "&gt;", '"': "&#34;" })[c]);
}

const linkRE = new RegExp("(https?://[^\\t\\n\\f\\r <>()\"'`]*[^\\t\\n\\f\\r <>()\"'`.,;:!?])" +
  "|([A-Za-z_][A-Za-z0-9_.:+-]*)\\(([0-9][a-z0-9]*|n|l)\\)", "g");

function splitRow(row) {
  row = trim(row);
  if (row.startsWith("|")) row = row.slice(1);
  if (row.endsWith("|")) row = row.slice(0, -1);
  return row.split("|").map(trim);
}

// renderHTML renders markup as HTML, returning {html, toc}; link(name,
// section) gives the URL for a reference like ls(1), or "" for none.
export function renderHTML(markup, { link } = {}) {
  let b = "";
  const lists = [], toc = [], ids = new Map();
  const linkify = (s) => {
    let out = "", last = 0;
    for (const m of s.matchAll(linkRE)) {
      let href = "";
      if (m[1] !== undefined) href = m[1];
      else if (link) href = link(m[2], m[3]);
      if (!href) continue;
      out += esc(s.slice(last, m.index)) + `<a href="${esc(href)}">${esc(m[0])}</a>`;
      last = m.index + m[0].length;
    }
    return out + esc(s.slice(last));
  };
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
  const id = (text) => {
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
