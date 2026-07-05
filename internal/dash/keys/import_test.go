package keys

import (
	"errors"
	"testing"
)

func TestParseRows_CSVAndPaste(t *testing.T) {
	csv := "label,secret,region,weight\nhunter-08,hk_live_aa11,us,50\nhunter-09,hk_live_bb22,eu,\n"
	for _, src := range []string{"csv", "paste"} {
		rows, err := parseRows(src, []byte(csv))
		if err != nil {
			t.Fatalf("parseRows(%s): %v", src, err)
		}
		if len(rows) != 2 {
			t.Fatalf("%s rows = %d, want 2", src, len(rows))
		}
		if rows[0].Label != "hunter-08" || rows[0].Secret != "hk_live_aa11" || rows[0].Region != "us" {
			t.Fatalf("%s row0 = %+v", src, rows[0])
		}
		if rows[0].Weight == nil || *rows[0].Weight != 50 {
			t.Fatalf("%s row0 weight = %v, want 50", src, rows[0].Weight)
		}
		if rows[1].Weight != nil {
			t.Fatalf("%s row1 weight = %v, want nil (empty cell)", src, rows[1].Weight)
		}
	}
}

func TestParseRows_HeaderAliases(t *testing.T) {
	// key -> secret, env -> environment, name -> label, rpm -> rpm_limit.
	csv := "name,key,env,rpm\nk1,sekret,production,60\n"
	rows, err := parseRows("csv", []byte(csv))
	if err != nil {
		t.Fatalf("parseRows: %v", err)
	}
	if rows[0].Label != "k1" || rows[0].Secret != "sekret" || rows[0].Environment != "production" {
		t.Fatalf("aliased row = %+v", rows[0])
	}
	if rows[0].RPMLimit == nil || *rows[0].RPMLimit != 60 {
		t.Fatalf("rpm alias failed: %v", rows[0].RPMLimit)
	}
}

func TestParseRows_JSON(t *testing.T) {
	body := `[{"label":"j1","secret":"js_live_1","region":"us"},{"label":"j2","api_key":"js_live_2"}]`
	rows, err := parseRows("json", []byte(body))
	if err != nil {
		t.Fatalf("parseRows(json): %v", err)
	}
	if len(rows) != 2 || rows[0].Label != "j1" || rows[0].Secret != "js_live_1" || rows[1].Secret != "js_live_2" {
		t.Fatalf("json rows = %+v", rows)
	}
}

func TestParseRows_BadJSON(t *testing.T) {
	if _, err := parseRows("json", []byte(`{"not":"an array"}`)); !errors.Is(err, errBadJSON) {
		t.Fatalf("err = %v, want errBadJSON", err)
	}
}

func TestParseRows_EmptySecretParsedNotDropped(t *testing.T) {
	// A missing secret is a per-row runtime error (recorded during processing), not a parse error;
	// the row must still parse with Secret == "".
	rows, err := parseRows("csv", []byte("label,secret\nno-secret,\n"))
	if err != nil {
		t.Fatalf("parseRows: %v", err)
	}
	if len(rows) != 1 || rows[0].Secret != "" || rows[0].Label != "no-secret" {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestRowsFromRecords_RowCap(t *testing.T) {
	records := make([][]string, 0, maxImportRows+2)
	records = append(records, []string{"label", "secret"})
	for i := 0; i < maxImportRows+1; i++ {
		records = append(records, []string{"x", "y"})
	}
	if _, err := rowsFromRecords(records); !errors.Is(err, errTooManyRows) {
		t.Fatalf("err = %v, want errTooManyRows", err)
	}
}

func TestParseRows_UnsupportedFormat(t *testing.T) {
	if _, err := parseRows("yaml", []byte("x")); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}
