
# Getting Started

## Dependencies

`kubectl` compatible with your Kubernetes cluster. The cluster must support the CRDs from the pinned COSI v1alpha2 revision; do not assume the previous v1alpha1 Kubernetes requirements still apply.

`helm` - v3.8+ due to support for OCI registries

## Install

1. Prepare compatible images. The driver uses the COSI proto at commit `274540c69400dc1e13278b149a4e073339cefde2`. Use controller and sidecar builds from that revision. The chart intentionally requires `sidecar.image.repository` and `sidecar.image.tag`; the old 2024 `objectstorage-sidecar` image and v1alpha1 releases are not compatible. A tag alone does not prove compatibility: verify the source revision of any pre-built image you use.

For example, build and publish your own controller and sidecar images, replacing `registry.example.com` with your registry:

```sh
git clone https://github.com/kubernetes-sigs/container-object-storage-interface.git cosi-v1alpha2
git -C cosi-v1alpha2 checkout 274540c69400dc1e13278b149a4e073339cefde2
docker build -f cosi-v1alpha2/controller/Dockerfile -t registry.example.com/cosi-controller:274540c69400 cosi-v1alpha2
docker build -f cosi-v1alpha2/sidecar/Dockerfile -t registry.example.com/cosi-sidecar:274540c69400 cosi-v1alpha2
docker push registry.example.com/cosi-controller:274540c69400
docker push registry.example.com/cosi-sidecar:274540c69400
```

2. Deploy the pinned COSI CRDs and controller with your controller image override. The driver chart installs neither the CRDs nor the central controller. Use a Kustomize overlay such as `cosi-install/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/kubernetes-sigs/container-object-storage-interface//?ref=274540c69400dc1e13278b149a4e073339cefde2
images:
  - name: gcr.io/k8s-staging-sig-storage/objectstorage-controller
    newName: registry.example.com/cosi-controller
    newTag: "274540c69400"
```

```sh
kubectl apply -k cosi-install
```

3. Build and publish the driver from this repository, or use a verified v1alpha2 build. The chart's `appVersion` remains the development image tag `v0.0.0`; it does not identify a published alpha2 release. For example, from this repository root:

```sh
make image
docker tag cloudian-cosi-driver:v0.0.0 registry.example.com/cloudian-cosi-driver:v1alpha2
docker push registry.example.com/cloudian-cosi-driver:v1alpha2
```

4. Configure your HyperStore endpoints and credentials using the chart's [values.yaml](../helm/cosi-driver/values.yaml). In your installation overrides, set the images you prepared:

```yaml
image:
  repository: registry.example.com/cloudian-cosi-driver
  tag: "v1alpha2"
sidecar:
  image:
    repository: registry.example.com/cosi-sidecar
    tag: "274540c69400"
```

All registry and tag values above are examples for images you build; they are not published image locations. Make sure your cluster can pull them. The default S3 credentials in the values file are placeholders and must be replaced, or use `existingCredentialsSecret` containing `S3_ACCESS_KEY` and `S3_SECRET_KEY`.

The chart defaults to IAM-generated S3 keys: leave `admin.username` and `admin.password` empty and supply the IAM endpoint. For HyperStore user-based keys, configure `admin.endpoint`, `admin.username`, `admin.password`, and `admin.group`, or use the corresponding existing-secret fields (`ADMIN_USER`, `ADMIN_PASSWORD`, and `GROUP`) together with `admin.endpoint`. COSI `authenticationType` remains `Key` for either backend.

5. Install from this source chart using your overrides:

```sh
helm upgrade --install cloudian-cosi-driver ./helm/cosi-driver \
  --namespace cloudian-cosi --create-namespace -f values.yaml
```

Do not use the old `0.0.7` chart release for this alpha2 driver. An existing v1alpha1 installation needs a coordinated CRD/controller/sidecar/driver migration and resource conversion, not just a Helm image update. Back up existing resources and credentials and review deletion policies before migrating; this chart does not convert resources or force-remove finalizers.

### Sidecar Runtime And Permissions

