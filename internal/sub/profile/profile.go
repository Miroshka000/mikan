// Package profile is what a client profile is made of, whatever core renders it: the
// mihomo, sing-box and Xray renderers all take a Profile.
package profile

import (
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// Profile is what a client profile is made of.
type Profile struct {
	Proxies []model.Proxy // the user's (build.Proxies)
	Nodes   []model.Node  // enabled nodes in display order
	Direct  []string      // hosts that bypass the tunnel: the panel and every node
	// Rules are the admin's own rules (routing.ServedRules) that the app's core parses
	// (RulesFor), before the built-in routing.
	Rules []routing.Rule
	// Routes go after the admin's rules, for the apps that take rule lists (App); Lang
	// names the groups they add.
	Routes routing.Routes
	App    apps.App
	Lang   string
}
