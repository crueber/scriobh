// Words in a document, as the status bar counts them: whitespace-delimited runs
// that contain a letter or digit. Private notes, code, and source blocks are not
// words; wiki links count as the text they show. Ghosted text (between a
// <scriobh-ghost> and a </scriobh-ghost> marker in one text block) is not
// counted: the first opener and the next closer pair up, an opener met while
// one is open and a closer with none open do nothing, and an opener left open at
// the end of the block ghosts nothing (internal/text/ghost.go is the same rule).
// Pure, no DOM.

const GHOST_OPEN = "<scriobh-ghost>"
const GHOST_CLOSE = "</scriobh-ghost>"

function ghostMask(kids) {
  const mask = new Array(kids.length).fill(false)
  let open = -1
  kids.forEach((child, i) => {
    if (child.type.name !== "source_inline") return
    if (child.attrs.raw === GHOST_OPEN) { if (open < 0) open = i }
    else if (child.attrs.raw === GHOST_CLOSE && open >= 0) {
      for (let j = open; j <= i; j++) mask[j] = true
      open = -1
    }
  })
  return mask
}

export function countWords(doc) {
  let count = 0
  doc.descendants(node => {
    if (node.type.spec.code) return false
    if (!node.isTextblock) return true
    let text = ""
    const kids = []
    node.forEach(child => kids.push(child))
    const ghosted = ghostMask(kids)
    kids.forEach((child, i) => {
      if (ghosted[i]) return
      if (child.isText) {
        if (!child.marks.some(m => m.type.name === "private_note")) text += child.text
      } else if (child.type.name === "wiki_link") text += child.attrs.alias || child.attrs.target
      else if (child.type.name === "hard_break") text += " "
    })
    for (const token of text.split(/\s+/)) if (token && /[\p{L}\p{N}]/u.test(token)) count++
    return false
  })
  return count
}
