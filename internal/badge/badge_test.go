package badge

import (
	"encoding/json"
	"encoding/xml"
	"strconv"
	"strings"
	"testing"

	"github.com/yeboahd24/cronwatch/internal/model"
)

func view(name, status string) model.JobView {
	return model.JobView{Job: model.Job{Name: name}, Status: status}
}

func TestForJob(t *testing.T) {
	for status, want := range map[string]string{
		"success":          "ok brightgreen",
		"failed":           "failed red",
		"timeout":          "timed out red",
		"missed":           "missed red",
		"invalid_schedule": "invalid schedule red",
		"running":          "running blue",
		"skipped":          "skipped yellow",
		"never_run":        "no runs lightgrey",
		"paused":           "paused lightgrey",
		"archived":         "archived lightgrey",
	} {
		b := ForJob(view("Backup", status))
		if got := b.Message + " " + b.Color; got != want || b.Label != "Backup" {
			t.Errorf("%s: %q %q, want %q", status, b.Label, got, want)
		}
	}
}

func TestForJobs(t *testing.T) {
	archived := view("Old", "failed")
	now := archived.Job
	archived.ArchivedAt = &now.CreatedAt
	for _, c := range []struct {
		views []model.JobView
		want  string
	}{
		{nil, "no jobs lightgrey"},
		{[]model.JobView{view("A", "success")}, "ok brightgreen"},
		{[]model.JobView{view("A", "success"), view("B", "paused"), archived}, "2 ok brightgreen"},
		{[]model.JobView{view("A", "success"), view("B", "missed"), view("C", "timeout")}, "2 of 3 failing red"},
	} {
		b := ForJobs("backup", c.views)
		if got := b.Message + " " + b.Color; got != c.want || b.Label != "backup" {
			t.Errorf("%d jobs: %q, want %q", len(c.views), got, c.want)
		}
	}
}

func TestSVGIsValidAndEscaped(t *testing.T) {
	b := ForJob(view(`<script>alert("x")</script> & co`, "failed"))
	svg := string(b.SVG())
	if strings.Contains(svg, "<script>") {
		t.Fatalf("label not escaped:\n%s", svg)
	}
	if err := xml.Unmarshal([]byte(svg), new(struct{})); err != nil {
		t.Fatalf("not well-formed XML: %v\n%s", err, svg)
	}
	for _, want := range []string{`fill="#e05d44"`, `role="img"`, ">failed</text>"} {
		if !strings.Contains(svg, want) {
			t.Errorf("SVG lacks %q", want)
		}
	}
	// Longer text makes a wider badge.
	short, long := string(ForJob(view("A", "success")).SVG()), string(ForJob(view("A much longer job name", "success")).SVG())
	if widthOf(t, long) <= widthOf(t, short) {
		t.Errorf("width %d for a long name, %d for a short one", widthOf(t, long), widthOf(t, short))
	}
	if label := ForJob(view(strings.Repeat("x", 100), "success")).label(); len([]rune(label)) != maxLabel {
		t.Errorf("label of %d characters", len([]rune(label)))
	}
}

// widthOf returns the width of the badge in svg.
func widthOf(t *testing.T, svg string) int {
	t.Helper()
	_, rest, _ := strings.Cut(svg, `width="`)
	w, _, _ := strings.Cut(rest, `"`)
	n, err := strconv.Atoi(w)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestShieldsJSON(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(ForJob(view("Backup", "missed")).WithLabel("nightly").ShieldsJSON(), &got); err != nil {
		t.Fatal(err)
	}
	if got["schemaVersion"] != 1.0 || got["label"] != "nightly" || got["message"] != "missed" || got["color"] != "red" {
		t.Errorf("JSON = %v", got)
	}
}

// Badges inlined in one HTML page must not share clip path or gradient ids.
func TestSVGIDsDiffer(t *testing.T) {
	a, b := string(ForJob(view("A", "success")).SVG()), string(ForJob(view("B", "failed")).SVG())
	idOf := func(svg string) string {
		_, rest, _ := strings.Cut(svg, `<clipPath id="`)
		id, _, _ := strings.Cut(rest, `"`)
		return id
	}
	if idOf(a) == "" || idOf(a) == idOf(b) {
		t.Errorf("clip path ids %q and %q", idOf(a), idOf(b))
	}
	if !strings.Contains(a, `clip-path="url(#`+idOf(a)+`)"`) {
		t.Error("the clip path is not used by its own id")
	}
}
