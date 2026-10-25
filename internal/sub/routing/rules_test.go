package routing

import (
	"errors"
	"strings"
	"testing"

	"mikan/internal/sub/model"
)

var testGroups = model.GroupNames{Main: "Мой VPN", Auto: "Быстрый"}

func TestParseRules(t *testing.T) {
	text := strings.Join([]string{
		"# banks go direct",
		"domain-suffix , sber.ru , direct",
		"",
		"DOMAIN,api.example.com,Мой VPN",
		"DOMAIN-KEYWORD,youtube,PROXY",
		"DOMAIN-WILDCARD,*.cdn.example.com,Быстрый",
		`DOMAIN-REGEX,^ads\d+\.example\.com$,REJECT`,
		"GEOSITE,category-ads-all,REJECT-DROP",
		"GEOIP,telegram,PROXY,no-resolve",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR6,2001:db8::/32,DIRECT",
		"IP-ASN,13335,PROXY",
		"DST-PORT,6881-6889/51413,REJECT",
		"NETWORK,udp,PROXY",
		"PROCESS-NAME,Telegram.exe,PROXY",
		"PROCESS-PATH,/usr/bin/curl,DIRECT",
	}, "\r\n")
	rules, err := ParseRules(text, testGroups)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 14 || rules[0] != "DOMAIN-SUFFIX,sber.ru,DIRECT" || rules[6] != "GEOIP,telegram,PROXY,no-resolve" {
		t.Fatalf("rules: %q", rules)
	}

	for line, code := range map[string]string{
		"DOMAIN,example.com":                             "rule_format",
		"DOMAIN,example.com,DIRECT,no-resolve,x":         "rule_format",
		"RULE-SET,ads,REJECT":                            "rule_type",
		"AND,((DOMAIN,a.com)),DIRECT":                    "rule_type",
		"MATCH,DIRECT":                                   "rule_format",
		"DOMAIN,exa mple.com,DIRECT":                     "rule_value",
		"DOMAIN,a..com,DIRECT":                           "rule_value",
		"DOMAIN-REGEX,(unclosed,DIRECT":                  "rule_value",
		"IP-CIDR,10.0.0.0/33,DIRECT":                     "rule_value",
		"IP-CIDR6,10.0.0.0/8,DIRECT":                     "rule_value",
		"IP-ASN,AS13335,DIRECT":                          "rule_value",
		"DST-PORT,0,DIRECT":                              "rule_value",
		"DST-PORT,9000-80,DIRECT":                        "rule_value",
		"NETWORK,icmp,DIRECT":                            "rule_value",
		"GEOSITE,youtube ru,DIRECT":                      "rule_value",
		`PROCESS-NAME,"evil",DIRECT`:                     "rule_value",
		"DOMAIN,example.com,Other":                       "rule_target",
		"DOMAIN,example.com,мой vpn":                     "rule_target",
		"DOMAIN,example.com,DIRECT,no-resolve":           "rule_option",
		"IP-CIDR,10.0.0.0/8,DIRECT,src":                  "rule_option",
		"DOMAIN," + strings.Repeat("a", 600) + ",DIRECT": "rule_too_long",
	} {
		_, err := ParseRules("# first\n"+line, testGroups)
		var re *RuleError
		if !errors.As(err, &re) || re.Code != code || re.Line != 2 {
			t.Errorf("%q: %v, want %s on line 2", line, err, code)
		}
	}
	many := strings.Repeat("DOMAIN,a.com,DIRECT\n", MaxRules+1)
	if _, err := ParseRules(many, testGroups); err == nil || !strings.Contains(err.Error(), "rules_too_many") {
		t.Fatalf("too many: %v", err)
	}
}

// Served rules skip what no longer parses (a group renamed since) instead of failing
// every profile, and the text is parsed once.
func TestServedRules(t *testing.T) {
	text := "DOMAIN-SUFFIX,sber.ru,DIRECT\nDOMAIN,old.example.com,Старая группа\nGEOSITE,youtube,PROXY"
	rules := ServedRules(text, testGroups)
	if len(rules) != 2 || rules[1] != "GEOSITE,youtube,PROXY" {
		t.Fatalf("served: %q", rules)
	}
	if again := ServedRules(text, testGroups); &again[0] != &rules[0] {
		t.Fatal("the same text is parsed again instead of cached")
	}
	if ServedRules(" \n", testGroups) != nil {
		t.Fatal("no text, no rules")
	}
}

func TestRuleTargets(t *testing.T) {
	if got := strings.Join(RuleTargets(testGroups), ","); got != "DIRECT,REJECT,REJECT-DROP,PROXY,Мой VPN,Быстрый" {
		t.Fatalf("targets: %s", got)
	}
}
