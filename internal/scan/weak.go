package scan

import "strings"

// A weak name is a poor REALITY target though its site passes every check: nobody opens it
// in a browser for hours. A mail or autoconfiguration host of somebody's domain, a
// dynamic-DNS name of a home router or a hobby server: traffic of many clients to such a
// name stands out, and so does a server that shows its certificate. Weak names are taken
// only when the neighborhood has nothing better.

// weakLabels are first labels of names that serve a protocol or a control panel, not a site.
var weakLabels = map[string]bool{
	"mail": true, "smtp": true, "imap": true, "pop": true, "pop3": true, "mx": true, "webmail": true,
	"autoconfig": true, "autodiscover": true, "cpanel": true, "whm": true, "webdisk": true, "cpcalendars": true,
	"cpcontacts": true, "ftp": true, "ns": true, "ns1": true, "ns2": true, "vpn": true, "proxy": true,
	"panel": true, "admin": true, "router": true, "nas": true, "localhost": true,
}

// dynamicZones are zones that hand a name to any address: dynamic DNS and address-in-name
// services.
var dynamicZones = []string{
	"duckdns.org", "ddns.net", "no-ip.org", "no-ip.biz", "no-ip.info", "noip.me", "hopto.org", "zapto.org",
	"sytes.net", "servebeer.com", "serveftp.com", "servehttp.com", "myftp.org", "myvnc.com", "redirectme.net",
	"dynu.net", "dynu.com", "freeddns.org", "dyndns.org", "dyndns.info", "dynv6.net", "dedyn.io", "mooo.com",
	"afraid.org", "nip.io", "sslip.io", "myds.me", "synology.me", "tplinkdns.com", "asuscomm.com",
	"keenetic.pro", "keenetic.link", "keenetic.name", "netcraze.pro", "mywire.org", "ddnsfree.com",
	"ddnsgeek.com", "giize.com", "gleeze.com", "kozow.com", "loseyourip.com", "ooguy.com", "theworkpc.com",
	"casacam.net", "accesscam.org", "camdvr.org", "freemyip.com", "ydns.eu", "twilightparadox.com",
}

// Weak says whether name is a poor target by its looks alone (see above).
func Weak(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	first, _, _ := strings.Cut(name, ".")
	if weakLabels[first] {
		return true
	}
	for _, z := range dynamicZones {
		if name == z || strings.HasSuffix(name, "."+z) {
			return true
		}
	}
	return false
}
