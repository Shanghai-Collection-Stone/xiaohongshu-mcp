package ippool

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Pool struct {
	path string
	mu   sync.RWMutex
	ips  []string
}

func NewPool(dataDir string) (*Pool, error) {
	p := &Pool{path: filepath.Join(dataDir, "ip.txt")}
	_ = p.load()
	return p, nil
}

func (p *Pool) FilePath() string {
	return p.path
}

func (p *Pool) All() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, len(p.ips))
	copy(out, p.ips)
	return out
}

func (p *Pool) Resolve(ref any) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	switch v := ref.(type) {
	case nil:
		return "", false
	case float64:
		idx := int(v)
		if idx < 0 || idx >= len(p.ips) {
			return "", false
		}
		return p.ips[idx], true
	case int:
		if v < 0 || v >= len(p.ips) {
			return "", false
		}
		return p.ips[v], true
	case int64:
		idx := int(v)
		if idx < 0 || idx >= len(p.ips) {
			return "", false
		}
		return p.ips[idx], true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "", false
		}
		return s, true
	default:
		return "", false
	}
}

func (p *Pool) load() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	f, err := os.Open(p.path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var ips []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		ips = append(ips, line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	p.ips = ips
	return nil
}
