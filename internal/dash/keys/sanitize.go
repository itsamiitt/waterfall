package keys

// CSV / spreadsheet formula-injection neutralization (doc 05 §7.3 handling discipline, doc 04 §4
// import caps). A cell whose first byte is one of = + - @ (or a leading TAB / CR / LF) is
// interpreted by Excel / Sheets / LibreOffice as a formula when the exported file is reopened;
// a value like `=cmd|'/c calc'!A1` becomes remote code execution on the reviewer's machine.
//
// The mitigation is symmetric and applied at BOTH boundaries: values are stored escaped on
// import (so a malicious import can never round-trip a live formula back out through any later
// export) AND re-escaped on export. Escaping prefixes a single quote, which spreadsheet apps
// render as a literal text-forced cell and strip on display — the value is preserved, the
// formula is neutered.

// dangerousLead is the set of first bytes that make a spreadsheet treat a cell as a formula.
// TAB (0x09), CR (0x0D) and LF (0x0A) are included because leading whitespace can smuggle a
// formula past a naive first-visible-char check.
func dangerousLead(b byte) bool {
	switch b {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

// sanitizeCell returns s neutralized for spreadsheet formula injection: unchanged if benign,
// otherwise prefixed with a single quote. Empty strings pass through untouched. It is
// idempotent for practical purposes (an already-quoted value does not start with a dangerous
// byte, so it is left alone).
func sanitizeCell(s string) string {
	if s == "" {
		return s
	}
	if dangerousLead(s[0]) {
		return "'" + s
	}
	return s
}

// isDangerousCell reports whether s would be treated as a formula by a spreadsheet app (used by
// tests and by the export path's assertions).
func isDangerousCell(s string) bool {
	return s != "" && dangerousLead(s[0])
}
