package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateGroupRequestLimitFieldsTriState(t *testing.T) {
	t.Run("omitted means unchanged", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{}`), &req))
		require.Nil(t, req.DailyLimitCredits.ToServiceInput())
		require.Nil(t, req.WeeklyLimitCredits.ToServiceInput())
		require.Nil(t, req.MonthlyLimitCredits.ToServiceInput())
	})

	t.Run("null means unlimited", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"daily_limit_credits":null}`), &req))
		limit := req.DailyLimitCredits.ToServiceInput()
		require.NotNil(t, limit)
		require.Negative(t, *limit)
	})

	t.Run("number is preserved", func(t *testing.T) {
		var req UpdateGroupRequest
		require.NoError(t, json.Unmarshal([]byte(`{"weekly_limit_credits":0,"monthly_limit_credits":42.5}`), &req))
		require.Equal(t, 0.0, *req.WeeklyLimitCredits.ToServiceInput())
		require.Equal(t, 42.5, *req.MonthlyLimitCredits.ToServiceInput())
	})
}
