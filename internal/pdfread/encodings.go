package pdfread

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// The single-byte encodings of PDF's simple fonts, and the Adobe glyph names
// that Differences arrays and Type 1 fonts use.

var winAnsiHigh = []rune("€\u0000‚ƒ„…†‡ˆ‰Š‹Œ\u0000Ž\u0000\u0000‘’“”•–—˜™š›œ\u0000žŸ")

const macRomanHigh = "ÄÅÇÉÑÖÜáàâäãåçéèêëíìîïñóòôöõúùûü†°¢£§•¶ß®©™´¨≠ÆØ∞±≤≥¥µ∂∑∏π∫ªºΩæø¿¡¬√ƒ≈∆«»… ÀÃÕŒœ–—“”‘’÷◊ÿŸ⁄€‹›ﬁﬂ‡·‚„‰ÂÊÁËÈÍÎÏÌÓÔÒÚÛÙıˆ˜¯˘˙˚¸˝˛ˇ"

// standardHigh is Adobe StandardEncoding from 0xA1 (0 for unused).
var standardHigh = map[byte]rune{
	0xA1: '¡', 0xA2: '¢', 0xA3: '£', 0xA4: '⁄', 0xA5: '¥', 0xA6: 'ƒ', 0xA7: '§', 0xA8: '¤', 0xA9: '\'', 0xAA: '“', 0xAB: '«', 0xAC: '‹', 0xAD: '›', 0xAE: 'ﬁ', 0xAF: 'ﬂ',
	0xB1: '–', 0xB2: '†', 0xB3: '‡', 0xB4: '·', 0xB6: '¶', 0xB7: '•', 0xB8: '‚', 0xB9: '„', 0xBA: '”', 0xBB: '»', 0xBC: '…', 0xBD: '‰', 0xBF: '¿',
	0xC1: '`', 0xC2: '´', 0xC3: 'ˆ', 0xC4: '˜', 0xC5: '¯', 0xC6: '˘', 0xC7: '˙', 0xC8: '¨', 0xCA: '˚', 0xCB: '¸', 0xCD: '˝', 0xCE: '˛', 0xCF: 'ˇ', 0xD0: '—',
	0xE1: 'Æ', 0xE3: 'ª', 0xE8: 'Ł', 0xE9: 'Ø', 0xEA: 'Œ', 0xEB: 'º', 0xF1: 'æ', 0xF5: 'ı', 0xF8: 'ł', 0xF9: 'ø', 0xFA: 'œ', 0xFB: 'ß',
}

// baseEncoding returns the 256 runes of a named encoding (0 where undefined).
func baseEncoding(name string) *[256]rune {
	var t [256]rune
	for i := 0x20; i < 0x7f; i++ {
		t[i] = rune(i)
	}
	switch name {
	case "WinAnsiEncoding":
		t[0x7f] = '•'
		for i, r := range winAnsiHigh {
			t[0x80+i] = r
		}
		for i := 0xa0; i <= 0xff; i++ {
			t[i] = rune(i)
		}
		t[0xa0] = ' '
		t[0xad] = '-'
	case "MacRomanEncoding":
		i := 0x80
		for _, r := range macRomanHigh {
			t[i] = r
			i++
		}
	default: // StandardEncoding
		t[0x27] = '’'
		t[0x60] = '‘'
		for k, r := range standardHigh {
			t[k] = r
		}
	}
	return &t
}

// glyphNames maps Adobe glyph names to characters. It covers the Latin
// alphabets, the typographic punctuation, ligatures and the common symbols; a
// name it lacks is read as uniXXXX or uXXXX, or is unmapped.
var glyphNames = func() map[string]rune {
	m := map[string]rune{}
	for r := 'A'; r <= 'Z'; r++ {
		m[string(r)] = r
		m[strings.ToLower(string(r))] = r + 32
	}
	for i, n := range strings.Fields("zero one two three four five six seven eight nine") {
		m[n] = rune('0' + i)
	}
	for _, kv := range strings.Fields(glyphTable) {
		i := strings.IndexByte(kv, ':')
		if i < 0 {
			continue
		}
		v, err := strconv.ParseUint(kv[i+1:], 16, 32)
		if err == nil {
			m[kv[:i]] = rune(v)
		}
	}
	return m
}()

