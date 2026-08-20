package web

import (
	"fmt"
	"html/template"
	"time"

	"github.com/tihonove/autoport/internal/fwd"
	"github.com/tihonove/autoport/internal/sshconf"
)

type pageData struct {
	CSRF      string
	Flash     string
	OK        string
	Forwards  []fwd.Forward
	Hosts     []sshconf.Host
	Options   []hostOption
	HasLast   bool
	AuthURLs  map[string]string
	SSHConfig string

	// только для /fix
	Back     string
	ErrCode  string
	FixHost  string
	FixPort  int
	Suggests []int
}

// hostOption — пункт выпадашки с хостами. Last стоит на том, который выбирали
// в прошлый раз: он идёт первым и подставлен заранее.
type hostOption struct {
	Alias string
	Last  bool
}

var funcs = template.FuncMap{
	"left": func(f fwd.Forward) string {
		if f.Pinned() {
			return "навсегда"
		}
		d := f.Left()
		if d < 0 {
			return "истёк"
		}
		if d < time.Minute {
			return fmt.Sprintf("%d сек", int(d.Seconds()))
		}
		if d < time.Hour {
			return fmt.Sprintf("%d мин", int(d.Minutes()))
		}
		return fmt.Sprintf("%.1f ч", d.Hours())
	},
	"since": func(t time.Time) string {
		d := time.Since(t)
		if d < time.Minute {
			return "только что"
		}
		if d < time.Hour {
			return fmt.Sprintf("%d мин назад", int(d.Minutes()))
		}
		return fmt.Sprintf("%.1f ч назад", d.Hours())
	},
}

