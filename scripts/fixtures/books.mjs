// Small books for the import browser scripts, made here and not kept as files: an EPUB 3 written the way Sigil writes one
// (a navigation document with a part, one file per chapter, a footnote, a cover picture), the same with DRM and with a fixed
// layout (which are refused), a text PDF set like a printed book (a title page, running heads, page numbers, chapters set
// larger than the text, one footnote), a scanned PDF, an encrypted PDF, and a Word document with tracked changes by two
// authors (one pair close enough to meet) and a comment. `zip` is the system's, as the other import scripts use it.
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { deflateSync } from "node:zlib";

const crcTable = (() => { const t = []; for (let n = 0; n < 256; n++) { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; t[n] = c >>> 0; } return t; })();
const crc = (b) => { let c = 0xffffffff; for (const x of b) c = crcTable[(c ^ x) & 255] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
function png(w, h, seed) {
  const chunk = (type, data) => { const len = Buffer.alloc(4); len.writeUInt32BE(data.length); const td = Buffer.concat([Buffer.from(type), data]); const c = Buffer.alloc(4); c.writeUInt32BE(crc(td)); return Buffer.concat([len, td, c]); };
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 2;
  const raw = Buffer.alloc((w * 3 + 1) * h);
  for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) { const o = y * (w * 3 + 1) + 1 + x * 3; raw[o] = (seed + x * 7) & 255; raw[o + 1] = (y * 5) & 255; raw[o + 2] = 90; }
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

function put(dir, files) {
  for (const [name, data] of Object.entries(files)) { const p = join(dir, name); mkdirSync(dirname(p), { recursive: true }); writeFileSync(p, data); }
}
function zipTo(out, dir, first) {
  const z = (args) => execFileSync("zip", ["-q", "-X", ...args], { cwd: dir });
  if (first) z(["-0", out, first]);
  z(["-r", "-9", "-D", out, ".", ...(first ? ["-x", first] : [])]);
}

const page = (title, attr, body) => `<?xml version="1.0" encoding="utf-8"?>\n<!DOCTYPE html>\n<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="en"><head><title>${esc(title)}</title></head><body${attr}>${body}</body></html>`;
const para = (t) => `<p>${t}</p>`;
const words = (n, seed) => { const w = "salt marsh water road wall boat reed tide grey wind low heron slow field light stone ditch gate".split(" "); let s = []; for (let i = 0; i < n; i++) s.push(w[(i * 7 + seed) % w.length]); s[0] = s[0][0].toUpperCase() + s[0].slice(1); return s.join(" ") + "."; };

// A long book for the narrow-screen checks: four parts of seven chapters, then ten parts inside one another with eight chapters at the bottom,
// long titles that wrap, and a first chapter of 400 short paragraphs (so editing every other one makes 200 differences).
export function makeBigEpub(file, scratch, paras1 = 400) {
  const dir = join(scratch, "big-" + Math.random().toString(36).slice(2, 8));
  const longTitle = (n) => `Chapter ${n}: ${["the long and winding road", "what the tide left behind", "a conversation at the sluice gate", "the year of the grey herons", "over the wall and far away"][n % 5]} and a title long enough to wrap`;
  const files = { mimetype: "application/epub+zip", "META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>` };
  const items = [], spine = [];
  const add = (id, href, body) => { files["OEBPS/" + href] = body; items.push(`<item id="${id}" href="${href}" media-type="application/xhtml+xml"/>`); spine.push(`<itemref idref="${id}"/>`); };
  let n = 0;
  const chapter = (title) => {
    n++;
    const paras = n === 1 ? Array.from({ length: paras1 }, (_, i) => `<p>Paragraph ${i + 1} of the first chapter, with words enough to stand on its own line. ${words(8, i)}</p>`).join("") : [1, 2, 3].map((i) => para(words(40, n * 3 + i))).join("");
    add("c" + n, `text/c${n}.xhtml`, page(title, "", `<section epub:type="chapter"><h2>${esc(title)}</h2>${paras}</section>`));
    return `<li><a href="text/c${n}.xhtml">${esc(title)}</a></li>`;
  };
  let partN = 0;
  let nav = "";
  for (let p = 1; p <= 4; p++) {
    partN++;
    const title = `Part ${p}: a part with a long name that has to wrap on a phone`, href = `text/p${partN}.xhtml`;
    add("p" + partN, href, page(title, "", `<section epub:type="part"><h1>${esc(title)}</h1></section>`));
    let inner = "";
    for (let c = 0; c < 7; c++) inner += chapter(longTitle(n + 1));
    nav += `<li><a href="${href}">${esc(title)}</a><ol>${inner}</ol></li>`;
  }
  // ten parts inside one another: the files are in reading order (outermost first), the contents nest them, the chapters are in the innermost
  const lv = [];
  for (let d = 1; d <= 10; d++) { partN++; add("p" + partN, `text/p${partN}.xhtml`, page(`Level ${d}`, "", `<section epub:type="part"><h1>Level ${d} part of the deepest nesting</h1></section>`)); lv.push(partN); }
  let deep = ""; for (let c = 0; c < 8; c++) deep += chapter(longTitle(n + 1));
  for (let d = 10; d >= 1; d--) deep = `<li><a href="text/p${lv[d - 1]}.xhtml">Level ${d} part of the deepest nesting</a><ol>${deep}</ol></li>`;
  nav += deep;
  files["OEBPS/nav.xhtml"] = page("Contents", "", `<nav epub:type="toc" id="toc"><h1>Contents</h1><ol>${nav}</ol></nav>`);
  items.push(`<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`);
  files["OEBPS/content.opf"] = `<?xml version="1.0" encoding="utf-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="en"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="bookid">urn:isbn:9780000000123</dc:identifier><dc:title>The Long Marsh Book</dc:title><dc:creator>Ada Fixture</dc:creator><dc:language>en</dc:language><meta property="dcterms:modified">2020-01-02T03:04:05Z</meta></metadata><manifest>${items.join("")}</manifest><spine>${spine.join("")}</spine></package>`;
  put(dir, files);
  zipTo(file, dir, "mimetype");
  return file;
}

// kind: "ok" | "drm" | "fixed" | "v2" (a revised edition of the same book: a reworded chapter, a renamed one, a new one) | "hostile"
// (chapter two holds tags, markers, wiki links, template braces and a bidirectional override as text)
export function makeEpub(file, kind = "ok", scratch) {
  const dir = join(scratch, "epub-" + kind + "-" + Math.random().toString(36).slice(2, 8));
  const v2 = kind === "v2", hostile = kind === "hostile";
  const ch = (n, title, extra = "") => page(title, "", `<section epub:type="chapter"><h2>${esc(title)}</h2>${[1, 2, 3, 4].map((i) => para(words(40, n * 3 + i))).join("")}${extra}</section>`);
  const opf = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="en"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="bookid">urn:isbn:9780000000099</dc:identifier><dc:title>The Marsh Almanac${v2 ? ", Second Edition" : ""}</dc:title><dc:creator>Ada Fixture</dc:creator><dc:language>en-GB</dc:language><dc:publisher>Fixture House</dc:publisher><dc:date>2019-05-01</dc:date>
<meta property="dcterms:modified">2020-01-02T03:04:05Z</meta><meta name="cover" content="cover-img"/>${kind === "fixed" ? '<meta property="rendition:layout">pre-paginated</meta>' : ""}</metadata>
<manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="cover-img" href="images/cover.png" media-type="image/png" properties="cover-image"/>
<item id="cover" href="text/cover.xhtml" media-type="application/xhtml+xml"/><item id="title" href="text/title.xhtml" media-type="application/xhtml+xml"/><item id="ded" href="text/dedication.xhtml" media-type="application/xhtml+xml"/>
<item id="p1" href="text/part1.xhtml" media-type="application/xhtml+xml"/><item id="c1" href="text/ch01.xhtml" media-type="application/xhtml+xml"/><item id="c2" href="text/ch02.xhtml" media-type="application/xhtml+xml"/><item id="c3" href="text/ch03.xhtml" media-type="application/xhtml+xml"/>
<item id="ack" href="text/ack.xhtml" media-type="application/xhtml+xml"/></manifest>
<spine><itemref idref="cover" linear="no"/><itemref idref="title"/><itemref idref="ded"/><itemref idref="p1"/><itemref idref="c1"/><itemref idref="c2"/><itemref idref="c3"/><itemref idref="ack"/></spine></package>`;
  const nav = page("Contents", "", `<nav epub:type="toc" id="toc"><h1>Contents</h1><ol><li><a href="text/dedication.xhtml">Dedication</a></li><li><a href="text/part1.xhtml">Part One: The Marsh</a><ol>
<li><a href="text/ch01.xhtml">The Road</a></li><li><a href="text/ch02.xhtml">The Wall</a></li><li><a href="text/ch03.xhtml">The Tide</a></li></ol></li><li><a href="text/ack.xhtml">Acknowledgments</a></li></ol></nav>`);
  const files = {
    mimetype: "application/epub+zip",
    "META-INF/container.xml": `<?xml version="1.0" encoding="UTF-8"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
    "OEBPS/content.opf": opf, "OEBPS/nav.xhtml": nav, "OEBPS/images/cover.png": png(60, 80, 1),
    "OEBPS/text/cover.xhtml": page("Cover", ' epub:type="cover"', `<section epub:type="cover"><img src="../images/cover.png" alt="Cover"/></section>`),
    "OEBPS/text/title.xhtml": page("The Marsh Almanac", ' epub:type="frontmatter"', `<section epub:type="titlepage"><h1>The Marsh Almanac</h1><p>Ada Fixture</p></section>`),
    "OEBPS/text/dedication.xhtml": page("Dedication", ' epub:type="frontmatter"', `<section epub:type="dedication"><h2>Dedication</h2><p><em>For the marsh.</em></p></section>`),
    "OEBPS/text/part1.xhtml": page("Part One", ' epub:type="bodymatter"', `<section epub:type="part"><h1>Part One: The Marsh</h1></section>`),
    "OEBPS/text/ch01.xhtml": ch(1, "The Road", `<p>The reeds stood high<a epub:type="noteref" href="#n1" id="r1">1</a> beside it.</p><aside epub:type="footnote" id="n1"><p>1. Reeds here grow to three metres.</p></aside>`),
    "OEBPS/text/ch02.xhtml": hostile ? ch(2, "The Wall", `<p>The &lt;script&gt;alert(1)&lt;/script&gt; boat came [[in]] at %%dawn%% with {{.Secret}} and \u202eesrever text.</p>`) : v2 ? ch(2, "The Great Wall", `<p>A wholly new paragraph about the wall, written for the second edition.</p>`) : ch(2, "The Wall"),
    "OEBPS/text/ch03.xhtml": v2 ? page("The Tide", "", `<section epub:type="chapter"><h2>The Tide</h2>${[1, 2, 3, 4].map((i) => para(words(40, 90 + i))).join("")}</section>`) : ch(3, "The Tide"),
    "OEBPS/text/ack.xhtml": page("Acknowledgments", ' epub:type="backmatter"', `<section epub:type="acknowledgments"><h2>Acknowledgments</h2>${para("Thanks to everyone who walked the wall.")}</section>`),
  };
  if (kind === "nocover") {
    files["OEBPS/content.opf"] = opf.replace('<meta name="cover" content="cover-img"/>', "").replace(/<item id="cover-img"[^>]*\/>/, "").replace(/<item id="cover"[^>]*\/>/, "").replace('<itemref idref="cover" linear="no"/>', "");
    delete files["OEBPS/images/cover.png"]; delete files["OEBPS/text/cover.xhtml"];
  }
  if (kind === "drm") files["META-INF/encryption.xml"] = `<?xml version="1.0" encoding="UTF-8"?><encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#"><enc:EncryptedData><enc:EncryptionMethod Algorithm="http://www.w3.org/2001/04/xmlenc#aes128-cbc"/><enc:CipherData><enc:CipherReference URI="OEBPS/text/ch01.xhtml"/></enc:CipherData></enc:EncryptedData></encryption>`;
  put(dir, files);
  zipTo(file, dir, "mimetype");
  return file;
}

// ---- PDF: set like a printed book, with the standard fonts -------------------------------------------------------
function wrap(text, max) { const out = []; let line = ""; for (const w of text.split(" ")) { if ((line + " " + w).trim().length > max) { out.push(line); line = w; } else line = (line + " " + w).trim(); } if (line) out.push(line); return out; }
const pdfEsc = (s) => s.replace(/\\/g, "\\\\").replace(/\(/g, "\\(").replace(/\)/g, "\\)");

// chapters: [{title, paras: [text]}]; returns a Buffer.
export function makePdf({ title = "The Harbour Book", author = "A. Writer", chapters, scanned = false, encrypted = false } = {}) {
  chapters = chapters || [1, 2, 3].map((n) => ({ title: ["Arrival", "The Quay", "Departure"][n - 1], paras: [1, 2, 3, 4].map((i) => words(95, n * 5 + i)) }));
  const pages = []; // each is a list of content-stream lines
  if (!scanned) {
    pages.push([`BT /F2 28 Tf 1 0 0 1 150 520 Tm (${pdfEsc(title)}) Tj ET`, `BT /F1 14 Tf 1 0 0 1 240 480 Tm (${pdfEsc(author)}) Tj ET`]);
    for (const c of chapters) {
      let cur = [`BT /F2 20 Tf 1 0 0 1 72 640 Tm (${pdfEsc(c.title)}) Tj ET`], y = 600, first = true;
      for (const p of c.paras) {
        const lines = wrap(p, 88);
        lines.forEach((l, i) => {
          if (y < 90) { pages.push(cur); cur = []; y = 690; }
          cur.push(`BT /F1 11 Tf 1 0 0 1 ${i === 0 && !first ? 90 : 72} ${y} Tm (${pdfEsc(l)}) Tj ET`);
          y -= 15;
        });
        first = false;
      }
      pages.push(cur);
    }
    // a running head and a page number on every page after the first
    pages.forEach((p, i) => { if (i > 0) { p.push(`BT /F1 9 Tf 1 0 0 1 72 750 Tm (${pdfEsc(title)}) Tj ET`, `BT /F1 9 Tf 1 0 0 1 300 40 Tm (${i + 1}) Tj ET`); } });
  } else {
    for (let i = 0; i < 4; i++) pages.push([`q 400 0 0 500 100 100 cm BI /W 4 /H 4 /CS /G /BPC 8 ID ${"\x10\x80\xf0\x40".repeat(4)} EI Q`]);
  }
  const objs = []; // 1-based
  const add = (s) => { objs.push(s); return objs.length; };
  add(""); add(""); // 1 catalog, 2 pages: filled below
  const f1 = add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>");
  const f2 = add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Bold /Encoding /WinAnsiEncoding >>");
  const kids = pages.map((lines) => {
    const body = lines.join("\n");
    const c = add(`<< /Length ${Buffer.byteLength(body, "latin1")} >>\nstream\n${body}\nendstream`);
    return add(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 ${f1} 0 R /F2 ${f2} 0 R >> >> /Contents ${c} 0 R >>`);
  });
  objs[0] = "<< /Type /Catalog /Pages 2 0 R >>";
  objs[1] = `<< /Type /Pages /Kids [${kids.map((k) => k + " 0 R").join(" ")}] /Count ${kids.length} >>`;
  let enc = "";
  if (encrypted) { const e = add("<< /Filter /Standard /V 1 /R 2 /O (aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa) /U (bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb) /P -4 >>"); enc = ` /Encrypt ${e} 0 R`; }
  let out = "%PDF-1.4\n"; const off = [];
  objs.forEach((o, i) => { off.push(Buffer.byteLength(out, "latin1")); out += `${i + 1} 0 obj\n${o}\nendobj\n`; });
  const xref = Buffer.byteLength(out, "latin1");
  out += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n${off.map((o) => String(o).padStart(10, "0") + " 00000 n \n").join("")}trailer\n<< /Size ${objs.length + 1} /Root 1 0 R${enc} >>\nstartxref\n${xref}\n%%EOF\n`;
  return Buffer.from(out, "latin1");
}
export function writePdf(file, opts) { writeFileSync(file, makePdf(opts)); return file; }

// ---- Word with tracked changes ---------------------------------------------------------------------------------------
const wns = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"';
let rev = 100;
const run = (t) => `<w:r><w:t xml:space="preserve">${esc(t)}</w:t></w:r>`;
const ins = (a, d, t) => `<w:ins w:id="${++rev}" w:author="${esc(a)}" w:date="${d}">${run(t)}</w:ins>`;
const del = (a, d, t) => `<w:del w:id="${++rev}" w:author="${esc(a)}" w:date="${d}"><w:r><w:delText xml:space="preserve">${esc(t)}</w:delText></w:r></w:del>`;
const wp = (style, ...parts) => `<w:p>${style ? `<w:pPr><w:pStyle w:val="${style}"/></w:pPr>` : ""}${parts.join("")}</w:p>`;

export function makeDocx(file, scratch) {
  const dir = join(scratch, "docx-" + Math.random().toString(36).slice(2, 8));
  const d1 = "2024-03-05T10:21:00Z", d2 = "2024-04-09T08:00:00Z";
  const doc = `<?xml version="1.0" encoding="UTF-8"?><w:document ${wns}><w:body>` +
    wp("Title", run("The Harbour Notes")) + wp("Heading1", run("One")) +
    wp("", run("The boat came in at "), del("Ann Editor", d1, "dawn"), ins("Ann Editor", d1, "first light"), run(" and the harbour was quiet. Nobody "), ins("Bo Reader", d2, "ever "), run("spoke of the winter.")) +
    `<w:p>${run("A second paragraph with a remark on ")}<w:commentRangeStart w:id="0"/>${run("this phrase")}<w:commentRangeEnd w:id="0"/><w:r><w:commentReference w:id="0"/></w:r>${run(" and a little more text after it.")}</w:p>` +
    wp("", run("A third paragraph that stays "), del("Bo Reader", d2, "very "), run("exactly as it was written, with enough words to be found.")) +
    `<w:p><w:pPr><w:rPr><w:del w:id="900" w:author="Ann Editor" w:date="${d1}"/></w:rPr></w:pPr>${del("Ann Editor", d1, "A paragraph the reviewer cut entirely.")}</w:p>` +
    wp("Heading1", run("Two")) + wp("", run("Chapter two has "), ins("Ann Editor", d1, "an added sentence. "), run("Then the rest of the text goes on for a while so it is long enough.")) +
    `</w:body></w:document>`;
  const styles = `<?xml version="1.0"?><w:styles ${wns}><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/></w:style></w:styles>`;
  const comments = `<?xml version="1.0"?><w:comments ${wns}><w:comment w:id="0" w:author="Bo Reader" w:date="${d2}"><w:p><w:r><w:t>Say this better.</w:t></w:r></w:p></w:comment></w:comments>`;
  put(dir, { "[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`, "word/document.xml": doc, "word/styles.xml": styles, "word/comments.xml": comments });
  zipTo(file, dir);
  return file;
}

// A Word file with one chapter of 16,000 words that has page breaks and a deeper heading inside it, then a short chapter: the look inside
// a long chapter has something to list.
export function makeLongDocx(file, scratch) {
  const dir = join(scratch, "longdocx-" + Math.random().toString(36).slice(2, 8));
  const brk = `<w:p><w:r><w:br w:type="page"/></w:r></w:p>`;
  let body = wp("Heading1", run("The Long One"));
  for (let i = 0; i < 160; i++) body += (i && i % 40 === 0 ? brk : "") + (i === 60 ? wp("Heading2", run("A deeper heading")) : "") + wp("", run(("word ".repeat(100)).trim() + ` ${i}.`));
  body += wp("Heading1", run("The Short One")) + wp("", run("A few words to end on, so it is not empty."));
  const doc = `<?xml version="1.0" encoding="UTF-8"?><w:document ${wns}><w:body>${body}</w:body></w:document>`;
  const styles = `<?xml version="1.0"?><w:styles ${wns}><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style></w:styles>`;
  put(dir, { "[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`, "word/document.xml": doc, "word/styles.xml": styles });
  zipTo(file, dir);
  return file;
}
