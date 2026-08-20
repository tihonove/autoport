// Package prefs помнит мелкие предпочтения между запусками сервиса.
//
// Пробросы принципиально не переживают перезапуск — иначе бывает рассинхрон
// «в файле записано, а туннеля нет». С предпочтением такого риска нет: это не
// состояние системы, а строка, которая всего лишь подставляется в форму и
// всё равно сверяется с ~/.ssh/config в момент использования.
package prefs

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Last — последний выбранный хост.
type Last struct {
	mu   sync.RWMutex
	path string
	host string
}

// DefaultPath — файл в XDG-каталоге состояния.
func DefaultPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "autoport", "last-host")
}

// Load поднимает сохранённое значение. Файла нет или он битый — не беда,
// просто начнём с пустой памяти.
func Load(path string) *Last {
	l := &Last{path: path}
	if path == "" {
		return l
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("не смог прочитать %s: %v", path, err)
		}
		return l
	}
	l.host = sane(string(b))
	return l
}

// Host возвращает запомненный алиас. Вызывающий обязан сверить его
// с текущим ~/.ssh/config: файл мог пережить правку конфига.
func (l *Last) Host() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.host
}

// Set запоминает хост. Не записалось — работаем дальше, память просто
// не переживёт перезапуск.
func (l *Last) Set(host string) {
	host = sane(host)
	if host == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if host == l.host {
		return
	}
	l.host = host

	if l.path == "" {
		return
	}
	if err := write(l.path, host); err != nil {
		log.Printf("не смог запомнить хост в %s: %v", l.path, err)
	}
}

// write кладёт файл через временный и rename: иначе при падении на середине
// останется обрезанное значение.
func write(path, host string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".last-host-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // после удачного rename уже нечего удалять

	if _, err := f.WriteString(host + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sane отсекает мусор из файла: алиас ssh — это одно слово без пробелов.
// Настоящая проверка всё равно впереди, по ~/.ssh/config.
func sane(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 255 || strings.ContainsAny(s, " \t\r\n") {
		return ""
	}
	return s
}
