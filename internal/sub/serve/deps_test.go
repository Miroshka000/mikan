package serve

import (
	"os/exec"
	"strings"
	"testing"
)

// The subscription module serves the panel and a subscription service apart from it, and
// the panel must not link the GPL core: nothing under internal/sub, its tests apart,
// imports the panel or mihomo, not even through another package.
func TestModuleLinksNeitherPanelNorCore(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool to list the imports with")
	}
	out, err := exec.Command(gobin, "list", "-deps", "-f", "{{.ImportPath}}", "mikan/internal/sub/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, p := range strings.Fields(string(out)) {
		if p == "mikan/internal/panel" || strings.HasPrefix(p, "mikan/internal/panel/") ||
			p == "github.com/metacubex/mihomo" || strings.HasPrefix(p, "github.com/metacubex/mihomo/") {
			t.Errorf("internal/sub links %s", p)
		}
	}
}
