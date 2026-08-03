package strip

import (
	"strings"

	"golang.org/x/text/language"
)

// iso6392ToBCP47Base crosswalks ISO 639-2 language codes (as found in
// embedded subtitle stream tags, e.g. "eng") to the base BCP-47 language
// subtag Sublime uses everywhere else. Both the bibliographic and
// terminology variant are listed for the languages where they differ.
//
// This is not exhaustive: it covers languages plausible as subtitle tracks
// in practice. An ISO 639-2 code missing from this table resolves as
// unmatched (see MatchesISO6392) rather than guessed at — consistent with
// Strip's fail-closed stance on ambiguous language tags.
var iso6392ToBCP47Base = map[string]string{
	"eng": "en",
	"spa": "es",
	"fre": "fr", "fra": "fr",
	"ger": "de", "deu": "de",
	"por": "pt",
	"ita": "it",
	"dut": "nl", "nld": "nl",
	"chi": "zh", "zho": "zh",
	"jpn": "ja",
	"kor": "ko",
	"rus": "ru",
	"ara": "ar",
	"heb": "he",
	"hin": "hi",
	"tha": "th",
	"vie": "vi",
	"ind": "id",
	"may": "ms", "msa": "ms",
	"tur": "tr",
	"pol": "pl",
	"cze": "cs", "ces": "cs",
	"slo": "sk", "slk": "sk",
	"slv": "sl",
	"hun": "hu",
	"rum": "ro", "ron": "ro",
	"bul": "bg",
	"hrv": "hr",
	"srp": "sr",
	"ukr": "uk",
	"gre": "el", "ell": "el",
	"swe": "sv",
	"nor": "no",
	"dan": "da",
	"fin": "fi",
	"est": "et",
	"lav": "lv",
	"lit": "lt",
	"cat": "ca",
	"baq": "eu", "eus": "eu",
	"glg": "gl",
	"wel": "cy", "cym": "cy",
	"per": "fa", "fas": "fa",
	"alb": "sq", "sqi": "sq",
	"arm": "hy", "hye": "hy",
	"geo": "ka", "kat": "ka",
	"mac": "mk", "mkd": "mk",
	"bur": "my", "mya": "my",
	"mao": "mi", "mri": "mi",
	"ice": "is", "isl": "is",
	"fil": "fil",
	"amh": "am",
	"aze": "az",
	"ben": "bn",
	"bos": "bs",
	"epo": "eo",
	"kaz": "kk",
	"khm": "km",
	"lao": "lo",
	"mal": "ml",
	"mon": "mn",
	"nep": "ne",
	"pan": "pa",
	"sin": "si",
	"som": "so",
	"srd": "sc",
	"swa": "sw",
	"tam": "ta",
	"tel": "te",
	"urd": "ur",
	"uzb": "uz",
}

// MatchesISO6392 reports whether an embedded subtitle stream's ISO 639-2
// language tag (e.g. "eng") refers to the same language as target, a
// BCP-47 tag. ISO 639-2 carries no region, so the comparison is against
// target's base language only.
//
// An empty, "und" (undefined), or unrecognized code returns false — an
// absent or unresolvable tag is left untouched rather than guessed at, per
// CONTEXT.md's fail-closed Strip stance.
func MatchesISO6392(code string, target language.Tag) bool {
	base, ok := iso6392ToBCP47Base[strings.ToLower(code)]
	if !ok {
		return false
	}

	targetBase, confidence := target.Base()
	if confidence == language.No {
		return false
	}

	return base == targetBase.String()
}
