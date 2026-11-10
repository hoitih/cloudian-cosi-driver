package bucket

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Configuration struct {
	StoragePolicyID string
	ObjectLock      bool
	Versioning      types.BucketVersioningStatus
	Retention       *types.DefaultRetention
	Lifecycle       *types.BucketLifecycleConfiguration
}

func ParseConfiguration(parameters map[string]string) (Configuration, error) {
	configuration := Configuration{StoragePolicyID: parameters["storagePolicyID"]}

	objectLock, err := parseObjectLock(parameters)
	if err != nil {
		return Configuration{}, err
	}

	configuration.ObjectLock = objectLock

	configuration.Versioning, err = parseVersioning(parameters, objectLock)
	if err != nil {
		return Configuration{}, err
	}

	retention, err := parseRetention(parameters, objectLock)
	if err != nil {
		return Configuration{}, err
	}

	if retention.Mode != "" {
		configuration.Retention = &retention
	}

	if value, exists := parameters["lifecyclePolicy"]; exists {
		configuration.Lifecycle, err = parseLifecycle(value)
		if err != nil {
			return Configuration{}, err
		}
	}

	return configuration, nil
}

func parseObjectLock(parameters map[string]string) (bool, error) {
	value, exists := parameters["objectLockEnabled"]
	if !exists {
		return false, nil
	}

	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("objectLockEnabled must be true or false")
	}
}

func parseVersioning(parameters map[string]string, objectLock bool) (types.BucketVersioningStatus, error) {
	var versioning types.BucketVersioningStatus

	if value, exists := parameters["versioning"]; exists {
		if value != string(types.BucketVersioningStatusEnabled) && value != string(types.BucketVersioningStatusSuspended) {
			return "", errors.New("versioning must be Enabled or Suspended")
		}

		versioning = types.BucketVersioningStatus(value)
	}

	if objectLock {
		if versioning == types.BucketVersioningStatusSuspended {
			return "", errors.New("object lock requires enabled versioning")
		}

		versioning = types.BucketVersioningStatusEnabled
	}

	return versioning, nil
}

func parseRetention(parameters map[string]string, objectLock bool) (types.DefaultRetention, error) {
	mode, hasMode := parameters["retentionMode"]
	_, hasDays := parameters["retentionDays"]
	_, hasYears := parameters["retentionYears"]

	if !hasMode && !hasDays && !hasYears {
		return types.DefaultRetention{}, nil
	}

	if !objectLock {
		return types.DefaultRetention{}, errors.New("retention requires objectLockEnabled=true")
	}

	if mode != string(types.ObjectLockRetentionModeGovernance) && mode != string(types.ObjectLockRetentionModeCompliance) {
		return types.DefaultRetention{}, errors.New("retentionMode must be GOVERNANCE or COMPLIANCE")
	}

	if hasDays == hasYears {
		return types.DefaultRetention{}, errors.New("retention requires exactly one of retentionDays or retentionYears")
	}

	return parseRetentionPeriod(parameters, types.ObjectLockRetentionMode(mode), hasYears)
}

func parseRetentionPeriod(parameters map[string]string, mode types.ObjectLockRetentionMode, hasYears bool) (types.DefaultRetention, error) {
	parameter := "retentionDays"
	if hasYears {
		parameter = "retentionYears"
	}

	period, err := strconv.ParseInt(parameters[parameter], 10, 32)
	if err != nil || period <= 0 {
		return types.DefaultRetention{}, fmt.Errorf("%s must be a positive 32-bit integer", parameter)
	}

	value := int32(period)
	retention := types.DefaultRetention{Mode: mode}

	if hasYears {
		retention.Years = &value
	} else {
		retention.Days = &value
	}

	return retention, nil
}

func parseLifecycle(value string) (*types.BucketLifecycleConfiguration, error) {
	var lifecycle types.BucketLifecycleConfiguration

	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&lifecycle); err != nil {
		return nil, fmt.Errorf("invalid lifecyclePolicy JSON: %w", err)
	}

	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("lifecyclePolicy must contain exactly one JSON object")
	}

	if len(lifecycle.Rules) == 0 {
		return nil, errors.New("lifecyclePolicy must contain at least one rule")
	}

	for _, rule := range lifecycle.Rules {
		if err := validateLifecycleRule(rule); err != nil {
			return nil, err
		}
	}

	return &lifecycle, nil
}

func validateLifecycleRule(rule types.LifecycleRule) error {
	if rule.Status != types.ExpirationStatusEnabled && rule.Status != types.ExpirationStatusDisabled {
		return errors.New("each lifecycle rule must have Status Enabled or Disabled")
	}

	if rule.Filter == nil {
		return errors.New("each lifecycle rule must specify Filter")
	}

	if rule.Expiration == nil && rule.NoncurrentVersionExpiration == nil && rule.AbortIncompleteMultipartUpload == nil &&
		len(rule.Transitions) == 0 && len(rule.NoncurrentVersionTransitions) == 0 {
		return errors.New("each lifecycle rule must specify an action")
	}

	return nil
}
