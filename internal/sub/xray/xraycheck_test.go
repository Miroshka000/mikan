package xray

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The configs the tests make are checked by xray itself (xray run -test): an xray at hand
// ($XRAY, or xray on the PATH), else the xray-core image in Docker; the check is skipped
// when there is neither. xray loads the geodata a config names, so it gets files of every
// category of runetfreedom's (routing/runetfreedom), one entry each: a category the apps'
// file lacks fails the check as it fails the app.

const xrayImage = "ghcr.io/xtls/xray-core:latest"

// xrayTest is the command that checks the config file name in dir, its geodata beside it.
type xrayTest func(dir, name string) *exec.Cmd

var findXray = sync.OnceValues(func() (xrayTest, string) {
	bin := os.Getenv("XRAY")
	if bin == "" {
		bin, _ = exec.LookPath("xray")
	}
	if bin != "" {
		return func(dir, name string) *exec.Cmd {
			cmd := exec.Command(bin, "run", "-test", "-c", filepath.Join(dir, name))
			cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+dir)
			return cmd
		}, ""
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return nil, "neither xray nor docker to check the configs with"
	}
	if exec.Command(docker, "image", "inspect", xrayImage).Run() != nil {
		return nil, "no " + xrayImage + " image to check the configs with"
	}
	return func(dir, name string) *exec.Cmd {
		return exec.Command(docker, "run", "--rm", "-v", dir+":/c", "-e", "XRAY_LOCATION_ASSET=/c", xrayImage, "run", "-test", "-c", "/c/"+name)
	}, ""
})

// checkXray has xray check every config of the profile raw.
func checkXray(t *testing.T, raw []byte) {
	t.Helper()
	run, why := findXray()
	if run == nil {
		t.Skip(why)
	}
	var configs []json.RawMessage
	if err := json.Unmarshal(raw, &configs); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// The image runs xray as a user of its own, which must read the files.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	geodata(t, dir)
	for i, c := range configs {
		name := "config-" + strconv.Itoa(i) + ".json"
		if err := os.WriteFile(filepath.Join(dir, name), c, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := run(dir, name).CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte("Configuration OK")) {
			var head struct {
				Remarks string `json:"remarks"`
			}
			_ = json.Unmarshal(c, &head)
			t.Errorf("xray refuses config %d (%s): %v\n%s", i, head.Remarks, err, lastLine(out))
		}
	}
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines[len(lines)-1]
}

// geodata writes geosite.dat and geoip.dat of runetfreedom's categories into dir: the
// protobuf lists xray reads (GeoSiteList, GeoIPList), each category with one entry.
func geodata(t *testing.T, dir string) {
	t.Helper()
	read := func(name string) []string {
		raw, err := os.ReadFile(filepath.Join("..", "routing", "runetfreedom", name))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range strings.Split(string(raw), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
		return out
	}
	var sites, ips []byte
	for _, code := range read("geosite.txt") {
		domain := pbUint(nil, 1, 3) // Full
		domain = pbBytes(domain, 2, []byte(code+".invalid"))
		site := pbBytes(nil, 1, []byte(strings.ToUpper(code)))
		sites = pbBytes(sites, 1, pbBytes(site, 2, domain))
	}
	for i, code := range read("geoip.txt") {
		cidr := pbBytes(nil, 1, []byte{10, byte(i >> 8), byte(i), 0})
		cidr = pbUint(cidr, 2, 24)
		ip := pbBytes(nil, 1, []byte(strings.ToUpper(code)))
		ips = pbBytes(ips, 1, pbBytes(ip, 2, cidr))
	}
	for name, b := range map[string][]byte{"geosite.dat": sites, "geoip.dat": ips} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func pbVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// pbBytes appends the length-delimited field of the number to b.
func pbBytes(b []byte, field int, v []byte) []byte {
	b = pbVarint(b, uint64(field)<<3|2)
	b = pbVarint(b, uint64(len(v)))
	return append(b, v...)
}

// pbUint appends the varint field of the number to b.
func pbUint(b []byte, field int, v uint64) []byte {
	return pbVarint(pbVarint(b, uint64(field)<<3), v)
}