The chart deploys the driver and v1alpha2 sidecar with a shared socket volume. `COSI_ENDPOINT` points to `unix:///var/lib/cosi/cosi.sock`. The sidecar uses controller-runtime/Zap arguments, not the old `--debug -v` flags. Its default arguments enable leader election, disable metrics, and expose readiness/liveness probes on port 8081. If overriding `sidecar.args`, keep the health endpoint on that port or adjust the probes as well. These health probes are not HyperStore connectivity checks.

The pod uses shared group 65532, and the driver starts through `/bin/sh -c 'umask 0007; exec /entrypoint.sh'` so its Unix socket is writable by that group without granting world access. The sidecar runs as user/group 65532 without privilege escalation. Custom driver images must provide the shell and entrypoint used by this repository's image. The driver still starts as root to install custom CA certificates; the chart does not switch the driver container to a non-root identity.

The pod uses the dedicated ServiceAccount in the Helm release namespace. Sidecar RBAC includes Bucket/BucketAccess reads and updates, status patches, finalizer updates, Secrets, and Events. Leader-election Lease permissions are limited to the release namespace. BucketClaim and class reconciliation remains the central controller's responsibility. The chart does not grant these privileges to the namespace's default ServiceAccount. ClusterRole and ClusterRoleBinding names include the release namespace and name.

## Create a Bucket

### Create a BucketClass

Create a yaml file containing your preferred bucket class configuration, e.g. `bucketclass.yaml`:
```
apiVersion: objectstorage.k8s.io/v1alpha2
kind: BucketClass
metadata:
  name: cloudian-bucketclass
spec:
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
apiVersion: objectstorage.k8s.io/v1alpha2
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

Configure these options in `BucketClass.spec.parameters`. A BucketClaim selects the class through `spec.bucketClassName`; neither the v1alpha1 nor v1alpha2 BucketClaim schema has a free-form parameters field. All parameter values are strings.

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
apiVersion: objectstorage.k8s.io/v1alpha2
kind: BucketClass
metadata:
  name: cloudian-protected-30d
spec:
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
apiVersion: objectstorage.k8s.io/v1alpha2
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

An omitted `versioning` or `lifecyclePolicy` does not send a request for that setting, except that Object Lock implies enabled versioning. With `objectLockEnabled: "true"`, the driver sends the requested Object Lock configuration, including a default retention rule only when specified; do not rely on an omitted retention rule to preserve an externally configured rule. BucketClass specs are immutable, so define a new class to provision new buckets with different settings.

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
apiVersion: objectstorage.k8s.io/v1alpha2
kind: BucketAccessClass
metadata:
  name: cloudian-bucketaccessclass                                  
spec:
  driverName: cloudian-cosi-driver
  authenticationType: Key
  multiBucketAccess: SingleBucket
```
The driver provisions S3 access keys through IAM by default. If HyperStore system-admin credentials are configured, it provisions HyperStore users instead.

Apply the BucketAccessClass Resource:
```
kubectl apply -f bucketaccessclass.yaml
```

### Create a BucketAccess

Create a yaml file with your preferred bucket access configuration, e.g. `bucketaccess.yaml`:
```
apiVersion: objectstorage.k8s.io/v1alpha2
kind: BucketAccess
metadata:
  name: cloudian-bucketaccess
spec:
  bucketAccessClassName: cloudian-bucketaccessclass
  protocol: S3
  bucketClaims:
    - bucketClaimName: cloudian-bucketclaim
      accessModes:
        objectData: ReadWrite
      accessSecretName: cloudian-secret
```
Apply the BucketAccess Resource:
```
kubectl apply -f bucketaccess.yaml
```

The access credentials and S3 bucket information are stored in the secret named by `accessSecretName`.

## Revoke Bucket Access

To revoke access to a bucket, simply remove the corresponding BucketAccess, e.g:
```
kubectl delete bucketaccess cloudian-bucketaccess
```
This deletes the credentials secret and revokes the bucket access. To remove the bucket itself, delete its BucketClaim.
