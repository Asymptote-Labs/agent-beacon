package cmd

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

const s3InlinePolicyName = "BeaconCloudTraceUpload"

type awsAccessKey struct {
	AccessKey struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
	} `json:"AccessKey"`
}

func runCloudS3Setup(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(cloudOpts.bucket) == "" {
		return fmt.Errorf("--bucket is required")
	}
	if strings.TrimSpace(cloudOpts.region) == "" {
		return fmt.Errorf("--region is required")
	}
	if strings.TrimSpace(cloudOpts.iamUser) == "" {
		return fmt.Errorf("--iam-user is required")
	}
	if !cloudOpts.apply && !cloudOpts.printOnly {
		return fmt.Errorf("choose --print to review commands or --apply to run them")
	}
	commands, err := s3SetupCommands(cloudOpts.bucket, cloudOpts.region, cloudOpts.prefix, cloudOpts.iamUser)
	if err != nil {
		return err
	}
	if cloudOpts.printOnly {
		for _, command := range commands {
			fmt.Println(shellCommand(command...))
		}
	}
	if !cloudOpts.apply {
		return nil
	}
	if err := ensureS3Bucket(cloudOpts.bucket, cloudOpts.region); err != nil {
		return err
	}
	if err := runAWS(s3PutPublicAccessBlockCommand(cloudOpts.bucket, cloudOpts.region)...); err != nil {
		return err
	}
	if err := ensureIAMUser(cloudOpts.iamUser); err != nil {
		return err
	}
	policy, err := s3UploadPolicy(cloudOpts.bucket, cloudOpts.prefix)
	if err != nil {
		return err
	}
	if err := runAWS("aws", "iam", "put-user-policy", "--user-name", cloudOpts.iamUser, "--policy-name", s3InlinePolicyName, "--policy-document", policy); err != nil {
		return err
	}
	if cloudOpts.printEnv {
		key, err := createIAMAccessKey(cloudOpts.iamUser)
		if err != nil {
			return err
		}
		fmt.Printf("BEACON_CLOUD_UPLOAD=s3\n")
		fmt.Printf("BEACON_CLOUD_S3_BUCKET=%s\n", cloudOpts.bucket)
		fmt.Printf("BEACON_CLOUD_S3_PREFIX=%s\n", strings.Trim(cloudOpts.prefix, "/"))
		fmt.Printf("BEACON_CLOUD_S3_REGION=%s\n", cloudOpts.region)
		fmt.Printf("AWS_ACCESS_KEY_ID=%s\n", key.AccessKey.AccessKeyID)
		fmt.Printf("AWS_SECRET_ACCESS_KEY=%s\n", key.AccessKey.SecretAccessKey)
	}
	return nil
}

func s3SetupCommands(bucket, region, prefix, iamUser string) ([][]string, error) {
	policy, err := s3UploadPolicy(bucket, prefix)
	if err != nil {
		return nil, err
	}
	commands := [][]string{
		{"aws", "s3api", "head-bucket", "--bucket", bucket},
		s3CreateBucketCommand(bucket, region),
		s3PutPublicAccessBlockCommand(bucket, region),
		{"aws", "iam", "get-user", "--user-name", iamUser},
		{"aws", "iam", "create-user", "--user-name", iamUser},
		{"aws", "iam", "put-user-policy", "--user-name", iamUser, "--policy-name", s3InlinePolicyName, "--policy-document", policy},
		{"aws", "iam", "create-access-key", "--user-name", iamUser, "--output", "json"},
	}
	return commands, nil
}

func s3CreateBucketCommand(bucket, region string) []string {
	args := []string{"aws", "s3api", "create-bucket", "--bucket", bucket, "--region", region}
	if region != "us-east-1" {
		args = append(args, "--create-bucket-configuration", "LocationConstraint="+region)
	}
	return args
}

func s3PutPublicAccessBlockCommand(bucket, region string) []string {
	return []string{"aws", "s3api", "put-public-access-block", "--bucket", bucket, "--region", region, "--public-access-block-configuration", "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true"}
}

func s3UploadPolicy(bucket, prefix string) (string, error) {
	prefix = strings.Trim(prefix, "/")
	resource := "arn:aws:s3:::" + bucket + "/*"
	if prefix != "" {
		resource = "arn:aws:s3:::" + bucket + "/" + prefix + "/*"
	}
	policy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect":   "Allow",
				"Action":   []string{"s3:PutObject"},
				"Resource": resource,
			},
		},
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func ensureS3Bucket(bucket, region string) error {
	describe := []string{"aws", "s3api", "head-bucket", "--bucket", bucket}
	output, err := runAWSCommand(describe...)
	if err == nil {
		return nil
	}
	if !isNotFoundOutput(string(output)) {
		return fmt.Errorf("%s failed: %w\n%s", shellCommand(describe...), err, strings.TrimSpace(string(output)))
	}
	return runAWS(s3CreateBucketCommand(bucket, region)...)
}

func ensureIAMUser(user string) error {
	describe := []string{"aws", "iam", "get-user", "--user-name", user}
	output, err := runAWSCommand(describe...)
	if err == nil {
		return nil
	}
	if !isNotFoundOutput(string(output)) {
		return fmt.Errorf("%s failed: %w\n%s", shellCommand(describe...), err, strings.TrimSpace(string(output)))
	}
	return runAWS("aws", "iam", "create-user", "--user-name", user)
}

func createIAMAccessKey(user string) (awsAccessKey, error) {
	var key awsAccessKey
	args := []string{"aws", "iam", "create-access-key", "--user-name", user, "--output", "json"}
	output, err := runAWSCommand(args...)
	if err != nil {
		return key, fmt.Errorf("%s failed: %w\n%s", shellCommand(args...), err, strings.TrimSpace(string(output)))
	}
	if err := json.Unmarshal(output, &key); err != nil {
		return key, fmt.Errorf("parse AWS access key response: %w", err)
	}
	if key.AccessKey.AccessKeyID == "" || key.AccessKey.SecretAccessKey == "" {
		return key, fmt.Errorf("AWS access key response missing credentials")
	}
	return key, nil
}

func runAWS(args ...string) error {
	output, err := runAWSCommand(args...)
	if err != nil {
		text := strings.TrimSpace(string(output))
		if isAlreadyExistsOutput(text) {
			return nil
		}
		return fmt.Errorf("%s failed: %w\n%s", shellCommand(args...), err, text)
	}
	return nil
}

func runAWSCommand(args ...string) ([]byte, error) {
	return exec.Command(args[0], args[1:]...).CombinedOutput()
}
