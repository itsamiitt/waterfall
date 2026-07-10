package ai

import (
	"errors"
	"testing"
)

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"bare", `{"a":1}`, `{"a":1}`, true},
		{"fenced", "```json\n{\"a\":1}\n```", `{"a":1}`, true},
		{"prose_wrapped", "Here is the result:\n{\"a\": {\"b\": 2}} — done", `{"a": {"b": 2}}`, true},
		{"brace_in_string", `{"s":"has a } brace and { inside"}`, `{"s":"has a } brace and { inside"}`, true},
		{"escaped_quote", `{"s":"a \" quote"}`, `{"s":"a \" quote"}`, true},
		{"none", `no object here`, ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractJSONObject([]byte(tc.in))
			if ok != tc.ok {
				t.Fatalf("ok=%v, want %v", ok, tc.ok)
			}
			if ok && string(got) != tc.want {
				t.Fatalf("got %q, want %q", string(got), tc.want)
			}
		})
	}
}

func TestValidateInto_Success(t *testing.T) {
	var o CompanyResearchOutput
	if err := ValidateInto([]byte("```json\n{\"summary\":\"acme makes widgets\",\"industry\":\"software\"}\n```"), &o); err != nil {
		t.Fatalf("ValidateInto: %v", err)
	}
	if o.Summary != "acme makes widgets" || o.Industry != "software" {
		t.Fatalf("parsed = %+v", o)
	}
}

func TestValidateInto_SchemaFailure(t *testing.T) {
	var o CompanyResearchOutput
	err := ValidateInto([]byte(`{"summary":"   "}`), &o) // empty-after-trim summary → Validate fails
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("err=%v, want wraps ErrSchema", err)
	}
}

func TestValidateInto_NoObject(t *testing.T) {
	var o CompanyResearchOutput
	if err := ValidateInto([]byte("the model refused to answer"), &o); !errors.Is(err, ErrSchema) {
		t.Fatalf("err=%v, want wraps ErrSchema", err)
	}
}

func TestCompetitorList_Validate(t *testing.T) {
	var o CompetitorListOutput
	if err := ValidateInto([]byte(`{"competitors":[{"name":"Acme","domain":"acme.com"},{"name":"Globex"}]}`), &o); err != nil {
		t.Fatalf("ValidateInto: %v", err)
	}
	if len(o.Competitors) != 2 {
		t.Fatalf("competitors = %d, want 2", len(o.Competitors))
	}
	// empty list must fail
	var empty CompetitorListOutput
	if err := ValidateInto([]byte(`{"competitors":[]}`), &empty); !errors.Is(err, ErrSchema) {
		t.Fatalf("empty list err=%v, want ErrSchema", err)
	}
}
