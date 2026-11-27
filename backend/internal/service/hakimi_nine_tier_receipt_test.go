package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

const testNinePeerID = "b57f02e6-53b8-4a85-9739-bc7f85bbeb27"

func nineTierTestInput(group int64, tier string) *OpenAIRecordUsageInput {
	r := &OpenAIForwardResult{UpstreamHeaders: http.Header{"X-Client-Request-Id": {testNinePeerID}}, UpstreamResponseServiceTier: "default"}
	if tier != "" {
		r.ServiceTier = &tier
	}
	return &OpenAIRecordUsageInput{Result: r, APIKey: &APIKey{GroupID: &group}, Account: &Account{ID: 1014, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "http://127.0.0.1:4947"}}, UpstreamEndpoint: "/v1/responses"}
}
func TestNineTierGroupBilling(t *testing.T) {
	for _, group := range []int64{101, 105} {
		for _, tc := range []struct {
			sent, receipt, issue, want string
			found                      bool
		}{
			{"", "priority", "", "priority", true}, {"default", "fast", "", "priority", true}, {"priority", "default", "", "default", true}, {"priority", "", "", "", true}, {"", "", "", "", true}, {"priority", "auto", "", "default", true}, {"priority", "invented", "", "default", false}, {"priority", "", "missing", "default", false}, {"", "", "database", "", false},
		} {
			in := nineTierTestInput(group, tc.sent)
			id, settled := reconcileNineTier(context.Background(), in, func() nineTierLookup {
				return func(_ context.Context, httpID, rid string) (nineTierReceipt, string) {
					require.Equal(t, "client:"+testNinePeerID, httpID)
					require.Empty(t, rid)
					return nineTierReceipt{httpID, tc.receipt}, tc.issue
				}
			})
			require.True(t, settled)
			require.Equal(t, tc.want, optionalStringValue(in.Result.ServiceTier))
			require.Equal(t, tc.found, id != "")
		}
	}
}
func TestNineTierScopeAndDisabled(t *testing.T) {
	for _, edit := range []func(*OpenAIRecordUsageInput){
		func(in *OpenAIRecordUsageInput) { *in.APIKey.GroupID = 138 },
		func(in *OpenAIRecordUsageInput) { in.Account.ID = 1015 },
		func(in *OpenAIRecordUsageInput) { in.Account.Type = AccountTypeOAuth },
		func(in *OpenAIRecordUsageInput) { in.Account.Credentials["base_url"] = "http://127.0.0.1:4947.evil" },
		func(in *OpenAIRecordUsageInput) { in.UpstreamEndpoint = "/v1/images/generations" },
		func(in *OpenAIRecordUsageInput) { in.Result.UpstreamEndpoint = "/v1/images/edits" },
		func(in *OpenAIRecordUsageInput) { in.CyberBlocked = true },
		func(in *OpenAIRecordUsageInput) { in.Result.ImageEndpoint = true },
		func(in *OpenAIRecordUsageInput) { in.UpstreamEndpoint = "/alpha/search"; in.Result.WebSearchCalls = 1 },
		func(in *OpenAIRecordUsageInput) { s := "ultrafast"; in.Result.ServiceTier = &s },
	} {
		in := nineTierTestInput(105, "priority")
		edit(in)
		prior := optionalStringValue(in.Result.ServiceTier)
		id, settled := reconcileNineTier(context.Background(), in, func() nineTierLookup { t.Fatal("unrelated usage invoked lookup"); return nil })
		require.False(t, settled)
		require.Empty(t, id)
		require.Equal(t, prior, optionalStringValue(in.Result.ServiceTier))
	}
	in := nineTierTestInput(101, "priority")
	id, settled := reconcileNineTier(context.Background(), in, func() nineTierLookup { return nil })
	require.False(t, settled)
	require.Empty(t, id)
	require.Equal(t, "priority", *in.Result.ServiceTier)
}
func TestNineTierChatHeadersAndToolRequests(t *testing.T) {
	for _, endpoint := range []string{"/v1/responses", "/v1/chat/completions"} {
		in := nineTierTestInput(105, "priority")
		in.UpstreamEndpoint = endpoint
		in.Result.WebSearchCalls = 1
		in.Result.UpstreamHeaders = http.Header{}
		in.Result.UpstreamHeaders.Set("X-Client-Request-ID", testNinePeerID)
		in.Result.UpstreamResponseServiceTier = "flex"
		got, ok := reconcileNineTier(context.Background(), in, func() nineTierLookup {
			return func(_ context.Context, id, resp string) (nineTierReceipt, string) {
				require.Equal(t, "client:"+testNinePeerID, id)
				return nineTierReceipt{}, "missing"
			}
		})
		require.True(t, ok)
		require.Empty(t, got)
		require.Equal(t, "flex", *in.Result.ServiceTier)
	}
}

func TestNineTierReceiptSources(t *testing.T) {
	r := nineTierTestInput(105, "").Result
	r.ResponseID = "resp_http"
	h, k := nineTierReceiptIDs(r)
	require.Equal(t, "client:"+testNinePeerID, h)
	require.Equal(t, "resp_http", k)
	r.OpenAIWSMode = true
	for _, response := range []string{"resp_turn_1", "resp_turn_2"} {
		r.ResponseID = response
		h, k = nineTierReceiptIDs(r)
		require.Empty(t, h)
		require.Equal(t, response, k)
	}
	r.ResponseID = ""
	r.RequestID = "resp_turn_fallback"
	h, k = nineTierReceiptIDs(r)
	require.Empty(t, h)
	require.Equal(t, "resp_turn_fallback", k)
	r.RequestID = "client:" + testNinePeerID
	h, k = nineTierReceiptIDs(r)
	require.Empty(t, h)
	require.Empty(t, k)
	r.OpenAIWSMode = false
	r.UpstreamHeaders.Add("X-Client-Request-Id", testNinePeerID)
	r.ResponseID = "resp_foo\nbar"
	h, k = nineTierReceiptIDs(r)
	require.Empty(t, h)
	require.Empty(t, k)
}
func TestNineTierReceiptDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	store := &nineTierLedger{db: db, slots: make(chan struct{}, 1)}
	id := "client:" + testNinePeerID
	q := func(rows *sqlmock.Rows) {
		mock.ExpectQuery("SELECT request_id").WithArgs(id, "resp_turn").WillReturnRows(rows)
	}
	newRows := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"request_id", "service_tier"}) }
	q(newRows())
	q(newRows().AddRow(id, "priority"))
	rec, reason := store.lookup(context.Background(), id, "resp_turn")
	require.Empty(t, reason)
	require.Equal(t, nineTierReceipt{id, "priority"}, rec)
	q(newRows().AddRow(id, "priority").AddRow("resp_turn", "default"))
	rec, reason = store.lookup(context.Background(), id, "resp_turn")
	require.Equal(t, "ambiguous", reason)
	require.Empty(t, rec.id)
	store.slots <- struct{}{}
	_, reason = store.lookup(context.Background(), id, "resp_turn")
	require.Equal(t, "capacity", reason)
	require.NoError(t, mock.ExpectationsWereMet())
}
