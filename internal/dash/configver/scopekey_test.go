package configver

import "testing"

func TestParseScopeKey(t *testing.T) {
	cases := []struct {
		key     string
		ok      bool
		product string
		country string
	}{
		{"default", true, "", ""},
		{"country:DE", true, "", "DE"},
		{"product:prospector", true, "prospector", ""},
		{"product:prospector+country:DE", true, "prospector", "DE"},
		{"product:my-app+country:US", true, "my-app", "US"},
		// malformed
		{"", false, "", ""},
		{"country:de", false, "", ""},           // lowercase alpha2
		{"country:DEU", false, "", ""},          // 3 letters
		{"product:UPPER", false, "", ""},        // uppercase slug
		{"country:DE+product:x", false, "", ""}, // wrong order
		{"product:a+product:b", false, "", ""},  // repeated dim
		{"product:a+country:DE+country:FR", false, "", ""},
		{"tenant:acme", false, "", ""}, // tenant is row tenancy, not the key
		{"Default", false, "", ""},
	}
	for _, c := range cases {
		dims, ok := ParseScopeKey(c.key)
		if ok != c.ok {
			t.Errorf("ParseScopeKey(%q) ok=%v, want %v", c.key, ok, c.ok)
			continue
		}
		if ok && (dims.Product != c.product || dims.Country != c.country) {
			t.Errorf("ParseScopeKey(%q) = %+v, want product=%q country=%q", c.key, dims, c.product, c.country)
		}
		if ValidScopeKey(c.key) != c.ok {
			t.Errorf("ValidScopeKey(%q) = %v, want %v", c.key, ValidScopeKey(c.key), c.ok)
		}
	}
}

func TestHashPayload_StableAndSensitive(t *testing.T) {
	a := []byte(`{"b":2,"a":1}`)
	b := []byte(`{"a":1,"b":2}`) // same object, different key order
	ha, err := hashPayload(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := hashPayload(b)
	if string(ha) != string(hb) {
		t.Fatal("canonical hash must be key-order-independent")
	}
	hc, _ := hashPayload([]byte(`{"a":1,"b":3}`))
	if string(ha) == string(hc) {
		t.Fatal("hash must change when the payload changes")
	}
}
