package iam

import (
	"encoding/json"
	"fmt"
)

func defaultPolicy(bucket string, bucketActions, objectActions []string) (string, error) {
	actions := append(append([]string(nil), objectActions...), bucketActions...)

	actionJSON, err := json.Marshal(actions)
	if err != nil {
		return "", fmt.Errorf("failed to serialize S3 actions: %w", err)
	}

	return fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [{
			"Effect": "Allow",
			"Action": %s,
			"Resource": ["arn:aws:s3:::%s", "arn:aws:s3:::%s/*"]
		}]
	}`, actionJSON, bucket, bucket), nil
}
