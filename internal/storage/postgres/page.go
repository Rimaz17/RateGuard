package postgres

import (
	"fmt"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// afterParam turns a page cursor into a query parameter: NULL for the first
// page, so the SQL never has to cast an empty string to UUID.
func afterParam(page domain.Page) (*string, error) {
	if page.Limit < 1 || page.Limit > domain.MaxPageLimit {
		return nil, fmt.Errorf("page limit must be between 1 and %d, got %d", domain.MaxPageLimit, page.Limit)
	}
	if page.After == "" {
		return nil, nil // A NULL cursor selects the first page.
	}
	if !domain.IsID(page.After) {
		return nil, fmt.Errorf("page cursor %q is not an ID", page.After)
	}
	return &page.After, nil
}
