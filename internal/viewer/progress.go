package viewer

import (
	"fmt"
	"sync"
)

// Progress collects download activity from other goroutines for display.
// The zero value is ready to use.
type Progress struct {
	mu     sync.Mutex
	log    []download
	active map[string]int // URL -> index in log
	files  int
	bytes  int64
}

type download struct {
	url         string
	done, total int64
	err         error
	finished    bool
}

const progressHistory = 500

// Report records the state of one download; it matches tact.Client.Progress.
func (p *Progress) Report(url string, done, total int64, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil {
		p.active = map[string]int{}
	}
	i, ok := p.active[url]
	if !ok {
		if len(p.log) >= progressHistory {
			drop := len(p.log) - progressHistory + 1
			p.log = append(p.log[:0], p.log[drop:]...)
			for u, j := range p.active {
				if j < drop {
					delete(p.active, u)
				} else {
					p.active[u] = j - drop
				}
			}
		}
		p.log = append(p.log, download{url: url, total: -1})
		i = len(p.log) - 1
		p.active[url] = i
	}
	d := &p.log[i]
	p.bytes += done - d.done
	d.done, d.err = done, err
	if total >= 0 {
		d.total = total
	}
	if err != nil || (total >= 0 && done == total) {
		d.finished = true
		p.files++
		delete(p.active, url)
	}
}

// Reset clears the history before a new lookup.
func (p *Progress) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log, p.active, p.files, p.bytes = nil, nil, 0, 0
}

// snapshot returns the last n log lines (oldest first), a summary line and
// the completed fraction of the downloads in flight (-1 when idle/unknown).
func (p *Progress) snapshot(n int) (lines []string, summary string, fraction float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.log[max(0, len(p.log)-n):] {
		state := ""
		switch {
		case d.err != nil:
			state = d.err.Error()
		case d.finished:
			state = size(d.done)
		case d.total > 0:
			state = fmt.Sprintf("%s / %s", size(d.done), size(d.total))
		default:
			state = size(d.done) + " ..."
		}
		lines = append(lines, d.url+"  "+state)
	}
	var done, total int64
	known := false
	for _, i := range p.active {
		d := p.log[i]
		if d.total > 0 {
			done, total, known = done+d.done, total+d.total, true
		}
	}
	fraction = -1
	if known {
		fraction = float64(done) / float64(total)
	}
	summary = fmt.Sprintf("CDN: %d files, %s downloaded, %d in progress", p.files, size(p.bytes), len(p.active))
	return lines, summary, fraction
}

func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
