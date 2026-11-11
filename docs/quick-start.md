
# Getting Started

## Dependencies

`kubectl` compatible with your Kubernetes cluster. Use the COSI v1alpha1 CRDs, controller, and sidecar from the pinned `v0.2.2` release.

`helm` - v3.8+ due to support for OCI registries

## Install

1. Prepare compatible images. This branch uses the COSI `v0.2.2` proto module, which implements the `v1alpha1` API. Use controller and sidecar builds from that release. The chart intentionally requires `sidecar.image.repository` and `sidecar.image.tag`; v1alpha2 sidecar builds are not compatible. A tag alone does not prove compatibility: verify the source revision of any pre-built image you use.

For example, build and publish your own controller and sidecar images, replacing `registry.example.com` with your registry:

```sh
git clone --branch v0.2.2 --depth 1 https://github.com/kubernetes-sigs/container-object-storage-interface.git cosi-v1alpha1
docker build -f cosi-v1alpha1/controller/Dockerfile -t registry.example.com/cosi-controller:v0.2.2 cosi-v1alpha1
docker build -f cosi-v1alpha1/sidecar/Dockerfile -t registry.example.com/cosi-sidecar:v0.2.2 cosi-v1alpha1
docker push registry.example.com/cosi-controller:v0.2.2
docker push registry.example.com/cosi-sidecar:v0.2.2
```

2. Deploy the pinned COSI CRDs and controller with your controller image override. The driver chart installs neither the CRDs nor the central controller. Use a Kustomize overlay such as `cosi-install/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/kubernetes-sigs/container-object-storage-interface//?ref=v0.2.2
images:
  - name: gcr.io/k8s-staging-sig-storage/objectstorage-controller
    newName: registry.example.com/cosi-controller
    newTag: "v0.2.2"
```

```sh
kubectl apply -k cosi-install
```

3. Build and publish the driver from this branch. The chart's `appVersion` remains the development image tag `v0.0.0`; it does not identify a published driver release. For example, from this repository root:

```sh
make image
docker tag cloudian-cosi-driver:v0.0.0 registry.example.com/cloudian-cosi-driver:v1alpha1-improvements
docker push registry.example.com/cloudian-cosi-driver:v1alpha1-improvements
```

4. Configure your HyperStore endpoints and credentials using the chart's [values.yaml](../helm/cosi-driver/values.yaml). In your installation overrides, set the images you prepared:

```yaml
image:
  repository: registry.example.com/cloudian-cosi-driver
  tag: "v1alpha1-improvements"
sidecar:
  image:
    repository: registry.example.com/cosi-sidecar
    tag: "v0.2.2"
```

All registry and tag values above are examples for images you build; they are not published image locations. Make sure your cluster can pull them. The default S3 credentials in the values file are placeholders and must be replaced, or use `existingCredentialsSecret` containing `S3_ACCESS_KEY` and `S3_SECRET_KEY`.

For IAM-generated S3 keys, choose `BucketAccessClass.authenticationType: IAM`, leave `admin.username` and `admin.password` empty, and supply the IAM endpoint. For HyperStore user-based keys, choose `authenticationType: Key` and configure `admin.endpoint`, `admin.username`, `admin.password`, and `admin.group`, or use the corresponding existing-secret fields (`ADMIN_USER`, `ADMIN_PASSWORD`, and `GROUP`) together with `admin.endpoint`. IAM requests continue to use IAM even if admin credentials are configured.

5. Install from this source chart using your overrides:

```sh
helm upgrade --install cloudian-cosi-driver ./helm/cosi-driver \
  --namespace cloudian-cosi --create-namespace -f values.yaml
```

Use the chart from this branch (`0.1.0-alpha1.1`) to include its namespace, RBAC, and socket-security fixes. The old `0.0.7` chart does not contain these improvements. Existing v1alpha1 resources do not require conversion to alpha2 schemas. Back up resources and credentials and review deletion policies before upgrading; this chart does not convert resources or force-remove finalizers. Moving from an already-installed v1alpha2 environment to this branch requires a separate, planned API migration.

### Sidecar Runtime And Permissions

The chart deploys the driver and v1alpha1 sidecar with a shared socket volume. The `--driver-addr=unix:///var/lib/cosi/cosi.sock` flag selects the driver endpoint. Default sidecar arguments are `--debug=false` and `--v=2`; keep gRPC debug logging disabled because the upstream debug logger includes requests and responses containing credentials. The v0.2.2 sidecar manages leader election internally and does not support the v1alpha2 controller-runtime/Zap flags or `/readyz` and `/healthz` endpoints. The chart therefore does not install those incompatible HTTP probes.

