package payment

import (
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
)

func cacheEntryFromTransaction(tx dbsqlc.Transaction, processedAt *time.Time) cache.TransactionStatusCacheEntry {
	return cache.TransactionStatusCacheEntry{
		ID:          tx.ID.String(),
		Status:      tx.Status,
		RequestedAt: tx.RequestedAt,
		ProcessedAt: processedAt,
		UpdatedAt:   tx.UpdatedAt,
	}
}
