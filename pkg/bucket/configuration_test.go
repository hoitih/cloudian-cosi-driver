package bucket

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

func TestParseConfiguration(t *testing.T) {
	t.Parallel()

	configuration, err := ParseConfiguration(map[string]string{
		"storagePolicyID":   "policy-1",
		"objectLockEnabled": "true",
		"retentionMode":     "COMPLIANCE",
		"retentionDays":     "30",
		"lifecyclePolicy": `{"Rules":[{"ID":"expire","Status":"Enabled","Filter":{"Prefix":""},
			"Expiration":{"Days":30},"NoncurrentVersionExpiration":{"NoncurrentDays":1}}]}`,
	})
	require.NoError(t, err)
	require.Equal(t, "policy-1", configuration.StoragePolicyID)
	require.True(t, configuration.ObjectLock)
	require.Equal(t, types.BucketVersioningStatusEnabled, configuration.Versioning)
	require.Equal(t, types.ObjectLockRetentionModeCompliance, configuration.Retention.Mode)
	require.EqualValues(t, 30, *configuration.Retention.Days)
	require.Nil(t, configuration.Retention.Years)
	require.Len(t, configuration.Lifecycle.Rules, 1)
	require.EqualValues(t, 1, *configuration.Lifecycle.Rules[0].NoncurrentVersionExpiration.NoncurrentDays)
}

func TestParseConfigurationDefaults(t *testing.T) {
	t.Parallel()

	configuration, err := ParseConfiguration(nil)
	require.NoError(t, err)
	require.Equal(t, Configuration{}, configuration)

	configuration, err = ParseConfiguration(map[string]string{"versioning": "Suspended", "objectLockEnabled": "false"})
	require.NoError(t, err)
	require.False(t, configuration.ObjectLock)
	require.Equal(t, types.BucketVersioningStatusSuspended, configuration.Versioning)

	configuration, err = ParseConfiguration(map[string]string{
		"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionYears": "2",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, *configuration.Retention.Years)
	require.Equal(t, types.ObjectLockRetentionModeGovernance, configuration.Retention.Mode)
}

func TestParseConfigurationVersioning(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		parameters map[string]string
		wantStatus types.BucketVersioningStatus
		wantLock   bool
		wantError  string
	}{
		{name: "omitted"},
		{name: "enabled", parameters: map[string]string{"versioning": "Enabled"}, wantStatus: types.BucketVersioningStatusEnabled},
		{name: "suspended", parameters: map[string]string{"versioning": "Suspended"}, wantStatus: types.BucketVersioningStatusSuspended},
		{
			name: "object lock implies enabled", parameters: map[string]string{"objectLockEnabled": "true"},
			wantStatus: types.BucketVersioningStatusEnabled, wantLock: true,
		},
		{
			name: "object lock with explicit enabled", parameters: map[string]string{"objectLockEnabled": "true", "versioning": "Enabled"},
			wantStatus: types.BucketVersioningStatusEnabled, wantLock: true,
		},
		{
			name: "no object lock with suspended", parameters: map[string]string{"objectLockEnabled": "false", "versioning": "Suspended"},
			wantStatus: types.BucketVersioningStatusSuspended,
		},
		{
			name: "object lock rejects suspended", parameters: map[string]string{"objectLockEnabled": "true", "versioning": "Suspended"},
			wantError: "object lock requires enabled versioning",
		},
		{name: "empty", parameters: map[string]string{"versioning": ""}, wantError: "versioning must be Enabled or Suspended"},
		{name: "lowercase enabled", parameters: map[string]string{"versioning": "enabled"}, wantError: "versioning must be Enabled or Suspended"},
		{name: "lowercase suspended", parameters: map[string]string{"versioning": "suspended"}, wantError: "versioning must be Enabled or Suspended"},
		{name: "disabled is not suspended", parameters: map[string]string{"versioning": "Disabled"}, wantError: "versioning must be Enabled or Suspended"},
		{name: "boolean is not a status", parameters: map[string]string{"versioning": "true"}, wantError: "versioning must be Enabled or Suspended"},
		{name: "whitespace", parameters: map[string]string{"versioning": " Enabled "}, wantError: "versioning must be Enabled or Suspended"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			configuration, err := ParseConfiguration(tc.parameters)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, configuration.Versioning)
			require.Equal(t, tc.wantLock, configuration.ObjectLock)
		})
	}
}

func TestParseConfigurationRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		parameters map[string]string
	}{
		{name: "invalid lock", parameters: map[string]string{"objectLockEnabled": "yes"}},
		{name: "empty lock", parameters: map[string]string{"objectLockEnabled": ""}},
		{name: "invalid versioning", parameters: map[string]string{"versioning": "false"}},
		{name: "suspended locked bucket", parameters: map[string]string{"objectLockEnabled": "true", "versioning": "Suspended"}},
		{name: "retention without lock", parameters: map[string]string{"retentionMode": "COMPLIANCE", "retentionDays": "30"}},
		{name: "invalid mode", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "invalid", "retentionDays": "30"}},
		{name: "missing duration", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE"}},
		{name: "both durations", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "30", "retentionYears": "1"}},
		{name: "negative duration", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "-1"}},
		{name: "zero duration", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "0"}},
		{name: "invalid duration", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "abc"}},
		{name: "overflow", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "2147483648"}},
		{name: "missing mode", parameters: map[string]string{"objectLockEnabled": "true", "retentionDays": "30"}},
		{name: "invalid JSON", parameters: map[string]string{"lifecyclePolicy": "{"}},
		{name: "unknown JSON fields", parameters: map[string]string{"lifecyclePolicy": `{"Rulez":[]}`}},
		{name: "multiple JSON values", parameters: map[string]string{"lifecyclePolicy": `{} {}`}},
		{name: "trailing garbage", parameters: map[string]string{"lifecyclePolicy": `{} garbage`}},
		{name: "empty rules", parameters: map[string]string{"lifecyclePolicy": `{"Rules":[]}`}},
		{name: "null", parameters: map[string]string{"lifecyclePolicy": `null`}},
		{name: "missing status", parameters: map[string]string{"lifecyclePolicy": `{"Rules":[{"Filter":{"Prefix":""},"Expiration":{"Days":30}}]}`}},
		{name: "missing filter", parameters: map[string]string{"lifecyclePolicy": `{"Rules":[{"Status":"Enabled","Expiration":{"Days":30}}]}`}},
		{name: "missing action", parameters: map[string]string{"lifecyclePolicy": `{"Rules":[{"Status":"Enabled","Filter":{"Prefix":""}}]}`}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseConfiguration(tc.parameters)
			require.Error(t, err)
		})
	}
}
