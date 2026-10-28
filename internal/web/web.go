// Package web — веб-морда сервиса: смотрелка и страница выбора.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tihonove/autoport/internal/fwd"
	"github.com/tihonove/autoport/internal/prefs"
	"github.com/tihonove/autoport/internal/sshconf"
)

type Server struct {
	mgr        *fwd.Manager
	sshConfig  string
	last       *prefs.Last
	csrf       string
	maxPortTTL time.Duration
}

func New(mgr *fwd.Manager, sshConfig string, last *prefs.Last) *Server {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return &Server{mgr: mgr, sshConfig: sshConfig, last: last, csrf: hex.EncodeToString(b)}
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /fix", s.handleFix)
	mux.HandleFunc("POST /api/forward", s.handleCreate)
	mux.HandleFunc("POST /api/forward/{id}/stop", s.handleStop)
	mux.HandleFunc("POST /api/host/{host}/check", s.handleCheck)
	mux.HandleFunc("GET /api/state", s.handleState)
	return mux
}

func (s *Server) hosts() []sshconf.Host {
	hosts, err := sshconf.Load(s.sshConfig)
	if err != nil {
		log.Printf("не смог прочитать %s: %v", s.sshConfig, err)
		return nil
	}
	return hosts
}

func (s *Server) base(r *http.Request) pageData {
	hosts := s.hosts()
	opts := hostOptions(hosts, s.last.Host())
	return pageData{
		CSRF:      s.csrf,
		Flash:     r.URL.Query().Get("err"),
		OK:        r.URL.Query().Get("ok"),
		Hosts:     hosts,
		Options:   opts,
		HasLast:   len(opts) > 0 && opts[0].Last,
		AuthURLs:  s.mgr.AuthURLs(),
		SSHConfig: s.sshConfig,
	}
}

// hostOptions ставит запомненный хост первым и помечает его выбранным, чтобы
// на странице выбора хватало одного Enter. Запомненного нет или он исчез из
// конфига — список остаётся как был, без предвыбора.
func hostOptions(hosts []sshconf.Host, last string) []hostOption {
	opts := make([]hostOption, 0, len(hosts))
	found := false
	for _, h := range hosts {
		if h.Alias == last {
			found = true
			continue
		}
		opts = append(opts, hostOption{Alias: h.Alias})
	}
	if !found {
		return opts
	}
	return append([]hostOption{{Alias: last, Last: true}}, opts...)
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Иначе Chrome закэширует страницу и повторный заход её не переспросит.
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("шаблон %s не отрисовался: %v", name, err)
	}
}

// ---------------------------------------------------------------------------
// защита
// ---------------------------------------------------------------------------

// checkMutation не даёт чужой вкладке дёргать наши действия. До 127.0.0.1:8777
// достучится любая открытая страница, а действия тут поднимают ssh-туннели.
//
// Три слоя: Sec-Fetch-Site отсекает кросс-сайтовые формы (заголовок ставит сам
// браузер, подделать со страницы нельзя), Origin — вторая проверка того же,
// а CSRF-токен ловит всё остальное, включая curl.
func (s *Server) checkMutation(r *http.Request) error {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return fmt.Errorf("запрос пришёл со стороны (Sec-Fetch-Site: %s)", site)
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			return fmt.Errorf("чужой Origin: %s", origin)
		}
	}
	got := r.FormValue("csrf")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.csrf)) != 1 {
		return fmt.Errorf("не тот CSRF-токен")
	}
	return nil
}

// ---------------------------------------------------------------------------
// хендлеры
// ---------------------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := s.base(r)
	data.Forwards = s.mgr.List()
	s.render(w, "index", data)
}