The pod uses shared group 65532, and the driver starts through `/bin/sh -c 'umask 0007; exec /entrypoint.sh'` so its Unix socket is writable by that group without granting world access. The sidecar runs as user/group 65532 without privilege escalation. Custom driver images must provide the shell and entrypoint used by this repository's image. The driver still starts as root to install custom CA certificates; the chart does not switch the driver container to a non-root identity.

The pod uses the dedicated ServiceAccount in the Helm release namespace. Sidecar RBAC includes Bucket/BucketAccess reads and updates, BucketAccess deletion, BucketClaim reads and updates, class reads, status/finalizer updates, Secrets, and Events. `POD_NAMESPACE` is set from the pod namespace, and leader-election Lease permissions are limited to that namespace. The chart does not grant these privileges to the namespace's default ServiceAccount. ClusterRole and ClusterRoleBinding names include the release namespace and name.

## Create a Bucket

### Create a BucketClass

Create a yaml file containing your preferred bucket class configuration, e.g. `bucketclass.yaml`:
```
apiVersion: objectstorage.k8s.io/v1alpha1
kind: BucketClass
metadata:
  name: cloudian-bucketclass
driverName: cloudian-cosi-driver
deletionPolicy: Delete
```

Apply the BucketClass Resource:
```
kubectl apply -f bucketclass.yaml
```

### Create a BucketClaim

Create a yaml file with your preferred bucket claim configuration, e.g. `bucketclaim.yaml`:
```
apiVersion: objectstorage.k8s.io/v1alpha1
kind: BucketClaim
metadata:
  name: cloudian-bucketclaim
spec:
  bucketClassName: cloudian-bucketclass
  protocols:
    - S3
```
Apply the BucketClaim Resource:
```
kubectl apply -f bucketclaim.yaml
```

A `bucket` kubernetes resource object will then be created as well as an actual bucket in the HyperStore backend.

### Bucket Configuration Parameters

Configure these options in the top-level `BucketClass.parameters` field. A BucketClaim selects the class through `spec.bucketClassName`; the v1alpha1 BucketClaim schema has no free-form parameters field. All parameter values are strings.

| Parameter | Values | Behavior when omitted |
| --- | --- | --- |
| `storagePolicyID` | HyperStore storage policy ID | Existing default storage policy behavior. |
| `versioning` | `Enabled` or `Suspended` | No versioning request, unless Object Lock is enabled. |
| `objectLockEnabled` | `"true"` or `"false"` | No Object Lock request. |
| `retentionMode` | `GOVERNANCE` or `COMPLIANCE`; requires Object Lock and exactly one duration | No default retention rule is supplied. |
| `retentionDays` | Positive 32-bit integer; cannot be combined with `retentionYears` | No duration in days; use years if configuring retention. |
| `retentionYears` | Positive 32-bit integer; cannot be combined with `retentionDays` | No duration in years; use days if configuring retention. |
| `lifecyclePolicy` | JSON object with a nonempty S3 `Rules` array | No lifecycle configuration request. |

Parameter values are case-sensitive. For example, use `versioning: "Enabled"`, not `versioning: "enabled"`. HyperStore may impose stricter limits on retention durations than the driver's positive 32-bit integer validation.

#### Retention And Object Lock

Retention protects an object version from permanent deletion; it does not schedule deletion when the retention period ends. Lifecycle rules perform the cleanup separately.

Retention requires `objectLockEnabled: "true"`. Object Lock enables versioning automatically; combining it with `versioning: "Suspended"` is rejected. Setting `objectLockEnabled: "false"` does not disable Object Lock on an existing bucket. Enabling Object Lock is not reversible.

- `COMPLIANCE`: retained versions cannot be permanently deleted, or have their retention shortened, before expiry, even by privileged callers.
- `GOVERNANCE`: appropriately authorized callers can bypass retention. The driver does not request a retention bypass.
- Default retention applies to new object versions, not retroactively to existing objects. Each new version receives its own retention period. Object Lock without a default retention rule does not itself protect every new object; callers must supply object-level retention or a legal hold.
- Retention protects versions, not object names: a new upload can create another version, and a delete request without a version ID can create a delete marker. Neither permanently removes the protected version.

For retention measured in years, replace the days-based retention settings in the example below with:

```yaml
  retentionMode: "COMPLIANCE"
  retentionYears: "1"
```

Do not also specify `retentionDays`. Omitting the mode, supplying both duration fields, or using a zero, negative, or non-numeric duration is invalid.

#### Lifecycle Cleanup

`lifecyclePolicy` contains the complete S3 lifecycle configuration as JSON inside a YAML string. Each rule needs a `Status` of `Enabled` or `Disabled`, a `Filter`, and at least one action. `Filter: {"Prefix": ""}` selects all object keys; use a nonempty prefix to limit a rule to particular keys. The legacy top-level rule `Prefix` field is not a substitute for `Filter` in this driver.

