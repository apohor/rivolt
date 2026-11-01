import { createElement, Fragment, type ReactNode } from "react";

// A small Markdown renderer for the pages Rivolt writes itself (the legal pages, the support pages) and the release notes (Maps 10).
// It replaces react-markdown, whose parser (micromark, mdast, hast, unified, ...) was 33 KB gzipped for a handful of constructs.
//
// What it renders: ATX headings, paragraphs (with hard breaks), flat bullet and numbered lists (tight or loose), block quotes, fenced code,
// thematic breaks, and the inlines **strong**, *emphasis*, `code`, [links](url), ![images](url), <autolinks>, backslash escapes and the
// common entities. Everything else (tables, nested lists, setext headings, reference links, raw HTML) is not supported: raw HTML is dropped, as
// react-markdown did with skipHtml, and the rest renders as the plain text it is. A test renders every released note and every content
// page with both this and react-markdown and requires identical HTML.
//
// Safety: the output is React elements, never an HTML string, so every piece of text is escaped by React. A link target is kept only if it is
// https, mailto, or a relative path of this site (/path, ?query, #hash, ./ ../; never "//host"): anything else (javascript:, data:, vbscript:,
// http:, a scheme hidden behind control characters or an entity) drops the link and keeps its text. An external link gets rel="noopener noreferrer"
// and target="_blank" by default. The scanning is linear, with no nested quantifiers, so hostile input cannot stall it; the tests fuzz it.

type Tag = "h1" | "h2" | "h3" | "h4" | "h5" | "h6" | "p" | "ul" | "ol" | "li" | "blockquote" | "pre" | "code" | "hr" | "br" | "strong" | "em" | "a" | "img";
// A component gets the props react-markdown gave it (only the ones this renderer sets), children included.
export type MdProps = { children?: ReactNode; href?: string; title?: string; target?: string; rel?: string; src?: string; alt?: string; start?: number; className?: string };
export type Components = { [T in Tag]?: (props: MdProps) => ReactNode };

type Props = Record<string, unknown>;
const el = (c: Components, tag: Tag, props: Props, children?: ReactNode): ReactNode => {
  const comp = c[tag];
  if (comp) {
    const { key, ...rest } = props;
    return createElement(Fragment, { key: key as string | number | undefined }, comp({ ...(rest as MdProps), children }));
  }
  return createElement(tag, props, children);
};

// --- link targets -----------------------------------------------------------------------------------------------------------------------

/** The URL if it may be linked, else "": https, mailto, or a relative path of this site. */
export function safeUrl(raw: string): string {
  // Control characters and whitespace anywhere in a scheme are how "java\tscript:" gets past a check; a URL never contains them raw.
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u0020\u007f-\u009f\u2028\u2029]/.test(raw)) return "";
  if (raw === "") return "";
  if (raw.startsWith("//") || raw.startsWith("\\")) return "";
  const c = raw[0];
  if (c === "/" || c === "#" || c === "?") return raw;
  if (raw.startsWith("./") || raw.startsWith("../")) return raw;
  const m = /^([A-Za-z][A-Za-z0-9+.-]*):/.exec(raw);
  if (!m) return /^[^:/?#]*([/?#]|$)/.test(raw) ? raw : ""; // a relative path like "docs/x" (no colon before the first / ? #)
  const scheme = m[1].toLowerCase();
  return scheme === "https" || scheme === "mailto" ? raw : "";
}

const isExternal = (href: string) => /^[a-z][a-z0-9+.-]*:/i.test(href);

// --- inline -----------------------------------------------------------------------------------------------------------------------------

