package key

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func defaultPolicy(canonicalUserID, bucket string, bucketActions, objectActions []string) (string, error) {
	statement, err := defaultPolicyStatement(canonicalUserID, bucket, bucketActions, objectActions)
	if err != nil {
		return "", err
	}

	return `{
		"Version": "2012-10-17",
		"Statement": [` + statement + `]
	}`, nil
}

func defaultPolicyStatement(canonicalUserID, bucket string, bucketActions, objectActions []string) (string, error) {
	actions := append(append([]string(nil), objectActions...), bucketActions...)

	actionJSON, err := json.Marshal(actions)
	if err != nil {
		return "", fmt.Errorf("failed to serialize S3 actions: %w", err)
	}

	return fmt.Sprintf(`{
		"Effect": "Allow",
		"Principal": {
        	"CanonicalUser": "%s"
      	},
		"Action": %s,
		"Resource": [
			"arn:aws:s3:::%s",
			"arn:aws:s3:::%s/*"
		]
	}`, canonicalUserID, actionJSON, bucket, bucket), nil
}

func updateBucketPolicy(ctx context.Context, client PolicyCreator, policy *s3.GetBucketPolicyOutput, statement, canonicalUser, bucket string) error {
	var policyJSON map[string]interface{}

	err := json.Unmarshal([]byte(*policy.Policy), &policyJSON)
	if err != nil {
		return fmt.Errorf("failed to parse bucket policy JSON: %w", err)
	}

	err = removePolicyStatementsForUser(policyJSON, canonicalUser)
	if err != nil {
		return fmt.Errorf("failed to remove pre-existing policy statements for user '%s': %w", canonicalUser, err)
	}

	err = addStatementToPolicy(policyJSON, statement)
	if err != nil {
		return fmt.Errorf("failed to add statement to policy: %w", err)
	}

	updatedPolicy, err := json.Marshal(policyJSON)
	if err != nil {
		return fmt.Errorf("failed to marshal updated policy: %w", err)
	}

	updatedPolicyStr := string(updatedPolicy)

	putParams := &s3.PutBucketPolicyInput{
		Bucket: &bucket,
		Policy: &updatedPolicyStr,
	}

	if _, err := client.PutBucketPolicy(ctx, putParams); err != nil {
		return fmt.Errorf("failed to put bucket policy: %w", err)
	}

	return nil
}

func removePolicyStatementsForUser(policy map[string]interface{}, user string) error {
	statements, ok := policy["Statement"].([]interface{})
	if !ok {
		return errors.New("failed to assert policy statements as type []interface{}")
	}

	updatedStatements := make([]interface{}, 0, len(statements))

	for _, statement := range statements {
		stmt, ok := statement.(map[string]interface{})
		if !ok {
			return errors.New("failed to assert policy statement as type map[string]interface{}")
		}

		if principal, exists := stmt["Principal"]; exists { //nolint:nestif
			if principalMap, ok := principal.(map[string]interface{}); ok {
				if canonicalUser, exists := principalMap["CanonicalUser"]; exists {
					canonicalUserString, ok := canonicalUser.(string)
					if !ok {
						return errors.New("failed to assert CanonicalUser as type string")
					}

					if strings.TrimSpace(canonicalUserString) == user {
						continue
					}
				}
			}
		}

		updatedStatements = append(updatedStatements, stmt)
	}

	policy["Statement"] = updatedStatements

	return nil
}

func addStatementToPolicy(policy map[string]interface{}, statement string) error {
	var statementJSON map[string]interface{}

	err := json.Unmarshal([]byte(statement), &statementJSON)
	if err != nil {
		return fmt.Errorf("failed to parse policy statement JSON: %w", err)
	}

	statements, ok := policy["Statement"].([]interface{})
	if !ok {
		return errors.New("failed to assert policy statements as type []interface{}")
	}

	policy["Statement"] = append(statements, statementJSON)

	return nil
}
