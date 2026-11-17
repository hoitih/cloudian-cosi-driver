# Cloudian COSI Driver

This repository contains the Cloudian [Container Object Storage Interface (COSI)](https://github.com/kubernetes-sigs/container-object-storage-interface) driver, compatible with a HyperStore backend.

# Getting Started

For installation and image compatibility requirements, see [Quick Start](docs/quick-start.md). This branch targets COSI v1alpha1 using the v0.2.2 proto/controller/sidecar release and requires an explicit compatible sidecar image.

Bucket creation supports HyperStore storage policy selection, S3 versioning, Object Lock with default retention, and lifecycle rules for current and noncurrent versions. Configure these through `BucketClass.parameters` and select the class from a BucketClaim. See [Bucket Configuration Parameters](docs/quick-start.md#bucket-configuration-parameters) for parameter names, examples, permissions, and retention/deletion caveats.

Retention protects object versions from permanent deletion; it does not delete them when the retention period ends. Lifecycle rules schedule cleanup and, with versioning enabled, must also address noncurrent versions and delete markers. See the [Protection And Cleanup Example](docs/quick-start.md#protection-and-cleanup-example) for a 30-day retention policy with lifecycle cleanup and an explanation of the timing. Object Lock is not reversible, and retained data can prevent bucket deletion even when its BucketClass uses `deletionPolicy: Delete`.

## COSI v1alpha1 compatibility

This branch preserves the improvements from the v1alpha2 development branch while serving only the `cosi.v1alpha1` gRPC services. Do not deploy it with a v1alpha2 sidecar or v1alpha2-only CRDs. The Go proto module is pinned to `v0.2.2`, which implements the v1alpha1 wire protocol despite the module's release number.

The v1alpha1 contract differs from v1alpha2:

- Bucket creation uses the name in `DriverCreateBucket`; there are no `DriverGenerateBucketId` or `DriverGetBucket` RPCs. Existing-bucket imports are handled by the v1alpha1 sidecar rather than by a driver lookup RPC.
- `BucketClass` and `BucketAccessClass` fields are at the resource's top level, not under `spec`. Retention, lifecycle, Object Lock, and versioning remain configurable through `BucketClass.parameters`.
- `BucketAccess.spec` selects one `bucketClaimName` and a `credentialsSecretName`. The v1alpha1 sidecar writes its `BucketInfo` JSON document into that Secret.
- `BucketAccessClass.authenticationType: IAM` creates IAM users and S3 access keys. `Key` creates HyperStore users and requires the admin client. The choice is explicit: configuring admin credentials does not change an IAM request into a Key request. Although the v1alpha1 sidecar requires a `serviceAccountName` for IAM requests, this driver issues keys; it does not implement Kubernetes token federation or automatic workload identity.
- Native v1alpha2 access modes are unavailable. Granular read/write permissions are retained as driver-specific `BucketAccessClass.parameters`: `objectDataAccessMode`, `objectMetadataAccessMode`, and `bucketMetadataAccessMode`. Without them, the original object-data read/write policy is preserved.

Idempotent deletion, partial-failure retries, credential-safe driver logging, parameter validation, S3 HTTP tests, and Helm namespace/RBAC/socket-security improvements are retained. Finalizer-removal retry changes remain outside the driver and are not included. See the [Quick Start](docs/quick-start.md) for v1alpha1 manifests and deployment requirements.

# Developing

The repository includes a dev container, inside which it can be built and tested.

Building requires `golang` v1.25 or later, `docker`, and `make` to be installed.

### Building

To build the code and an up to date image:
```
make image
```

Pushes to `feat/v1alpha1_improvements` build and publish `ghcr.io/hoitih/cloudian-cosi-driver`, tagged with both the short commit ID and `feat-v1alpha1-improvements`. The workflow authenticates with `GITHUB_TOKEN` and requires no additional registry secrets.

### Testing

To build a dev version of the image, then bring up a k3d test environment and load this image see `test/deploy.sh`.

Run `make test-helm` to validate chart rendering, sidecar settings, release-namespace wiring, and RBAC. These tests require Helm and do not deploy anything to a cluster.
