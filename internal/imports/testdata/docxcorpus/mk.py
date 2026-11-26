# Builds small .docx samples by hand-zipping WordprocessingML, one per way writers mark chapters.
import zipfile, sys, os
from xml.sax.saxutils import escape

W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"'
CT = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>'''
RELS = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>'''
DOCRELS = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/></Relationships>'''
CORE = '''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Sample Novel</dc:title><dc:creator>A. Writer</dc:creator></cp:coreProperties>'''
def style(sid, name, based=None, outline=None, extra_p='', extra_r='', typ='paragraph'):
    b = f'<w:basedOn w:val="{based}"/>' if based else ''
    o = f'<w:outlineLvl w:val="{outline}"/>' if outline is not None else ''
    return f'<w:style w:type="{typ}" w:styleId="{sid}"><w:name w:val="{name}"/>{b}<w:qFormat/><w:pPr>{extra_p}{o}</w:pPr><w:rPr>{extra_r}</w:rPr></w:style>'
STYLES = f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles {W}><w:docDefaults><w:rPrDefault><w:rPr><w:lang w:val="en-US"/></w:rPr></w:rPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>
{style("Title","Title",based="Normal")}
{style("Subtitle","Subtitle",based="Normal")}
{style("Heading1","heading 1",based="Normal",outline=0)}
{style("Heading2","heading 2",based="Normal",outline=1)}
{style("Heading3","heading 3",based="Normal",outline=2)}
{style("ChapterTitle","Chapter Title",based="Normal")}
{style("ChapterHeading","Chapter Heading",based="Heading1")}
{style("PartTitle","Part Title",based="Normal")}
{style("SceneBreak","Scene Break",based="Normal")}
{style("H1PB","Chapter PB",based="Normal",extra_p='<w:pageBreakBefore/>')}
{style("Dedication","Dedication",based="Normal")}
{style("TOCHeading","TOC Heading",based="Normal")}
</w:styles>'''
NUMBERING = f'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering {W}><w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="multilevel"/><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="Chapter %1"/><w:lvlJc w:val="left"/></w:lvl></w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num></w:numbering>'''

def run(t, b=False, i=False):
    rp = ('<w:b/>' if b else '') + ('<w:i/>' if i else '')
    rp = f'<w:rPr>{rp}</w:rPr>' if rp else ''
    return f'<w:r>{rp}<w:t xml:space="preserve">{escape(t)}</w:t></w:r>'
def P(t='', style=None, b=False, center=False, pbb=False, pagebr=False, sect=False, num=False, sb=None, runs=None, outline=None):
    pp = ''
    if style: pp += f'<w:pStyle w:val="{style}"/>'
    if pbb: pp += '<w:pageBreakBefore/>'
    if num: pp += '<w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr>'
    if sb: pp += f'<w:spacing w:before="{sb}"/>'
    if center: pp += '<w:jc w:val="center"/>'
    if outline is not None: pp += f'<w:outlineLvl w:val="{outline}"/>'
    if sect: pp += '<w:sectPr><w:type w:val="nextPage"/><w:pgSz w:w="12240" w:h="15840"/></w:sectPr>'
    pp = f'<w:pPr>{pp}</w:pPr>' if pp else ''
    body = ''.join(runs) if runs else (run(t, b=b) if t else '')
    if pagebr: body = '<w:r><w:br w:type="page"/></w:r>' + body
    return f'<w:p>{pp}{body}</w:p>'
def doc(name, paras):
    d = f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document {W}><w:body>{"".join(paras)}<w:sectPr><w:pgSz w:w="12240" w:h="15840"/></w:sectPr></w:body></w:document>'
    z = zipfile.ZipFile(name, 'w', zipfile.ZIP_DEFLATED)
    z.writestr('[Content_Types].xml', CT); z.writestr('_rels/.rels', RELS)
    z.writestr('word/_rels/document.xml.rels', DOCRELS); z.writestr('word/document.xml', d)
    z.writestr('word/styles.xml', STYLES); z.writestr('word/numbering.xml', NUMBERING); z.writestr('docProps/core.xml', CORE)
    z.close()

LOREM = ["The road ran north out of the village and nobody she knew had ever followed it past the mill.",
         "He said nothing for a long while. The fire had burned down to a line of orange along the bars.",
         "By morning the snow had covered the tracks, and with them the last argument for turning back."]
def body(n=3): return [P(LOREM[i % 3]) for i in range(n)]
SB = P('* * *', center=True)

out = sys.argv[1]; os.makedirs(out, exist_ok=True)
def mk(n, paras): doc(os.path.join(out, n + '.docx'), paras)

# 01 Heading 1 chapters, Title style, three kinds of scene break
mk('01-h1-chapters', [P('Sample Novel', 'Title'), P('A. Writer', 'Subtitle')] +
   sum([[P(f'Chapter {k}', 'Heading1')] + body(2) + [P('***')] + body(1) + [P('#', center=True)] + body(1) + [P('* * *', center=True)] + body(1) for k in (1,2,3)], []))
# 02 Parts as Heading 1, chapters as Heading 2
mk('02-parts-h1-chapters-h2', [P('Part One', 'Heading1')] + sum([[P(f'Chapter {k}', 'Heading2')] + body(2) for k in (1,2)], []) +
   [P('Part Two', 'Heading1')] + sum([[P(f'Chapter {k}', 'Heading2')] + body(2) for k in (3,4)], []))
