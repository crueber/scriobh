# Builds plain-manuscript.docx the way a writer's word processor would: a title page, a dedication,
# eight chapters with Heading 1 titles, ordinary paragraphs, one scene break, no pictures, no licence text.
# Usage: python3 plain.py OUTPUT.docx   (needs python-docx; the file is committed as a sample)
import re
import sys
from docx import Document
from docx.enum.text import WD_ALIGN_PARAGRAPH, WD_BREAK
from docx.shared import Pt

CHAPTERS = [
    ("The Road North", [
        "The road ran north out of Carrow, past the last of the orchards and the mill with its wheel stopped for the winter. Nobody in the village had walked it further than the bridge in living memory, and Nell had always taken that as a sort of promise.",
        "She left before the bells. Her boots were still wet from the night before and her bag held bread, two apples, a clean shirt and the letter her mother had not wanted her to read. She read it at the crossroads, by the light of a lantern she had not bothered to light.",
        "It said only that the house at the end of the road had a door that opened from the inside, and that she should not knock.",
    ]),
    ("The Miller's Daughter", [
        "Tomas Ferrow had been the miller's daughter's only friend for eleven years, and in all that time he had never once asked her where she went on Sundays. He asked now, leaning on the gate with his hands in his pockets as if the question were nothing.",
        "She told him the truth, which was that she did not know. The letter had come from a woman whose name she had never heard, and the handwriting was her mother's, though her mother had been dead since the spring.",
        "Tomas said nothing for a long while. Then he went inside and came back with a second lantern and his father's coat, and said that if she was going he was going too.",
    ]),
    ("Snow on the Pass", [
        "By the third day the snow had covered the tracks, and with them the last argument for turning back. They climbed in single file, Tomas in front because he knew the cairns, Nell behind counting her steps to keep from thinking about the drop on the left.",
        "At noon they stopped in the lee of a boulder the size of a chapel and ate the last of the bread. Tomas took off his gloves to break the crust and his fingers were white to the second knuckle. She wrapped them in her own scarf without a word.",
        "The wind changed in the afternoon. It came off the far side of the pass smelling of woodsmoke, and that was how they knew there was a house.",
    ]),
    ("The House Without Knocking", [
        "The door opened before they reached it. A woman stood in the lamplight wearing an apron over a dark dress, and she looked at Nell for a long moment before she spoke.",
        "\"You read the letter,\" she said. It was not a question, and it was not quite an accusation either.",
        "Nell said that she had. The woman stepped aside to let them in, and as Nell passed her she saw that the handwriting on the cupboard door, the list of names pinned there, was her mother's own.",
    ]),
    ("What the Cupboard Held", [
        "The cupboard was deeper than it had any right to be. Inside were jars, a stack of ledgers tied with string, and a box of letters addressed in a hand Nell now recognised as her grandmother's.",
        "She read the first ledger by the fire while Tomas slept against the wall with his coat over his knees. The entries were plain: a name, a date, a sum of money, and beside each one a small mark like a lantern drawn with three strokes.",
        "The last entry was in her mother's writing and was dated the week before she died. It said only: the girl will come, and she will need the road.",
    ]),
    ("The Woman at the Window", [
        "In the morning the woman was at the window, watching the pass. Her name was Ada Brisk, she said, and she had kept the house for thirty years, since before Nell's mother was born.",
        "\"Your mother walked the same road,\" Ada said. \"She was sixteen and she was running. You are not running. That is the difference, and it is why I wrote.\"",
        "Nell asked what she was supposed to do with the ledgers. Ada looked at her for a long time and then said that they were not hers to keep, only hers to finish.",
    ]),
    ("The Lantern Keeper", [
        "They spent four days in the house. Tomas learned to split wood and Nell learned to read the ledgers with a pen in her hand, copying every entry into a book of her own so that nothing would be lost if the cupboard burned.",
        "On the fifth morning Ada gave her a lantern. It was an old brass thing, its glass worn thin, and when she lit it the flame was blue at the base and gold at the top.",
        "\"It goes down the valley,\" Ada said. \"Walk behind it, and do not stop until you see the mill.\"",
    ]),
    ("Down to the Mill", [
        "The valley was green in the way that valleys are only green after a hard winter, with the thaw running loud under the ice and the first lambs crying on the far slope.",
        "They reached the mill at dusk. The wheel had turned again, and the miller's wife came out with a lamp and a face full of questions, and Nell found that she had no idea where to begin.",
        "She began with the letter. Then she took the ledger from her bag and opened it at the first page, and the woman's face changed, and she said, quietly, that she had been waiting for someone to come and read it.",
    ]),
]

SCENE_BREAK_AFTER = (5, 1)  # chapter index 5 ("What the Cupboard Held"), after its second paragraph


def build(out):
    doc = Document()
    core = doc.core_properties
    core.title = "The Lantern Road"
    core.author = "Jo Hartley"
    core.last_modified_by = "Jo Hartley"

    # Title page: the Title style for the book, a Subtitle, the author's name.
    doc.add_paragraph("The Lantern Road", style="Title")
    doc.add_paragraph("A novel", style="Subtitle")
    p = doc.add_paragraph("Jo Hartley")
    p.alignment = WD_ALIGN_PARAGRAPH.CENTER
    doc.add_paragraph().add_run().add_break(WD_BREAK.PAGE)

    # Dedication, on its own page, ordinary paragraph set centred.
    d = doc.add_paragraph("For Ruth, who read every draft and never once said it was finished.")
    d.alignment = WD_ALIGN_PARAGRAPH.CENTER
    doc.add_paragraph().add_run().add_break(WD_BREAK.PAGE)

    for i, (title, paras) in enumerate(CHAPTERS):
        if i > 0:
            # A word processor's Insert > Page Break before each chapter heading.
            doc.add_paragraph().add_run().add_break(WD_BREAK.PAGE)
        doc.add_paragraph(title, style="Heading 1")
        for j, text in enumerate(paras):
            doc.add_paragraph(text)
            if (i, j) == SCENE_BREAK_AFTER:
                s = doc.add_paragraph("* * *")
                s.alignment = WD_ALIGN_PARAGRAPH.CENTER
    doc.save(out)
    strip_thumbnail(out)


def strip_thumbnail(path):
    # The python-docx template ships a preview picture that Word does not write by default: drop it, and its relationship.
    import zipfile, shutil, os
    tmp = path + ".tmp"
    with zipfile.ZipFile(path) as zin, zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            if item.filename == "docProps/thumbnail.jpeg":
                continue
            data = zin.read(item.filename)
            if item.filename == "_rels/.rels":
                data = re.sub(rb'<Relationship [^>]*thumbnail\.jpeg"/>', b"", data)
            zout.writestr(item, data)
    shutil.move(tmp, path)


if __name__ == "__main__":
    build(sys.argv[1])
