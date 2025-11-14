module github.com/LeanerCloud/CUDly/providers/aws

go 1.22

toolchain go1.24.4

require (
	github.com/aws/aws-sdk-go-v2 v1.39.2
	github.com/aws/aws-sdk-go-v2/config v1.26.2
	github.com/aws/aws-sdk-go-v2/service/costexplorer v1.51.2
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.251.2
	github.com/aws/aws-sdk-go-v2/service/elasticache v1.50.3
	github.com/aws/aws-sdk-go-v2/service/memorydb v1.31.4
	github.com/aws/aws-sdk-go-v2/service/opensearch v1.52.3
	github.com/aws/aws-sdk-go-v2/service/organizations v1.45.3
	github.com/aws/aws-sdk-go-v2/service/rds v1.97.3
	github.com/aws/aws-sdk-go-v2/service/redshift v1.58.3
	github.com/aws/aws-sdk-go-v2/service/savingsplans v1.24.2
	github.com/aws/aws-sdk-go-v2/service/sts v1.26.6
	github.com/LeanerCloud/CUDly/pkg v0.0.0
)

replace github.com/LeanerCloud/CUDly/pkg => ../../pkg
