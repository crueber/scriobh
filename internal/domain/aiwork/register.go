package aiwork

import (
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

// ServiceName is the name the layer registers under in the op environment.
const ServiceName = "aiwork"

// Register adds the ops of the AI recipes' layer and installs the result hook on
// the AI core. It must run after docs.Register and aicore.Register. Every op is
// for a project editor (readers and reviewers cannot run AI, so they have no
// results), and everything kept is private to the person who made it.
func Register(r *ops.Registry) *Service {
	s := &Service{}
	if r.Env() != nil {
		s.leases = docs.LeasesOf(r.Env())
		if core, ok := aicore.ServiceOf(r.Env()); ok {
			s.ai = core
			core.SetResultHook(s.onResult)
		}
		r.Env().SetService(ServiceName, s)
	}
	ed := ops.RequireProject(ops.ProjectEditor)
	p := "/api/v1/projects/{id}/ai/"

	// Accepting. These write to the manuscript, and only from text the server holds.
	ops.Register(r, ops.Op{Name: "ai.accept.edit", Method: "POST", Path: p + "accept/edit", Auth: ed,
		Desc: "Accept one suggestion of a selection, chat or continue run: the server applies the text it holds for that run (never text you send) to the document, in one transaction, as one history entry with reason ai-accepted, with no word credit. The document revision you hold must be current (409 with the current revision otherwise); if the document changed in between, the suggestion applies only where its original text is still found exactly once with all the context it had (the words around it in its paragraph, and the paragraph's edges), and is a 409 otherwise. Accepting twice is harmless (applied false, already true). Refused while AI is off for the project."}, s.acceptEdit)
	ops.Register(r, ops.Op{Name: "ai.accept.alternative", Method: "POST", Path: p + "accept/alternative", Auth: ed,
		Desc: "Add candidates of an alternatives run to an alternative (an existing one, or a new one over the text they were made for), as wordings with source generated. The text of the document does not change unless you name a candidate in choose. One transaction; revision checked."}, s.acceptAlternative)
	ops.Register(r, ops.Op{Name: "ai.accept.reject", Method: "POST", Path: p + "accept/reject", Auth: ed,
		Desc: "Dismiss a suggestion or candidate of a run (or every open one). The held text is deleted once none is open."}, s.acceptReject)

	// Style guide and default chains.
	ops.Register(r, ops.Op{Name: "ai.style.get", Method: "GET", Path: p + "style", Auth: ed,
		Desc: "The project's style guide: the author's voice rules, sent only as the style guide chip of a request."}, s.styleGet)
	ops.Register(r, ops.Op{Name: "ai.style.set", Method: "PUT", Path: p + "style", Auth: ops.RequireProject(ops.ProjectOwner).ForSessions(),
		Desc: "Set the project's style guide (up to 8,000 characters), from a browser session only (it is configuration, like the policy). The project's owner only: it is sent with other members' requests too. It is ordinary project text, readable by editors. It reaches a provider only when a request includes the style guide chip, which the preview shows."}, s.styleSet)
	ops.Register(r, ops.Op{Name: "ai.defaults.get", Method: "GET", Path: p + "defaults", Auth: ed,
		Desc: "The default provider chains per model class (cheap, strong, fast, local): the project's, yours and the instance's, and the one each class resolves to."}, s.defaultsGet)
	ops.Register(r, ops.Op{Name: "ai.defaults.set", Method: "PUT", Path: p + "defaults", Auth: ed.ForSessions(),
		Desc: "From a browser session only (it is configuration, like the policy). Set a default provider chain for a model class at one level: project (its owner), user (yourself) or instance (an administrator). Resolution: the request's own provider, then the project, then you, then the instance. A local-only project's chains may name only local providers. An empty chain removes the default."}, s.defaultsSet)

	// Reviews: critique notes, continuity findings and proposed entities.
	ops.Register(r, ops.Op{Name: "ai.review.list", Method: "GET", Path: p + "reviews", Auth: ed,
		Desc: "Your reviews on the project: the notes of critiques, the findings of continuity audits and the entities extractions proposed, newest first, with how many notes are open. Reviews are private to the person who ran them."}, s.reviewList)
	ops.Register(r, ops.Op{Name: "ai.review.get", Method: "GET", Path: p + "review", Auth: ed,
		Desc: "One of your reviews with its notes. Each note's quote is looked up in the text as it is now: placed (with its place), unplaced (never found), or stale (the text changed so that it cannot be found exactly once)."}, s.reviewGet)
	ops.Register(r, ops.Op{Name: "ai.review.note.resolve", Method: "POST", Path: p + "review/note/resolve", Auth: ed,
		Desc: "Mark a note as dealt with. It changes nothing in the text."}, s.noteAct("accepted"))
	ops.Register(r, ops.Op{Name: "ai.review.note.dismiss", Method: "POST", Path: p + "review/note/dismiss", Auth: ed,
		Desc: "Dismiss a note: you disagree, or it does not apply. It changes nothing in the text."}, s.noteAct("dismissed"))
	ops.Register(r, ops.Op{Name: "ai.review.delete", Permanent: true, Method: "DELETE", Path: p + "review", Auth: ed,
		Desc: "Delete one of your reviews and its notes."}, s.reviewDelete)
	ops.Register(r, ops.Op{Name: "ai.extract.accept", Method: "POST", Path: p + "extract/accept", Auth: ed,
		Desc: "Create the entity an extraction proposed, in a Compendium the project draws on and you may edit. A name or alias an entity already has there is a 409 that names it (nothing is merged). Proposed fields the Compendium does not define are left out and listed."}, s.extractAccept)
	ops.Register(r, ops.Op{Name: "ai.extract.dismiss", Method: "POST", Path: p + "extract/dismiss", Auth: ed,
		Desc: "Dismiss a proposed entity."}, s.extractDismiss)

	// The Lab.
	ops.Register(r, ops.Op{Name: "ai.lab.analyse", Method: "POST", Path: p + "lab/analyse", Auth: ed,
		Desc: "Run the Lab's local analysers on a document: long sentences, hedge words (English) and small punctuation fixes (double spaces, repeated marks, space before punctuation, straight against curly quotes). No model, no policy, no cost; it works with AI off. It replaces your open local marks of those kinds on the document and returns the marks."}, s.labAnalyse)
	ops.Register(r, ops.Op{Name: "ai.lab.marks", Method: "GET", Path: p + "lab/marks", Auth: ed,
		Desc: "Your Lab marks on a document, local and model-made together, each looked up in the text as it is now (placed, or stale)."}, s.labMarks)
	ops.Register(r, ops.Op{Name: "ai.lab.accept", Method: "POST", Path: p + "lab/accept", Auth: ed,
		Desc: "Accept one mark. A mark with a fix applies exactly that edit (a trim deletes the marked words and one neighbouring space), as one history entry with reason ai-accepted and no word credit. A mark with no fix (an observation) is only marked resolved. A stale mark is a 409."}, s.labAccept)
	ops.Register(r, ops.Op{Name: "ai.lab.preview", Method: "POST", Path: p + "lab/preview", Auth: ed,
		Desc: "What accepting a chosen set of marks would do, worked out on the server from the text as it is now: which marks apply, which are skipped and why, and the combined change as a word diff. Writes nothing."}, s.labPreview)
	ops.Register(r, ops.Op{Name: "ai.lab.accept_all", Method: "POST", Path: p + "lab/accept_all", Auth: ed,
		Desc: "Accept a chosen set of marks of one document as one change: one transaction, one history entry (ai-accepted). Overlaps are resolved deterministically (the earlier start wins, then the longer); stale marks are skipped and listed; a stale document revision is a 409."}, s.labAcceptAll)
	ops.Register(r, ops.Op{Name: "ai.lab.dismiss", Method: "POST", Path: p + "lab/dismiss", Auth: ed,
		Desc: "Dismiss marks. Dismissed marks stay dismissed when the analysers run again."}, s.labDismiss)

	// Summaries.
	ops.Register(r, ops.Op{Name: "ai.summary.accept", Method: "POST", Path: p + "summary/accept", Auth: ed,
		Desc: "Write an accepted summary into the document's synopsis (short, long or both), through the document's own save: revision checked, no word credit. With job_id, the step of a rolling summary is marked done."}, s.summaryAccept)
	ops.Register(r, ops.Op{Name: "ai.summary.rolling.start", Method: "POST", Path: p + "summary/rolling", Auth: ed,
		Desc: "Plan a rolling summary of the manuscript, chapter by chapter in reading order, each using the previous chapter's summary as context. Returns the whole job's estimate and refuses up front when the project's policy or the monthly caps cannot cover it. The job is run one chapter at a time by the client (every send is previewed); it is resumable and cancellable."}, s.rollingStart)
	ops.Register(r, ops.Op{Name: "ai.summary.rolling.get", Method: "GET", Path: p + "summary/rolling", Auth: ed,
		Desc: "A rolling summary job and how far it has got. Without job_id, your open job on the project, to resume it."}, s.rollingGet)
	ops.Register(r, ops.Op{Name: "ai.summary.rolling.next", Method: "POST", Path: p + "summary/rolling/next", Auth: ed,
		Desc: "The next step of a rolling summary: the chapter and the request fields to preview and run (summary.chapter)."}, s.rollingNext)
	ops.Register(r, ops.Op{Name: "ai.summary.rolling.skip", Method: "POST", Path: p + "summary/rolling/skip", Auth: ed,
		Desc: "Skip the current step of a rolling summary."}, s.rollingSkip)
	ops.Register(r, ops.Op{Name: "ai.summary.rolling.cancel", Method: "POST", Path: p + "summary/rolling/cancel", Auth: ed,
		Desc: "Cancel a rolling summary. Summaries already accepted stay."}, s.rollingCancel)

	// Chat. A chat is held by the browser unless it is kept; a kept chat is private to its author.
	ops.Register(r, ops.Op{Name: "ai.chat.create", Method: "POST", Path: p + "chats", Status: 201, Auth: ed,
		Desc: "Keep a conversation: nothing is stored on the server unless you create a chat and add to it. A kept chat is yours alone, and deleted with ai.chat.delete."}, s.chatCreate)
	ops.Register(r, ops.Op{Name: "ai.chat.list", Method: "GET", Path: p + "chats", Auth: ed,
		Desc: "Your kept chats on the project. Works while AI is switched off: it sends nothing."}, s.chatList)
	ops.Register(r, ops.Op{Name: "ai.chat.get", Method: "GET", Path: p + "chat", Auth: ed,
		Desc: "One of your kept chats with its messages. Works while AI is switched off: it sends nothing."}, s.chatGet)
	ops.Register(r, ops.Op{Name: "ai.chat.delete", Permanent: true, Method: "DELETE", Path: p + "chat", Auth: ed,
		Desc: "Delete a kept chat and its messages. Works while AI is switched off: it sends nothing."}, s.chatDelete)
	ops.Register(r, ops.Op{Name: "ai.chat.append", Method: "POST", Path: p + "chat/append", Auth: ed,
		Desc: "Add a finished exchange to a kept chat (after a streamed chat run): the answer must be the one the run produced; its hash is checked against the run record."}, s.chatAppend)
	ops.Register(r, ops.Op{Name: "ai.chat.preview", Method: "POST", Path: p + "chat/preview", Auth: ed, SecretOutput: true,
		Desc: "Preview one chat message exactly as ai.context.preview does, with the earlier messages taken from a kept chat. Writes nothing and calls no provider."}, s.chatPreview)
	ops.Register(r, ops.Op{Name: "ai.chat.send", Method: "POST", Path: p + "chat/send", Auth: ed, NoDryRun: true, SecretOutput: true,
		Desc: "Send a previewed chat message (the preview's payload_hash and nonce are required, as for ai.run) and, for a kept chat, add the exchange to it. Not streamed: use the stream route with the chat recipe for a streamed answer and ai.chat.append afterwards. Nothing is inserted into the manuscript; a proposed wording for a selection is a suggestion for ai.accept.edit."}, s.chatSend)
	return s
}
