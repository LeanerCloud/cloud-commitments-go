module github.com/LeanerCloud/CUDly/providers/gcp

go 1.22

toolchain go1.24.4

require (
	cloud.google.com/go/billing v1.18.2
	cloud.google.com/go/compute v1.23.3
	cloud.google.com/go/recommender v1.12.0
	cloud.google.com/go/resourcemanager v1.9.0
	github.com/LeanerCloud/CUDly/pkg v0.0.0
	google.golang.org/api v0.156.0
)

replace github.com/LeanerCloud/CUDly/pkg => ../../pkg