const ENTITIES: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " ", copy: "©", reg: "®", mdash: "—", ndash: "–", hellip: "…", rarr: "→", larr: "←" };
function decodeEntity(s: string, i: number): [string, number] | null {
  const semi = s.indexOf(";", i);
  if (semi < 0 || semi - i > 10) return null;
  const body = s.slice(i + 1, semi);
  let out: string | undefined;
  if (body.startsWith("#x") || body.startsWith("#X")) out = /^[0-9a-fA-F]{1,6}$/.test(body.slice(2)) ? cp(parseInt(body.slice(2), 16)) : undefined;
  else if (body.startsWith("#")) out = /^[0-9]{1,7}$/.test(body.slice(1)) ? cp(parseInt(body.slice(1), 10)) : undefined;
  else out = ENTITIES[body];
  return out === undefined ? null : [out, semi + 1];
}
function cp(n: number): string | undefined {
  return n > 0 && n <= 0x10ffff && !(n >= 0xd800 && n <= 0xdfff) ? String.fromCodePoint(n) : "�";
}

const PUNCT = /[!-/:-@[-`{-~]/;
const isPunct = (ch: string | undefined) => ch !== undefined && (PUNCT.test(ch) || /\p{P}|\p{S}/u.test(ch));
const isSpace = (ch: string | undefined) => ch === undefined || /\s/u.test(ch);

// Bounds that keep hostile input linear-ish: a link label or target longer than this is text, and an inline run with more emphasis
// delimiters than this renders them literally. Real content is far below both.
const MAX_LINK = 2048;
const MAX_DELIMS = 2000;
const MAX_DEPTH = 12; // block quotes inside block quotes, links inside link labels: deeper is plain text

type Tok =
  | { t: "n"; node: ReactNode }
  | { t: "d"; ch: string; n: number; open: boolean; close: boolean; text: string };

/** Tokenise inline text into nodes and emphasis delimiter runs. */
function tokens(s: string, c: Components, kb: { n: number }): Tok[] {
  const out: Tok[] = [];
  let buf = "";
  const flush = () => {
    if (buf) out.push({ t: "n", node: buf });
    buf = "";
  };
  const push = (node: ReactNode) => {
    flush();
    out.push({ t: "n", node });
  };
  const key = () => kb.n++;
  let delims = 0;
  let i = 0;
  while (i < s.length) {
    const ch = s[i];
    if (ch === "\\") {
      const nx = s[i + 1];
      if (nx === "\n") { push(el(c, "br", { key: key() })); i += 2; continue; }
      if (nx !== undefined && PUNCT.test(nx)) { buf += nx; i += 2; continue; }
      buf += ch; i++; continue;
    }
    if (ch === "\n") {
      // Two or more trailing spaces before a newline are a hard break; otherwise the newline stays as it is (a soft break).
      const m = /( {2,})$/.exec(buf);
      if (m) { buf = buf.slice(0, buf.length - m[1].length); push(el(c, "br", { key: key() })); }
      else buf = buf.replace(/ +$/, "") + "\n";
      i++;
      while (s[i] === " " || s[i] === "\t") i++; // leading spaces of the next line are not content
      continue;
    }
    if (ch === "`") {
      let n = 1;
      while (s[i + n] === "`") n++;
      const close = findBackticks(s, i + n, n);
      if (close < 0) { buf += "`".repeat(n); i += n; continue; }
      let code = s.slice(i + n, close).replace(/\n/g, " ");
      if (code.length > 2 && code.startsWith(" ") && code.endsWith(" ") && code.trim() !== "") code = code.slice(1, -1);
      push(el(c, "code", { key: key() }, code));
      i = close + n;
      continue;
    }
    if (ch === "&") {
      const e = decodeEntity(s, i);
      if (e) { buf += e[0]; i = e[1]; continue; }
      buf += ch; i++; continue;
    }
    if (ch === "<") {
      const auto = /^<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*)>/.exec(s.slice(i, i + 2100));
      if (auto) { push(link(c, auto[1], [auto[1]], kb)); i += auto[0].length; continue; }
      const mail = /^<([A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)>/.exec(s.slice(i, i + 300));
      if (mail) { push(link(c, "mailto:" + mail[1], [mail[1]], kb)); i += mail[0].length; continue; }
      if (s.startsWith("<!--", i)) { const end = s.indexOf("-->", i + 4); i = end < 0 ? s.length : end + 3; flush(); continue; }
      const tag = /^<\/?[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*)?\/?>/.exec(s.slice(i, i + 2100));
      if (tag) { i += tag[0].length; flush(); continue; } // raw HTML is dropped, its text kept
      buf += ch; i++; continue;
    }
    if (ch === "!" && s[i + 1] === "[") {
      const l = parseLink(s, i + 1);
      if (l) { push(img(c, l.href, l.label, l.title, kb)); i = l.end; continue; }
      buf += ch; i++; continue;
    }
    if (ch === "[") {
      const l = parseLink(s, i);
      if (l) { push(link(c, l.href, inline(l.label, c, kb), kb, l.title)); i = l.end; continue; }
      buf += ch; i++; continue;
    }
    if ((ch === "*" || ch === "_") && delims < MAX_DELIMS) {
      delims++;
      let n = 1;
      while (s[i + n] === ch) n++;
      const before = s[i - 1] ?? undefined;
      const after = s[i + n] ?? undefined;
      const leftFlank = !isSpace(after) && (!isPunct(after) || isSpace(before) || isPunct(before));
      const rightFlank = !isSpace(before) && (!isPunct(before) || isSpace(after) || isPunct(after));
      const open = ch === "*" ? leftFlank : leftFlank && (!rightFlank || isPunct(before));
      const close = ch === "*" ? rightFlank : rightFlank && (!leftFlank || isPunct(after));
      flush();
      out.push({ t: "d", ch, n, open, close, text: ch.repeat(n) });
      i += n;
      continue;
    }
    buf += ch;
    i++;
  }
  flush();
  return out;
}