func (s *Server) handleFix(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	data := s.base(r)
	data.Back = safeBack(q.Get("back"))
	data.ErrCode = q.Get("error")
	data.FixHost = q.Get("host")
	data.FixPort, _ = strconv.Atoi(q.Get("port"))

	data.FixRemotePort = data.FixPort
	data.FixTTL = 3600

	if data.FixPort <= 0 || data.FixPort > 65535 {
		data.Flash = "не понял, какой порт пробрасывать — открой смотрелку и заведи вручную"
	} else if f, ok := s.mgr.Broken(data.FixPort); ok && sshconf.Known(data.Hosts, f.Host) {
		// Порт уже был проброшен, но соединение под ним оборвалось. Человек
		// пришёл туда же — значит, хочет то же самое: подставляем как было.
		data.Restore = &f
		data.Options = hostOptions(data.Hosts, f.Host)
		data.HasLast = true
		data.FixRemotePort = f.RemotePort
		data.FixTTL = 0
		if !f.Pinned() {
			data.FixTTL = int(f.Expires.Sub(f.Created).Seconds())
		}
	}
	s.render(w, "fix", data)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if err := s.checkMutation(r); err != nil {
		log.Printf("отклонил создание проброса: %v", err)
		http.Error(w, "403 — "+err.Error(), http.StatusForbidden)
		return
	}

	back := safeBack(r.FormValue("back"))
	// При ошибке возвращаем на ту же страницу, откуда пришли: если это была
	// /fix, там сохранится контекст и можно повторить, подтвердив сессию.
	fail := func(msg string) {
		to := "/?err=" + url.QueryEscape(msg)
		if back != "" {
			to = "/fix?err=" + url.QueryEscape(msg) +
				"&back=" + url.QueryEscape(back) +
				"&port=" + url.QueryEscape(r.FormValue("localPort"))
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	}

	localPort, err := strconv.Atoi(r.FormValue("localPort"))
	if err != nil || localPort < 1 || localPort > 65535 {
		fail("некорректный локальный порт")
		return
	}
	remotePort := localPort
	if v := strings.TrimSpace(r.FormValue("remotePort")); v != "" {
		remotePort, err = strconv.Atoi(v)
		if err != nil || remotePort < 1 || remotePort > 65535 {
			fail("некорректный порт на той стороне")
			return
		}
	}

	// Имя хоста уезжает в аргументы ssh, поэтому принимаем только то,
	// что реально описано в ~/.ssh/config.
	host := r.FormValue("host")
	if !sshconf.Known(s.hosts(), host) {
		fail("неизвестный хост: " + host)
		return
	}

	var ttl time.Duration
	if secs, err := strconv.Atoi(r.FormValue("ttl")); err == nil && secs > 0 {
		ttl = time.Duration(secs) * time.Second
	}

	if _, err := s.mgr.Add(host, localPort, remotePort, ttl); err != nil {
		fail(err.Error())
		return
	}
	// Запоминаем только удавшийся выбор: предлагать машину, на которую
	// не пустило, смысла нет.
	s.last.Set(host)

	// Вернуться туда, откуда увело расширение. location.replace тут не нужен:
	// 303 не оставляет страницу выбора в истории.
	if back != "" {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.checkMutation(r); err != nil {
		log.Printf("отклонил снятие проброса: %v", err)
		http.Error(w, "403 — "+err.Error(), http.StatusForbidden)
		return
	}
	if err := s.mgr.Remove(r.PathValue("id")); err != nil {
		http.Redirect(w, r, "/?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleCheck — «Проверить» на плашке про аутентификацию. Пользователь сходил
// по ссылке, а узнать об этом можно только попробовав соединиться.
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.checkMutation(r); err != nil {
		log.Printf("отклонил проверку хоста: %v", err)
		http.Error(w, "403 — "+err.Error(), http.StatusForbidden)
		return
	}

	host := r.PathValue("host")
	if !sshconf.Known(s.hosts(), host) {
		http.Redirect(w, r, "/?err="+url.QueryEscape("неизвестный хост: "+host), http.StatusSeeOther)
		return
	}

	if err := s.mgr.Check(host); err != nil {
		// Не пустил снова — Check уже положил свежую ссылку в плашку.
		http.Redirect(w, r, "/?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.mgr.Forget(host)
	http.Redirect(w, r, "/?ok="+url.QueryEscape(host+": сессия подтверждена"), http.StatusSeeOther)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"forwards": s.mgr.List(),
		"auth":     s.mgr.AuthURLs(),
	})
}

// safeBack пропускает только http/https. Строка приходит из адресной строки
// и уходит обратно в Location — javascript: тут быть не должно.
func safeBack(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		log.Printf("отбросил back с непонятной схемой: %q", raw)
		return ""
	}
	return raw
}
