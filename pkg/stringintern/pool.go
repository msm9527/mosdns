package stringintern

import (
	"strings"
	"sync"
)

type Pool struct {
	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	value string
	refs  int
}

func New() *Pool {
	return &Pool{
		entries: make(map[string]*entry),
	}
}

func (p *Pool) Acquire(s string) string {
	if s == "" {
		return ""
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if e := p.entries[s]; e != nil {
		e.refs++
		return e.value
	}

	canonical := strings.Clone(s)
	p.entries[canonical] = &entry{
		value: canonical,
		refs:  1,
	}
	return canonical
}

func (p *Pool) Release(s string) {
	if s == "" {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	e := p.entries[s]
	if e == nil {
		return
	}

	e.refs--
	if e.refs <= 0 {
		delete(p.entries, e.value)
	}
}

func (p *Pool) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = make(map[string]*entry)
}

func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}
