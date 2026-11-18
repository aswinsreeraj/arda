package platformconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateValue(t *testing.T) {
	tests := []struct {
		key     string
		val     string
		want    string
		wantErr bool
	}{
		{KeyGatewayRateLimitDefault, "600", "600", false},
		{KeyGatewayRateLimitDefault, "5", "", true},
		{KeyGatewayRateLimitDefault, "not-a-num", "", true},
		{KeyCalibrationSamplingInterval, "0", "0", false},
		{KeyCalibrationSamplingInterval, "5", "5", false},
		{KeyCalibrationSamplingInterval, "-1", "", true},
		{KeyReliabilityDefaultThreshold, "0.75", "0.75", false},
		{KeyReliabilityDefaultThreshold, "1.5", "", true},
		{KeyRetrievalTopK, "8", "8", false},
		{KeyRetrievalTopK, "0", "", true},
		{KeyRetrievalTopK, "100", "", true},
		{KeyRuntimeCheckpointRetention, "14d", "14d", false},
		{KeyRuntimeCheckpointRetention, "14", "14d", false},
		{KeyRuntimeCheckpointRetention, "0d", "", true},
		{KeyRuntimeCheckpointRetention, "999d", "", true},
		{"unknown.key", "123", "", true},
	}

	for _, tc := range tests {
		got, err := ValidateValue(tc.key, tc.val)
		if tc.wantErr {
			assert.Error(t, err, "key=%s val=%s", tc.key, tc.val)
		} else {
			require.NoError(t, err, "key=%s val=%s", tc.key, tc.val)
			assert.Equal(t, tc.want, got)
		}
	}
}

func TestMemoryStore_UpdateAndSnapshot(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()

	items, changes, err := st.Update(ctx, map[string]string{
		KeyGatewayRateLimitDefault:     "600",
		KeyCalibrationSamplingInterval: "4",
		KeyReliabilityDefaultThreshold: "0.72",
		KeyRetrievalTopK:               "8",
		KeyRuntimeCheckpointRetention:  "14d",
	}, "admin@arda.security")
	require.NoError(t, err)
	assert.Len(t, items, 5)
	assert.Len(t, changes, 5)

	snap := st.GetSnapshot(ctx)
	assert.Equal(t, 600, snap.RateLimitPerMinute)
	assert.InDelta(t, 10.0, snap.RateLimitRPS, 0.001)
	assert.Equal(t, 20, snap.RateLimitBurst)
	assert.Equal(t, 4, snap.CalibrationSamplingInterval)
	assert.InDelta(t, 0.72, snap.DefaultReliabilityThreshold, 0.001)
	assert.Equal(t, 8, snap.RetrievalTopK)
	assert.Equal(t, 14, snap.RetentionDays)

	audits, err := st.ListAuditEvents(ctx, 20)
	require.NoError(t, err)
	assert.Len(t, audits, 5)
	assert.Equal(t, "admin@arda.security", audits[0].Operator)
}