For versioned buckets, configure each cleanup stage explicitly:

| Lifecycle action | Purpose | Age measured from |
| --- | --- | --- |
| `Expiration.Days` | Expires the current version, normally creating a delete marker rather than permanently removing data. | Creation of that object version. |
| `NoncurrentVersionExpiration.NoncurrentDays` | Permanently removes eligible old versions. Active retention or a legal hold still blocks removal. | When the version became noncurrent, for example after an overwrite or expiration. |
| `Expiration.ExpiredObjectDeleteMarker` | Removes an eligible delete marker after the other versions have been removed. | Backend eligibility, not a configurable age. |

Use a separate rule for `ExpiredObjectDeleteMarker: true`; do not combine it with `Days` or `Date` in the same `Expiration` object. Lifecycle cleanup is asynchronous. Neither retention expiry nor a lifecycle age guarantees immediate deletion.

Lifecycle rules can also be used without Object Lock or retention. To keep versioning and cleanup from the example below without deletion protection, omit `objectLockEnabled`, `retentionMode`, and `retentionDays`, and keep `versioning` and `lifecyclePolicy`. For an unversioned bucket, current-object expiration permanently deletes eligible objects instead of creating delete markers.

Supplying `lifecyclePolicy` replaces the bucket's lifecycle rule set; it does not append or merge rules. Include all rules that should remain configured. The backend must support any additional S3 actions you include, such as transitions or incomplete multipart upload cleanup.

#### Protection And Cleanup Example

This class protects new object versions for 30 days and configures lifecycle cleanup for all keys:

```yaml
apiVersion: objectstorage.k8s.io/v1alpha1
kind: BucketClass
metadata:
  name: cloudian-protected-30d
driverName: cloudian-cosi-driver
deletionPolicy: Retain
parameters:
  storagePolicyID: "your-storage-policy-id"
  objectLockEnabled: "true"
  versioning: "Enabled"
  retentionMode: "COMPLIANCE"
  retentionDays: "30"
  lifecyclePolicy: |
    {
      "Rules": [
        {
          "ID": "expire-data",
          "Status": "Enabled",
          "Filter": {"Prefix": ""},
          "Expiration": {"Days": 30},
          "NoncurrentVersionExpiration": {"NoncurrentDays": 1}
        },
        {
          "ID": "remove-expired-delete-markers",
          "Status": "Enabled",
          "Filter": {"Prefix": ""},
          "Expiration": {"ExpiredObjectDeleteMarker": true}
        }
      ]
    }
```

Reference it from a claim:

```yaml
apiVersion: objectstorage.k8s.io/v1alpha1
kind: BucketClaim
metadata:
  name: cloudian-protected-data
spec:
  bucketClassName: cloudian-protected-30d
  protocols:
    - S3
```

For a version that is never overwritten or manually deleted, this example works as follows:

1. Uploading the object creates a version with 30 days of retention.
2. At the current-version expiration age of 30 days, lifecycle can create a delete marker. The data version becomes noncurrent.
3. After at least one day as a noncurrent version, that data version becomes eligible for permanent cleanup, provided retention has expired and no legal hold applies.
4. The delete-marker rule can remove the remaining expired marker once it is eligible.

This is not a promise of deletion exactly 30 or 31 days after upload. Backend scheduling can delay each step. An overwrite can make a version noncurrent earlier, but permanent deletion remains blocked until its own retention period expires.

#### Validation And Provisioning

The lifecycle JSON uses S3 configuration field names, such as `Rules`, `Status`, `Filter`, `Expiration`, and `NoncurrentVersionExpiration`. Malformed JSON, unknown JSON fields, missing rule status/filter/action, and invalid retention/versioning combinations are rejected with COSI gRPC `InvalidArgument` before any S3 bucket creation call. The error message starts with `invalid bucket parameters:` and identifies the rejected setting. For example, combining Object Lock with suspended versioning returns `invalid bucket parameters: object lock requires enabled versioning`.

HyperStore validates the remaining S3-specific constraints and supported actions. Ensure your HyperStore version supports the requested APIs and the driver credentials allow `s3:PutBucketVersioning`, `s3:PutBucketObjectLockConfiguration`, and `s3:PutLifecycleConfiguration` as needed, in addition to bucket creation. These are permissions for the driver's provisioning identity, not permissions automatically granted to application workloads.

Configuration is applied during dynamic provisioning, not to statically imported buckets or as a continuous configuration controller. Object Lock is requested during bucket creation; the driver then applies versioning, Object Lock/default retention, and lifecycle configuration in that order. On a partial provisioning failure, the driver returns an error and reapplies the requested configuration on a COSI create retry; it does not delete the partially configured bucket.

