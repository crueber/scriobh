#!/usr/bin/env node
// The administrators' console in Chromium: a person who is not an administrator finds nothing (404 for every page); an
// administrator sees the people with their emails, switches one off by typing their name (their open page is signed out
// and their key stops), switches them on again, sees the limits and use, and downloads a diagnostics bundle that holds
// nothing private. Works at 390 px and by keyboard.
//
//   node scripts/e2e-admin.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync, readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join } from "node:path";

const kit = await createKit("e2e-admin");
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const writer = await kit.signedIn({}, "writer@dev.scriobh");
const reader = await kit.signedIn({}, "reader@dev.scriobh");
const adm = await kit.signedIn({}, "admin@dev.scriobh");
const pages = ["/admin", "/admin/users", "/admin/quotas", "/admin/diagnostics", "/admin/diagnostics/download"];

// something private to look for in the bundle
const made = await api(writer.ctx, "POST", "/api/v1/projects", { title: "CANARYTITLE Salt Road", kind: "novel" });
const first = (await api(writer.ctx, "GET", `/api/v1/projects/${made.json.item.id}/docs`)).json.order[0];
const g = (await api(writer.ctx, "GET", `/api/v1/docs/${first}`)).json.item;
await api(writer.ctx, "PATCH", `/api/v1/docs/${first}`, { base_revision: g.revision, content: "CANARYTEXT the tide came in.\n" });
const key = (await api(writer.ctx, "POST", "/api/v1/keys", { name: "CANARYKEY" })).json;

await kit.test("a person who is not an administrator finds no console page, form or menu entry", async () => {
  kit.allowed.push(/404/);
  for (const who of [writer, reader]) {
    for (const p of pages) {
      const r = await who.page.goto(kit.base() + p, { waitUntil: "load" });
      eq(r.status(), 404, `${p} for a non-administrator`);
    }
    await who.page.goto(kit.base() + "/", { waitUntil: "load" });
    eq(await who.page.locator('a[href^="/admin"]').count(), 0, "the menu offers no console");
  }
});

await kit.test("an administrator sees the people, with emails, and the menu entry", async () => {
  const page = adm.page; kit.main = page;
  await page.goto(kit.base() + "/", { waitUntil: "load" });
  await page.locator(".avatar-btn").click();
  await page.getByRole("link", { name: "Administration" }).click();
  await page.waitForURL(/\/admin\/users/);
  const text = await page.locator("#adm").innerText();
  ok(text.includes("writer@dev.scriobh") && text.includes("reader@dev.scriobh") && text.includes("Wren Writer"), "everyone is listed with an email: " + text.slice(0, 200));
  ok(text.includes("3 people"), "the summary");
  await page.fill("#adm-q", "rory");
  await page.keyboard.press("Enter");
  await page.waitForURL(/q=rory/);
  eq(await page.locator(".adm-user").count(), 1, "the search");
});

await kit.test("switching someone off asks for their name, ends their session and key, and is undone by switching on", async () => {
  const page = adm.page; kit.main = page;
  // the reader has a page open and a key
  kit.allowed.push(/422/); // the refused confirmation
  const rkey = key.secret;
  const withKey = async () => (await fetch(kit.base() + "/api/v1/projects", { headers: { Authorization: "Bearer " + rkey } })).status;
  eq(await withKey(), 200, "the key works");
  await page.goto(kit.base() + "/admin/users", { waitUntil: "load" });
  await page.locator(".adm-user", { hasText: "Wren Writer" }).getByRole("link", { name: /Switch off/ }).click();
  await page.waitForSelector("#adm-name");
  await page.fill("#adm-name", "someone else");
  await page.keyboard.press("Enter");
  await page.waitForSelector(".field-error");
  ok((await page.locator(".field-error").innerText()).includes("not the name"), "a wrong name is refused");
  eq(await page.locator("#adm-name").getAttribute("aria-invalid"), "true", "the field is marked invalid");
  eq(await page.locator("#adm-name").getAttribute("aria-describedby"), "adm-err", "and points at the error");
  eq(await withKey(), 200, "nothing changed yet");
  await page.fill("#adm-name", "Wren Writer");
  await page.keyboard.press("Enter");
  await page.waitForSelector(".saved");
  ok((await page.locator(".saved").innerText()).includes("switched off"), "said so");
  await kit.waitFor(async () => (await withKey()) === 401, "the key stops");
  await writer.page.goto(kit.base() + "/", { waitUntil: "load" });
  ok((await writer.page.locator('a[href*="/auth/login"], button:has-text("Sign in")').count()) > 0, "the open session is signed out");
  // yourself and the last administrator
  await page.goto(kit.base() + "/admin/users", { waitUntil: "load" });
  eq(await page.locator(".adm-user", { hasText: "Ada Admin" }).getByRole("link", { name: /Switch off/ }).count(), 0, "no way to switch yourself off");
  await page.locator(".adm-user", { hasText: "Wren Writer" }).getByRole("button", { name: "Switch on" }).click();
  await page.waitForSelector(".saved");
  eq(await withKey(), 200, "the key works again");
});

await kit.test("limits and use are shown read-only, and the diagnostics bundle holds nothing private", async () => {
  const page = adm.page; kit.main = page;
  await page.goto(kit.base() + "/admin/quotas", { waitUntil: "load" });
  const q = await page.locator("#adm").innerText();
  ok(/configuration/.test(q) && /Wren Writer/.test(q) && /no limit/.test(q), "limits: " + q.slice(0, 160));
  if (shots) { await page.screenshot({ path: `${shots}/admin-quotas-1280.png` }); }
  await page.goto(kit.base() + "/admin/diagnostics", { waitUntil: "load" });
  const [dl] = await Promise.all([page.waitForEvent("download"), page.getByRole("link", { name: "Download the bundle" }).click()]);
  const zipPath = join(kit.tmp, "diag.zip");
  await dl.saveAs(zipPath);
  const js = execFileSync("unzip", ["-p", zipPath, "diagnostics.json"]).toString();
  const readme = execFileSync("unzip", ["-p", zipPath, "README.txt"]).toString();
  ok(/nothing|no manuscript/i.test(readme), "a README");
  for (const secret of ["CANARY", "dev.scriobh", key.secret, kit.dataDir, "Wren", "Rory", "127.0.0.1"]) ok(!js.includes(secret), `the bundle holds ${secret}`);
  const b = JSON.parse(js);
  ok(b.tables.find((t) => t.name === "documents").rows >= 1 && b.migrations.pending === 0 && b.config.secret_key_set === true, "counts, migrations and the config shape");
  if (shots) { await page.screenshot({ path: `${shots}/admin-diagnostics-1280.png` }); kit.main = null; }
});

await kit.test("390 px: the console reads and nothing scrolls sideways", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 800 } }, "admin@dev.scriobh");
  const page = ph.page; kit.main = page;
  for (const p of ["/admin/users", "/admin/quotas", "/admin/diagnostics"]) {
    await page.goto(kit.base() + p, { waitUntil: "load" });
    ok(!(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)), p + " scrolls sideways");
  }
  await page.goto(kit.base() + "/admin/users", { waitUntil: "load" });
  if (shots) { await page.screenshot({ path: `${shots}/admin-users-390.png` }); kit.main = null; }
  await ph.ctx.close();
});

await kit.finish("e2e-admin");
