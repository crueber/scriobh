// The bible the entity screens and the sidebar's end-to-end script both measure: one reference character (eleven defined
// fields, four of them filled, three other names, two relationships and a description of about two hundred words), a few
// more entries of every kind, and enough filler for a list of thirty. One seed, so the "before" numbers taken on the old
// pages and the "after" numbers taken on the new ones are about the same entity.
//
//   const bible = await seedBible(api);   // api(method, path, data) -> the JSON of the reply, thrown on 3xx and up

export const REFERENCE_DESCRIPTION = [
  "She has kept the **Salt Quay** ferry since the flood took her father, and she has not forgiven the river for it. Every morning she walks the length of the slip before it is light, touches the third post, and says the name of the boat aloud, as her father did.",
  "## What she knows",
  "- Every tide of the estuary, by smell and by the sound of the water under the planks.\n- Who owes the Guild, and how much, and since when.\n- Which of the quay's lamps are lit by the Guild and which by the people who live under them.",
  "She is not a kind woman, but she is a fair one, and the fare has never gone up without a reason she is willing to give to anyone who asks. Strangers who ask twice are told to walk.",
  "> The sea gives things back, in its own time.",
  "In the second book she is older, and the quay is quieter. The brass lantern she found on the slip after the storm hangs by her door, unlit. She says it is not hers to light. Tadhg says that is the only true thing she has ever said about it, and that he would like to know whose it is.",
].join("\n\n");

export function wordCount(s) { return (s.match(/[\p{L}\p{N}'’-]+/gu) || []).length; }

export async function seedBible(api, opts = {}) {
  const fillers = opts.fillers ?? 15;
  const comp = (await api("POST", "/api/v1/compendiums", { name: "The Salt Road Bible", kind: "fiction", description: "Everything the Salt Road books agree on." })).item;
  const empty = (await api("POST", "/api/v1/compendiums", { name: "Hedgerow Notes" })).item;
  const mk = async (kind, name, extra = {}) => (await api("POST", `/api/v1/compendiums/${comp.id}/entities`, { kind, name, ...extra })).item;
  // The starter fields of a character are eight (role, age, appearance, voice, wants, fears, secrets, arc); three more make eleven.
  await api("PUT", `/api/v1/compendiums/${comp.id}/entity-fields`, { kind: "character", key: "fare", label: "Ferry fare", type: "number" });
  await api("PUT", `/api/v1/compendiums/${comp.id}/entity-fields`, { kind: "character", key: "side", label: "Allegiance", type: "choice", choices: ["Crown", "Quay", "Neither"] });
  await api("PUT", `/api/v1/compendiums/${comp.id}/entity-fields`, { kind: "character", key: "born", label: "Born", type: "date" });
  const maire = await mk("character", "Máire Nolan", {
    sort_name: "Nolan, Máire", summary: "A ferrywoman with a long memory and a short temper.", color: "#2F5D50", aliases: ["The Ferrywoman", "Mam", "Mrs Nolan"],
    fields: { fare: "3", side: "Quay", age: "34", role: "Ferry keeper" },
    body: REFERENCE_DESCRIPTION,
  });
  const quay = await mk("place", "Salt Quay", { summary: "Where the road meets the water.", color: "#4A6FA5", aliases: ["The Quay"], body: "The last stone quay before the marsh." });
  const tadhg = await mk("character", "Tadhg Ó Néill", { summary: "Harbour master, and Máire's rival.", color: "#7A4B2A", fields: { side: "Crown", fare: "5" } });
  const lantern = await mk("item", "The brass lantern", { summary: "Found on the slip after the storm." });
  const guild = await mk("org", "The Lamplighters' Guild", { summary: "Keeps the lights from Salt Quay to the bar.", aliases: ["The Guild"] });
  await mk("concept", "The crossing", { summary: "What the ferry is for, and what it costs." });
  for (const [i, n] of ["Cathal Brennan", "Sorcha Quinn", "Donal Fox", "Orla Byrne", "Colm Tobin", "Eithne Moran"].entries()) await mk("character", n, { summary: `A name on the quay, ${i + 1}.` });
  for (const n of ["Glen Hollow", "Marsh Cross", "Ridge Lough"]) await mk("place", n, { summary: "A day's walk from the quay." });
  for (let i = 0; i < fillers; i++) await mk(i % 3 === 0 ? "place" : "character", `Filler ${String(i + 1).padStart(2, "0")} of the quay`, { summary: `One of the names that turn up on the quay in the second book, number ${i + 1}.` });
  // Máire has two relationships, as the plan's reference entity does; the others hang off other entries.
  await api("POST", `/api/v1/entities/${maire.id}/relations`, { to_entity_id: quay.id, kind: "custom", label: "keeps the ferry at", inverse_label: "ferry kept by" });
  await api("POST", `/api/v1/entities/${maire.id}/relations`, { to_entity_id: tadhg.id, kind: "rival", symmetric: true });
  await api("POST", `/api/v1/entities/${guild.id}/relations`, { to_entity_id: quay.id, kind: "custom", label: "lights", notes: "Since the flood." });
  await api("POST", `/api/v1/entities/${lantern.id}/relations`, { to_entity_id: quay.id, kind: "custom", label: "was found at", inverse_label: "where it was found" });
  return { comp, empty, maire, quay, tadhg, lantern, guild, mk };
}

// What the older screens add on top: two near-duplicates, a merged entity, a trashed one and a book with overrides.
export async function seedExtras(api, b) {
  const dupA = await b.mk("character", "Maire N.", { summary: "A second note on the ferrywoman.", fields: { fare: "4", side: "Quay" }, aliases: ["Mairín"] });
  const gone = await b.mk("character", "Old Tadhg", { summary: "Merged long ago." });
  const book = (await api("POST", "/api/v1/projects", { title: "The Salt Road", kind: "novel", author_name: "Máire Nolan", word_goal: 90000 })).item;
  await api("PUT", `/api/v1/projects/${book.id}/compendium`, { compendium_id: b.comp.id });
  await api("PUT", `/api/v1/projects/${book.id}/entity-overrides`, { entity_id: b.maire.id, name: "Mairín Nolan", fields: { fare: "4" }, notes: "In book two she is older, and the fare has gone up." });
  await api("PUT", `/api/v1/projects/${book.id}/entity-overrides`, { entity_id: dupA.id, summary: "This book's note on her." });
  await api("POST", `/api/v1/entities/${gone.id}/merge`, { into_id: b.tadhg.id });
  await api("POST", `/api/v1/entities/${b.lantern.id}/trash`, {});
  return { dupA, gone, book };
}
