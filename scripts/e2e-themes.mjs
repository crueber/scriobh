#!/usr/bin/env node
// End-to-end test of the colour themes in a real Chromium: choosing a light and a dark theme in
// Settings by keyboard, the live preview, the saved choice on the very first paint (read with
// JavaScript off), following the system in both schemes, and a forced mode winning over the system.
//
//   node scripts/e2e-themes.mjs
import { createKit, eq, ok } from "./e2e-kit.mjs";

const kit = await createKit("e2e-themes");
const { api, sleep } = kit;
const base = () => kit.base();
const { ctx: main, page: pg } = await kit.signedIn();
kit.main = pg;

// Page backgrounds, as the browser computes them (rgb).
const RGB = { paper: "rgb(251, 250, 247)", sepia: "rgb(241, 231, 208)", ink: "rgb(19, 19, 17)", slate: "rgb(26, 31, 38)", moss: "rgb(22, 30, 37)" };
RGB.moss = "rgb(22, 30, 25)";
const bg = (page) => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
const raw = async (ctx, path = "/") => (await ctx.request.fetch(base() + path)).text();
const htmlTag = (s) => (/<html[^>]*>/.exec(s) || [""])[0];
const setPrefs = (items) => api(main, "PUT", "/api/v1/me/prefs", { items });
// A fresh page in this signed-in context under a given system colour scheme, with or without script.
const view = async (scheme, path = "/", js = true) => {
  const state = await main.storageState();
  const ctx = await kit.browser.newContext({ storageState: state, colorScheme: scheme, javaScriptEnabled: js, serviceWorkers: "block", viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  await page.goto(base() + path, { waitUntil: "load" });
  return { ctx, page };
};

await kit.test("themes: the defaults are Paper and Ink and put no attribute on <html>", async () => {
  const html = await raw(main);
  ok(!/data-(lt|dk|theme)=/.test(htmlTag(html)), "a default theme puts an attribute on <html>: " + htmlTag(html));
  for (const [scheme, want] of [["light", RGB.paper], ["dark", RGB.ink]]) {
    const v = await view(scheme);
    eq(await bg(v.page), want, `the default ${scheme} background`);
    await v.ctx.close();
  }
});

await kit.test("themes: Settings offers each scheme as a radio group, and the keyboard picks and previews", async () => {
  await pg.goto(`${base()}/settings`);
  const light = pg.getByRole("radiogroup").or(pg.locator("fieldset.theme-pick"));
  eq(await light.count(), 2, "one group per scheme");
  eq(await pg.locator('input[name="theme_light"]').count(), 2, "light themes offered");
  eq(await pg.locator('input[name="theme_dark"]').count(), 3, "dark themes offered");
  // Light: Paper is checked; an arrow key moves to Sepia, which previews at once.
  await pg.locator('input[name="theme_light"][value="paper"]').focus();
  await pg.keyboard.press("ArrowRight");
  ok(await pg.locator('input[name="theme_light"][value="sepia"]').isChecked(), "ArrowRight did not choose Sepia");
  eq(await pg.locator("html").getAttribute("data-lt"), "sepia", "the live preview sets data-lt");
  eq(await bg(pg), RGB.sepia, "the page shows Sepia at once");
  // Dark: Ink, then two steps to Moss; the attribute follows, and so does the swatch's own state.
  await pg.locator('input[name="theme_dark"][value="ink"]').focus();
  await pg.keyboard.press("ArrowRight");
  eq(await pg.locator("html").getAttribute("data-dk"), "slate", "the dark preview sets data-dk");
  await pg.keyboard.press("ArrowRight");
  eq(await pg.locator("html").getAttribute("data-dk"), "moss", "and moves on to Moss");
  // Back to Paper removes the attribute again (the default draws without one).
  await pg.locator(".theme-opt", { has: pg.locator('input[name="theme_light"][value="paper"]') }).click();
  eq(await pg.locator("html").getAttribute("data-lt"), null, "choosing Paper removes data-lt");
  await pg.locator(".theme-opt", { has: pg.locator('input[name="theme_light"][value="sepia"]') }).click();
  // A focus ring shows on the card, and the checked card is marked by more than colour (a tick).
  await pg.locator('input[name="theme_light"][value="sepia"]').focus();
  ok(await pg.locator(".theme-opt:has(input:checked) .theme-tick").first().isVisible(), "no tick on the chosen theme");
  // Save from the keyboard.
  await pg.getByRole("button", { name: "Save settings" }).focus();
  await pg.keyboard.press("Enter");
  await pg.getByText("Settings saved.").waitFor();
  eq(await pg.locator('input[name="theme_light"][value="sepia"]').isChecked(), true, "Sepia stays chosen after saving");
  eq(await pg.locator('input[name="theme_dark"][value="moss"]').isChecked(), true, "Moss stays chosen after saving");
});

await kit.test("themes: the choice is in the served HTML, so the first paint is right with JavaScript off", async () => {
  const tag = htmlTag(await raw(main));
  ok(/data-lt="sepia"/.test(tag) && /data-dk="moss"/.test(tag), "the saved themes are not on <html>: " + tag);
  for (const [scheme, want, name] of [["light", RGB.sepia, "Sepia"], ["dark", RGB.moss, "Moss"]]) {
    const v = await view(scheme, "/", false);
    eq(await bg(v.page), want, `${name} on a ${scheme} system, no script`);
    await v.ctx.close();
  }
  // The same on a document page and in Settings: every page paints the chosen theme.
  const p = (await api(main, "POST", "/api/v1/projects", { title: "Theme book", kind: "novel" })).json.item;
  for (const path of ["/settings", `/p/${p.slug}`, "/search?q=x"]) {
    const v = await view("light", path, false);
    eq(await bg(v.page), RGB.sepia, `${path} on a light system`);
    await v.ctx.close();
  }
});

await kit.test("themes: the browser chrome colour follows the theme and the mode", async () => {
  const metas = (s) => [...s.matchAll(/<meta name="theme-color"[^>]*>/g)].map((m) => m[0]);
  let m = metas(await raw(main));
  eq(m.length, 2, "following the system gives a light and a dark colour");
  ok(m.some((x) => /#F1E7D0/.test(x) && /light/.test(x)) && m.some((x) => /#161E19/.test(x) && /dark/.test(x)), "metas: " + m.join(" "));
  await setPrefs({ theme: "dark" });
  m = metas(await raw(main));
  ok(m.length === 1 && /#161E19/.test(m[0]) && !/media/.test(m[0]), "forced dark: " + m.join(" "));
  await setPrefs({ theme: "light" });
  m = metas(await raw(main));
  ok(m.length === 1 && /#F1E7D0/.test(m[0]), "forced light: " + m.join(" "));
  await setPrefs({ theme: "system" });
});

await kit.test("themes: following the system uses the chosen light or dark theme; a forced mode wins", async () => {
  for (const [scheme, want] of [["light", RGB.sepia], ["dark", RGB.moss]]) {
    const v = await view(scheme);
    eq(await bg(v.page), want, `following a ${scheme} system`);
    await v.ctx.close();
  }
  await setPrefs({ theme: "dark" });
  let v = await view("light");
  eq(await bg(v.page), RGB.moss, "forced dark on a light system shows the dark theme");
  await v.ctx.close();
  await setPrefs({ theme: "light" });
  v = await view("dark");
  eq(await bg(v.page), RGB.sepia, "forced light on a dark system shows the light theme");
  await v.ctx.close();
  await setPrefs({ theme: "system", theme_dark: "slate" });
  v = await view("dark");
  eq(await bg(v.page), RGB.slate, "another dark theme on a dark system");
  await v.ctx.close();
});

await kit.test("themes: a theme the allowlist does not know is refused, and nothing changes", async () => {
  const r = await setPrefs({ theme_dark: "neon" });
  eq(r.status, 422, "an unknown theme");
  const r2 = await setPrefs({ theme_light: "slate" });
  eq(r2.status, 422, "a dark theme in the light slot");
  const r3 = await setPrefs({ theme_light: "paper", theme_dark: "ink" });
  eq(r3.status, 200, "the defaults are accepted");
  await sleep(50);
  ok(!/data-(lt|dk)=/.test(htmlTag(await raw(main))), "back at the defaults, no attribute");
  const v = await view("dark");
  eq(await bg(v.page), RGB.ink, "Ink again");
  await v.ctx.close();
});

await kit.finish("e2e-themes");
