package userpool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type User struct {
	Account    string `json:"account"`
	Password   string `json:"password,omitempty"`
	CookieFile string `json:"cookie_file,omitempty"`
	IPRef      any    `json:"ip_ref,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type UserFile struct {
	Version int    `json:"version"`
	Users   []User `json:"users"`
}

type UserSummary struct {
	Index      int    `json:"index"`
	Account    string `json:"account"`
	Enabled    bool   `json:"enabled"`
	CookieFile string `json:"cookie_file,omitempty"`
	IPRef      any    `json:"ip_ref,omitempty"`
}

type Manager struct {
	path string
	mu   sync.RWMutex
	f    UserFile
}

func NewManager(dataDir string) (*Manager, error) {
	path := filepath.Join(dataDir, "users.json")
	m := &Manager{
		path: path,
		f: UserFile{
			Version: 1,
			Users:   nil,
		},
	}
	_ = m.load()
	return m, nil
}

func (m *Manager) FilePath() string {
	return m.path
}

func (m *Manager) ListSummaries() []UserSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]UserSummary, 0, len(m.f.Users))
	for i, u := range m.f.Users {
		out = append(out, UserSummary{
			Index:      i,
			Account:    u.Account,
			Enabled:    u.Enabled,
			CookieFile: u.CookieFile,
			IPRef:      u.IPRef,
		})
	}
	return out
}

func (m *Manager) EnabledAccounts() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	accounts := make([]string, 0, len(m.f.Users))
	for _, u := range m.f.Users {
		if u.Enabled {
			accounts = append(accounts, u.Account)
		}
	}
	return accounts
}

func (m *Manager) Resolve(account string, index *int) (User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if account != "" {
		for _, u := range m.f.Users {
			if u.Account == account {
				return u, nil
			}
		}
		return User{}, errors.New("user not found")
	}
	if index != nil {
		if *index < 0 || *index >= len(m.f.Users) {
			return User{}, errors.New("user index out of range")
		}
		return m.f.Users[*index], nil
	}
	if len(m.f.Users) == 0 {
		return User{Account: "default", Enabled: true}, nil
	}
	return m.f.Users[0], nil
}

func (m *Manager) IndexOfAccount(account string) (int, bool) {
	account = strings.TrimSpace(account)
	if account == "" {
		account = "default"
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	for i, u := range m.f.Users {
		if u.Account == account {
			return i, true
		}
	}
	return -1, false
}

func (m *Manager) UpsertIPRef(account string, ipRef any) (User, error) {
	if account == "" {
		account = "default"
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.f.Users {
		if m.f.Users[i].Account == account {
			m.f.Users[i].IPRef = ipRef
			err := m.saveLocked()
			return m.f.Users[i], err
		}
	}

	u := User{Account: account, IPRef: ipRef, Enabled: true}
	m.f.Users = append(m.f.Users, u)
	err := m.saveLocked()
	return u, err
}

func (m *Manager) EnsureSequentialIPRefs(maxIPs int) error {
	if maxIPs <= 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	changed := false
	for i := range m.f.Users {
		if m.f.Users[i].IPRef != nil {
			continue
		}
		if i >= maxIPs {
			continue
		}
		m.f.Users[i].IPRef = i
		changed = true
	}
	if !changed {
		return nil
	}
	return m.saveLocked()
}

func (m *Manager) UpsertCookie(account string, cookieFile string) (User, error) {
	if account == "" {
		account = "default"
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.f.Users {
		if m.f.Users[i].Account == account {
			m.f.Users[i].CookieFile = cookieFile
			m.f.Users[i].Enabled = true
			err := m.saveLocked()
			return m.f.Users[i], err
		}
	}

	u := User{Account: account, CookieFile: cookieFile, Enabled: true}
	m.f.Users = append(m.f.Users, u)
	err := m.saveLocked()
	return u, err
}

func (m *Manager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.path)
	if err != nil {
		return nil
	}
	var f UserFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	if f.Version == 0 {
		f.Version = 1
	}
	for i := range f.Users {
		f.Users[i].Account = strings.TrimSpace(f.Users[i].Account)
		if f.Users[i].Enabled == false {
			continue
		}
		f.Users[i].Enabled = true
	}
	m.f = f
	return nil
}

func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(m.path, data, 0644)
}

func SafeAccount(account string) string {
	account = strings.TrimSpace(account)
	if account == "" {
		return "default"
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	s := replacer.Replace(account)
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
