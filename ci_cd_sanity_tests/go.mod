module github.com/LeanerCloud/cloud-commitments-go/ci_cd_sanity_tests

go 1.26.6

require (
	github.com/aws/aws-sdk-go-v2 v1.41.5
	github.com/aws/aws-sdk-go-v2/config v1.29.12
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.251.2
	github.com/aws/aws-sdk-go-v2/service/rds v1.97.3
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.21.1
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.13.1
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.12.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5 v5.4.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions v1.3.0
	github.com/AzureAD/microsoft-authentication-library-for-go v1.6.0 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.17.65 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.16.30 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.21 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.21 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.25.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.30.0 // indirect
	github.com/aws/smithy-go v1.24.2 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources v1.2.0
	github.com/LeanerCloud/cloud-commitments-go/pkg v0.0.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.33.17
)

require github.com/google/uuid v1.6.0 // indirect

replace github.com/LeanerCloud/cloud-commitments-go/pkg => ../pkg

replace github.com/LeanerCloud/cloud-commitments-go/providers/aws => ../providers/aws

replace github.com/LeanerCloud/cloud-commitments-go/providers/azure => ../providers/azure

replace github.com/LeanerCloud/cloud-commitments-go/providers/gcp => ../providers/gcp
