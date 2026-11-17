// A fake model server for the end-to-end scripts: an OpenAI-compatible endpoint on a loopback port that answers from a
// canned table and counts what it was asked. Test-time only; it is never started by the application and no script ever
// talks to a real provider.
//
//   const fake = await startFake();           // { base, requests(), count(), say(fn | text), close() }
//   fake.say((body) => "answer text");        // what the next requests answer with (a function of the request body)
//
// It answers by the task it finds in the request body, so a script can run a recipe without queuing text:
// rewrite-like tasks get a fixed rewrite, "alternatives" three candidates, the Lab's recipes marks of words found in
// the text, critique notes, summaries and extraction JSON, and chat a short reply. Every request is kept (the exact body),
// so a test can assert that a cancelled preview sent nothing.
import { createServer } from "node:http";

export async function startFake() {
  const seen = [];
  let answer = null, delay = 0;
  // The material of a block of one kind, and the task, as the request carries them.
  const block = (body, kind) => { const m = new RegExp("kind=" + kind + "[^>]*>>>\\n([\\s\\S]*?)\\n<<<end:").exec(body); return m ? m[1] : ""; };
  const taskOf = (body) => { try { const msgs = JSON.parse(body).messages || []; const last = msgs[msgs.length - 1]; const t = String(last.content).split("\nTASK\n"); return t.length > 1 ? t[t.length - 1] : String(last.content); } catch { return body; } };
  const canned = (body) => {
    let task = taskOf(body), body2 = (() => { try { return JSON.parse(body).messages.map((m) => m.content).join("\n"); } catch { return body; } })();
    const doc = block(body2, "document") || block(body2, "selection"), sel = block(body2, "selection") || doc;
    const filler = /\b(very|really|just|quite)\b/.exec(doc);
    if (/alternative wordings/i.test(task)) return "1. The harbour lay still.\n2. Quiet held the harbour.\n3. The harbour was calm.";
    if (/^Mark /i.test(task)) {
      if (!filler) return JSON.stringify({ marks: [] });
      const at = filler.index;
      return JSON.stringify({ marks: [{ quote: filler[0], before: doc.slice(Math.max(0, at - 9), at), after: doc.slice(at + filler[0].length, at + filler[0].length + 7), reason: "filler" }] });
    }
    if (/^Give notes/i.test(task)) return JSON.stringify({ notes: [{ quote: (doc.split(/(?<=[.!?])\s/)[0] || doc).trim(), category: "pacing", severity: "medium", note: "The opening is slow." }] });
    if (/^Audit/i.test(task)) return JSON.stringify({ findings: [] });
    if (/^Summari[sz]e/i.test(task)) return "A woman counts boats in a quiet harbour and finds one too many.";
    if (/^List the entities/i.test(task)) return JSON.stringify({ entities: [{ name: "Mara Voss", kind: "character", summary: "A harbour pilot." }] });
    if (/the selected text/i.test(task)) { const out = sel.replace(/\b(very|really|just|quite) /, ""); return out !== sel ? out : sel.replace(/^(\S+)/, "$1,") + " Revised."; }
    return "Here is an answer from the fake model.";
  };
  const srv = createServer((req, res) => {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      const raw = Buffer.concat(chunks).toString("utf8");
      if (req.url.endsWith("/models")) { res.setHeader("Content-Type", "application/json"); res.end(JSON.stringify({ data: [{ id: "fake-model" }] })); return; }
      if (req.url === "/__count") { res.end(String(seen.length)); return; }
      seen.push(raw);
      const text = typeof answer === "function" ? answer(raw) : typeof answer === "string" ? answer : canned(raw);
      const send = () => {
        let stream = false;
        try { stream = !!JSON.parse(raw).stream; } catch { /* not JSON */ }
        if (stream) {
          res.setHeader("Content-Type", "text/event-stream");
          const parts = text.match(/.{1,24}/gs) || [""];
          let i = 0;
          const tick = () => {
            if (i < parts.length) { res.write(`data: ${JSON.stringify({ choices: [{ delta: { content: parts[i++] } }] })}\n\n`); setTimeout(tick, 15); return; }
            res.write(`data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: "stop" }] })}\n\n`);
            res.write(`data: ${JSON.stringify({ choices: [], usage: { prompt_tokens: 100, completion_tokens: 40 } })}\n\n`);
            res.end("data: [DONE]\n\n");
          };
          tick();
          return;
        }
        res.setHeader("Content-Type", "application/json");
        res.end(JSON.stringify({ choices: [{ message: { role: "assistant", content: text }, finish_reason: "stop" }], usage: { prompt_tokens: 100, completion_tokens: 40 } }));
      };
      if (delay) setTimeout(send, delay); else send();
    });
  });
  await new Promise((r) => srv.listen(0, "127.0.0.1", r));
  const base = `http://127.0.0.1:${srv.address().port}`;
  return {
    base,
    requests: () => seen.slice(),
    count: () => seen.length,
    say: (a) => { answer = a; },
    slow: (ms) => { delay = ms; },
    reset: () => { seen.length = 0; answer = null; delay = 0; },
    close: () => new Promise((r) => { srv.closeAllConnections?.(); srv.close(r); }),
  };
}
