package api

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// codeSources are where error codes the web shows are written: the API's own (huma's
// top-level code and an ErrorDetail's Message), and the packages whose codes the API
// passes on as a detail's Message (templates, presets, WARP, Clash rules, payments).
var codeSources = []struct {
	dir      string
	patterns []*regexp.Regexp
}{
	{"internal/panel/api", []*regexp.Regexp{
		regexp.MustCompile(`huma\.Error\d{3}[A-Za-z]*\(\s*"([a-z][a-z0-9_]*)"\s*[,)]`),
		regexp.MustCompile(`Message:\s*"([a-z][a-z0-9_]*)"\s*[,}]`),
	}},
	{"internal/proto", []*regexp.Regexp{failCode, structCode}},
	{"internal/panel/presets", []*regexp.Regexp{failCode, structCode}},
	{"internal/panel/warp", []*regexp.Regexp{structCode}},
	{"internal/panel/subs", []*regexp.Regexp{structCode}},
	// billing.errCode keeps a provider's refusal in payments.error, for the history.
	{"internal/panel/billing", []*regexp.Regexp{structCode}},
	{"internal/panel/addons", []*regexp.Regexp{structCode}},
	// subpage's checks: bad(field, "code", value) and the image errors' texts.
	{"internal/panel/subpage", []*regexp.Regexp{regexp.MustCompile(`"([a-z][a-z0-9_]*)",\s*[^,()"]*\)`), regexp.MustCompile(`errors\.New\("([a-z][a-z0-9_]*)"\)`)}},
}

var (
	failCode   = regexp.MustCompile(`\bfail\(\s*"([a-z][a-z0-9_]*)"\s*[,)]`)
	structCode = regexp.MustCompile(`\bCode:\s*"([a-z][a-z0-9_]*)"\s*[,}]`)
)

// codesByText reach the web as text with no literal the scan sees.
var codesByText = []string{
	// billing.errCode: payments.error of a paid payment the panel could not apply yet.
	"addon_unreachable", "no_slots", "timeout", "user_gone", "package_gone",
	// subs.Routes.Check, as a detail's Message.
	"routes_service", "routes_target", "routes_direct", "routes_dns",
	"routes_lists_many", "routes_list_name", "routes_list_url", "routes_list_behavior", "routes_list_format", "routes_servers",
	// subs.TemplateError, as a detail's Message.
	"template_yaml", "template_empty", "template_groups", "template_group_name", "template_group_dup", "template_group_member",
	"template_rule", "template_rule_target", "template_rule_set",
	// checkCA, as a detail's Message.
	"email_invalid", "eab_kid_invalid", "eab_hmac_invalid", "zerossl_email_required", "google_eab_required",
}

// codesNotShown are sent but never shown by their text, so they need none.
var codesNotShown = map[string]bool{
	// Top-level codes of 409/422 that always carry details: the web shows the details.
	"validation": true, "invalid_config": true, "cascade": true, "warp": true,
	"bad_name": true, "bad_certificate": true, "bad_dest": true,
	// Statuses with texts of their own (errors.notFound, errors.tooMany, errors.noSlots).
	"not_found": true, "session_not_found": true, "rate_limited": true, "no_free_slots": true,
	// The login page words these itself.
	"invalid_credentials": true, "totp_required": true,
}

// Every error code the web can get has a text in both languages under errors.api of
// web/src/i18n: the web shows the bare code otherwise.
func TestErrorCodesHaveTexts(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	texts := map[string]map[string]any{}
	for _, lang := range []string{"ru", "en"} {
		raw, err := os.ReadFile(filepath.Join(root, "web", "src", "i18n", lang+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Errors struct {
				API map[string]any `json:"api"`
			} `json:"errors"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s.json: %v", lang, err)
		}
		texts[lang] = f.Errors.API
	}
	where := map[string]string{} // code → the first file that sends it
	for _, src := range codeSources {
		err := filepath.WalkDir(filepath.Join(root, src.dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for _, re := range src.patterns {
				for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
					if _, ok := where[m[1]]; !ok {
						where[m[1]] = filepath.ToSlash(rel)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(where) < 100 {
		t.Fatalf("only %d codes found: the scan lost its way", len(where))
	}
	for _, e := range cascadeRefusals {
		where[e.Error()] = "cascadeError"
	}
	for _, code := range codesByText {
		where[code] = "codesByText"
	}
	codes := make([]string, 0, len(where))
	for code := range where {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		if codesNotShown[code] {
			continue
		}
		for _, lang := range []string{"ru", "en"} {
			if s, ok := texts[lang][code].(string); !ok || s == "" {
				t.Errorf("%s (%s): no errors.api.%s in %s.json", code, where[code], code, lang)
			}
		}
	}
	// A code that is no longer sent leaves the list.
	for code := range codesNotShown {
		if _, ok := where[code]; !ok {
			t.Errorf("codesNotShown has %s, which nothing sends", code)
		}
	}
}
