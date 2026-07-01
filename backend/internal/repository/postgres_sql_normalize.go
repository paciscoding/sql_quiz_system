package repository

import "strings"

// smartQuoteReplacer maps the Unicode "smart"/typographic quote characters that
// word processors, PDFs, and rich-text editors silently substitute for the
// ASCII quotes Postgres actually understands. Postgres only accepts U+0027 (')
// as a string-literal delimiter and U+0022 (") as a quoted-identifier delimiter;
// anything else is parsed as part of an identifier, producing confusing errors
// like: column "‘111111100’" does not exist.
//
// This is a teaching tool where lecturers and students routinely paste SQL from
// documents, so curly quotes are practically always an accident, never intent.
var smartQuoteReplacer = strings.NewReplacer(
	"‘", "'", // ‘ left single quotation mark
	"’", "'", // ’ right single quotation mark
	"‚", "'", // ‚ single low-9 quotation mark
	"‛", "'", // ‛ single high-reversed-9 quotation mark
	"′", "'", // ′ prime (often pasted for apostrophes)
	"“", "\"", // “ left double quotation mark
	"”", "\"", // ” right double quotation mark
	"„", "\"", // „ double low-9 quotation mark
	"‟", "\"", // ‟ double high-reversed-9 quotation mark
	"″", "\"", // ″ double prime
)

// normalizeSmartQuotes rewrites typographic quote characters to their ASCII
// equivalents so that copy-pasted SQL executes instead of failing with a
// spurious "column ... does not exist" error. It is a no-op for SQL that
// already uses plain ASCII quotes.
func normalizeSmartQuotes(sql string) string {
	return smartQuoteReplacer.Replace(sql)
}
