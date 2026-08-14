// autoport — ставит и снимает локальные ssh-пробросы и показывает,
// что сейчас проброшено.
//
// Расширение Chrome ловит упавшую навигацию на localhost и уводит вкладку
// на /fix. Там выбирается хост, сервис поднимает проброс и возвращает
// на исходный URL.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/tihonove/autoport/internal/fwd"
	"github.com/tihonove/autoport/internal/sshconf"
	"github.com/tihonove/autoport/internal/web"
)

const defaultAddr = "127.0.0.1:8777"

func main() {
	addr := flag.String("addr", defaultAddr, "адрес, который слушаем")
	sshConfig := flag.String("ssh-config", sshconf.DefaultPath(), "путь к ~/.ssh/config")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mgr := fwd.New()
	srv := &http.Server{Addr: *addr, Handler: web.New(mgr, *sshConfig).Routes()}

	// Протухшие пробросы снимаем по таймеру: 10 секунд — достаточно точно
	// для TTL в минутах и не создаёт заметного фона.
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mgr.Reap()
			}
		}
	}()

	go func() {
		log.Printf("autoport слушает http://%s/", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	log.Print("останавливаюсь, гашу пробросы")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	// Пробросы умирают вместе с сервисом — так задумано, состояние никуда
	// не пишется и рассинхрона не бывает.
	mgr.Close()
}
