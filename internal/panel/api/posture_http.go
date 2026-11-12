package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/targets"
)

// The security presets over HTTP: which one the installation is on, a preset's plan.

type postureOutput struct {
	Body struct {
		Current  string `json:"current" enum:"standard,secure,custom"`
		Secure   int    `json:"secure" doc:"Сколько изменений осталось до «Безопасного»"`
		Standard int    `json:"standard" doc:"Сколько изменений осталось до «Стандартного»"`
	}
}

type posturePlanInput struct {
	Preset    string `path:"preset" enum:"standard,secure"`
	Neighbors bool   `query:"neighbors" doc:"Протоколам, которым не хватило собственных имён панели, подобрать чужие сайты рядом с сервером"`
}

type posturePlanOutput struct {
	Body struct {
		Changes []PostureChange `json:"changes"`
	}
}

type postureApplyInput struct {
	Preset string `path:"preset" enum:"standard,secure"`
	Body   struct {
		Changes []PostureChange `json:"changes" maxItems:"500" doc:"Строки плана, как их показала панель"`
	}
}

func (h *handlers) registerPosture() {
	huma.Register(h.api, huma.Operation{OperationID: "get-security-preset", Method: http.MethodGet, Path: "/api/v1/security/presets", Summary: "На каком пресете безопасности стоит панель", Tags: []string{"settings"}}, h.getPosture)
	huma.Register(h.api, huma.Operation{OperationID: "plan-security-preset", Method: http.MethodPost, Path: "/api/v1/security/presets/{preset}/plan", Summary: "Что изменит пресет безопасности", Tags: []string{"settings"}}, h.planPosture)
	huma.Register(h.api, huma.Operation{OperationID: "apply-security-preset", Method: http.MethodPost, Path: "/api/v1/security/presets/{preset}/apply", Summary: "Применить пресет безопасности", Tags: []string{"settings"}}, h.applyPosture)
}

func (h *handlers) getPosture(ctx context.Context, _ *struct{}) (*postureOutput, error) {
	p, err := h.posture(ctx, false)
	if err != nil {
		return nil, err
	}
	out := &postureOutput{}
	out.Body.Current, out.Body.Secure, out.Body.Standard = p.current()
	return out, nil
}

// planPosture is the preset's list of changes. For "secure" with neighbors, a node whose
// REALITY protocols the panel's own names do not cover is scanned for sites next to it.
func (h *handlers) planPosture(ctx context.Context, in *posturePlanInput) (*posturePlanOutput, error) {
	if err := requireSession(ctx, "preset"); err != nil {
		return nil, err
	}
	p, err := h.posture(ctx, true)
	if err != nil {
		return nil, err
	}
	out := &posturePlanOutput{}
	if in.Preset == postureStandard {
		out.Body.Changes = changesOrNone(p.standard())
		return out, nil
	}
	p.neighbors = in.Neighbors
	need := map[int64]bool{}
	for _, c := range p.secure() {
		if c.Code == "site" && c.Params["dest"] == "" {
			need[c.Node] = true
		}
	}
	if len(need) > 0 {
		if !scanning.TryLock() {
			return nil, huma.Error409Conflict("scan_busy")
		}
		defer scanning.Unlock()
	}
	for i := range p.nodes {
		pn := &p.nodes[i]
		pn.scanned = true
		if !need[pn.node.ID] {
			continue
		}
		// A scan that fails leaves the node without sites: its protocols are named in the
		// plan as the admin's to settle.
		if found, err := h.targetDeps(pn.node.ID).Scan(ctx, targets.ScanRequest{Node: pn.node}); err == nil {
			pn.sites = found.Results
		}
	}
	out.Body.Changes = changesOrNone(p.secure())
	return out, nil
}

func changesOrNone(c []PostureChange) []PostureChange {
	if c == nil {
		return []PostureChange{}
	}
	return c
}
