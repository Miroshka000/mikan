package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The panel and the bot addon that runs next to it must not link the GPL core, the Citrus
// fork included (it keeps the mihomo import path through a replace): only mikan-node does.
// A failure names the chain of imports from the binary to where the core comes in.
func TestDepsLinkNoCore(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool to list the imports with")
	}
	out, err := exec.Command(gobin, "list", "-deps", "-f", "{{.ImportPath}} {{join .Imports \" \"}}",
		"mikan/cmd/mikan", "mikan/cmd/mikan-telegram").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	isCore := func(p string) bool {
		return p == "github.com/metacubex/mihomo" || strings.HasPrefix(p, "github.com/metacubex/mihomo/")
	}
	importer := map[string]string{} // a package -> the first package listed that imports it
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		pkgs = append(pkgs, f[0])
		for _, imp := range f[1:] {
			if _, ok := importer[imp]; !ok {
				importer[imp] = f[0]
			}
		}
	}
	for _, p := range pkgs {
		if !isCore(p) || isCore(importer[p]) {
			continue
		}
		chain := p
		for q, ok := importer[p]; ok; q, ok = importer[q] {
			chain = q + " -> " + chain
		}
		t.Errorf("links the core: %s", chain)
	}
}