function findBackticks(s: string, from: number, n: number): number {
  let i = from;
  while (i < s.length) {
    const j = s.indexOf("`", i);
    if (j < 0) return -1;
    let k = 1;
    while (s[j + k] === "`") k++;
    if (k === n) return j;
    i = j + k;
  }
  return -1;
}

/** [label](dest "title") starting at s[i] === "[": the label text, the target, the title and the index after it; or null. */
function parseLink(s: string, i: number): { label: string; href: string; title?: string; end: number } | null {
  let depth = 0;
  let j = i;
  for (; j < s.length && j - i <= MAX_LINK; j++) {
    const ch = s[j];
    if (ch === "\\") { j++; continue; }
    if (ch === "`") {
      let n = 1;
      while (s[j + n] === "`") n++;
      const close = findBackticks(s, j + n, n);
      if (close >= 0) { j = close + n - 1; continue; }
      j += n - 1;
      continue;
    }
    if (ch === "[") depth++;
    else if (ch === "]") { depth--; if (depth === 0) break; }
  }
  if (j >= s.length || j - i > MAX_LINK || s[j + 1] !== "(") return null;
  const label = s.slice(i + 1, j);
  let k = j + 2;
  while (s[k] === " " || s[k] === "\t" || s[k] === "\n") k++;
  let href: string;
  if (s[k] === "<") {
    const e = s.indexOf(">", k);
    if (e < 0 || e - k > MAX_LINK || s.slice(k, e).includes("\n")) return null;
    href = s.slice(k + 1, e);
    k = e + 1;
  } else {
    let paren = 0;
    const st = k;
    for (; k < s.length && k - st <= MAX_LINK; k++) {
      const ch = s[k];
      if (ch === "\\") { k++; continue; }
      if (ch === " " || ch === "\t" || ch === "\n" || (ch.charCodeAt(0) < 32)) break;
      if (ch === "(") paren++;
      else if (ch === ")") { if (paren === 0) break; paren--; }
    }
    href = s.slice(st, k);
  }
  while (s[k] === " " || s[k] === "\t" || s[k] === "\n") k++;
  let title: string | undefined;
  if (s[k] === '"' || s[k] === "'" || s[k] === "(") {
    const closer = s[k] === "(" ? ")" : s[k];
    const e = s.indexOf(closer, k + 1);
    if (e < 0) return null;
    title = s.slice(k + 1, e);
    k = e + 1;
    while (s[k] === " " || s[k] === "\t" || s[k] === "\n") k++;
  }
  if (s[k] !== ")") return null;
  return { label, href: encodeUrl(unescapeUrl(href)), title, end: k + 1 };
}

