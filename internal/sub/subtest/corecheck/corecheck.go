// Package corecheck has the configs the tests render checked by the cores the apps run:
// sing-box (sing-box check) and xray (xray run -test). A core at hand ($SINGBOX or $XRAY,
// else the binary on the PATH), else the core's image in Docker, pulled by its pinned tag;
// the check is skipped when there is none of them, and fails on CI when the image cannot
// be pulled.
//
// The images are pinned to the releases ci.yml and release.yml download: the tests check
// against one version wherever they run, and the versions here and there move together.
package corecheck

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

	"mikan/internal/sub/routing"
)

const (
	singBoxImage = "ghcr.io/sagernet/sing-box:v1.12.23"
	xrayImage    = "ghcr.io/xtls/xray-core:26.10.21"
)

// core is a core to check configs with: its binary, or docker and the image (with the
// configs' directory mounted at /c); why there is none when bin is empty, fatal when that
// fails the test rather than skips it.
type core struct {
	bin, image, why string
	fatal           bool
	assetEnv        string // the variable that names the configs' directory to the core
}

// command runs the core on the file name in dir with args before it.
func (c core) command(dir, name string, args ...string) *exec.Cmd {
	if c.image == "" {
		cmd := exec.Command(c.bin, append(args, filepath.Join(dir, name))...)
		if c.assetEnv != "" {
			cmd.Env = append(os.Environ(), c.assetEnv+"="+dir)
		}
		return cmd
	}
	run := []string{"run", "--rm", "--network", "none", "-v", dir + ":/c"}
	if c.assetEnv != "" {
		run = append(run, "-e", c.assetEnv+"=/c")
	}
	run = append(append(run, c.image), args...)
	return exec.Command(c.bin, append(run, "/c/"+name)...)
}

// find is the core of the binary named by the environment variable env or on the PATH,
// else of the image in Docker, pulled when it is not there yet.
func find(env, name, image string) core {
	if bin := os.Getenv(env); bin != "" {
		return core{bin: bin}
	}
	if bin, err := exec.LookPath(name); err == nil {
		return core{bin: bin}
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return core{why: "neither $" + env + ", " + name + " on the PATH nor docker to check the configs with"}
	}
	if exec.Command(docker, "image", "inspect", image).Run() != nil {
		if out, err := exec.Command(docker, "pull", image).CombinedOutput(); err != nil {
			return core{why: "no " + image + " image to check the configs with: " + lastLine(out), fatal: os.Getenv("CI") != ""}
		}
	}
	return core{bin: docker, image: image}
}

var (
	singBox = sync.OnceValue(func() core { return find("SINGBOX", "sing-box", singBoxImage) })
	xray    = sync.OnceValue(func() core {
		c := find("XRAY", "xray", xrayImage)
		c.assetEnv = "XRAY_LOCATION_ASSET"
		return c
	})
)

// ready is the core, the test skipped (or failed, on CI) when there is none.
func ready(t testing.TB, c core) core {
	t.Helper()
	if c.bin == "" {
		if c.fatal {
			t.Fatal(c.why)
		}
		t.Skip(c.why)
	}
	return c
}

// tempDir is a directory for the configs that the images, which run their cores as users
// of their own, can read.
func tempDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t testing.TB, dir, name string, b []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// SingBox has sing-box check the config raw.
//
// sing-box check only builds the config: a remote rule set is fetched when the box
// starts, which check never does, so the configs' rule sets on GitHub are not downloaded
// and the check needs no network (the Docker check runs without one).
func SingBox(t testing.TB, raw []byte) {
	t.Helper()
	c := ready(t, singBox())
	dir := tempDir(t)
	write(t, dir, "config.json", raw)
	if out, err := c.command(dir, "config.json", "check", "-c").CombinedOutput(); err != nil {
		t.Errorf("sing-box refuses the config: %v\n%s", err, strings.TrimSpace(string(out)))
	}
}

// Xray has xray check every config of the xray JSON profile raw (an array of configs).
// xray loads the geodata a config names, so it gets files of every category of
// runetfreedom's (routing.Runetfreedom), one entry each: a category the apps' file lacks
// fails the check as it fails the app.
func Xray(t testing.TB, raw []byte) {
	t.Helper()
	c := ready(t, xray())
	var configs []json.RawMessage
	if err := json.Unmarshal(raw, &configs); err != nil {
		t.Fatal(err)
	}
	dir := tempDir(t)
	geodata(t, dir)
	for i, cfg := range configs {
		name := "config-" + strconv.Itoa(i) + ".json"
		write(t, dir, name, cfg)
		out, err := c.command(dir, name, "run", "-test", "-c").CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte("Configuration OK")) {
			var head struct {
				Remarks string `json:"remarks"`
			}
			_ = json.Unmarshal(cfg, &head)
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
func geodata(t testing.TB, dir string) {
	t.Helper()
	var sites, ips []byte
	for _, code := range routing.Runetfreedom.Names("geosite") {
		domain := pbUint(nil, 1, 3) // Full
		domain = pbBytes(domain, 2, []byte(code+".invalid"))
		site := pbBytes(nil, 1, []byte(strings.ToUpper(code)))
		sites = pbBytes(sites, 1, pbBytes(site, 2, domain))
	}
	for i, code := range routing.Runetfreedom.Names("geoip") {
		cidr := pbBytes(nil, 1, []byte{10, byte(i >> 8), byte(i), 0})
		cidr = pbUint(cidr, 2, 24)
		ip := pbBytes(nil, 1, []byte(strings.ToUpper(code)))
		ips = pbBytes(ips, 1, pbBytes(ip, 2, cidr))
	}
	write(t, dir, "geosite.dat", sites)
	write(t, dir, "geoip.dat", ips)
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
