package model

import (
	"fmt"
	"slices"
	"strings"
)

// maxTagLen bounds a tag, which is shown as a label.
const maxTagLen = 40

// Tags checks and normalizes tags given by a user: each is lowercased and
// must then be lowercase letters, digits and hyphens, not starting or ending
// with a hyphen, like a slug. A comma also separates tags, so "--tag a,b"
// is two. The result is sorted and has no repeats.
func Tags(given []string) ([]string, error) {
	var tags []string
	for _, g := range given {
		for t := range strings.SplitSeq(g, ",") {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" {
				continue
			}
			if !validTag(t) {
				return nil, fmt.Errorf("tag %q must be lowercase letters, digits or hyphens, at most %d", t, maxTagLen)
			}
			tags = append(tags, t)
		}
	}
	slices.Sort(tags)
	return slices.Compact(tags), nil
}

func validTag(t string) bool {
	if len(t) > maxTagLen || t[0] == '-' || t[len(t)-1] == '-' {
		return false
	}
	for _, r := range t {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// HasTags reports whether the job has every one of tags.
func (j Job) HasTags(tags []string) bool {
	for _, t := range tags {
		if !slices.Contains(j.Tags, t) {
			return false
		}
	}
	return true
}

// WithTags returns the views of jobs that have every one of tags, all of
// them if tags is empty.
func WithTags(views []JobView, tags []string) []JobView {
	if len(tags) == 0 {
		return views
	}
	var out []JobView
	for _, v := range views {
		if v.HasTags(tags) {
			out = append(out, v)
		}
	}
	return out
}
