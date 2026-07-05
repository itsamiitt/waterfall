package keys

// Hand-rolled .xlsx reader (stdlib only: archive/zip + encoding/xml). An .xlsx is a ZIP of XML
// parts; we read xl/sharedStrings.xml (the string pool) and the first worksheet
// (xl/worksheets/sheet1.xml), yielding a [][]string of cell text. No third-party spreadsheet
// library is used — the whole feature is ~200 lines against two stdlib packages.
//
// Safety caps (doc 04 §4 import contract): the file byte cap is enforced by the caller; here we
// add a ZIP decompression-ratio guard (a "zip bomb" declares a few KB compressed that inflate to
// gigabytes — a classic DoS) using the central-directory declared sizes so a bomb is rejected
// BEFORE any entry is decompressed, plus a per-entry decompressed-byte limit as defence in depth,
// plus the 50k-row cap. A tripped guard surfaces as a 422 validation_failed at the HTTP layer.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Import caps shared across the import pipeline.
const (
	maxImportBytes          = 25 << 20  // 25 MiB request cap (doc 04 §4)
	maxImportRows           = 50_000    // row cap (doc 04 §4)
	maxUncompressedPerEntry = 64 << 20  // per-entry decompressed byte ceiling
	maxTotalUncompressed    = 128 << 20 // whole-archive decompressed byte ceiling
	maxZipRatio             = 200       // uncompressed:compressed ratio ceiling (bomb guard)
	maxImportErrors         = 1000      // per-batch error array cap (doc 04 §4.4); overflow truncates
)

// xlsx sentinel errors (mapped to 422 validation_failed / 400 payload_too_large by http.go).
var (
	errZipBomb     = errors.New("keys: xlsx failed the decompression-ratio / size guard")
	errTooManyRows = errors.New("keys: import exceeds the 50k row cap")
	errBadXLSX     = errors.New("keys: file is not a readable .xlsx workbook")
	errBadCSV      = errors.New("keys: file is not readable CSV")
	errBadJSON     = errors.New("keys: import body is not a JSON array of objects")
)

// readXLSX parses the first worksheet of an .xlsx workbook into rows of cell text. It never
// decompresses an archive that trips the bomb guard.
func readXLSX(data []byte) ([][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errBadXLSX
	}
	if err := zipBombGuard(zr); err != nil {
		return nil, err
	}

	var sharedFile, sheetFile *zip.File
	for _, f := range zr.File {
		switch {
		case f.Name == "xl/sharedStrings.xml":
			sharedFile = f
		case f.Name == "xl/worksheets/sheet1.xml":
			sheetFile = f
		}
	}
	if sheetFile == nil {
		// Fall back to the lexicographically first worksheet part.
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
				if sheetFile == nil || f.Name < sheetFile.Name {
					sheetFile = f
				}
			}
		}
	}
	if sheetFile == nil {
		return nil, errBadXLSX
	}

	var shared []string
	if sharedFile != nil {
		b, err := readEntry(sharedFile)
		if err != nil {
			return nil, err
		}
		if shared, err = parseSharedStrings(b); err != nil {
			return nil, errBadXLSX
		}
	}

	sheetBytes, err := readEntry(sheetFile)
	if err != nil {
		return nil, err
	}
	rows, err := parseSheet(sheetBytes, shared)
	if err != nil {
		return nil, errBadXLSX
	}
	return rows, nil
}

// zipBombGuard rejects an archive whose declared (central-directory) sizes indicate a
// decompression bomb, without decompressing anything.
func zipBombGuard(zr *zip.Reader) error {
	var totalC, totalU uint64
	for _, f := range zr.File {
		if f.UncompressedSize64 > maxUncompressedPerEntry {
			return errZipBomb
		}
		totalC += f.CompressedSize64
		totalU += f.UncompressedSize64
	}
	if totalU > maxTotalUncompressed {
		return errZipBomb
	}
	if totalC > 0 && totalU/totalC > maxZipRatio {
		return errZipBomb
	}
	return nil
}

// readEntry decompresses one zip entry with a hard byte ceiling (defence in depth against a
// central directory that lies about UncompressedSize64).
func readEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, errBadXLSX
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxUncompressedPerEntry+1))
	if err != nil {
		return nil, errBadXLSX
	}
	if int64(len(b)) > maxUncompressedPerEntry {
		return nil, errZipBomb
	}
	return b, nil
}

// parseSharedStrings reads the <sst> string pool. Each <si> yields one string; rich-text runs
// (<si><r><t>..</t></r>..) are concatenated so a styled cell reads as its plain text.
func parseSharedStrings(b []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var out []string
	var cur strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI, inT = true, false
				cur.Reset()
			case "t":
				inT = true
			}
		case xml.CharData:
			if inSI && inT {
				cur.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out = append(out, cur.String())
				inSI = false
			}
		}
	}
	return out, nil
}

// parseSheet reads <sheetData> rows. Cells are placed by their column letter (r="B7" -> col 1)
// so blank cells produce empty positions; type "s" resolves through the shared-string pool,
// "inlineStr"/"str" carry literal text, and numeric/bool cells keep their <v> text.
func parseSheet(b []byte, shared []string) ([][]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var rows [][]string
	var row []string
	var raw strings.Builder
	var cellRef, cellType string
	col := -1
	inV, inIS, inT := false, false, false

	place := func(text string, at int) {
		if at < 0 {
			at = len(row)
		}
		for len(row) <= at {
			row = append(row, "")
		}
		row[at] = text
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = nil
				col = -1
			case "c":
				raw.Reset()
				cellRef, cellType = "", ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						cellRef = a.Value
					case "t":
						cellType = a.Value
					}
				}
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				inT = true
			}
		case xml.CharData:
			if inV || (inIS && inT) {
				raw.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "is":
				inIS = false
			case "c":
				text := raw.String()
				if cellType == "s" {
					if idx, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && idx >= 0 && idx < len(shared) {
						text = shared[idx]
					} else {
						text = ""
					}
				}
				at := col + 1
				if cellRef != "" {
					at = colIndex(cellRef)
				}
				place(text, at)
				col = at
			case "row":
				rows = append(rows, row)
				if len(rows) > maxImportRows {
					return nil, errTooManyRows
				}
			}
		}
	}
	return rows, nil
}

// colIndex converts the leading letters of a cell reference (e.g. "AB12") to a 0-based column
// index. Non-letter suffixes (the row number) are ignored.
func colIndex(ref string) int {
	n := 0
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= 'A' && c <= 'Z':
			n = n*26 + int(c-'A'+1)
		case c >= 'a' && c <= 'z':
			n = n*26 + int(c-'a'+1)
		default:
			return n - 1
		}
	}
	return n - 1
}