/** Backslash escapes and entities in a link target, before it is checked: "&#106;avascript:" is "javascript:". */
function unescapeUrl(u: string): string {
  let out = "";
  for (let i = 0; i < u.length; i++) {
    const ch = u[i];
    if (ch === "\\" && u[i + 1] !== undefined && PUNCT.test(u[i + 1])) { out += u[++i]; continue; }
    if (ch === "&") {
      const e = decodeEntity(u, i);
      if (e) { out += e[0]; i = e[1] - 1; continue; }
    }
    out += ch;
  }
  return out;
}

/** Percent-encodes what a URL cannot carry (spaces, controls, non-ASCII), as react-markdown does, keeping existing %XX escapes. */
function encodeUrl(u: string): string {
  let out = "";
  for (let i = 0; i < u.length; i++) {
    const ch = u[i];
    if (ch === "%" && /^[0-9A-Fa-f]{2}$/.test(u.slice(i + 1, i + 3))) { out += "%"; continue; }
    if (/[!#$&-;=?-Z_a-z~]/.test(ch)) { out += ch; continue; }
    const code = u.codePointAt(i)!;
    if (code > 0xffff) i++;
    try {
      out += encodeURIComponent(String.fromCodePoint(code));
    } catch {
      out += "%EF%BF%BD"; // a lone surrogate
    }
  }
  return out;
}

function link(c: Components, rawHref: string, children: ReactNode[], kb: { n: number }, title?: string): ReactNode {
  const href = safeUrl(rawHref);
  if (href === "") return createElement(Fragment, { key: kb.n++ }, children);
  const props: Props = { key: kb.n++, href };
  if (title) props.title = title;
  if (isExternal(href)) {
    props.target = "_blank";
    props.rel = "noopener noreferrer";
  }
  return el(c, "a", props, children);
}

function img(c: Components, rawHref: string, alt: string, title: string | undefined, kb: { n: number }): ReactNode {
  const src = safeUrl(rawHref);
  if (src === "" || !c.img) return null; // an image is shown only where the page asks for them and the source is allowed
  return el(c, "img", { key: kb.n++, src, alt: plain(alt), title }, undefined);
}

const plain = (s: string) => s.replace(/[*_`[\]\\]/g, "");

/** The CommonMark delimiter algorithm over the tokens: matches openers with closers into strong and emphasis. */
function emphasis(toks: Tok[], c: Components, kb: { n: number }): ReactNode[] {
  type Item = Tok & { nodes?: ReactNode[] };
  const items: Item[] = toks.map((t) => ({ ...t }));
  for (let ci = 0; ci < items.length; ci++) {
    const closer = items[ci];
    if (closer.t !== "d" || !closer.close || closer.n === 0) continue;
    for (let oi = ci - 1; oi >= 0 && closer.n > 0; oi--) {
      const opener = items[oi];
      if (opener.t !== "d" || !opener.open || opener.ch !== closer.ch || opener.n === 0) continue;
      // The "rule of 3": if one of them can both open and close, the lengths must not add up to a multiple of 3 (unless both are).
      if ((opener.close || closer.open) && (opener.n + closer.n) % 3 === 0 && !(opener.n % 3 === 0 && closer.n % 3 === 0)) continue;
      const use = opener.n >= 2 && closer.n >= 2 ? 2 : 1;
      const inner: ReactNode[] = [];
      for (let k = oi + 1; k < ci; k++) {
        const it = items[k];
        if (it.t === "n") inner.push(it.node);
        else if (it.n > 0) inner.push(it.ch.repeat(it.n));
      }
      const node = el(c, use === 2 ? "strong" : "em", { key: kb.n++ }, inner);
      items.splice(oi + 1, ci - oi - 1, { t: "n", node } as Item);
      ci = oi + 2; // the closer's new index: opener, the new node, closer
      opener.n -= use;
      closer.n -= use;
      if (opener.n === 0) { items.splice(oi, 1); ci--; }
      if (closer.n === 0) break;
      oi = ci; // look for another opener for what is left of the closer (the loop steps back from the closer)
    }
  }
  return items.map((it) => (it.t === "n" ? it.node : it.n > 0 ? it.ch.repeat(it.n) : null));
}

let depth = 0;
function inline(s: string, c: Components, kb: { n: number }): ReactNode[] {
  if (depth >= MAX_DEPTH) return [s];
  depth++;
  try {
    return emphasis(tokens(s, c, kb), c, kb);
  } finally {
    depth--;
  }
}

// --- blocks -----------------------------------------------------------------------------------------------------------------------------

const HEADING = /^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$/;
const BULLET = /^ {0,3}([-*+])(?:[ \t]+(.*))?$/;
const ORDERED = /^ {0,3}(\d{1,9})([.)])(?:[ \t]+(.*))?$/;
const HR = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const FENCE = /^ {0,3}(`{3,}|~{3,})[ \t]*([^`\s]*)[^`]*$/;
const QUOTE = /^ {0,3}> ?(.*)$/;
const HTMLBLOCK = /^ {0,3}<(?:!--|\/?[A-Za-z][A-Za-z0-9-]*(?:\s|\/?>|$))/;
const isBlank = (l: string) => /^[ \t]*$/.test(l);

function startsBlock(l: string): boolean {
  return HEADING.test(l) || HR.test(l) || FENCE.test(l) || QUOTE.test(l) || BULLET.test(l) || /^ {0,3}1[.)](?:[ \t]|$)/.test(l);
}

function blocks(lines: string[], c: Components, kb: { n: number }): ReactNode[] {
  if (depth >= MAX_DEPTH) return [lines.join("\n")];
  depth++;
  try {
    return blocksAt(lines, c, kb);
  } finally {
    depth--;
  }
}

function blocksAt(lines: string[], c: Components, kb: { n: number }): ReactNode[] {
  const out: ReactNode[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (isBlank(line)) { i++; continue; }
    let m: RegExpExecArray | null;
    if ((m = FENCE.exec(line))) {
      const fence = m[1];
      const body: string[] = [];
      i++;
      while (i < lines.length && !(lines[i].trim().startsWith(fence) && lines[i].trim().replace(new RegExp(`^${fence[0]}+`), "") === "")) body.push(lines[i++]);
      i++;
      const props: Props = m[2] ? { className: `language-${m[2].replace(/[^A-Za-z0-9_-]/g, "")}` } : {};
      out.push(el(c, "pre", { key: kb.n++ }, el(c, "code", { key: 0, ...props }, body.length ? body.join("\n") + "\n" : "")));
      continue;
    }
    if ((m = HEADING.exec(line))) {
      out.push(el(c, ("h" + m[1].length) as Tag, { key: kb.n++ }, inline((m[2] ?? "").trim(), c, kb)));
      i++;
      continue;
    }
    if (HR.test(line)) { out.push(el(c, "hr", { key: kb.n++ })); i++; continue; }
    if (QUOTE.test(line)) {
      const inner: string[] = [];
      while (i < lines.length && !isBlank(lines[i])) {
        const q = QUOTE.exec(lines[i]);
        if (q) inner.push(q[1]);
        else if (startsBlock(lines[i])) break;
        else inner.push(lines[i]); // lazy continuation
        i++;
      }
      out.push(el(c, "blockquote", { key: kb.n++ }, blocks(inner, c, kb)));
      continue;
    }
    if (HTMLBLOCK.test(line)) {
      // A raw HTML block (a comment, a tag on its own line) is dropped up to the end of the comment or the next blank line.
      if (/^ {0,3}<!--/.test(line)) { while (i < lines.length && !lines[i].includes("-->")) i++; i++; }
      else while (i < lines.length && !isBlank(lines[i])) i++;
      continue;
    }
    const bullet = BULLET.exec(line);
    const ordered = ORDERED.exec(line);
    if (bullet || ordered) {
      const kind = bullet ? "ul" : "ol";
      const marker = bullet ? bullet[1] : ordered![2];
      const start = ordered ? parseInt(ordered[1], 10) : 1;
      const items: string[][] = [];
      let loose = false;
      while (i < lines.length) {
        const l = lines[i];
        const b = BULLET.exec(l);
        const o = ORDERED.exec(l);
        const isItem = kind === "ul" ? b && b[1] === marker : o && o[2] === marker;
        if (!isItem) break;
        const first = (kind === "ul" ? b![2] : o![3]) ?? "";
        const item: string[] = [first];
        i++;
        while (i < lines.length) {
          const n = lines[i];
          if (isBlank(n)) {
            // A blank line ends the item unless the next non-blank line is indented under it.
            let j = i;
            while (j < lines.length && isBlank(lines[j])) j++;
            if (j < lines.length && /^( {2,}|\t)\S/.test(lines[j]) && !BULLET.test(lines[j]) && !ORDERED.test(lines[j])) { item.push(""); i++; loose = true; continue; }
            break;
          }
          if (/^ {0,3}(?:[-*+]|\d{1,9}[.)])(?:[ \t]|$)/.test(n) || startsBlock(n) && !/^( {2,}|\t)/.test(n)) break;
          item.push(n.replace(/^ {1,4}/, "")); // lazy or indented continuation of the item's paragraph
          i++;
        }
        items.push(item);
        // Items separated by a blank line make the list loose.
        let j = i;
        let blankBetween = false;
        while (j < lines.length && isBlank(lines[j])) { j++; blankBetween = true; }
        if (j < lines.length && blankBetween) {
          const b2 = BULLET.exec(lines[j]);
          const o2 = ORDERED.exec(lines[j]);
          const next = kind === "ul" ? b2 && b2[1] === marker : o2 && o2[2] === marker;
          if (next) { loose = true; i = j; }
        }
      }
      const lis = items.map((item) => {
        const text = item.join("\n").replace(/\n+$/, "");
        const kids = blocksInItem(text, c, kb, loose);
        return el(c, "li", { key: kb.n++ }, kids);
      });
      const props: Props = { key: kb.n++ };
      if (kind === "ol" && start !== 1) props.start = start;
      out.push(el(c, kind, props, lis));
      continue;
    }
    // A paragraph: lines until a blank line or the start of another block.
    const para: string[] = [line.replace(/^[ \t]+/, "")];
    i++;
    while (i < lines.length && !isBlank(lines[i]) && !startsBlock(lines[i]) && !HTMLBLOCKINTERRUPT.test(lines[i])) para.push(lines[i++].replace(/^[ \t]+/, ""));
    out.push(el(c, "p", { key: kb.n++ }, inline(para.join("\n").replace(/[ \t]+$/, ""), c, kb)));
  }
  return out;
}
// Only a few kinds of HTML can interrupt a paragraph; a comment is one.
const HTMLBLOCKINTERRUPT = /^ {0,3}<!--/;

/** A list item's content: a tight item is its inline content as it is, a loose one has paragraphs. */
function blocksInItem(text: string, c: Components, kb: { n: number }, loose: boolean): ReactNode[] {
  if (loose || text.includes("\n\n")) return blocks(text.split("\n"), c, kb);
  return inline(text, c, kb);
}

export function parse(source: string, c: Components = {}): ReactNode[] {
  const kb = { n: 0 };
  return blocks(source.replace(/\r\n?/g, "\n").split("\0").join("\ufffd").split("\n"), c, kb);
}

/** Renders Markdown. Props mirror what the pages used of react-markdown: the source as children and a component per element. */
export default function Markdown({ children, components = {} }: { children: string; components?: Components; skipHtml?: boolean }): ReactNode {
  return createElement(Fragment, null, parse(children ?? "", components));
}
