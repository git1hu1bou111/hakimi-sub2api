package repository

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestGetLegacyGrokSafetyRejectionFee(t *testing.T) {
	for _, tt := range []struct {
		name string
		rows *sqlmock.Rows
		err  error
		want float64
	}{
		{name: "configured", rows: sqlmock.NewRows([]string{"fee"}).AddRow(0.10), want: 0.10},
		{name: "disabled", rows: sqlmock.NewRows([]string{"fee"}).AddRow(0.0)},
		{name: "missing or deleted group", rows: sqlmock.NewRows([]string{"fee"})},
		{name: "query error", err: errors.New("database unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			expect := mock.ExpectQuery(`(?s)SELECT COALESCE\(\(to_jsonb\(g\).*FROM groups AS g WHERE g.id = \$1 AND g.deleted_at IS NULL`).WithArgs(int64(85))
			if tt.err != nil {
				expect.WillReturnError(tt.err)
			} else {
				expect.WillReturnRows(tt.rows)
			}
			repo := &settingRepository{client: ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
			fee, err := repo.GetLegacyGrokSafetyRejectionFee(context.Background(), 85)
			require.Equal(t, tt.err != nil, err != nil)
			require.Equal(t, tt.want, fee)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
