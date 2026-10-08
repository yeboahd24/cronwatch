package model

import (
	"slices"
	"testing"
)

func TestTags(t *testing.T) {
	got, err := Tags([]string{"Backup,db", " nightly ", "db", ""})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"backup", "db", "nightly"}; !slices.Equal(got, want) {
		t.Errorf("Tags = %q, want %q", got, want)
	}
	if got, err := Tags([]string{""}); err != nil || len(got) != 0 {
		t.Errorf(`Tags("") = %q, %v`, got, err)
	}
	for _, bad := range []string{"two words", "-db", "db-", "db_1", "ü", string(make([]byte, 41))} {
		if _, err := Tags([]string{bad}); err == nil {
			t.Errorf("Tags(%q) accepted", bad)
		}
	}
	j := Job{Tags: []string{"backup", "db"}}
	if !j.HasTags(nil) || !j.HasTags([]string{"db"}) || !j.HasTags([]string{"backup", "db"}) || j.HasTags([]string{"db", "web"}) {
		t.Error("HasTags")
	}
}
