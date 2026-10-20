# Cloudian COSI Driver

This repository contains the Cloudian [Container Object Storage Interface (COSI)](https://github.com/kubernetes-sigs/container-object-storage-interface) driver, compatible with a HyperStore backend.

# Getting Started

To deploy the pre-built containers, see [Quick Start](docs/quick-start.md).

## COSI v1alpha2 compatibility

This driver targets COSI `v1alpha2` only. It does not register the `v1alpha1` gRPC services, so the COSI API/controller and this driver must be upgraded together. The Quick Start pins the COSI manifests to the upstream commit used by the driver's protobuf dependency; keep those versions aligned because `v1alpha2` is still pre-release.

The migration changes both the driver RPC flow and the Kubernetes resource schema:

- Bucket provisioning now uses `DriverGenerateBucketId` before `DriverCreateBucket`. `DriverGetBucket` is also implemented for existing buckets.
- `BucketClass` and `BucketAccessClass` configuration is under `spec` in `v1alpha2`.
- `BucketAccess` now specifies a `protocol` and a `bucketClaims` list. Each entry supplies `accessModes` and an `accessSecretName`; the generated Secret contains the S3 bucket information and credentials.
- The driver advertises and supports S3 only. The `IAM` option from this driver's `v1alpha1` integration provisioned IAM users and access keys; in `v1alpha2`, that same workflow is requested as `Key` because the API has no separate `IAM` authentication type. HyperStore IAM creates the users and access keys by default. If system-admin credentials are configured, the driver creates HyperStore users instead. COSI `ServiceAccount` authentication is a different mode, based on Kubernetes ServiceAccounts, and is not supported.
- A `BucketAccess` may reference exactly one bucket. Multi-bucket requests are rejected. Object data, object metadata, and bucket metadata access modes are translated into S3 policy actions.

When upgrading existing installations, update the COSI CRDs/controller and convert or recreate alpha1 resources using the `v1alpha2` schemas. Changing only `apiVersion` is not sufficient because fields and access semantics have changed. See the [Quick Start](docs/quick-start.md) for current manifests and examples.

# Developing

The repository includes a dev container, inside which it can be built and tested.

Building requires `golang` v1.23 or later, `docker`, and `make` to be installed.

### Building

To build the code and an up to date image:
```
make image
```

### Testing

To build a dev version of the image, then bring up a k3d test environment and load this image see `test/deploy.sh`.
