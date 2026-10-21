//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAccountIntelligenceHistoryOnlyReturnsEnabledActiveOpenAI(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	enabled := sqlmock.NewRows([]string{"id"}).AddRow(int64(7)).AddRow(int64(8))
	mock.ExpectQuery(`SELECT DISTINCT a\.id`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(enabled)
	history := sqlmock.NewRows([]string{"account_id", "model", "checked_at", "status", "latency_ms"}).
		AddRow(int64(8), IntelligenceModel, now.Add(-time.Minute), "yellow", int64(1200)).
		AddRow(int64(8), IntelligenceModel, now, "red", int64(30000))
	mock.ExpectQuery(`FROM channel_monitor_account_intelligence_history`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(history)

	svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, db, "http://127.0.0.1")
	defer svc.Stop()
	views, err := svc.AccountHistory(context.Background(), []int64{7, 8})
	require.NoError(t, err)
	// One view per account and model, ordered by account then model.
	require.Len(t, views, 4)
	require.Equal(t, int64(7), views[0].AccountID)
	require.Equal(t, IntelligenceModel, views[0].Model)
	require.Equal(t, "unknown", views[0].Uptime.Status)
	require.Empty(t, views[0].Uptime.Points)
	require.Equal(t, "gpt-6.1-sol", views[1].Model)
	require.Equal(t, "unknown", views[1].Uptime.Status)

	require.Equal(t, int64(8), views[2].AccountID)
	require.Equal(t, IntelligenceModel, views[2].Model)
	require.Equal(t, "red", views[2].Uptime.Status)
	require.Len(t, views[2].Uptime.Points, 1)
	require.Equal(t, "yellow", views[2].Uptime.Points[0].Status)
	require.Equal(t, "gpt-6.1-sol", views[3].Model)
	require.NoError(t, mock.ExpectationsWereMet())
}
