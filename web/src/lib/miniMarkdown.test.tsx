import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import { describe, expect, it } from "vitest";
import Mini, { safeUrl, type Components } from "./miniMarkdown";
import notes from "./__fixtures__/release-notes.json";

const html = (md: string, comps?: Components) => renderToStaticMarkup(createElement(Mini, { components: comps }, md));
const ref = (md: string, comps?: object) => renderToStaticMarkup(createElement(ReactMarkdown, { skipHtml: true, components: comps as never }, md));
// react-markdown marks external links with nothing; Mini adds target and rel to every external link, so compare without them.
// react-markdown also puts a newline text node between block elements (and after a block's opening tag), which renders as nothing; a newline
// between inline content (a soft break) is kept and must match exactly.
const BLOCK = "(?:h[1-6]|p|ul|ol|li|blockquote|pre|hr)";
const norm = (h: string) =>
  h
    .replace(/ target="_blank"/g, "")
    .replace(/ rel="noopener noreferrer"/g, "")
    .replace(/\r\n/g, "\n")
    .replace(/<br\/>\n/g, "<br/>")
    .replace(new RegExp(`>\\n+(?=<\\/?${BLOCK}\\b)`, "g"), ">")
    .replace(/^\n+|\n+$/g, "");

describe("the same HTML as react-markdown on everything Rivolt renders", () => {
  const pages = readdirSync(join(__dirname, "../content")).filter((f) => f.endsWith(".md"));
  it.each(pages)("content page %s", (f) => {
    const md = readFileSync(join(__dirname, "../content", f), "utf8");
    expect(norm(html(md))).toBe(norm(ref(md)));
  });
  it("every released note (%d)", () => {
    expect(notes.length).toBeGreaterThan(150);
    const bad: string[] = [];
    let strong = 0, items = 0, links = 0, heads = 0;
    for (const n of notes) {
      const h = html(n.body);
      if (norm(h) !== norm(ref(n.body))) bad.push(n.tag);
      strong += (h.match(/<strong>/g) ?? []).length;
      items += (h.match(/<li>/g) ?? []).length;
      links += (h.match(/<a href=/g) ?? []).length;
      heads += (h.match(/<h[1-6]>/g) ?? []).length;
    }
    expect(bad).toEqual([]);
    // The corpus really exercises the renderer (a test that compared two empty strings would pass too).
    expect(strong).toBeGreaterThan(100);
    expect(items).toBeGreaterThan(500);
    expect(heads).toBeGreaterThan(100);
    expect(links).toBeGreaterThan(0);
  });
});

// Constructs the corpus does not have but the renderer supports, against react-markdown.
describe("supported constructs beyond the corpus", () => {
  const cases: Record<string, string> = {
    "emphasis and strong": "a *em* and **strong** and ***both*** and __strong__ and _em_ here",
    "nested emphasis": "**bold with *em* inside** and *em with **bold** inside*",
    "intraword underscores are not emphasis": "snake_case_name and 2*3*4",
    "an unclosed delimiter is literal": "a * b and ** c and _ d",
    "code spans": "`a` and ``b ` c`` and ` d `",
    "an unclosed backtick is literal": "a ` b",
    "links": "[a](https://x.test/a) [b](/b \"title\") [c](#d) [e](<https://x.test/a b>)",
    "a link with emphasis and code in the text": "[**bold** and `code`](https://x.test)",
    "autolinks": "<https://x.test/a> and <me@x.test>",
    "escapes": "\\* not em \\* and \\[not a link\\](x) and \\\\ and \\a",
    "entities": "a &amp; b &lt;c&gt; &quot;d&quot; &#169; &#x263A; &unknown;",
    "hard breaks": "line one  \nline two\\\nline three",
    "a soft break": "line one\nline two",
    "headings": "# h1\n## h2 ##\n### h3\n#### h4\n##### h5\n###### h6\n####### seven",
    "an empty heading": "#\n\ntext",
    "bullets with all three markers": "- a\n- b\n\nparagraph\n\n* c\n* d\n\n+ e\n+ f",
    "a tight list": "- one\n- two **bold**\n- three",
    "a loose list": "- one\n\n- two\n\n- three",
    "an ordered list that starts at 1": "1. a\n2. b\n3. c",
    "an ordered list that starts at 5": "5. a\n6. b",
    "a list item with a continuation line": "- first line\n  continued\n- second",
    "a list item with a lazy continuation": "- first line\ncontinued\n- second",
    "a list interrupting a paragraph": "text\n- a\n- b",
    "a block quote": "> quoted *text*\n> more\n\nafter",
    "a fenced code block": "```js\nconst a = 1 < 2;\n```\n\nafter",
    "a tilde fence": "~~~\nplain\n~~~",
    "thematic breaks": "a\n\n---\n\nb\n\n***\n\nc\n\n___",
    "an html comment is dropped": "a <!-- hidden --> b\n\n<!--\nmulti\nline\n-->\n\nafter",
    "inline html is dropped, its text kept": "a <b>bold</b> and <span class=\"x\">span</span> b",
    "a lone less-than is text": "1 < 2 and a <3 b and <18>",
    "a raw html block is dropped": "<div>\nhidden\n</div>\n\nvisible",
    "windows line endings": "a\r\nb\r\n\r\n- c\r\n- d",
  };
  it.each(Object.entries(cases))("%s", (_name, md) => {
    expect(norm(html(md))).toBe(norm(ref(md)));
  });
});

