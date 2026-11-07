package repository

import "context"

// GetLegacyGrokSafetyRejectionFee preserves the existing per-group fee during
// rolling upgrades. Reading through the row's JSON representation also works
// after an operator has deliberately removed the optional legacy column.
func (r *settingRepository) GetLegacyGrokSafetyRejectionFee(ctx context.Context, groupID int64) (float64, error) {
	rows, err := r.client.QueryContext(ctx, `
		SELECT COALESCE((to_jsonb(g)->>'grok_safety_rejection_fee')::double precision, 0)
		FROM groups AS g WHERE g.id = $1 AND g.deleted_at IS NULL`, groupID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, rows.Err()
	}
	var fee float64
	if err := rows.Scan(&fee); err != nil {
		return 0, err
	}
	return fee, rows.Err()
}
