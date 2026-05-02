package legacy

import (
	"context"
	"errors"

	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/google/uuid"
)

// ErrUnavailable reports that the legacy adapter is not implemented or not reachable.
var ErrUnavailable = errors.New("legacy adapter unavailable")

// ErrNotFound reports that the merchant does not exist in the legacy system.
var ErrNotFound = errors.New("legacy merchant not found")

// ErrLookupInProgress reports that another request is already filling the merchant cache.
var ErrLookupInProgress = errors.New("merchant lookup already in progress")

// MerchantReader reads merchant data from the legacy system.
type MerchantReader interface {
	GetMerchant(ctx context.Context, merchantID uuid.UUID) (*dbsqlc.Merchant, error)
}

// NoopMerchantClient is a placeholder legacy adapter until the real integration exists.
type NoopMerchantClient struct{}

// GetMerchant returns ErrUnavailable because the legacy integration is not implemented yet.
func (NoopMerchantClient) GetMerchant(ctx context.Context, merchantID uuid.UUID) (*dbsqlc.Merchant, error) {
	return nil, ErrUnavailable
}