// What the pages may use, by construct: the ones react-markdown rendered and this renders identically are compared with it; the ones it does
// not support are pinned to the plain text they degrade to (no markup outside the allowed set), so a content change that starts using one
// is a decision, not a surprise. The content pages and the release notes are already compared with react-markdown above, so a future
// Terms or changelog entry that used an unsupported construct fails the corpus test before it ships.
describe("constructs a future page might use", () => {
  const same: Record<string, string> = {
    "a table (react-markdown without GFM shows it as text too)": "| a | b |\n|---|---|\n| 1 | 2 |\n",
    "a table written without pipes at the edges": "Plan | Price\n--- | ---\nFree | $0",
    "an ordered list": "1. a\n2. b\n3. c",
    "an ordered list with parentheses": "1) a\n2) b",
    "a loose ordered list": "1. a\n\n2. b",
    "a block quote": "> quote\n> more",
    "a block quote holding a list": "> - a\n> - b",
    "a fenced code block": "```\ncode\n```",
    "a thematic break": "---",
    "strikethrough (plain text without GFM)": "~~gone~~",
    "a task list (plain text without GFM)": "- [ ] a\n- [x] b",
    "a bare URL (not linked without GFM)": "see https://x.test/a now",
    "a raw HTML block": "<div>hi</div>",
    "a heading with closing hashes": "## Title ##",
  };
  it.each(Object.entries(same))("%s renders as react-markdown does", (_n, md) => {
    expect(norm(html(md))).toBe(norm(ref(md)));
  });

  it("an image is shown only where the page supplies an img component, and only from an allowed source", () => {
    const img = (p: Record<string, unknown>) => createElement("img", p as never);
    expect(norm(html("![alt](https://x.test/a.png)", { img } as never))).toBe(norm(ref("![alt](https://x.test/a.png)")));
    // The legal pages and the changelog pass no img component: images are dropped (the changelog always dropped them).
    expect(html("![alt](https://x.test/a.png)")).toBe("<p></p>");
    expect(html("![alt](javascript:alert(1))", { img } as never)).toBe("<p></p>");
  });

  const unsupported: Record<string, [string, string]> = {
    "indented code": ["    code line\n\nafter", "<p>code line</p><p>after</p>"],
    "a nested bullet list (flattened)": ["- a\n  - b\n  - c\n- d", "<ul><li>a</li><li>b</li><li>c</li><li>d</li></ul>"],
    "an ordered list inside a bullet": ["- a\n  1. b", "<ul><li>a</li></ul><ol><li>b</li></ol>"],
    "a setext heading (=====)": ["Title\n=====", "<p>Title\n=====</p>"],
    "a setext heading (-----)": ["Title\n-----", "<p>Title</p><hr/>"],
    "a reference link": ["[a][1]\n\n[1]: https://x.test", "<p>[a][1]</p><p>[1]: https://x.test</p>"],
    "a footnote": ["a[^1]\n\n[^1]: note", "<p>a[^1]</p><p>[^1]: note</p>"],
  };
  it.each(Object.entries(unsupported))("%s degrades to plain, safe text", (_n, [md, want]) => {
    expect(html(md)).toBe(want);
    expect(tagsOf(html(md)).filter((t) => !ALLOWED_TAGS.has(t))).toEqual([]);
  });
});