# 03 Plain bold centred "Chapter N", Normal style
mk('03-plain-bold-centred', [P('Sample Novel', b=True, center=True)] + sum([[P(f'Chapter {w}', b=True, center=True)] + body(3) for w in ('One','Two','Three')], []))
# 04 ALL CAPS with a title on the next line
mk('04-caps-two-line', sum([[P(f'CHAPTER {w}', center=True), P(t, center=True)] + body(3) for w,t in (('ONE','THE ROAD'),('TWO','THE MILL'),('THREE','SNOW'))], []))
# 05 roman numerals alone
mk('05-roman-numerals', sum([[P(r, center=True, b=True)] + body(3) for r in ('I','II','III','IV')], []))
# 06 page break inside a run, then plain "Chapter N"
mk('06-pagebreak-run', [P('Chapter 1')] + body(3) + sum([[P('', pagebr=True), P(f'Chapter {k}')] + body(3) for k in (2,3)], []))
mk('06b-pagebreak-run-no-label', body(3) + sum([[P('', pagebr=True)] + body(3) for k in (2,3)], []))
# 07 pageBreakBefore in the paragraph and in a style, no label text
mk('07-pagebreakbefore-para', [P('The Road', pbb=True, b=True)] + body(3) + [P('The Mill', pbb=True, b=True)] + body(3) + [P('Snow', pbb=True, b=True)] + body(3))
mk('07b-pagebreakbefore-style', [P('The Road', 'H1PB')] + body(3) + [P('The Mill', 'H1PB')] + body(3))
# 08 section breaks (next page) at the end of each chapter, no headings
mk('08-section-breaks', body(2) + [P(LOREM[0], sect=True)] + body(2) + [P(LOREM[1], sect=True)] + body(3))
mk('08b-section-breaks-with-chapter-label', [P('Chapter 1', b=True)] + body(2) + [P(LOREM[0], sect=True), P('Chapter 2', b=True)] + body(2) + [P(LOREM[1], sect=True), P('Chapter 3', b=True)] + body(2))
# 09 custom styles
mk('09-custom-style-no-outline', sum([[P(f'The Chapter {k}', 'ChapterTitle')] + body(2) for k in (1,2,3)], []))
mk('09b-custom-style-based-on-h1', sum([[P(f'The Chapter {k}', 'ChapterHeading')] + body(2) for k in (1,2,3)], []))
mk('09c-direct-outline-level', sum([[P(f'Some Title {k}', outline=0)] + body(2) for k in (1,2,3)], []))
# 10 automatic numbering ("Chapter 1" is not in the text)
mk('10-auto-numbered-heading', sum([[P(t, 'Heading1', num=True)] + body(2) for t in ('The Road','The Mill','Snow')], []))
# 11 front and back matter
mk('11-front-back-matter', [P('Sample Novel', 'Title'), P('A. Writer', 'Subtitle'), P('Copyright 2026 A. Writer. All rights reserved.', pbb=True), P('ISBN 000-0-00-000000-0'),
   P('For my mother', 'Dedication', pbb=True, center=True), P('Contents', 'TOCHeading', pbb=True), P('Chapter 1 ........ 5'), P('Chapter 2 ........ 19')] +
   sum([[P(f'Chapter {k}', 'Heading1')] + body(2) for k in (1,2)], []) + [P('Acknowledgments', 'Heading1')] + body(1) + [P('About the Author', 'Heading1')] + body(1))
# 12 no detectable breaks
mk('12-no-breaks', [P(LOREM[i % 3] + f' ({i})') for i in range(40)])
# 13 scene-break markers only (one chapter, three scenes)
mk('13-scene-markers', [P('Chapter 1', 'Heading1')] + body(1) + [P('***')] + body(1) + [P('#', center=True)] + body(1) + [P('* * *', center=True)] + body(1) + [P('—', center=True)] + body(1) + [P('')] + [P('')] + body(1))
# 14 mixed: some H1, some plain bold
mk('14-mixed', [P('Chapter 1', 'Heading1')] + body(2) + [P('Chapter 2', b=True, center=True)] + body(2) + [P('Chapter 3', 'Heading1')] + body(2) + [P('CHAPTER 4')] + body(2))
# 15 false positives
mk('15-false-positives', [P('Chapter 1', 'Heading1'), P('She read the line twice. Chapter 3 of the report was missing.'), P('Chapter 3 of the report was missing, he said.'), P('I', ), P('"I," she said.'), P('1.'), P('The End', center=True), P('Chapter 2', 'Heading1')] + body(2))
# 16 Heading 1 used for the book title and Heading 2 for chapters (common)
mk('16-h1-title-h2-chapters', [P('Sample Novel', 'Heading1')] + sum([[P(f'Chapter {k}', 'Heading2')] + body(2) for k in (1,2,3)], []))
# 17 Word-style Shunn manuscript: chapter heading dropped a third of the page, page break before
mk('17-shunn-like', sum([[P(f'Chapter {w}', pbb=True, sb=4000, center=True)] + body(3) for w in ('One','Two','Three')], []))
print('ok')
