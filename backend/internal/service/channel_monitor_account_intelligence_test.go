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
	rows := sqlmock.NewRows([]string{"id", "id", "name", "checked_at", "status", "latency_ms"}).
		AddRow(int64(7), int64(105), "OpenAI", nil, nil, nil).
		AddRow(int64(8), int64(105), "OpenAI", now.Add(-time.Minute), "yellow", int64(1200)).
		AddRow(int64(8), int64(105), "OpenAI", now, "red", int64(30000))
	mock.ExpectQuery(`g\.platform='openai'[\s\S]*g\.account_intelligence_enabled=TRUE[\s\S]*a\.status='active'`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(rows)

	svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, db, "http://127.0.0.1")
	defer svc.Stop()
	views, err := svc.AccountHistory(context.Background(), []int64{7, 8})
	require.NoError(t, err)
	require.Len(t, views, 2)
	require.Equal(t, "unknown", views[0].Uptime.Status)
	require.Empty(t, views[0].Uptime.Points)
	require.Equal(t, "red", views[1].Uptime.Status)
	require.Equal(t, int64(8), views[1].AccountID)
	require.Equal(t, int64(105), views[1].GroupID)
	require.Len(t, views[1].Uptime.Points, 1)
	require.Equal(t, "yellow", views[1].Uptime.Points[0].Status)
	require.NoError(t, mock.ExpectationsWereMet())
}
