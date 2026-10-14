package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/targets"
	"mikan/internal/scan"
)

type checkTargetInput struct {
	Body struct {
		Dest string `json:"dest" maxLength:"255" doc:"host:port"`
		SNI  string `json:"sni,omitempty" maxLength:"253" doc:"Имя для клиентов; по умолчанию — хост из dest"`
	}
}

type checkTargetOutput struct{ Body scan.Result }

type scanTargetsOutput struct {
	Body struct {
		IP        string        `json:"ip" doc:"Адрес сервера, вокруг которого искали"`
		Scanned   int           `json:"scanned"`
		SelfSteal *scan.Result  `json:"self_steal,omitempty" doc:"Свой домен с сертификатом панели"`
		Results   []scan.Result `json:"results"`
	}
}

// scanning allows one neighbor scan at a time: it opens ~250 connections.
var scanning sync.Mutex

func (h *handlers) registerTargets() {
	huma.Register(h.api, huma.Operation{OperationID: "check-target", Method: http.MethodPost, Path: "/api/v1/inbounds/check-target", Summary: "Проверить сайт как цель REALITY", Tags: []string{"inbounds"}}, h.checkTarget)
	huma.Register(h.api, huma.Operation{OperationID: "scan-targets", Method: http.MethodPost, Path: "/api/v1/inbounds/scan-targets", Summary: "Подобрать цели REALITY рядом с сервером", Tags: []string{"inbounds"}}, h.scanTargets)
}

// targetDeps is what the shared checks need from the panel. The check form is not tied to
// a node, so the panel dials the site itself; a scan is the node's, when it can.
func (h *handlers) targetDeps(nodeID int64) targets.Deps {
	d := targets.Deps{Settings: h.d.Settings, Resolve: h.d.Resolve}
	if h.d.Nodes != nil {
		d.ScanTargets = func(ctx context.Context, req nodeapi.TargetScanRequest) (nodeapi.TargetScan, error) {
			return h.d.Nodes.ScanTargets(ctx, nodeID, req)
		}
	}
	return d
}

func (h *handlers) checkTarget(ctx context.Context, in *checkTargetInput) (*checkTargetOutput, error) {
	r, err := h.targetDeps(0).Check(ctx, in.Body.Dest, in.Body.SNI)
	switch {
	case errors.Is(err, targets.ErrBadDest):
		return nil, huma.Error422UnprocessableEntity("bad_dest", &huma.ErrorDetail{Location: "body.dest", Message: "dest_format"})
	case errors.Is(err, targets.ErrPrivateDest):
		return nil, huma.Error422UnprocessableEntity("bad_dest", &huma.ErrorDetail{Location: "body.dest", Message: "reality_dest_private"})
	case err != nil:
		return nil, err
	}
	return &checkTargetOutput{Body: r}, nil
}

type scanTargetsInput struct {
	NodeID int64 `query:"node_id" default:"1" minimum:"1" doc:"Нода, рядом с которой искать"`
}

func (h *handlers) scanTargets(ctx context.Context, in *scanTargetsInput) (*scanTargetsOutput, error) {
	if !scanning.TryLock() {
		return nil, huma.Error409Conflict("scan_busy")
	}
	defer scanning.Unlock()
	node, err := h.nodeOf(ctx, in.NodeID)
	if err != nil {
		return nil, err
	}
	// The panel's own domain is offered once it has its Let's Encrypt certificate.
	selfSteal := h.d.Cert != nil && h.d.Cert().Kind == "acme"
	found, err := h.targetDeps(node.ID).Scan(ctx, targets.ScanRequest{Node: node, SelfSteal: selfSteal})
	var noIP *targets.NoIPError
	if errors.As(err, &noIP) {
		return nil, huma.Error422UnprocessableEntity("scan_no_ip")
	} else if err != nil {
		return nil, err
	}
	out := &scanTargetsOutput{}
	out.Body.IP, out.Body.Scanned, out.Body.SelfSteal, out.Body.Results = found.IP, found.Scanned, found.SelfSteal, found.Results
	return out, nil
}
