package keys

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// buildXLSX assembles a minimal .xlsx (a ZIP of the two XML parts our reader consumes).
func buildXLSX(t *testing.T, sheetXML, sharedXML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if sharedXML != "" {
		write("xl/sharedStrings.xml", sharedXML)
	}
	write("xl/worksheets/sheet1.xml", sheetXML)
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestReadXLSX_SharedStringsAndInline(t *testing.T) {
	shared := `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>label</t></si>
  <si><t>secret</t></si>
  <si><t>region</t></si>
  <si><t>hunter-08</t></si>
  <si><t>hk_live_aa11</t></si>
  <si><r><t>u</t></r><r><t>s</t></r></si>
</sst>`
	// Row 1 header via shared strings; row 2 via shared strings; row 3 mixes an inlineStr cell.
	sheet := `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
  <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c></row>
  <row r="2"><c r="A2" t="s"><v>3</v></c><c r="B2" t="s"><v>4</v></c><c r="C2" t="s"><v>5</v></c></row>
  <row r="3"><c r="A3" t="inlineStr"><is><t>hunter-09</t></is></c><c r="B3" t="inlineStr"><is><t>hk_live_bb22</t></is></c><c r="C3" t="inlineStr"><is><t>eu</t></is></c></row>
</sheetData></worksheet>`

	rows, err := readXLSX(buildXLSX(t, sheet, shared))
	if err != nil {
		t.Fatalf("readXLSX: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: %v", len(rows), rows)
	}
	if got := rows[0]; got[0] != "label" || got[1] != "secret" || got[2] != "region" {
		t.Fatalf("header = %v", got)
	}
	if got := rows[1]; got[0] != "hunter-08" || got[1] != "hk_live_aa11" || got[2] != "us" {
		t.Fatalf("row1 = %v (rich-text shared string should join to 'us')", got)
	}
	if got := rows[2]; got[0] != "hunter-09" || got[1] != "hk_live_bb22" || got[2] != "eu" {
		t.Fatalf("row2 (inlineStr) = %v", got)
	}

	// Full pipeline: header + rows -> importRows.
	parsed, err := rowsFromRecords(rows)
	if err != nil {
		t.Fatalf("rowsFromRecords: %v", err)
	}
	if len(parsed) != 2 || parsed[0].Label != "hunter-08" || parsed[0].Secret != "hk_live_aa11" || parsed[1].Region != "eu" {
		t.Fatalf("parsed importRows = %+v", parsed)
	}
}

func TestReadXLSX_GappedColumnsPlacedByRef(t *testing.T) {
	// A row that skips column B: the empty position must be preserved so column C stays at index 2.
	sheet := `<worksheet><sheetData>
  <row r="1"><c r="A1" t="inlineStr"><is><t>a</t></is></c><c r="C1" t="inlineStr"><is><t>c</t></is></c></row>
</sheetData></worksheet>`
	rows, err := readXLSX(buildXLSX(t, sheet, ""))
	if err != nil {
		t.Fatalf("readXLSX: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 3 || rows[0][0] != "a" || rows[0][1] != "" || rows[0][2] != "c" {
		t.Fatalf("gapped row = %#v, want [a  c]", rows[0])
	}
}

func TestReadXLSX_ZipBombRejected(t *testing.T) {
	// A highly compressible entry: ~4 MiB of one byte deflates to a few KB, so the declared
	// uncompressed:compressed ratio blows past the guard and is rejected BEFORE decompression.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("A"), 4<<20)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = readXLSX(buf.Bytes())
	if !errors.Is(err, errZipBomb) {
		t.Fatalf("readXLSX(bomb) err = %v, want errZipBomb", err)
	}
}

func TestReadXLSX_NotAZip(t *testing.T) {
	if _, err := readXLSX([]byte("this is not a zip file at all")); !errors.Is(err, errBadXLSX) {
		t.Fatalf("err = %v, want errBadXLSX", err)
	}
}

func TestColIndex(t *testing.T) {
	cases := map[string]int{"A1": 0, "B7": 1, "Z1": 25, "AA1": 26, "AB2": 27, "BA10": 52}
	for ref, want := range cases {
		if got := colIndex(ref); got != want {
			t.Errorf("colIndex(%q) = %d, want %d", ref, got, want)
		}
	}
}

func TestParseSheet_RowCap(t *testing.T) {
	// The sheet-level row cap is tested against parseSheet directly: an all-identical 50k-row
	// workbook would trip the (earlier) zip-ratio guard, so this isolates the row-count guard.
	var b strings.Builder
	b.WriteString("<worksheet><sheetData>")
	for i := 0; i < maxImportRows+2; i++ {
		b.WriteString(`<row><c t="inlineStr"><is><t>x</t></is></c></row>`)
	}
	b.WriteString("</sheetData></worksheet>")
	if _, err := parseSheet([]byte(b.String()), nil); !errors.Is(err, errTooManyRows) {
		t.Fatalf("err = %v, want errTooManyRows", err)
	}
}
