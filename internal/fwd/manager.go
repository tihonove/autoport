// Package fwd ставит и снимает локальные ssh-пробросы.
//
// На каждый хост держится одно соединение (ControlMaster), а отдельные пробросы
// добавляются и снимаются на нём через `ssh -O forward` / `-O cancel`.
// Пробросы живут только пока жив процесс: состояние никуда не пишется, поэтому
// рассинхрона «в файле записано, а туннеля нет» не бывает.
package fwd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"
)

// RemoteLoopback — то, куда мы почти всегда ходим на той стороне. Именно
// loopback удалёнки недостижим иначе никак: tailnet-адрес до него не дотянется.
const RemoteLoopback = "localhost"

// Forward — один активный проброс.
type Forward struct {
	ID         string    `json:"id"`
	LocalPort  int       `json:"localPort"`
	Host       string    `json:"host"`
	RemoteHost string    `json:"remoteHost"`
	RemotePort int       `json:"remotePort"`
	Created    time.Time `json:"created"`
	Expires    time.Time `json:"expires,omitzero"` // нулевое = навсегда
	// Down — соединение под пробросом оборвалось, порт не слушается.
	// Запись держим, чтобы восстановить проброс с теми же настройками.
	Down bool `json:"down,omitempty"`
}

// Pinned — проброс без срока.
func (f Forward) Pinned() bool { return f.Expires.IsZero() }

// Left — сколько осталось жить.
func (f Forward) Left() time.Duration {
	if f.Pinned() {
		return 0
	}
	return time.Until(f.Expires)
}

// URL — адрес, по которому проброс открывается в браузере.
func (f Forward) URL() string {
	scheme := "http"
	if f.LocalPort == 443 || f.LocalPort == 8443 {
		scheme = "https"
	}
	return fmt.Sprintf("%s://localhost:%d/", scheme, f.LocalPort)
}

// Сколько помним ссылку на подтверждение сессии после того, как соединение
// сорвалось. Мастера уже нет, а ссылка пользователю ещё нужна.
const authNoteTTL = 5 * time.Minute

type authNote struct {
	url string
	at  time.Time
}

// Manager — реестр пробросов и соединений.
type Manager struct {
	mu       sync.Mutex
	forwards map[string]*Forward
	masters  map[string]*master
	pending  map[string]authNote
	closed   bool

	// dialing — по замку на хост, чтобы два одновременных подъёма не делили
	// один сокет: startMaster удаляет его перед стартом и снёс бы соседа.
	dialing map[string]*sync.Mutex
}

func New() *Manager {
	return &Manager{
		forwards: make(map[string]*Forward),
		masters:  make(map[string]*master),
		pending:  make(map[string]authNote),
		dialing:  make(map[string]*sync.Mutex),
	}
}

func newID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand не отказывает на практике; если отказал — падать честнее,
		// чем выдавать предсказуемые ID.
		panic(err)
	}
	return hex.EncodeToString(b)
}

// master возвращает живое соединение с хостом, поднимая его при необходимости.
// Вызывается без общего мьютекса: подъём может занять минуты, если Tailscale
// просит подтвердить сессию, а всё остальное в это время должно работать.
func (m *Manager) master(host string) (*master, error) {
	m.mu.Lock()
	dl, ok := m.dialing[host]
	if !ok {
		dl = &sync.Mutex{}
		m.dialing[host] = dl
	}
	m.mu.Unlock()
	dl.Lock()
	defer dl.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("сервис останавливается")
	}
	ms, ok := m.masters[host]
	m.mu.Unlock()

	if ok {
		if ms.alive() {
			return ms, nil
		}
		log.Printf("соединение с %s отвалилось, поднимаю заново", host)
		m.mu.Lock()
		delete(m.masters, host)
		m.mu.Unlock()
	}

	ms, err := startMaster(host)
	if err != nil {
		var authErr *AuthRequiredError
		if errors.As(err, &authErr) {
			m.mu.Lock()
			m.pending[host] = authNote{url: authErr.URL, at: time.Now()}
			m.mu.Unlock()
		}
		return nil, err
	}
	m.mu.Lock()
	m.masters[host] = ms
	delete(m.pending, host) // поднялись — старая ссылка больше не нужна
	m.mu.Unlock()
	go m.watch(ms)
	return ms, nil
}

// watch ждёт, пока ssh-мастер завершится. Если его погасили мы сами, в реестре
// его к этому моменту уже нет. Если нет — он умер сам (сеть, сон, перезагрузка
// той стороны). Пробросы с него не снимаем, а помечаем оборвавшимися: когда
// человек снова упрётся в порт, страница выбора поднимет его с теми же
// настройками, а не заставит вспоминать, куда он вёл.
func (m *Manager) watch(ms *master) {
	<-ms.done

	m.mu.Lock()
	if m.closed || m.masters[ms.host] != ms {
		m.mu.Unlock()
		return
	}
	delete(m.masters, ms.host)
	n := 0
	for _, f := range m.forwards {
		if f.Host == ms.host {
			f.Down = true
			n++
		}
	}
	m.mu.Unlock()

	_ = os.Remove(ms.sock)
	log.Printf("соединение с %s оборвалось, пробросов на нём: %d — восстановятся при следующем заходе", ms.host, n)
}

// Broken — оборвавшийся проброс на этом локальном порту, если он есть.
func (m *Manager) Broken(localPort int) (Forward, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.forwards {
		if f.LocalPort == localPort && f.Down {
			return *f, true
		}
	}
	return Forward{}, false
}

