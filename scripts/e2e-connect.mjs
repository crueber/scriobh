#!/usr/bin/env node
// Webhooks and "connect an agent" in Chromium. Webhooks: an address the server refuses is refused in its own words with
// what was typed kept; a webhook made by keyboard shows its signing secret once (focused, with a copy button) and never
// again; a test is delivered to a receiver on this machine and its signature checks out with that secret; a save in the
// book arrives as document.updated; a receiver that says 410 leaves a dead delivery in the log, and "Send again" delivers
// it; switch off and on; delete asks first. Connect: a key made in Settings, the address the page shows answers
// tools/list with it, the snippets carry this page's own origin and the copy button says so. 390 px, both themes.
// The receiver is a plain HTTP server on 127.0.0.1, allowed only because this server runs with
// SCRIOBH_WEBHOOK_ALLOW_PRIVATE=1.
//
//   node scripts/e2e-connect.mjs [--shots DIR]
import { createKit, eq, ok } from "./e2e-kit.mjs";
import { mkdirSync } from "node:fs";
import { createServer } from "node:http";
import { createHmac } from "node:crypto";

// the receiver: records every request; answers with the status set here
const got = [];
let answer = 200;
const recv = createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => { got.push({ headers: req.headers, body, url: req.url }); res.writeHead(answer, { "Content-Type": "text/plain" }); res.end(answer === 200 ? "ok" : "go away"); });
});
await new Promise((r) => recv.listen(0, "127.0.0.1", r));
const hookURL = `http://127.0.0.1:${recv.address().port}/hook/tok-secret-path`;

const kit = await createKit("e2e-connect", { env: { SCRIOBH_WEBHOOK_ALLOW_PRIVATE: "1", SCRIOBH_WEBHOOK_DEBOUNCE_MS: "0" } });
kit.deadline(150000);
const shots = process.argv.includes("--shots") ? process.argv[process.argv.indexOf("--shots") + 1] : "";
if (shots) mkdirSync(shots, { recursive: true });
const { api } = kit;
const owner = await kit.signedIn({}, "writer@dev.scriobh");
await owner.ctx.grantPermissions(["clipboard-read", "clipboard-write"], { origin: new URL(kit.base()).origin });
const made = await api(owner.ctx, "POST", "/api/v1/projects", { title: "Harbour Notes", kind: "novel" });
const pid = made.json.item.id, slug = made.json.item.slug;
const doc = (await api(owner.ctx, "GET", `/api/v1/projects/${pid}/docs`)).json.order[0];
const hooks = `${kit.base()}/p/${slug}/webhooks`;
const T = (page, sel) => page.locator(sel).innerText();
const noHScroll = (page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1);
const signed = (r, secret) => {
  const want = "v1=" + createHmac("sha256", secret).update(r.headers["scriobh-webhook-timestamp"] + "." + r.body).digest("hex");
  return (r.headers["scriobh-webhook-signature"] || "").split(",").map((s) => s.trim()).includes(want);
};
let secret = "", hookID = "";

await kit.test("an address the server refuses is refused in its own words, with what was typed kept", async () => {
  const page = owner.page; kit.main = page;
  kit.allowed.push(/422/); // the refused form is answered 422 with the page, and the browser logs the status
  await page.goto(hooks, { waitUntil: "load" });
  ok((await T(page, "#wh-url-hint")).includes("allows addresses on its own machine"), "the rules say what this server allows");
  await page.fill("#wh-name", "Refused");
  await page.fill("#wh-url", "https://someone@hooks.example.org/x");
  await page.check('input[name="event"][value="comment.created"]');
  await Promise.all([page.waitForNavigation(), page.click('button:has-text("Make the webhook")')]);
  const err = await T(page, "#wh-error");
  ok(err.length > 10 && !/⟦/.test(err), "the server's message is shown: " + err);
  eq(await page.locator("#wh-error").getAttribute("role"), "alert", "announced");
  eq(await page.inputValue("#wh-url"), "https://someone@hooks.example.org/x", "the address is kept");
  eq(await page.locator("#wh-url").getAttribute("aria-invalid"), "true", "and marked");
  ok(await page.locator('input[name="event"][value="comment.created"]').isChecked(), "the events are kept");
});

