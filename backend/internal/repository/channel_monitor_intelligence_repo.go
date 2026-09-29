package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type intelligenceRepository struct{ db *sql.DB }

func NewIntelligenceRepository(db *sql.DB) service.IntelligenceRepository {
	return &intelligenceRepository{db: db}
}

func (r *intelligenceRepository) ListGroups(ctx context.Context, ids []int64) ([]service.IntelligenceGroup, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,name FROM groups WHERE deleted_at IS NULL AND status='active'
		AND platform='openai' AND (cardinality($1::bigint[])=0 OR id=ANY($1)) ORDER BY id`, pq.Array(nonNilInt64(ids)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []service.IntelligenceGroup
	for rows.Next() {
		var g service.IntelligenceGroup
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func nonNilInt64(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func (r *intelligenceRepository) KeyCandidates(ctx context.Context, groupID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT k.id FROM api_keys k JOIN users u ON u.id=k.user_id
		WHERE k.group_id=$1 AND k.deleted_at IS NULL AND k.status='active'
		AND (k.expires_at IS NULL OR k.expires_at>NOW()) AND (k.quota=0 OR k.quota_used<k.quota)
		AND u.deleted_at IS NULL AND u.status='active'
		ORDER BY (u.role='admin') DESC, (k.name LIKE '勿删！%uptime专用') DESC,k.id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *intelligenceRepository) AdminIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *intelligenceRepository) Latest(ctx context.Context, id int64) (*service.IntelligencePoint, error) {
	var p service.IntelligencePoint
	err := r.db.QueryRowContext(ctx, `SELECT checked_at,status,latency_ms,detail FROM channel_monitor_intelligence_history WHERE group_id=$1 ORDER BY checked_at DESC LIMIT 1`, id).Scan(&p.CheckedAt, &p.Status, &p.LatencyMs, &p.Detail)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}
func (r *intelligenceRepository) Save(ctx context.Context, id int64, p service.IntelligencePoint) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO channel_monitor_intelligence_history(group_id,checked_at,status,latency_ms,detail) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, p.CheckedAt, p.Status, p.LatencyMs, p.Detail)
	return err
}
func (r *intelligenceRepository) Prune(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM channel_monitor_intelligence_history WHERE checked_at<$1`, before)
	return err
}
func (r *intelligenceRepository) History(ctx context.Context, ids []int64, start, end time.Time, bucket time.Duration) (map[int64][]service.IntelligencePoint, error) {
	// One representative per chart bucket: worst result wins, while checked_at
	// remains the latest actual sample (not a fabricated historical timestamp).
	rows, err := r.db.QueryContext(ctx, `SELECT group_id,MAX(checked_at),
		CASE MAX(CASE status WHEN 'red' THEN 3 WHEN 'yellow' THEN 2 ELSE 1 END)
			WHEN 3 THEN 'red' WHEN 2 THEN 'yellow' ELSE 'green' END, MAX(latency_ms)
		FROM channel_monitor_intelligence_history WHERE group_id=ANY($1) AND checked_at>=$2 AND checked_at<$3
		GROUP BY group_id,FLOOR(EXTRACT(EPOCH FROM checked_at)/$4) ORDER BY group_id,MAX(checked_at)`,
		pq.Array(ids), start, end, int64(bucket.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]service.IntelligencePoint)
	for rows.Next() {
		var id int64
		var p service.IntelligencePoint
		if err := rows.Scan(&id, &p.CheckedAt, &p.Status, &p.LatencyMs); err != nil {
			return nil, err
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}
