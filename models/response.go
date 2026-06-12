// Package models
package models

// TotCount is the aggregate model returned by collection queries.
type TotCount struct {
	TotCount int `json:"tot_count" agg:"count(*)"`
}

type RowsAffectedResponse struct {
	RowsAffected int64 `json:"rows_affected"`
}

type CollectionResponse[T any] struct {
	Rows  []T `json:"rows"`
	Total any `json:"total,omitempty"`
}



