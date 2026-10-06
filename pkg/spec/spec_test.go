package spec

import "testing"

func TestEmbeddedContractsLoad(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if len(set.Services()) == 0 {
		t.Fatal("no contracts loaded")
	}
}
