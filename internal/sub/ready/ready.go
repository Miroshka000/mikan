// Package ready is the catalog of ready subscription templates: what an admin loads into
// the template editor to start from. Loading one saves nothing; the template is the
// admin's once it is saved, and the panel never puts one in the database itself.
//
// mikan's own are the panel's built-in profile (mihomo.Starter) and the examples of the
// JSON cores (singbox.Example, xray.Example), made with the panel's settings when loaded:
// the catalog names them and holds no copy. The others are files of this package, each
// with its author, license and source: a template of somebody else's goes in only under a
// license that lets the project ship it.
package ready

import (
	"embed"

	"mikan/internal/sub/model"
)

// Template is a ready template.
type Template struct {
	ID   string
	Core model.Core
	// Name and Summary are Russian fallbacks: the admin panel translates them by the id.
	Name    string
	Summary string
	Author  string
	License string // SPDX id of the template's text
	Source  string // where it comes from, the lists it links
	// Starter: mikan's own, which the panel makes when it is loaded (Body is empty).
	Starter bool
	Body    string
}

//go:embed *.yaml
var files embed.FS

func file(name string) string {
	b, err := files.ReadFile(name)
	if err != nil {
		panic(err) // a name below without its file: the tests catch it
	}
	return string(b)
}

const (
	mikanAuthor  = "mikan"
	mikanLicense = "GPL-3.0-only"
	mikanSource  = "https://github.com/Miroshka000/mikan"
	privWLSource = "https://github.com/Nemu-x/privWL-clash"
)

var all = []Template{
	{ID: "mikan-clash", Core: model.CoreMihomo, Name: "mikan", Summary: "Встроенный профиль панели с текущими настройками маршрутизации; группы просят серверы сами",
		Author: mikanAuthor, License: mikanLicense, Source: mikanSource, Starter: true},
	{ID: "mikan-singbox", Core: model.CoreSingBox, Name: "mikan", Summary: "Рабочий конфиг sing-box: TUN, DNS через туннель, локальные сети напрямую",
		Author: mikanAuthor, License: mikanLicense, Source: mikanSource, Starter: true},
	{ID: "mikan-xray", Core: model.CoreXray, Name: "mikan", Summary: "Рабочий конфиг Xray: локальные SOCKS и HTTP, DNS через туннель, локальные сети напрямую",
		Author: mikanAuthor, License: mikanLicense, Source: mikanSource, Starter: true},
	// The panel's "only what is blocked" and the address lists of privWL-clash it lacks.
	{ID: "privwl-blocked", Core: model.CoreMihomo, Name: "privWL · только заблокированное",
		Summary: "Через VPN только заблокированное и замедленное в России, Telegram и диапазоны адресов, которые замедляют (Cloudflare, AWS и другие); остальное напрямую. Списки: Nemu-x/privWL-clash. В Happ и INCY (конфиги Xray) эти списки не работают и всё идёт напрямую: дайте этим приложениям другой шаблон правилом ответа или выключите им конфиги Xray",
		Author:  mikanAuthor, License: mikanLicense, Source: privWLSource, Body: file("privwl-blocked.yaml")},
}

// All is the catalog, by core in the order the editor lists it.
func All() []Template { return all }

// Get is the ready template of id.
func Get(id string) (Template, bool) {
	for _, t := range all {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
