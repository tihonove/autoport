package fwd

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Сколько ждём готовности мастера в обычном случае.
const masterReadyTimeout = 20 * time.Second

// Если ssh попросил подтвердить сессию в браузере (Tailscale SSH), ждём дольше —
// человеку надо успеть сходить по ссылке.
const masterAuthTimeout = 3 * time.Minute

var authURLRe = regexp.MustCompile(`https://\S+`)

// master — одно ssh-соединение с хостом. Пробросы добавляются и снимаются
// на живом соединении через `ssh -O forward` / `-O cancel`, поэтому новая
// аутентификация на каждый проброс не нужна.
type master struct {
	host string
	sock string
	cmd  *exec.Cmd
	done chan struct{} // закрывается, когда процесс ssh завершился

	mu      sync.Mutex
	authURL string // непустой, пока ssh ждёт подтверждения сессии
}

func socketPath(host string) string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "autoport-"+strings.ReplaceAll(host, "/", "_")+".sock")
}

func startMaster(host string) (*master, error) {
	sock := socketPath(host)
	_ = os.Remove(sock)

	m := &master{host: host, sock: sock, done: make(chan struct{})}
	m.cmd = exec.Command("ssh",
		"-N", "-M", "-S", sock,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		// Явно и намеренно: с ControlPersist ssh уходит в фон, родитель выходит
		// с rc=0, и процесс становится неуправляемым. Уже наступали.
		"-o", "ControlPersist=no",
		host,
	)
	stderr, err := m.cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := m.cmd.Start(); err != nil {
		return nil, fmt.Errorf("не смог запустить ssh на %s: %w", host, err)
	}

	// Один читатель stderr и один Wait — иначе гонка за пайпом и ProcessState.
	stderrDone := make(chan struct{})
	go func() {
		m.drainStderr(stderr)
		close(stderrDone)
	}()
	go func() {
		<-stderrDone
		_ = m.cmd.Wait()
		close(m.done)
	}()

	deadline := time.Now().Add(masterReadyTimeout)
	extended := false
	for {
		if m.alive() {
			// Ссылку обязательно гасим: ssh печатает её до того, как сессию
			// подтвердили, и если её не сбросить, плашка «нужна аутентификация»
			// будет висеть всё время, пока живо соединение.
			m.clearAuthURL()
			log.Printf("ssh на %s готов", host)
			return m, nil
		}
		select {
		case <-m.done:
			return nil, failure(host, m, "соединение оборвалось")
		default:
		}
		if !extended && m.AuthURL() != "" {
			extended = true
			deadline = time.Now().Add(masterAuthTimeout)
			log.Printf("%s ждёт подтверждения сессии: %s", host, m.AuthURL())
		}
		if time.Now().After(deadline) {
			m.stop()
			return nil, failure(host, m, "соединение не поднялось")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// AuthRequiredError — ssh не пустил, пока сессия не подтверждена в браузере
// (Tailscale SSH в режиме check). Отдельный тип нужен, чтобы UI показал
// ссылку кликабельной, а не строкой посреди текста ошибки.
type AuthRequiredError struct {
	Host string
	URL  string
}

func (e *AuthRequiredError) Error() string {
	return e.Host + ": нужно подтвердить сессию в браузере — ссылка выше"
}

func failure(host string, m *master, what string) error {
	if u := m.AuthURL(); u != "" {
		return &AuthRequiredError{Host: host, URL: u}
	}
	return fmt.Errorf("%s: %s", host, what)
}

func (m *master) drainStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		log.Printf("ssh[%s]: %s", m.host, line)

		low := strings.ToLower(line)
		if strings.Contains(low, "authenticate") || strings.Contains(low, "login.tailscale.com") {
			if u := authURLRe.FindString(line); u != "" {
				m.mu.Lock()
				m.authURL = u
				m.mu.Unlock()
			}
		}
	}
}

// AuthURL — ссылка, по которой ssh просит подтвердить сессию, если просит.
func (m *master) AuthURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.authURL
}

func (m *master) clearAuthURL() {
	m.mu.Lock()
	m.authURL = ""
	m.mu.Unlock()
}

func (m *master) alive() bool {
	return exec.Command("ssh", "-O", "check", "-S", m.sock, m.host).Run() == nil
}

// spec — аргумент для -L в том виде, в каком его понимает ssh.
func spec(localPort int, remoteHost string, remotePort int) string {
	return fmt.Sprintf("127.0.0.1:%d:%s:%d", localPort, remoteHost, remotePort)
}

func (m *master) forward(localPort int, remoteHost string, remotePort int) error {
	return m.control("forward", localPort, remoteHost, remotePort)
}

func (m *master) cancel(localPort int, remoteHost string, remotePort int) error {
	return m.control("cancel", localPort, remoteHost, remotePort)
}

func (m *master) control(op string, localPort int, remoteHost string, remotePort int) error {
	cmd := exec.Command("ssh", "-O", op, "-L", spec(localPort, remoteHost, remotePort), "-S", m.sock, m.host)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// ssh пишет сюда внятное «forwarding request failed» — например, когда
		// локальный порт уже занят. Доносим это до пользователя как есть.
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", lastLine(msg))
	}
	return nil
}

func (m *master) stop() {
	_ = exec.Command("ssh", "-O", "exit", "-S", m.sock, m.host).Run()
	select {
	case <-m.done:
	case <-time.After(3 * time.Second):
		if m.cmd.Process != nil {
			_ = m.cmd.Process.Kill()
		}
		<-m.done
	}
	_ = os.Remove(m.sock)
}

// lastLine — из многострочного вывода ssh полезна обычно последняя строка.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
