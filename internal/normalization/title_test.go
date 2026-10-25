package normalization

import "testing"

func TestTitleFoldsAccentsPunctuationAndWhitespace(t *testing.T) {
	got := Title("  Amélie: &   Co.  ")
	if got != "amelie co" {
		t.Fatalf("unexpected normalized title %q", got)
	}
	if Title("Cafe\u0301 Noir") != "cafe noir" {
		t.Fatalf("decomposed accent was not folded: %q", Title("Cafe\u0301 Noir"))
	}
	if Title("Duna 2") == Title("Duna") {
		t.Fatal("normalization discarded an identifying number")
	}
}