// ---- safety ---------------------------------------------------------------------------------------------------------------------------
const ALLOWED_TAGS = new Set(["p", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "blockquote", "pre", "code", "hr", "br", "strong", "em", "a"]);
const tagsOf = (h: string) => [...h.matchAll(/<\/?([a-zA-Z][a-zA-Z0-9]*)/g)].map((m) => m[1].toLowerCase());
// The event-handler attributes (onclick, onerror, ...) a browser would see: the markup parsed, not pattern-matched.
const eventAttrs = (h: string) => {
  const d = document.createElement("div");
  d.innerHTML = h;
  return [...d.querySelectorAll("*")].flatMap((e) => e.getAttributeNames().filter((n) => n.startsWith("on")));
};
const hrefsOf = (h: string) => [...h.matchAll(/\shref="([^"]*)"/g)].map((m) => m[1].replace(/&amp;/g, "&").replace(/&#x27;/g, "'"));
const SAFE_HREF = /^(?:https:|mailto:|\/(?!\/)|#|\?|\.\.?\/|[^:/?#]*(?:[/?#]|$))/;

describe("hostile input", () => {
  const BAD_LINKS = [
    "javascript:alert(1)", "JaVaScRiPt:alert(1)", " javascript:alert(1)", "<javascript:alert(1)>", "java&#x09;script:alert(1)", "&#106;avascript:alert(1)",
    "&#x6A;avascript:alert(1)", "jav\tascript:alert(1)", "data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==", "vbscript:msgbox(1)", "//evil.test/x",
    "http://insecure.test/", "file:///etc/passwd", "blob:https://x.test/1", "about:blank", "ftp://x.test/",
    "jav%0Ascript:alert(1)", "javascript :alert(1)", "\u0001javascript:alert(1)", "javascript://%0Aalert(1)", "x:y",
  ];
  it.each(BAD_LINKS)("a link to %j keeps its text and loses the link", (bad) => {
    for (const md of [`[click](${bad})`, `[click](<${bad}>)`, `<${bad}>`, `![click](${bad})`]) {
      const h = html(md, { img: (p) => createElement("img", p as never) });
      expect(hrefsOf(h).filter((u) => u !== ""), md).toEqual([]);
      expect(h, md).not.toMatch(/\ssrc=/);
    }
    expect(html(`[click](${bad})`)).toContain("click");
  });

  it.each(["https://x.test/a?b=c#d", "mailto:a@x.test", "/path", "/path?q=1#h", "#section", "?q=1", "./a", "../a", "docs/page", "https://x.test/a%20b"])("keeps %s", (good) => {
    expect(hrefsOf(html(`[t](${good})`))).toEqual([good]);
  });

  it("an external link opens safely, an internal one does not need to", () => {
    expect(html("[a](https://x.test)")).toContain('target="_blank" rel="noopener noreferrer"');
    expect(html("[a](/x)")).not.toContain("target=");
    expect(html("<me@x.test>")).toContain('rel="noopener noreferrer"');
  });

  it("escapes all text and drops raw HTML", () => {
    for (const md of [
      "<script>alert(1)</script>", "<img src=x onerror=alert(1)>", "\"><svg onload=alert(1)>", "a <b onmouseover=x>b</b>", "<iframe src=javascript:1></iframe>",
      "`<script>alert(1)</script>`", "**<script>**", "[<script>alert(1)</script>](/x)", "&lt;script&gt;alert(1)&lt;/script&gt;", "&#60;script&#62;", "<!--\n<script>alert(1)</script>\n-->",
      "# <script>alert(1)</script>", "- <script>alert(1)</script>", "> <script>alert(1)</script>",
    ]) {
      const h = html(md);
      expect(tagsOf(h).filter((t) => !ALLOWED_TAGS.has(t)), md).toEqual([]);
      expect(h, md).not.toMatch(/<script|<iframe|<svg|<img/i);
      expect(eventAttrs(h), md).toEqual([]);
    }
    expect(html("`<b>x</b>`")).toBe("<p><code>&lt;b&gt;x&lt;/b&gt;</code></p>");
    expect(html("&lt;b&gt;")).toBe("<p>&lt;b&gt;</p>");
    // A hostile title stays inside its attribute: no onclick attribute is created.
    expect(eventAttrs(html('[a](/x "t&quot; onclick=&quot;x")'))).toEqual([]);
    expect(eventAttrs(html('[a](/x "t\\" onclick=\\"x")'))).toEqual([]);
  });

  it("is quick on pathological input", () => {
    const big = (s: string, n: number) => s.repeat(n);
    const cases = [big("[", 30000), big("]", 30000), big("*", 30000), big("*a ", 10000), big("_a_", 10000), big("`", 30000), big("`a", 15000), big("[a](", 8000), big("[a](<", 8000),
      big("![", 15000), big("(", 30000), big("<", 30000), big("<a ", 10000), big("&", 30000), big("&#x", 10000), big("\\", 30000), big("> ", 5000), big("- ", 5000), big("1. ", 5000),
      big("#", 30000), big("\n", 30000), big("  \n", 10000), big("* ", 10000) + "a", big("**a ", 5000), big("[**a](", 4000), "[".repeat(5000) + "a" + "](x)".repeat(5000),
      // A heading line with a very long run of spaces or tabs (DA: the HEADING pattern must stay linear if it is ever edited).
      "# " + " ".repeat(50000) + "x", "#\t" + "\t".repeat(50000) + "x", "# a" + " ".repeat(50000) + "#" + " ".repeat(50000) + "x", "# a " + "# ".repeat(25000),
      "```" + " ".repeat(50000) + "`", "~~~" + " ".repeat(50000) + "x~", "a" + " ".repeat(50000) + "x\ny", "a" + " ".repeat(50000) + "\nb", "- - ".repeat(15000) + "x", "* * ".repeat(15000) + "x", "1. ".repeat(15000) + "x"];
    for (const md of cases) {
      const t0 = performance.now();
      const h = html(md);
      const ms = performance.now() - t0;
      expect(ms, `${md.slice(0, 12)}… took ${ms.toFixed(0)} ms`).toBeLessThan(1500);
      expect(tagsOf(h).filter((t) => !ALLOWED_TAGS.has(t))).toEqual([]);
    }
  });

  it("fuzz: random hostile Markdown never throws, never emits a tag or a link outside the allowed set", () => {
    // A small seeded generator (mulberry32) so a failure reproduces.
    let a = 0x9e3779b9;
    const rnd = () => ((a = (a + 0x6d2b79f5) | 0), ((t) => ((t = Math.imul(t ^ (t >>> 15), t | 1)), (t ^= t + Math.imul(t ^ (t >>> 7), t | 61)), ((t ^ (t >>> 14)) >>> 0) / 4294967296))(a));
    const pieces = ["[", "]", "(", ")", "<", ">", "*", "**", "_", "__", "`", "``", "!", "\\", "&", "&#106;", "&amp;", "#", "# ", ";", " ", "  \n", "\n", "\n\n", "- ", "1. ", "> ", "---", "```", "~~~",
      "http", "https://x.test/", "javascript:", "alert(1)", "data:", "//", "/x", "%", "%0A", '"', "'", "a", "x", "é", "😀", "\u0000", "\r\n", "<!--", "-->", "<script>", "</b>", "mailto:a@x.test", "\t"];
    const t0 = performance.now();
    for (let n = 0; n < 4000; n++) {
      let md = "";
      for (let k = 0, len = 1 + Math.floor(rnd() * 70); k < len; k++) md += pieces[Math.floor(rnd() * pieces.length)];
      let h = "";
      expect(() => (h = html(md, { img: (p) => createElement("img", p as never) })), JSON.stringify(md)).not.toThrow();
      expect(tagsOf(h).filter((t) => !ALLOWED_TAGS.has(t) && t !== "img"), JSON.stringify(md)).toEqual([]);
      for (const u of hrefsOf(h)) expect(SAFE_HREF.test(u) && !/^javascript:/i.test(u) && !/^(?:data|vbscript|file|blob|about|http):/i.test(u), `${JSON.stringify(md)} -> ${u}`).toBe(true);
      expect(eventAttrs(h), JSON.stringify(md)).toEqual([]);
      expect(h, JSON.stringify(md)).not.toMatch(/<script/i);
    }
    expect(performance.now() - t0).toBeLessThan(8000);
  });

  it("a backslash or an unknown entity in a target is a harmless relative path, not a host", () => {
    for (const rel of ["\\\\evil.test", "\\/evil.test", "javascript&colon;alert(1)"]) {
      for (const u of hrefsOf(html(`[t](${rel})`))) expect(u.startsWith("//") || /^[a-z][a-z0-9+.-]*:/i.test(u), `${rel} -> ${u}`).toBe(false);
    }
  });

  it("safeUrl", () => {
    expect(safeUrl("https://x.test")).toBe("https://x.test");
    for (const bad of ["", "//x", "javascript:x", "data:x", "http://x", " https://x", "https://x\n", "\\x"]) expect(safeUrl(bad), JSON.stringify(bad)).toBe("");
  });
});