An omitted `versioning` or `lifecyclePolicy` does not send a request for that setting, except that Object Lock implies enabled versioning. With `objectLockEnabled: "true"`, the driver sends the requested Object Lock configuration, including a default retention rule only when specified; do not rely on an omitted retention rule to preserve an externally configured rule. Changing a class does not continuously reconfigure already-created buckets. Define a new class to provision new buckets with different settings.

## Delete a Bucket

To delete a bucket, simply remove the corresponding BucketClaim, e.g:
```
kubectl delete bucketclaim cloudian-bucketclaim
```
This will delete the `bucket` kubernetes resource object. Whether the actual bucket in HyperStore is deleted depends
on the `deletionPolicy` set in the BucketClass.

The driver does not empty buckets, purge object versions, or bypass Object Lock. With `deletionPolicy: Delete`, bucket deletion can fail while versions, delete markers, or protected data remain. `Retain` is recommended for retention-protected buckets until the data has expired and the bucket can be safely removed.

### Deletion Retries And Finalizers

COSI may repeat backend deletion requests after a restart, a partial failure, or a failed Kubernetes update. The driver treats an already-missing bucket as successfully deleted. IAM access revocation also tolerates already-missing users, inline policies, and access keys. HyperStore user-based revocation removes the user's bucket policy statement before deleting the user, so a failed policy update can be retried while the canonical user ID is still available. Missing users, bucket policies, or buckets do not block access revocation; permission errors and other backend failures remain errors.

BucketClaim, BucketAccess, and BucketAccessClass reconciliation and Kubernetes finalizers belong to the separately deployed COSI controller/sidecar, not this driver's gRPC server. This repository does not implement finalizer updates. A finalizer update that conflicts with a concurrent resource change must be retried in that controller against a freshly fetched object, preserving other finalizers and treating an already-deleted object as success. Cleanup and dependency checks must still succeed before removing the controller's finalizer; do not force-remove finalizers to bypass retention, backend errors, or resources still in use.

## Grant Bucket Access

### Create a BucketAccessClass

Create a yaml file containing your preferred bucket access class configuration, e.g. `bucketaccessclass.yaml`:
```
apiVersion: objectstorage.k8s.io/v1alpha1
kind: BucketAccessClass
metadata:
  name: cloudian-bucketaccessclass                                  
driverName: cloudian-cosi-driver
authenticationType: IAM
parameters:
  objectDataAccessMode: ReadWrite
```
Choose `IAM` for IAM users and S3 access keys, or `Key` for HyperStore users (requires configured admin credentials). These are explicit v1alpha1 authentication types. The driver does not implement automatic Kubernetes token federation.

Optional driver-specific access parameters are `objectDataAccessMode`, `objectMetadataAccessMode`, and `bucketMetadataAccessMode`. Each accepts `ReadOnly`, `WriteOnly`, `ReadWrite`, or `Any`. If all three are absent, object data receives the original read/write policy. Once any is provided, an omitted category receives no permissions, and `Any` also grants no permissions for that category. At least one category must grant permissions. These are top-level `BucketAccessClass.parameters`, not native v1alpha2 `accessModes` fields on a BucketAccess.

Apply the BucketAccessClass Resource:
```
kubectl apply -f bucketaccessclass.yaml
```

### Create a BucketAccess

Create a yaml file with your preferred bucket access configuration, e.g. `bucketaccess.yaml`:
```
apiVersion: objectstorage.k8s.io/v1alpha1
kind: BucketAccess
metadata:
  name: cloudian-bucketaccess
spec:
  bucketAccessClassName: cloudian-bucketaccessclass
  bucketClaimName: cloudian-bucketclaim
  protocol: S3
  credentialsSecretName: cloudian-secret
  serviceAccountName: cloudian-application
```
Apply the BucketAccess Resource:
```
kubectl apply -f bucketaccess.yaml
```

The v1alpha1 sidecar requires a nonempty `serviceAccountName` for IAM requests; set it to the ServiceAccount used by your workload. For this driver, IAM still returns access keys, not ServiceAccount token credentials. The sidecar stores S3 bucket information and credentials as its `BucketInfo` JSON document in the Secret named by `credentialsSecretName`. It does not use the v1alpha2 flat Secret format.

## Revoke Bucket Access

To revoke access to a bucket, simply remove the corresponding BucketAccess, e.g:
```
kubectl delete bucketaccess cloudian-bucketaccess
```
This deletes the credentials secret and revokes the bucket access. To remove the bucket itself, delete its BucketClaim.
