// Input rules, commands, and the keymap. No Markdown parsing happens here: the
// rules only change the document, and Markdown is whatever the serializer says.

import {TextSelection} from "prosemirror-state"
import {InputRule, inputRules, undoInputRule, wrappingInputRule, textblockTypeInputRule} from "prosemirror-inputrules"
import {keymap} from "prosemirror-keymap"
import {baseKeymap, chainCommands, toggleMark, setBlockType, wrapIn} from "prosemirror-commands"
import {splitListItem, liftListItem, sinkListItem, wrapInList} from "prosemirror-schema-list"
import {undo, redo} from "prosemirror-history"
import {schema} from "./schema.js"

const {nodes, marks} = schema

// ---------------------------------------------------------------- input rules

// `**bold**`-style rules. `re` must capture (prefix, content) and end with the
// closing delimiter (the character being typed is not yet in the document).
function markRule(re, type) {
  return new InputRule(re, (state, match, start, end) => {
    const [, prefix, content] = match
    const from = start + prefix.length
    if (state.doc.rangeHasMark(from, end, marks.code)) return null
    const tr = state.tr
    tr.replaceWith(from, end, schema.text(content, [type.create()]))
    tr.removeStoredMark(type)
    return tr
  })
}

function codeRule() {
  return new InputRule(/(^|[^`\\])`([^`]+)`$/, (state, match, start, end) => {
    const from = start + match[1].length
    const tr = state.tr
    tr.replaceWith(from, end, schema.text(match[2], [marks.code.create()]))
    tr.removeStoredMark(marks.code)
    return tr
  })
}

function nodeRule(re, make) {
  return new InputRule(re, (state, match, start, end) => {
    const node = make(match)
    if (!node) return null
    const tr = state.tr
    tr.replaceWith(start, end, node)
    tr.removeStoredMark(marks.link)
    return tr
  })
}

// A scene break is a leaf block: one position wide.
const SCENE_SIZE = 1

export function sceneBreakNodes() {
  return [nodes.scene_break.create(), nodes.paragraph.create()]
}

// Replace the (empty-after-clearing) paragraph around `pos` with a scene break
// followed by an empty paragraph, and put the cursor in that paragraph.
function replaceParagraphWithSceneBreak(tr, pos) {
  const $pos = tr.doc.resolve(pos)
  const from = $pos.before(), to = $pos.after()
  tr.replaceWith(from, to, sceneBreakNodes())
  tr.setSelection(TextSelection.create(tr.doc, from + SCENE_SIZE + 1))
  return tr
}

function onlyTextOfParagraph($pos, re) {
  const parent = $pos.parent
  return parent.type === nodes.paragraph && re.test(parent.textContent)
}

const SCENE_TEXT = /^(?:-{3,}|_{3,}|\*{3,}|\*(?: \*){2,})$/
const FENCE_TEXT = /^```([A-Za-z0-9_+#.-]*)$/

export function buildInputRules({typography = false} = {}) {
  const rules = [
    textblockTypeInputRule(/^(#{1,6})\s$/, nodes.heading, m => ({level: m[1].length})),
    wrappingInputRule(/^\s*([-+*])\s$/, nodes.bullet_list),
    wrappingInputRule(/^(\d{1,9})\.\s$/, nodes.ordered_list, m => ({order: +m[1]}), (m, node) => node.childCount + node.attrs.order === +m[1]),
    wrappingInputRule(/^\s*>\s$/, nodes.blockquote),
    textblockTypeInputRule(/^```([A-Za-z0-9_+#.-]*)\s$/, nodes.code_block, m => ({params: m[1] || ""})),

    // `---` or `___` alone on a line: scene break at once. `***` and `* * *` wait
    // for Enter (see sceneBreakOnEnter) because `***bold italic***` starts with them.
    new InputRule(/^(?:---|___)$/, (state, match, start, end) => {
      if (!onlyTextOfParagraph(state.doc.resolve(start), /^-{2}$|^_{2}$/)) return null
      const tr = state.tr.delete(start, end)
      return replaceParagraphWithSceneBreak(tr, start)
    }),

    // `[ ] ` or `[x] ` at the start of a list item.
    new InputRule(/^\[([ xX]?)\]\s$/, (state, match, start, end) => {
      const $start = state.doc.resolve(start)
      if ($start.parent.type !== nodes.paragraph || $start.depth < 2) return null
      const item = $start.node($start.depth - 1)
      if (item.type !== nodes.list_item || $start.index($start.depth - 1) !== 0) return null
      const tr = state.tr.delete(start, end)
      return tr.setNodeMarkup($start.before($start.depth - 1), null, {checked: match[1].toLowerCase() === "x"})
    }),

    markRule(/(^|[^*\\])\*\*([^*\s](?:[^*]*[^*\s])?)\*\*$/, marks.strong),
    markRule(/(^|[^_\\\w])__([^_\s](?:[^_]*[^_\s])?)__$/, marks.strong),
    markRule(/(^|[^*\\])\*([^*\s](?:[^*]*[^*\s])?)\*$/, marks.em),
    markRule(/(^|[^_\\\w])_([^_\s](?:[^_]*[^_\s])?)_$/, marks.em),
    markRule(/(^|[^~\\])~~([^~\s](?:[^~]*[^~\s])?)~~$/, marks.strike),
    markRule(/(^|[^%\\])%%([^%\n]+)%%$/, marks.private_note),
    codeRule(),

    nodeRule(/\[\[([^\[\]|\n]+?)(?:\|([^\[\]\n]+?))?\]\]$/, m => nodes.wiki_link.create({target: m[1].trim(), alias: m[2] ? m[2].trim() : null})),
    nodeRule(/\[\^([^\s\[\]]+)\]$/, m => nodes.footnote_ref.create({label: m[1]}))
  ]

  if (typography) {
    // Opt-in. The curly characters are stored as typed; Markdown stays the truth.
    // Code is exempt (the plugin never runs inside code nodes; the code mark is checked here).
    const text = (re, fn) => new InputRule(re, (state, match, start, end) => {
      if (state.doc.rangeHasMark(Math.max(0, start - 1), end, marks.code)) return null
      // Inside a code span that is still being typed (an unclosed backtick).
      const $start = state.doc.resolve(start)
      const before = $start.parent.textBetween(0, $start.parentOffset, "", "￼")
      if (((before.match(/`/g) || []).length) % 2 === 1) return null
      return state.tr.insertText(fn(match), start, end)
    })
    rules.push(
      text(/(?:^|[\s([{—–-])"$/, m => m[0].slice(0, -1) + "“"),
      text(/"$/, () => "”"),
      text(/(?:^|[\s([{—–-])'$/, m => m[0].slice(0, -1) + "‘"),
      text(/'$/, () => "’"),
      text(/\.\.\.$/, () => "…"),
      text(/[^-]--$/, m => m[0][0] + "—")
    )
  }
  return inputRules({rules})
}

// ------------------------------------------------------------------- commands

export const toggleBold = toggleMark(marks.strong)
export const toggleItalic = toggleMark(marks.em)
export const toggleStrike = toggleMark(marks.strike)
export const toggleCode = toggleMark(marks.code)
export const togglePrivate = toggleMark(marks.private_note)

function sceneBreakOnEnter(state, dispatch) {
  const {$from, empty} = state.selection
  if (!empty || !onlyTextOfParagraph($from, SCENE_TEXT)) return false
  if (dispatch) {
    const tr = state.tr.delete($from.start(), $from.end())
    dispatch(replaceParagraphWithSceneBreak(tr, $from.start()).scrollIntoView())
  }
  return true
}

function fenceOnEnter(state, dispatch) {
  const {$from, empty} = state.selection
  if (!empty || $from.parent.type !== nodes.paragraph) return false
  const m = FENCE_TEXT.exec($from.parent.textContent)
  if (!m) return false
  if (dispatch) {
    const tr = state.tr.delete($from.start(), $from.end())
    tr.setBlockType($from.start(), $from.start(), nodes.code_block, {params: m[1] || ""})
    dispatch(tr.scrollIntoView())
  }
  return true
}

// Mod+Enter: a scene break after the current block (replacing an empty paragraph).
export function insertSceneBreak(state, dispatch) {
  const {$from} = state.selection
  if ($from.parent.type.spec.code) return false
  if (dispatch) {
    let tr = state.tr
    if ($from.parent.type === nodes.paragraph && $from.parent.content.size === 0 && $from.depth === 1) {
      tr = replaceParagraphWithSceneBreak(tr, $from.pos)
    } else {
      const after = $from.after($from.depth >= 1 ? 1 : 0)
      tr.insert(after, sceneBreakNodes())
      tr.setSelection(TextSelection.create(tr.doc, after + SCENE_SIZE + 1))
    }
    dispatch(tr.scrollIntoView())
  }
  return true
}

function hardBreak(state, dispatch) {
  const {$from} = state.selection
  if ($from.parent.type.spec.code || $from.parent.type === nodes.heading) return false
  if (dispatch) dispatch(state.tr.replaceSelectionWith(nodes.hard_break.create()).scrollIntoView())
  return true
}

// Backspace directly after a wiki link turns it back into editable source.
function unwrapWikiLink(state, dispatch) {
  const {$from, empty} = state.selection
  if (!empty) return false
  const before = $from.nodeBefore
  if (!before || before.type !== nodes.wiki_link) return false
  if (dispatch) {
    const {target, alias} = before.attrs
    const raw = "[[" + target + (alias ? "|" + alias : "") + "]"
    const from = $from.pos - before.nodeSize
    const tr = state.tr.replaceWith(from, $from.pos, schema.text(raw))
    tr.setSelection(TextSelection.create(tr.doc, from + raw.length))
    dispatch(tr.scrollIntoView())
  }
  return true
}

function inList(state) {
  const {$from} = state.selection
  for (let d = $from.depth; d > 0; d--) if ($from.node(d).type === nodes.list_item) return true
  return false
}
// A newline inside code stays in the code block, even inside a list item.
const notInCode = cmd => (state, dispatch, view) => state.selection.$from.parent.type.spec.code ? false : cmd(state, dispatch, view)
const guardList = cmd => (state, dispatch, view) => inList(state) ? cmd(state, dispatch, view) : false

// Paragraph or heading. Code is turned into prose line by line (a hard break in a
// paragraph, a space in a heading): a paragraph never holds a newline character,
// because Markdown would read it back as a space. A source block is not converted
// at all; it is the source of something the schema cannot show.
export function setHeading(level) {
  const plain = level ? setBlockType(nodes.heading, {level}) : setBlockType(nodes.paragraph)
  return (state, dispatch, view) => {
    const {$from, $to} = state.selection
    const parent = $from.parent
    const single = parent === $to.parent && parent.type === nodes.code_block
    if (!single) {
      let code = false
      state.doc.nodesBetween($from.pos, $to.pos, node => { if (node.type.spec.code) code = true })
      return code ? false : plain(state, dispatch, view)
    }
    if (dispatch) {
      const lines = parent.textContent.split("\n")
      const content = []
      lines.forEach((line, i) => {
        if (i) content.push(level ? schema.text(" ") : nodes.hard_break.create())
        if (line) content.push(schema.text(line))
      })
      const block = level ? nodes.heading.create({level}, content) : nodes.paragraph.create(null, content)
      dispatch(state.tr.replaceWith($from.before(), $from.after(), block).scrollIntoView())
    }
    return true
  }
}

// Toggle the enclosing list item between plain and a task item.
function setTaskState(state, dispatch, force) {
  const {$from} = state.selection
  for (let d = $from.depth; d > 0; d--) {
    const node = $from.node(d)
    if (node.type !== nodes.list_item) continue
    if (dispatch) {
      const checked = force !== undefined ? force : node.attrs.checked === null ? false : null
      dispatch(state.tr.setNodeMarkup($from.before(d), null, {checked}))
    }
    return true
  }
  return false
}

export const commandTable = {
  bold: toggleBold,
  italic: toggleItalic,
  strike: toggleStrike,
  code: toggleCode,
  private: togglePrivate,
  h1: setHeading(1), h2: setHeading(2), h3: setHeading(3), paragraph: setHeading(0),
  quote: wrapIn(nodes.blockquote),
  bullet: wrapInList(nodes.bullet_list),
  ordered: wrapInList(nodes.ordered_list),
  task(state, dispatch, view) {
    if (!inList(state)) {
      if (!wrapInList(nodes.bullet_list)(state, dispatch)) return false
      if (dispatch && view) setTaskState(view.state, view.dispatch, false)
      return true
    }
    return setTaskState(state, dispatch)
  },
  scene_break: insertSceneBreak,
  undo, redo
}

// --------------------------------------------------------------------- keymap

export function buildKeymaps({onSource, onFlush, onLink}) {
  const link = (state, dispatch) => {
    const {from, to, empty, $from} = state.selection
    const has = state.doc.rangeHasMark(from, to, marks.link)
    if (has || (empty && marks.link.isInSet($from.marks()))) return toggleMark(marks.link)(state, dispatch)
    if (onLink) { onLink({from, to, text: state.doc.textBetween(from, to, " ")}); return true }
    return false
  }
  return [
    keymap({
      "Mod-z": undo,
      "Shift-Mod-z": redo,
      "Mod-y": redo,
      "Mod-b": toggleBold,
      "Mod-i": toggleItalic,
      "Mod-Shift-x": toggleStrike,
      "Mod-k": link,
      "Mod-e": (state, dispatch, view) => { onSource(); return true },
      "Mod-s": () => { onFlush(); return true },
      "Mod-Enter": insertSceneBreak,
      "Mod-Alt-0": setHeading(0),
      "Mod-Alt-1": setHeading(1),
      "Mod-Alt-2": setHeading(2),
      "Mod-Alt-3": setHeading(3),
      "Shift-Enter": hardBreak,
      Enter: chainCommands(sceneBreakOnEnter, fenceOnEnter, notInCode(splitListItem(nodes.list_item))),
      Tab: guardList(sinkListItem(nodes.list_item)),
      "Shift-Tab": guardList(liftListItem(nodes.list_item)),
      Backspace: chainCommands(undoInputRule, unwrapWikiLink)
    }),
    keymap(baseKeymap)
  ]
}
