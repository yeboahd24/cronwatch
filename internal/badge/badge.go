// Package badge renders a job's status, or a group of jobs' status, as a
// small SVG badge for READMEs and wikis, or as a shields.io endpoint.
package badge

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"strings"

	"github.com/yeboahd24/cronwatch/internal/model"
)

// Colors, by the name shields.io gives each.
var colors = map[string]string{
	"brightgreen": "#4c1",
	"red":         "#e05d44",
	"yellow":      "#dfb317",
	"blue":        "#007ec6",
	"lightgrey":   "#9f9f9f",
}

// Badge is a label and a message on a colored background.
type Badge struct {
	Label   string
	Message string
	Color   string // a key of colors
}

// maxLabel bounds the label, which is a job's name or a tag by default.
const maxLabel = 60

// problem reports whether a job's status needs attention.
func problem(status string) bool {
	return model.Failing(status) || status == "missed" || status == "invalid_schedule"
}

// ForJob is the badge of one job: its name and status.
func ForJob(v model.JobView) Badge {
	b := Badge{Label: v.Name, Message: v.Status, Color: "lightgrey"}
	switch v.Status {
	case "success":
		b.Message, b.Color = "ok", "brightgreen"
	case "failed", "missed":
		b.Color = "red"
	case "timeout":
		b.Message, b.Color = "timed out", "red"
	case "invalid_schedule":
		b.Message, b.Color = "invalid schedule", "red"
	case "running":
		b.Color = "blue"
	case "skipped":
		b.Color = "yellow"
	case "never_run":
		b.Message = "no runs"
	}
	return b
}

// ForJobs is the badge of a group of jobs, such as a tag's: how many need
// attention, or that all are ok. Archived jobs are left out, and paused
// ones count as ok.
func ForJobs(label string, views []model.JobView) Badge {
	views = model.Unarchived(views)
	failing := 0
	for _, v := range views {
		if problem(v.Status) {
			failing++
		}
	}
	switch {
	case len(views) == 0:
		return Badge{Label: label, Message: "no jobs", Color: "lightgrey"}
	case failing > 0:
		return Badge{Label: label, Message: fmt.Sprintf("%d of %d failing", failing, len(views)), Color: "red"}
	case len(views) == 1:
		return Badge{Label: label, Message: "ok", Color: "brightgreen"}
	}
	return Badge{Label: label, Message: fmt.Sprintf("%d ok", len(views)), Color: "brightgreen"}
}

// WithLabel returns b with its label replaced, unless label is empty.
func (b Badge) WithLabel(label string) Badge {
	if label = strings.TrimSpace(label); label != "" {
		b.Label = label
	}
	return b
}

func (b Badge) label() string {
	r := []rune(b.Label)
	if len(r) > maxLabel {
		return string(r[:maxLabel-1]) + "…"
	}
	return b.Label
}

// SVG renders b in the flat style shields.io uses.
func (b Badge) SVG() []byte {
	label, message := b.label(), b.Message
	lw, mw := textWidth(label)+10, textWidth(message)+10
	w := lw + mw
	color := colors[b.Color]
	if color == "" {
		color = colors["lightgrey"]
	}
	title := html.EscapeString(label + ": " + message)
	// IDs unique to the badge, so badges inlined in one HTML page do not
	// use each other's clip path and gradient.
	h := fnv.New32a()
	_, _ = io.WriteString(h, label+"\x00"+message+"\x00"+color)
	id := fmt.Sprintf("cw%08x", h.Sum32())
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s">`, w, title)
	fmt.Fprintf(&sb, `<title>%s</title>`, title)
	fmt.Fprintf(&sb, `<linearGradient id="%s-s" x2="0" y2="100%%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>`, id)
	fmt.Fprintf(&sb, `<clipPath id="%s-r"><rect width="%d" height="20" rx="3" fill="#fff"/></clipPath>`, id, w)
	fmt.Fprintf(&sb, `<g clip-path="url(#%s-r)"><rect width="%d" height="20" fill="#555"/><rect x="%d" width="%d" height="20" fill="%s"/><rect width="%d" height="20" fill="url(#%s-s)"/></g>`,
		id, lw, lw, mw, color, w, id)
	sb.WriteString(`<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">`)
	for _, t := range []struct {
		text      string
		x, length int
	}{{label, lw / 2, lw - 10}, {message, lw + mw/2, mw - 10}} {
		text := html.EscapeString(t.text)
		// A shadow, then the text; textLength fits the text to the width
		// estimated for it, whatever font the viewer has.
		fmt.Fprintf(&sb, `<text x="%d" y="15" fill="#010101" fill-opacity=".3" textLength="%d">%s</text>`, t.x, t.length, text)
		fmt.Fprintf(&sb, `<text x="%d" y="14" textLength="%d">%s</text>`, t.x, t.length, text)
	}
	sb.WriteString(`</g></svg>`)
	return []byte(sb.String())
}

// ShieldsJSON renders b as a shields.io endpoint, for
// https://img.shields.io/endpoint?url=… to draw in its own styles.
func (b Badge) ShieldsJSON() []byte {
	out, _ := json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		Label         string `json:"label"`
		Message       string `json:"message"`
		Color         string `json:"color"`
	}{1, b.label(), b.Message, b.Color})
	return out
}

// textWidth estimates the width in pixels of s in 11px Verdana.
func textWidth(s string) int {
	w := 0.0
	for _, r := range s {
		switch {
		case strings.ContainsRune("il.,:;'|!", r):
			w += 3.5
		case strings.ContainsRune("frtjI()[] -", r):
			w += 4.5
		case r == 'm':
			w += 10.7
		case r == 'w' || r == 'M':
			w += 9.5
		case r == 'W':
			w += 11
		case r >= '0' && r <= '9':
			w += 7
		case r >= 'a' && r <= 'z':
			w += 6.5
		case r >= 'A' && r <= 'Z':
			w += 7.5
		default:
			w += 8
		}
	}
	return int(w + 0.5)
}
