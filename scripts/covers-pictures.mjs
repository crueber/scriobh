// Cover pictures for the browser scripts, made in the page with a canvas (Node has no JPEG encoder): flat colours and
// plain shapes, no gradients, so a screenshot of one reads as a picture of something. Build-time only; nothing here ships.
//
//   const jpg = await picture(page, { w: 1200, h: 1800, type: "image/jpeg" });
//   await page.locator('input[name=front_file]').setInputFiles({ name: "front.jpg", mimeType: "image/jpeg", buffer: jpg });

const PALETTES = {
  harbour: { sky: "#d9e2e4", sea: "#3b5b7a", sun: "#e2b04a", hull: "#7a4b2a", sail: "#f4f0e6", ink: "#1c1b18" },
  dusk: { sky: "#e8d9cc", sea: "#4a4740", sun: "#c4673a", hull: "#2f5d50", sail: "#f4f0e6", ink: "#1c1b18" },
};

// A harbour at dusk, as flat shapes. kind: "front" (a portrait), "wrap" (back, spine and front side by side; the bleed
// is the margin the caller says), "wide" (a landscape picture, to be cropped or fitted).
export async function picture(page, { w, h, type = "image/jpeg", palette = "harbour", marks = true }) {
  const b64 = await page.evaluate(async ({ w, h, type, p, marks }) => {
    const c = document.createElement("canvas");
    c.width = w; c.height = h;
    const g = c.getContext("2d");
    g.fillStyle = p.sky; g.fillRect(0, 0, w, h);
    g.fillStyle = p.sea; g.fillRect(0, h * 0.62, w, h * 0.38);
    g.fillStyle = p.sun; g.beginPath(); g.arc(w * 0.68, h * 0.5, Math.min(w, h) * 0.12, 0, Math.PI * 2); g.fill();
    g.fillStyle = p.hull; g.beginPath(); g.moveTo(w * 0.2, h * 0.64); g.lineTo(w * 0.52, h * 0.64); g.lineTo(w * 0.46, h * 0.7); g.lineTo(w * 0.26, h * 0.7); g.fill();
    g.fillStyle = p.sail; g.beginPath(); g.moveTo(w * 0.36, h * 0.3); g.lineTo(w * 0.36, h * 0.62); g.lineTo(w * 0.5, h * 0.62); g.fill();
    if (marks) { // a frame a little inside the edge, so a crop or a margin shows
      g.strokeStyle = p.ink; g.lineWidth = Math.max(2, Math.min(w, h) * 0.006);
      g.strokeRect(w * 0.04, h * 0.03, w * 0.92, h * 0.94);
    }
    const blob = await new Promise((r) => c.toBlob(r, type, 0.9));
    const buf = new Uint8Array(await blob.arrayBuffer());
    let s = ""; for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
    return btoa(s);
  }, { w, h, type, p: PALETTES[palette], marks });
  return Buffer.from(b64, "base64");
}

// A wrap for a trim (inches) with a bleed and a spine, at a resolution: the back and the front are two pictures of the
// trim's proportions, the spine is a flat strip.
export async function wrap(page, { trimW = 6, trimH = 9, bleed = 0.125, spine = 0.5, dpi = 150, type = "image/jpeg" }) {
  const W = Math.round((2 * trimW + spine + 2 * bleed) * dpi), H = Math.round((trimH + 2 * bleed) * dpi);
  const b64 = await page.evaluate(async ({ W, H, type, bleedPx, panel, spinePx }) => {
    const c = document.createElement("canvas");
    c.width = W; c.height = H;
    const g = c.getContext("2d");
    g.fillStyle = "#3b5b7a"; g.fillRect(0, 0, W, H);
    g.fillStyle = "#d9e2e4"; g.fillRect(bleedPx, bleedPx, panel, H - 2 * bleedPx);                          // back
    g.fillStyle = "#e8d9cc"; g.fillRect(W - bleedPx - panel, bleedPx, panel, H - 2 * bleedPx);              // front
    g.fillStyle = "#7a4b2a"; g.fillRect(bleedPx + panel, 0, spinePx, H);                                    // spine
    g.fillStyle = "#e2b04a"; g.beginPath(); g.arc(W - bleedPx - panel * 0.5, H * 0.45, panel * 0.2, 0, Math.PI * 2); g.fill();
    g.fillStyle = "#2f5d50"; g.fillRect(W - bleedPx - panel * 0.85, H * 0.62, panel * 0.7, H * 0.14);
    g.fillStyle = "#1c1b18"; g.fillRect(bleedPx + panel * 0.1, H * 0.2, panel * 0.8, H * 0.012);
    const blob = await new Promise((r) => c.toBlob(r, type, 0.9));
    const buf = new Uint8Array(await blob.arrayBuffer());
    let s = ""; for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
    return btoa(s);
  }, { W, H, type, bleedPx: Math.round(bleed * dpi), panel: Math.round(trimW * dpi), spinePx: Math.round(spine * dpi) });
  return { buffer: Buffer.from(b64, "base64"), W, H };
}

export const SVG = Buffer.from('<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10"/></svg>');

// ---- pictures made in Node, for a script that has no page to draw on at seed time -------------------------------
import { deflateSync } from "node:zlib";
function crc32(buf) { let c, crc = ~0; for (let n = 0; n < buf.length; n++) { c = (crc ^ buf[n]) & 0xff; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; crc = (crc >>> 8) ^ c; } return ~crc >>> 0; }
// A PNG of flat rectangles: fn(x, y) gives [r, g, b]. Row by row; a 1900 x 1400 picture is a few hundred KB once deflated.
export function flatPng(w, h, fn) {
  const chunk = (type, data) => { const len = Buffer.alloc(4); len.writeUInt32BE(data.length); const td = Buffer.concat([Buffer.from(type), data]); const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td)); return Buffer.concat([len, td, crc]); };
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 2;
  const raw = Buffer.alloc((1 + w * 3) * h);
  for (let y = 0; y < h; y++) { const o = y * (1 + w * 3); for (let x = 0; x < w; x++) { const c = fn(x, y); raw[o + 1 + x * 3] = c[0]; raw[o + 2 + x * 3] = c[1]; raw[o + 3 + x * 3] = c[2]; } }
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}
// The harbour at dusk as flat shapes: sky, sea, a sun and a hull.
export const harbourPng = (w, h) => flatPng(w, h, (x, y) => {
  const u = x / w, v = y / h;
  if ((u - 0.68) ** 2 * (w / h) ** 2 + (v - 0.5) ** 2 < 0.012) return [226, 176, 74];
  if (v > 0.64 && v < 0.7 && u > 0.2 && u < 0.5) return [122, 75, 42];
  return v > 0.62 ? [59, 91, 122] : [217, 226, 228];
});
// A wrap for a 6 x 9 trim at 150 dpi with a 0.125 in bleed and a 0.5 in spine, as flat panels.
export const wrapPng = () => { const dpi = 150, W = Math.round(12.75 * dpi), H = Math.round(9.25 * dpi), b = Math.round(0.125 * dpi), p = 6 * dpi, s = Math.round(0.5 * dpi);
  return { buffer: flatPng(W, H, (x, y) => x >= b + p && x < b + p + s ? [122, 75, 42] : x < b + p ? [217, 226, 228] : y > H * 0.6 && y < H * 0.74 && x > W - b - p * 0.85 && x < W - b - p * 0.15 ? [47, 93, 80] : [232, 217, 204]), W, H }; };
