// Writing history for the Writing Studio's browser scripts. No op writes into the past, so the days are written into the
// running development server's database by a Go helper (TestSeedStudioHistory in internal/server, as screens-goals.mjs does
// with its own): the rows the save loop keeps, one day row and one session a day. It needs only the Go toolchain the scripts
// already build the server with. Build-time only; nothing here ships.
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { repo } from "./harness.mjs";

export const dayAgo = (n) => new Date(Date.now() - n * 864e5).toISOString().slice(0, 10);

// rows: [{ ago, words, net?, minutes?, hour?, target?, hit? }]: a day `ago` days before today (UTC, the profile's default zone).
export function seedDays(dataDir, userId, projectId, rows) {
  const dir = mkdtempSync(join(tmpdir(), "scriobh-seed-"));
  const file = join(dir, "rows.json");
  writeFileSync(file, JSON.stringify({ user: userId, project: projectId, rows: rows.map((r) => ({ day: dayAgo(r.ago), words: r.words, net: r.net ?? r.words,
    minutes: r.minutes ?? Math.max(1, Math.round(r.words / 25)), hour: r.hour ?? 9, target: r.target ?? 0, hit: !!r.hit })) }));
  try {
    execFileSync("go", ["test", "./internal/server", "-run", "^TestSeedStudioHistory$", "-count=1"], { cwd: repo, stdio: ["ignore", "ignore", "inherit"], env: { ...process.env, SCRIOBH_SEED_DIR: dataDir, SCRIOBH_SEED_ROWS: file } });
  } finally { rmSync(dir, { recursive: true, force: true }); }
}

// Five years of days, most of them written, with quiet stretches: a long-time writer.
export function fiveYears() {
  const rows = [];
  let x = 12345;
  const rnd = () => (x = (x * 1103515245 + 12345) % 2147483648) / 2147483648;
  for (let ago = 1; ago <= 1825; ago++) {
    const quiet = (ago > 400 && ago < 430) || (ago > 900 && ago < 960) || rnd() < 0.22;
    if (quiet) continue;
    const words = 60 + Math.floor(rnd() * rnd() * 1900);
    rows.push({ ago, words, net: words - Math.floor(rnd() * 120), hour: [6, 7, 7, 8, 13, 20, 21, 22][Math.floor(rnd() * 8)], target: 300, hit: words >= 300 });
  }
  return rows;
}
