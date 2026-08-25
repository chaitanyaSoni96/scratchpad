package store

import (
	"strings"
	"testing"
)

func TestFormatReportShellQuotesDocumentPath(t *testing.T) {
	docs := []DocNotes{{Doc: "project/my doc's.html", Notes: NotesFile{Annotations: []Annotation{{ID: "n1", Status: "open"}}}}}
	report := FormatReport(docs, ReportOptions{})
	want := "scratchpad notes resolve 'project/my doc'\\''s.html' <id>"
	if !strings.Contains(report, want) {
		t.Fatalf("report does not contain safely quoted command %q:\n%s", want, report)
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"":          "''",
		"plain":     "'plain'",
		"two words": "'two words'",
		"a'b":       "'a'\\''b'",
		"$(unsafe)": "'$(unsafe)'",
	}
	for input, want := range tests {
		if got := shellQuote(input); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}
