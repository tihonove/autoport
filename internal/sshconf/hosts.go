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

// maxIncludeDepth повторяет ограничение ssh: глубже он включать отказывается.
const maxIncludeDepth = 16

// Load читает конфиг и возвращает записи, по которым реально можно ходить.
//
// Директива `Include` разворачивается так же, как у ssh: без этого хосты не
// находятся вовсе, когда конфиг держит инвентарь в подключаемом файле
// (`Include ~/.ssh/config.d/*.conf`) — распространённый приём, чтобы адреса
// машин не уезжали в общий или публичный dotfiles-репозиторий.
//
// Шаблоны (`Host *.devsy`, `Host *`) отбрасываются: это не машины, а правила,
// применяемые к другим записям. Если в строке есть и конкретные имена, и
// шаблоны, шаблоны убираем, а запись оставляем.
func Load(path string) ([]Host, error) {
	var hosts []Host
	seen := make(map[string]bool)
	visited := make(map[string]bool)
	if err := load(path, &hosts, seen, visited, 0); err != nil {
		return nil, err
	}
	return hosts, nil
}

// load разбирает один файл и спускается в подключённые им.
//
// seen дедуплицирует алиасы по всему обходу, visited — файлы, чтобы Include
// по кругу не зациклил чтение.
func load(path string, hosts *[]Host, seen, visited map[string]bool, depth int) error {
	if depth > maxIncludeDepth {
		return nil
	}
	// Один и тот же файл, подключённый двумя масками, читать второй раз незачем.
	// Сравниваем по разрешённому пути: сам ~/.ssh/config часто симлинк в dotfiles.
	key := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		key = real
	}
	if visited[key] {
		return nil
	}
	visited[key] = true

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		switch {
		case strings.EqualFold(fields[0], "include"):
			for _, pat := range fields[1:] {
				for _, inc := range expandInclude(pat, filepath.Dir(path)) {
					// Пропавший или нечитаемый включённый файл не повод бросать
					// остальной конфиг: ssh на такое тоже идёт дальше.
					_ = load(inc, hosts, seen, visited, depth+1)
				}
			}

		case strings.EqualFold(fields[0], "host"):
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
			*hosts = append(*hosts, Host{Alias: aliases[0], Aliases: aliases})
		}
	}
	return sc.Err()
}

// expandInclude разворачивает один аргумент Include в список путей.
//
// `~` — в домашний каталог; относительный путь — относительно каталога файла,
// в котором встретился Include (для обычного ~/.ssh/config это ~/.ssh, как и
// у ssh, но заодно работает для конфига, переданного флагом). Маска
// раскрывается через Glob, отсутствие совпадений — пустой список, не ошибка.
func expandInclude(pat, base string) []string {
	pat = strings.Trim(pat, `"`)
	if pat == "" {
		return nil
	}
	if pat == "~" || strings.HasPrefix(pat, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		pat = filepath.Join(home, strings.TrimPrefix(pat, "~"))
	}
	if !filepath.IsAbs(pat) {
		pat = filepath.Join(base, pat)
	}
	matches, err := filepath.Glob(pat)
	if err != nil {
		return nil // битая маска — не повод ронять разбор конфига
	}
	return matches
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