await kit.test("keyboard alone: a webhook is made and its secret is shown once, focused, with a copy button", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(hooks, { waitUntil: "load" });
  await page.locator("#wh-name").focus();
  await page.keyboard.type("Local receiver");
  await page.keyboard.press("Tab");
  await page.keyboard.type(hookURL);
  for (const ev of ["document.updated", "comment.created"]) { await page.locator(`input[name="event"][value="${ev}"]`).focus(); await page.keyboard.press("Space"); }
  await page.locator('button:has-text("Make the webhook")').focus();
  await Promise.all([page.waitForNavigation(), page.keyboard.press("Enter")]);
  if (!(await page.locator("#wh-once").count())) throw new Error("no secret: " + (await page.locator("main").innerText()).slice(0, 400));
  eq(await page.evaluate(() => document.activeElement.id), "wh-secret", "focus is on the secret");
  secret = await page.inputValue("#wh-secret");
  ok(secret.length > 20, "a secret");
  ok(!page.url().includes(secret), "not in the address");
  const copy = page.locator('[data-copy="#wh-secret"]');
  ok(await copy.isVisible(), "a copy button");
  await copy.click();
  await kit.waitFor(async () => (await T(page, "#cx-live")).includes("copied"), "the copy is announced");
  eq(await page.evaluate(() => navigator.clipboard.readText()), secret, "the clipboard holds the secret");
  if (shots) await page.screenshot({ path: `${shots}/webhooks-secret-1280.png` });
  hookID = (await page.locator(".shl-link").first().getAttribute("id")).replace("hook-", "");
  await page.goto(hooks, { waitUntil: "load" });
  eq(await page.locator("#wh-once").count(), 0, "not shown again");
  const html = await page.content();
  ok(!html.includes(secret), "the secret is nowhere on the page");
  ok(!html.includes("tok-secret-path"), "the sealed path is not shown");
  kit.main = null;
});

await kit.test("a test is delivered and its signature checks out; a save arrives as document.updated", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(hooks, { waitUntil: "load" });
  const before = got.length;
  await Promise.all([page.waitForNavigation(), page.locator(`#hook-${hookID} button:has-text("Send a test")`).click()]);
  ok((await T(page, "#wh-test-result")).includes("delivered"), "the page says it was delivered: " + (await T(page, "#wh-test-result")));
  const ping = got[before];
  ok(ping && ping.url === "/hook/tok-secret-path", "the receiver got it at the whole address");
  ok(signed(ping, secret), "the signature checks out with the secret shown once");
  const g = (await api(owner.ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(owner.ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: g.revision, content: "The tide came in.\n" });
  await kit.waitFor(() => got.some((r) => r.body.includes("document.updated")), "document.updated at the receiver", 15000);
  const up = got.find((r) => r.body.includes("document.updated"));
  ok(signed(up, secret), "signed");
  ok(!up.body.includes("The tide came in"), "no words of the book in it");
});

await kit.test("a receiver that says 410 leaves a dead delivery; Send again delivers it; the log filters by state", async () => {
  const page = owner.page; kit.main = page;
  answer = 410;
  const g = (await api(owner.ctx, "GET", `/api/v1/docs/${doc}`)).json.item;
  await api(owner.ctx, "PATCH", `/api/v1/docs/${doc}`, { base_revision: g.revision, content: "The tide went out.\n" });
  await kit.waitFor(async () => {
    const l = await api(owner.ctx, "GET", `/api/v1/projects/${pid}/webhooks/${hookID}/deliveries?state=dead`);
    return l.json.items.length > 0;
  }, "a dead delivery", 15000);
  answer = 200;
  await page.goto(`${hooks}?log=${hookID}&state=dead#wh-log`, { waitUntil: "load" });
  ok((await T(page, "#wh-log")).includes("failed for good"), "the dead one is listed");
  ok((await T(page, "#wh-log")).includes("asked not to be called again"), "with the failure in words");
  eq(await page.locator('#wh-log a[aria-current="true"]').innerText().then((t) => t.startsWith("Failed")), true, "the filter shows where we are");
  if (shots) await page.screenshot({ path: `${shots}/webhooks-log-1280.png`, fullPage: true });
  const n = got.length;
  await page.locator('#wh-log button:has-text("Send again")').first().focus();
  await Promise.all([page.waitForNavigation(), page.keyboard.press("Enter")]);
  ok((await page.locator("p.saved").innerText()).includes("queued again"), "queued again");
  await kit.waitFor(() => got.length > n, "the replay arrives", 15000);
  kit.main = null;
});

await kit.test("switch off and on; delete asks first, Escape cancels", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(hooks, { waitUntil: "load" });
  await Promise.all([page.waitForNavigation(), page.locator(`#hook-${hookID} button:has-text("Switch off")`).click()]);
  ok((await page.locator(`#hook-${hookID}`).getAttribute("class")).includes("is-off"), "shown as off");
  await Promise.all([page.waitForNavigation(), page.locator(`#hook-${hookID} button:has-text("Switch on")`).click()]);
  await page.locator(`#hook-${hookID} summary`).focus();
  await page.keyboard.press("Enter");
  const del = page.locator(`#hook-${hookID} button:has-text("Delete the webhook")`);
  await del.focus();
  await page.keyboard.press("Enter");
  await page.waitForSelector(`#hook-${hookID} .arm`);
  eq(await page.evaluate(() => document.activeElement.textContent), "Cancel", "focus on Cancel");
  await page.keyboard.press("Escape");
  eq(await page.locator(`#hook-${hookID} .arm`).count(), 0, "Escape cancels");
  await page.keyboard.press("Enter");
  await page.waitForSelector(`#hook-${hookID} .arm`);
  await kit.sleep(600);
  await page.keyboard.press("Tab");
  await Promise.all([page.waitForNavigation(), page.keyboard.press("Enter")]);
  eq(await page.locator(`#hook-${hookID}`).count(), 0, "deleted");
});