const glyphTable = `space:20 exclam:21 quotedbl:22 numbersign:23 dollar:24 percent:25 ampersand:26 quotesingle:27 parenleft:28 parenright:29 asterisk:2A plus:2B comma:2C hyphen:2D minus:2212 period:2E slash:2F colon:3A semicolon:3B less:3C equal:3D greater:3E question:3F at:40 bracketleft:5B backslash:5C bracketright:5D asciicircum:5E underscore:5F grave:60 braceleft:7B bar:7C braceright:7D asciitilde:7E
exclamdown:A1 cent:A2 sterling:A3 currency:A4 yen:A5 brokenbar:A6 section:A7 dieresis:A8 copyright:A9 ordfeminine:AA guillemotleft:AB logicalnot:AC sfthyphen:AD softhyphen:AD registered:AE macron:AF degree:B0 plusminus:B1 twosuperior:B2 threesuperior:B3 acute:B4 mu:B5 paragraph:B6 periodcentered:B7 cedilla:B8 onesuperior:B9 ordmasculine:BA guillemotright:BB onequarter:BC onehalf:BD threequarters:BE questiondown:BF
Agrave:C0 Aacute:C1 Acircumflex:C2 Atilde:C3 Adieresis:C4 Aring:C5 AE:C6 Ccedilla:C7 Egrave:C8 Eacute:C9 Ecircumflex:CA Edieresis:CB Igrave:CC Iacute:CD Icircumflex:CE Idieresis:CF Eth:D0 Ntilde:D1 Ograve:D2 Oacute:D3 Ocircumflex:D4 Otilde:D5 Odieresis:D6 multiply:D7 Oslash:D8 Ugrave:D9 Uacute:DA Ucircumflex:DB Udieresis:DC Yacute:DD Thorn:DE germandbls:DF
agrave:E0 aacute:E1 acircumflex:E2 atilde:E3 adieresis:E4 aring:E5 ae:E6 ccedilla:E7 egrave:E8 eacute:E9 ecircumflex:EA edieresis:EB igrave:EC iacute:ED icircumflex:EE idieresis:EF eth:F0 ntilde:F1 ograve:F2 oacute:F3 ocircumflex:F4 otilde:F5 odieresis:F6 divide:F7 oslash:F8 ugrave:F9 uacute:FA ucircumflex:FB udieresis:FC yacute:FD thorn:FE ydieresis:FF
nbspace:A0 nonbreakingspace:A0 Amacron:100 amacron:101 Abreve:102 abreve:103 Aogonek:104 aogonek:105 Cacute:106 cacute:107 Ccaron:10C ccaron:10D Dcaron:10E dcaron:10F Dcroat:110 dcroat:111 Emacron:112 emacron:113 Edotaccent:116 edotaccent:117 Eogonek:118 eogonek:119 Ecaron:11A ecaron:11B Gbreve:11E gbreve:11F Imacron:12A imacron:12B Iogonek:12E iogonek:12F Idotaccent:130 dotlessi:131 Lslash:141 lslash:142 Nacute:143 nacute:144 Ncaron:147 ncaron:148 Omacron:14C omacron:14D Ohungarumlaut:150 ohungarumlaut:151 OE:152 oe:153 Racute:154 racute:155 Rcaron:158 rcaron:159 Sacute:15A sacute:15B Scedilla:15E scedilla:15F Scaron:160 scaron:161 Tcaron:164 tcaron:165 Umacron:16A umacron:16B Uring:16E uring:16F Uhungarumlaut:170 uhungarumlaut:171 Uogonek:172 uogonek:173 Ydieresis:178 Zacute:179 zacute:17A Zdotaccent:17B zdotaccent:17C Zcaron:17D zcaron:17E florin:192
circumflex:2C6 caron:2C7 breve:2D8 dotaccent:2D9 ring:2DA ogonek:2DB tilde:2DC hungarumlaut:2DD
endash:2013 emdash:2014 quoteleft:2018 quoteright:2019 quotesinglbase:201A quotedblleft:201C quotedblright:201D quotedblbase:201E dagger:2020 daggerdbl:2021 bullet:2022 ellipsis:2026 perthousand:2030 guilsinglleft:2039 guilsinglright:203A fraction:2044 Euro:20AC trademark:2122 Omega:2126 partialdiff:2202 Delta:2206 product:220F summation:2211 radical:221A infinity:221E integral:222B approxequal:2248 notequal:2260 lessequal:2264 greaterequal:2265 lozenge:25CA
fi:FB01 fl:FB02 ff:FB00 ffi:FB03 ffl:FB04 st:FB06 longs:17F afii10017:410 uni00A0:A0 asciicircum:5E
Alpha:391 Beta:392 Gamma:393 Epsilon:395 Zeta:396 Eta:397 Theta:398 Iota:399 Kappa:39A Lambda:39B Mu:39C Nu:39D Xi:39E Omicron:39F Pi:3A0 Rho:3A1 Sigma:3A3 Tau:3A4 Upsilon:3A5 Phi:3A6 Chi:3A7 Psi:3A8 alpha:3B1 beta:3B2 gamma:3B3 delta:3B4 epsilon:3B5 zeta:3B6 eta:3B7 theta:3B8 iota:3B9 kappa:3BA lambda:3BB nu:3BD xi:3BE omicron:3BF pi:3C0 rho:3C1 sigma:3C3 tau:3C4 upsilon:3C5 phi:3C6 chi:3C7 psi:3C8 omega:3C9
section1:A7 firsttonechinese:0 ornament:2766 asterism:2042 fleuron:2767 hedera:2767`

// runeOfGlyph maps a glyph name to its character, or 0.
func runeOfGlyph(name string) rune {
	if r, ok := glyphNames[name]; ok {
		return r
	}
	if i := strings.IndexByte(name, '.'); i > 0 { // "a.sc", "one.oldstyle": the base character
		return runeOfGlyph(name[:i])
	}
	switch {
	case strings.HasPrefix(name, "uni") && len(name) >= 7:
		if v, err := strconv.ParseUint(name[3:7], 16, 32); err == nil && utf8.ValidRune(rune(v)) {
			return rune(v)
		}
	case strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7:
		if v, err := strconv.ParseUint(name[1:], 16, 32); err == nil && utf8.ValidRune(rune(v)) {
			return rune(v)
		}
	}
	return 0
}

var smallRunes = func() (t [256]string) {
	for i := range t {
		t[i] = string(rune(i))
	}
	return
}()

// runeString is string(r) without an allocation for the first 256 characters.
func runeString(r rune) string {
	if r >= 0 && r < 256 {
		return smallRunes[r]
	}
	return string(r)
}