// html/template экранирует с учётом контекста: в href и в тексте по-разному.
// Back и прочее приходят из адресной строки, ручной склейки тут быть не должно.
var tmpl = template.Must(template.New("").Funcs(funcs).Parse(`
{{define "head"}}<!doctype html>
<meta charset="utf-8">
<title>{{.}}</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 15px/1.6 system-ui, sans-serif; max-width: 52rem;
         margin: 3rem auto; padding: 0 1.5rem; }
  h1 { font-size: 1.4rem; margin: 0 0 .2rem; }
  h2 { font-size: 1.05rem; margin: 2.5rem 0 .6rem; font-weight: 600; }
  .sub { color: #888; margin-top: 0; }
  table { border-collapse: collapse; width: 100%; }
  th { text-align: left; font-weight: 500; color: #888; font-size: .85rem;
       border-bottom: 1px solid #8884; padding: .3rem .6rem .3rem 0; }
  td { padding: .5rem .6rem .5rem 0; border-bottom: 1px solid #8882;
       font-family: ui-monospace, monospace; }
  .empty { color: #888; font-style: italic; }
  form.inline { display: inline; }
  button { font: inherit; padding: .25rem .7rem; border-radius: 5px;
           border: 1px solid #8886; background: transparent; cursor: pointer; }
  button.primary { background: #1a73e8; color: #fff; border-color: #1a73e8;
                   padding: .5rem 1.1rem; }
  button:hover { border-color: #1a73e8; }
  input, select { font: inherit; padding: .35rem .5rem; border-radius: 5px;
                  border: 1px solid #8886; background: transparent; color: inherit; }
  .row { display: flex; gap: .6rem; align-items: end; flex-wrap: wrap; }
  .row label { display: flex; flex-direction: column; gap: .2rem;
               font-size: .85rem; color: #888; }
  .flash { background: #b00; color: #fff; padding: .7rem 1rem;
           border-radius: 6px; margin-bottom: 1.5rem; }
  .ok { background: #1a7f37; color: #fff; padding: .7rem 1rem;
        border-radius: 6px; margin-bottom: 1.5rem; }
  .auth { background: #c60; color: #fff; padding: .7rem 1rem;
          border-radius: 6px; margin-bottom: 1.5rem; }
  .auth a { color: #fff; }
  .auth button { color: #fff; border-color: #fff9; margin-left: .3rem; }
  .auth button:hover { border-color: #fff; }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: .3rem 1rem; }
  dt { color: #888; }
  dd { margin: 0; font-family: ui-monospace, monospace; word-break: break-all; }
</style>
{{end}}

{{define "hostpicker"}}
  <select name="host"{{if not $.HasLast}} autofocus{{end}}>
    {{range .Options}}
      <option value="{{.Alias}}"{{if .Last}} selected{{end}}>{{.Alias}}{{if .Last}} — прошлый раз{{end}}</option>
    {{end}}
  </select>
{{end}}

{{define "banners"}}
  {{with .Flash}}<div class="flash">{{.}}</div>{{end}}
  {{with .OK}}<div class="ok">{{.}}</div>{{end}}
  {{if not .Hosts}}
    <div class="auth">
      В <code>{{.SSHConfig}}</code> нет ни одной подходящей записи <code>Host</code>,
      поэтому пробрасывать некуда. autoport берёт машины только оттуда;
      шаблоны вроде <code>Host *</code> не в счёт.
    </div>
  {{end}}
  {{range $host, $url := .AuthURLs}}
    <div class="auth">
      <b>{{$host}}</b> просит подтвердить сессию —
      <a href="{{$url}}" target="_blank" rel="noopener">открыть ссылку</a>.
      Уже подтвердил?
      <form class="inline" method="post" action="/api/host/{{$host}}/check">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <button>Проверить</button>
      </form>
    </div>
  {{end}}
{{end}}

{{define "index"}}
{{template "head" "autoport — проброшенные порты"}}
<h1>Проброшенные порты</h1>
<p class="sub">Живут, пока жив сервис. Перезапуск гасит все.</p>
{{template "banners" .}}

<table>
  <tr><th>Порт</th><th>Куда</th><th>Осталось</th><th>Поднят</th><th></th></tr>
  {{range .Forwards}}
  <tr>
    <td><a href="{{.URL}}">{{.LocalPort}}</a></td>
    <td>{{.Host}}:{{.RemotePort}}</td>
    <td>{{left .}}</td>
    <td>{{since .Created}}</td>
    <td>
      <form class="inline" method="post" action="/api/forward/{{.ID}}/stop">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <button>снять</button>
      </form>
    </td>
  </tr>
  {{else}}
  <tr><td colspan="5" class="empty">пока ничего не проброшено</td></tr>
  {{end}}
</table>

<h2>Пробросить вручную</h2>
<form method="post" action="/api/forward">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <div class="row">
    <label>локальный порт<input name="localPort" type="number" min="1" max="65535" required style="width:8rem"></label>
    <label>хост{{template "hostpicker" .}}</label>
    <label>порт на той стороне<input name="remotePort" type="number" min="1" max="65535" placeholder="как локальный" style="width:11rem"></label>
    <label>насколько<select name="ttl">
      <option value="300">5 минут</option>
      <option value="3600">1 час</option>
      <option value="0" selected>навсегда</option>
    </select></label>
    <button class="primary">Пробросить</button>
  </div>
</form>
{{end}}

{{define "fix"}}
{{template "head" "autoport — куда пробросить?"}}
<h1>Страница не открылась</h1>
<p class="sub">{{if .HasLast}}Прошлый хост уже подставлен — Enter, и вернёмся на неё же.{{else}}Выбери, куда пробросить порт — и вернёмся на неё же.{{end}}</p>
{{template "banners" .}}

<dl>
  <dt>URL</dt><dd>{{with .Back}}{{.}}{{else}}<span class="empty">не передан</span>{{end}}</dd>
  {{with .ErrCode}}<dt>Ошибка</dt><dd>{{.}}</dd>{{end}}
  <dt>Порт</dt><dd>{{.FixPort}}</dd>
</dl>

<h2>Пробросить {{.FixPort}} на</h2>
<form method="post" action="/api/forward">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <input type="hidden" name="back" value="{{.Back}}">
  <input type="hidden" name="localPort" value="{{.FixPort}}">
  <div class="row">
    <label>хост{{template "hostpicker" .}}</label>
    <label>порт на той стороне<input name="remotePort" type="number" min="1" max="65535" value="{{.FixPort}}" style="width:11rem"></label>
    <label>насколько<select name="ttl">
      <option value="300">5 минут</option>
      <option value="3600" selected>1 час</option>
      <option value="0">навсегда</option>
    </select></label>
    <button class="primary"{{if .HasLast}} autofocus{{end}}>Пробросить и вернуться</button>
  </div>
</form>

<p style="margin-top:2rem"><a href="/">Что уже проброшено</a></p>
{{end}}
`))
