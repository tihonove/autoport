// Package sshconf вытаскивает список хостов из ~/.ssh/config.
package sshconf

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Host — одна запись `Host a b c` из конфига.
type Host struct {
	Alias   string   // первый алиас, его и показываем
	Aliases []string // все, включая первый
}

// DefaultPath — стандартное расположение клиентского конфига.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// Load читает конфиг и возвращает записи, по которым реально можно ходить.
//
// Шаблоны (`Host *.devsy`, `Host *`) отбрасываются: это не машины, а правила,
// применяемые к другим записям. Если в строке есть и конкретные имена, и
// шаблоны, шаблоны убираем, а запись оставляем.
func Load(path string) ([]Host, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hosts []Host
	seen := make(map[string]bool)

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "host") {
			continue
		}

		var aliases []string
		for _, a := range fields[1:] {
			if strings.ContainsAny(a, "*?!") {
				continue
			}
			if seen[a] {
				continue
			}
			seen[a] = true
			aliases = append(aliases, a)
		}
		if len(aliases) == 0 {
			continue
		}
		hosts = append(hosts, Host{Alias: aliases[0], Aliases: aliases})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return hosts, nil
}

// Known говорит, известен ли алиас. Нужен для проверки того, что пришло из формы:
// имя хоста уезжает в аргументы ssh, так что принимать произвольную строку нельзя.
func Known(hosts []Host, alias string) bool {
	for _, h := range hosts {
		for _, a := range h.Aliases {
			if a == alias {
				return true
			}
		}
	}
	return false
}
