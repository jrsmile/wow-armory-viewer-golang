package viewer

import (
	"errors"
	"strings"
	"testing"
)

func TestProgress(t *testing.T) {
	var p Progress
	p.Report("https://cdn/a", 0, -1, nil)
	p.Report("https://cdn/a", 50, 100, nil)
	p.Report("https://cdn/b", 0, -1, nil)
	p.Report("https://cdn/b", 0, -1, errors.New("CDN HTTP 404"))
	lines, summary, fraction := p.snapshot(10)
	if fraction != 0.5 || len(lines) != 2 || !strings.Contains(lines[0], "50 B / 100 B") || !strings.Contains(lines[1], "  CDN HTTP 404") {
		t.Fatal(lines, fraction)
	}
	p.Report("https://cdn/a", 100, 100, nil)
	// Same URL again (e.g. another archive range) is a new log entry.
	p.Report("https://cdn/a", 0, -1, nil)
	lines, summary, fraction = p.snapshot(3)
	if fraction != -1 || len(lines) != 3 || !strings.HasSuffix(lines[0], "100 B") || summary != "CDN: 2 files, 100 B downloaded, 1 in progress" {
		t.Fatal(lines, summary, fraction)
	}
	for i := range progressHistory + 10 {
		p.Report(string(rune('a'+i%26))+strings.Repeat("x", i), 1, 1, nil)
	}
	if lines, _, _ := p.snapshot(1 << 20); len(lines) != progressHistory {
		t.Fatal(len(lines))
	}
	p.Report("https://cdn/a", 10, 10, nil) // trimmed entries leave the in-flight set
	if _, summary, _ := p.snapshot(0); !strings.HasSuffix(summary, "0 in progress") {
		t.Fatal(summary)
	}
}
