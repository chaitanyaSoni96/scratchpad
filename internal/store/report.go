package store

import (
	"fmt"
	"strings"
	"time"
)

// The notes report is the agent-facing view of annotations, and both surfaces
// that produce it — `scratchpad notes` and GET /notes/{path} — must render the
// same bytes, so it lives here with the domain rather than in either caller.
// It is markdown because it has one audience twice over: an agent reading it
// off curl or a pipe, and a human opening the same URL in a browser, where the
// web layer runs it through the same goldmark page every other .md gets.
//
// Anchored-vs-unanchored is deliberately absent: whether a selector or quote
// still resolves is a question only the rendered DOM can answer, and the store
// keeps no content history. The stored selector, fingerprint and quote are
// printed instead, which is what an agent needs to find the spot anyway.

// ReportOptions controls what FormatReport renders.
type ReportOptions struct {
	// Path is the store path the report was requested for — a document, an
	// artifact, a folder, or "" for the whole store. It titles the report.
	Path string
	// All includes resolved notes. The default (open only) is what an agent
	// wants: resolved notes are its own closed claims.
	All bool
}

// FormatReport renders notes as the markdown report. docs is normally
// WalkNotes output (sorted by document).
func FormatReport(docs []DocNotes, opts ReportOptions) string {
	var b strings.Builder
	subject := opts.Path
	if subject == "" {
		subject = "scratchpad"
	}
	fmt.Fprintf(&b, "# Notes — %s\n\n", subject)

	// Filter first: a document whose every note is resolved must not print an
	// empty heading when the report is open-only.
	type kept struct {
		doc   string
		notes []Annotation
	}
	var shown []kept
	open, resolved := 0, 0
	for _, d := range docs {
		var ns []Annotation
		for _, a := range d.Notes.Annotations {
			if a.Status == "open" {
				open++
			} else {
				resolved++
			}
			if opts.All || a.Status == "open" {
				ns = append(ns, a)
			}
		}
		if len(ns) > 0 {
			shown = append(shown, kept{doc: d.Doc, notes: ns})
		}
	}

	if len(shown) == 0 {
		if opts.All {
			b.WriteString("No notes.\n")
		} else if resolved > 0 {
			fmt.Fprintf(&b, "No open notes. %s resolved — re-run with --all (CLI) or ?status=all (HTTP) to see them.\n",
				plural(resolved, "note is", "notes are"))
		} else {
			b.WriteString("No open notes.\n")
		}
		return b.String()
	}

	fmt.Fprintf(&b, "%s across %s.",
		plural(open, "open note", "open notes"), plural(len(shown), "document", "documents"))
	if resolved > 0 && !opts.All {
		fmt.Fprintf(&b, " (%s resolved, not shown.)", plural(resolved, "note", "notes"))
	} else if resolved > 0 {
		fmt.Fprintf(&b, " %s resolved.", plural(resolved, "note", "notes"))
	}
	b.WriteString("\n")

	for _, k := range shown {
		fmt.Fprintf(&b, "\n## %s\n", k.doc)
		for _, a := range k.notes {
			writeNote(&b, a)
		}
	}

	b.WriteString("\n---\n\n")
	b.WriteString("Fix each open note, then close it with a one-line summary of the change:\n\n")
	b.WriteString("```bash\n")
	fmt.Fprintf(&b, "scratchpad notes resolve %s <id> -m \"what changed\"\n", shown[0].doc)
	fmt.Fprintf(&b, "scratchpad notes reply   %s <id> -m \"a question, when the note needs clarifying instead of a fix\"\n", shown[0].doc)
	b.WriteString("```\n")
	b.WriteString("\nOnly a human can create, edit, reopen or delete a note.\n")
	return b.String()
}

// writeNote renders one annotation: the human's text first (it is the point),
// then where it is anchored, when it happened, and the thread so far.
func writeNote(b *strings.Builder, a Annotation) {
	fmt.Fprintf(b, "\n### %s · %s\n\n", a.ID, a.Status)
	body := strings.TrimSpace(a.Body)
	if body == "" {
		body = "_(no text)_"
	}
	fmt.Fprintf(b, "%s\n\n", body)
	fmt.Fprintf(b, "- anchor: %s\n", anchorLine(a.Target))
	stamps := "created " + stamp(a.Created)
	if a.Updated != nil {
		stamps += " · edited " + stamp(*a.Updated)
	}
	fmt.Fprintf(b, "- %s\n", stamps)
	for _, r := range a.Replies {
		who := r.By
		if r.Action != "" {
			who += " " + pastTense(r.Action)
		}
		line := fmt.Sprintf("- %s · %s", who, stamp(r.Created))
		if body := strings.TrimSpace(r.Body); body != "" {
			line += " — " + oneLine(body)
		}
		fmt.Fprintf(b, "%s\n", line)
	}
}

// anchorLine describes where a note hangs, in the form the reader can act on:
// a selector to look up, or the quoted text with its neighbours.
func anchorLine(t Target) string {
	if t.Type == "text" && t.Quote != nil {
		s := fmt.Sprintf("text %q", t.Quote.Exact)
		if t.Quote.Prefix != "" || t.Quote.Suffix != "" {
			s += fmt.Sprintf(" (…%s[…]%s…)", oneLine(t.Quote.Prefix), oneLine(t.Quote.Suffix))
		}
		return s
	}
	s := "element `" + t.Selector + "`"
	if t.Fingerprint != "" {
		s += fmt.Sprintf(" — text was %q", t.Fingerprint)
	}
	return s
}

// stamp renders a time the way an agent compares them: UTC, RFC 3339.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

// oneLine folds a multi-line body onto one line so it cannot break the list
// item it is rendered inside.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func pastTense(action string) string {
	switch action {
	case "resolve":
		return "resolved"
	case "reopen":
		return "reopened"
	}
	return action
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