await kit.test("connect an agent: a key made in Settings works at the address the page shows", async () => {
  const page = owner.page; kit.main = page;
  await page.goto(`${kit.base()}/settings#keys`, { waitUntil: "load" });
  await page.fill("#key-name", "Agent");
  await page.selectOption("#key-scope", "read");
  await Promise.all([page.waitForNavigation(), page.click('button:has-text("Create key")')]);
  const key = (await T(page, "#new-key")).trim();
  ok(key.startsWith("scr_"), "a key");
  await page.click('a[href="/settings/mcp"]');
  await page.waitForSelector("#mcp-url");
  const addr = await page.inputValue("#mcp-url");
  eq(addr, new URL(kit.base()).origin + "/mcp?groups=core&readonly=1", "the address is this page's origin");
  ok((await T(page, "#mcp-desktop")).includes(`"SCRIOBH_URL": "${new URL(kit.base()).origin}"`), "the snippet carries the origin");
  ok((await T(page, "#mcp-keys")).includes("Agent"), "the key is listed with what it sees");
  const r = await owner.ctx.request.post(addr, { headers: { Authorization: "Bearer " + key, "Content-Type": "application/json" }, data: { jsonrpc: "2.0", id: 1, method: "tools/list" } });
  eq(r.status(), 200, "tools/list answers");
  const tools = (await r.json()).result.tools.map((t) => t.name);
  ok(tools.includes("doc_get") && !tools.some((t) => t.startsWith("webhook_")), "read tools of the manuscript, no webhook tool");
  ok(!tools.includes("doc_update"), "read only");
  // the tool set, by keyboard: the bible, writes allowed
  await page.locator('input[name="set"][value="bible"]').focus();
  await page.keyboard.press("Space");
  await page.locator('input[name="readonly"]').focus();
  await page.keyboard.press("Space");
  await Promise.all([page.waitForNavigation(), page.locator('button:has-text("Show this configuration")').press("Enter")]);
  eq(await page.inputValue("#mcp-url"), new URL(kit.base()).origin + "/mcp?groups=bible", "the address follows the choice");
  ok((await T(page, "#mcp-cli")).includes("--groups bible") && !(await T(page, "#mcp-cli")).includes("--read-only"), "so does the command");
  await page.locator('[data-copy="#mcp-cli"]').click();
  await kit.waitFor(async () => (await T(page, "#cx-live")).includes("Copied"), "copied");
  ok((await page.evaluate(() => navigator.clipboard.readText())).includes("scriobh-cli mcp --groups bible"), "the clipboard has the command");
  if (shots) { await page.screenshot({ path: `${shots}/connect-1280.png`, fullPage: true }); kit.main = null; }
});

await kit.test("390 px, dark: both pages read, nothing scrolls sideways", async () => {
  const ph = await kit.signedIn({ viewport: { width: 390, height: 844 }, colorScheme: "dark", hasTouch: true }, "writer@dev.scriobh");
  const page = ph.page; kit.main = page;
  const h = await api(ph.ctx, "POST", `/api/v1/projects/${pid}/webhooks`, { name: "Phone receiver with a long name", url: hookURL, events: ["document.updated", "comment.created", "suggestion.created", "export.completed"] });
  eq(h.status, 201, "a webhook to look at");
  const id = h.json.item.id;
  await api(ph.ctx, "POST", `/api/v1/projects/${pid}/webhooks/${id}/test`);
  await page.goto(`${hooks}?log=${id}#wh-log`, { waitUntil: "load" });
  ok(await page.locator("#wh-log .cx-log li").count() > 0, "the log has a delivery");
  await page.locator(`#hook-${id} summary`).click();
  ok(await noHScroll(page), "webhooks with a card, its edit fold and the log open: no sideways scroll");
  const btn = await page.locator(`#hook-${id} .cx-actions form .btn`).first().evaluate((e) => e.getBoundingClientRect().width);
  ok(btn > 300, "the card's buttons are full width: " + btn);
  if (shots) await page.screenshot({ path: `${shots}/webhooks-390-dark.png` });
  await page.goto(`${kit.base()}/settings/mcp`, { waitUntil: "load" });
  ok(await noHScroll(page), "connect: no sideways scroll (the code blocks scroll inside themselves)");
  if (shots) await page.screenshot({ path: `${shots}/connect-390-dark.png` });
  kit.main = null;
  await ph.ctx.close();
});

recv.close();
await kit.finish("e2e-connect");
