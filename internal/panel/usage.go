package panel

import (
	"net/http"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/usage"
)

// usageReport returns the savings analysis for the dashboard.
func (p *Panel) usageReport(*http.Request) (any, error) {
	if p.Usage == nil {
		return nil, notFound("usage tracking is not enabled")
	}
	srv := p.Store.Current().Server
	var plan *usage.Plan
	if pl, ok := usage.PlanByID(srv.Plan, srv.PlanPrice); ok {
		plan = &pl
	}
	return p.Usage.Build(time.Now(), plan), nil
}