// Add ставит проброс. ttl == 0 — навсегда.
func (m *Manager) Add(host string, localPort, remotePort int, ttl time.Duration) (*Forward, error) {
	m.mu.Lock()
	for id, f := range m.forwards {
		if f.LocalPort != localPort {
			continue
		}
		if f.Down {
			// Соединение под ним умерло, порт не слушается — ставим заново,
			// с тем, что выбрали сейчас.
			log.Printf("порт %d висел на оборвавшемся %s, ставлю заново", localPort, f.Host)
			delete(m.forwards, id)
			continue
		}
		m.mu.Unlock()
		return nil, fmt.Errorf("порт %d уже проброшен на %s", localPort, f.Host)
	}
	m.mu.Unlock()

	ms, err := m.master(host)
	if err != nil {
		return nil, err
	}
	if err := ms.forward(localPort, RemoteLoopback, remotePort); err != nil {
		return nil, fmt.Errorf("порт %d: %w", localPort, err)
	}

	f := &Forward{
		ID:         newID(),
		LocalPort:  localPort,
		Host:       host,
		RemoteHost: RemoteLoopback,
		RemotePort: remotePort,
		Created:    time.Now(),
	}
	if ttl > 0 {
		f.Expires = f.Created.Add(ttl)
	}

	m.mu.Lock()
	m.forwards[f.ID] = f
	m.mu.Unlock()

	log.Printf("проброс %d → %s:%d (%s)", localPort, host, remotePort, ttlWord(ttl))
	return f, nil
}

func ttlWord(ttl time.Duration) string {
	if ttl <= 0 {
		return "навсегда"
	}
	return "на " + ttl.String()
}

// Remove снимает проброс.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	f, ok := m.forwards[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("проброс %s не найден", id)
	}
	delete(m.forwards, id)
	ms := m.masters[f.Host]
	m.mu.Unlock()

	if ms != nil {
		if err := ms.cancel(f.LocalPort, f.RemoteHost, f.RemotePort); err != nil {
			// Из реестра уже убрали: даже если ssh упрямится, показывать
			// несуществующий проброс в смотрелке хуже, чем потерять порт.
			log.Printf("порт %d снять не вышло: %v", f.LocalPort, err)
		}
	}
	log.Printf("снят проброс %d → %s:%d", f.LocalPort, f.Host, f.RemotePort)
	m.closeIdleMaster(f.Host)
	return nil
}

// closeIdleMaster гасит соединение, если на нём не осталось пробросов.
func (m *Manager) closeIdleMaster(host string) {
	m.mu.Lock()
	for _, f := range m.forwards {
		if f.Host == host {
			m.mu.Unlock()
			return
		}
	}
	ms, ok := m.masters[host]
	if ok {
		delete(m.masters, host)
	}
	m.mu.Unlock()

	if ok {
		log.Printf("на %s пробросов не осталось, закрываю соединение", host)
		ms.stop()
	}
}

// List отдаёт пробросы, отсортированные по локальному порту.
func (m *Manager) List() []Forward {
	m.mu.Lock()
	out := make([]Forward, 0, len(m.forwards))
	for _, f := range m.forwards {
		out = append(out, *f)
	}
	m.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].LocalPort < out[j].LocalPort })
	return out
}

// AuthURLs — хосты, которым нужно подтвердить сессию в браузере: и те, что
// ждут прямо сейчас, и те, у кого соединение из-за этого только что сорвалось.
func (m *Manager) AuthURLs() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string]string)
	for host, ms := range m.masters {
		if u := ms.AuthURL(); u != "" {
			out[host] = u
		}
	}
	for host, note := range m.pending {
		if time.Since(note.at) > authNoteTTL {
			delete(m.pending, host)
			continue
		}
		if _, ok := out[host]; !ok {
			out[host] = note.url
		}
	}
	return out
}

// Check проверяет, пускает ли хост. Нужен после того, как пользователь подтвердил
// сессию в браузере: сам сервис об этом узнать не может, а висящая плашка
// «нужна аутентификация» врёт до первой следующей попытки.
//
// Соединение, поднятое только ради проверки, сразу закрываем — иначе на хосте
// без пробросов останется висеть лишний ssh.
func (m *Manager) Check(host string) error {
	if _, err := m.master(host); err != nil {
		return err
	}
	m.closeIdleMaster(host)
	return nil
}

// Forget убирает запомненную ссылку на подтверждение сессии.
func (m *Manager) Forget(host string) {
	m.mu.Lock()
	delete(m.pending, host)
	m.mu.Unlock()
}

// Reap снимает протухшие пробросы. Вызывать по таймеру.
func (m *Manager) Reap() {
	now := time.Now()
	m.mu.Lock()
	var expired []string
	for id, f := range m.forwards {
		if !f.Pinned() && now.After(f.Expires) {
			expired = append(expired, id)
		}
	}
	m.mu.Unlock()

	for _, id := range expired {
		log.Printf("истёк срок проброса %s", id)
		_ = m.Remove(id)
	}
}

// Close гасит все соединения. Пробросы умирают вместе с сервисом — так задумано.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	masters := make([]*master, 0, len(m.masters))
	for host, ms := range m.masters {
		masters = append(masters, ms)
		delete(m.masters, host)
	}
	m.forwards = make(map[string]*Forward)
	m.mu.Unlock()

	for _, ms := range masters {
		ms.stop()
	}
}
