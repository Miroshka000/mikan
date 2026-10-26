package subsrc

import (
	"context"

	"mikan/internal/panel/model"
)

// shopPackage is a traffic package (GitHub issue #12) the Mini App offers for the
// subscription on screen.
type shopPackage struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Pool        string `json:"pool,omitempty"` // the pool's name; none: the main traffic
	Stars       int64  `json:"stars,omitempty"`
	Rub         int64  `json:"rub,omitempty"`
}

// shopPackages lists the packages the subscription userID can buy; none without one.
func (h *Handler) shopPackages(ctx context.Context, userID int64, lang string) ([]shopPackage, error) {
	out := []shopPackage{}
	if userID == 0 {
		return out, nil
	}
	offers, _, err := h.shop.PackageOffers(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, o := range offers {
		out = append(out, shopPackage{ID: o.Package.ID, Name: o.Package.Name, Description: model.DescribePackage(model.TrafficPackage(o.Package), o.Pool, lang),
			Pool: o.Pool, Stars: o.Stars, Rub: o.Rub})
	}
	return out, nil
}
