
# Getting Started

## Dependencies

`kubectl` - v1.23+

`helm` - v3.8+ due to support for OCI registries

## Install

1. Apply the COSI v1alpha2 API and Controller. The driver uses the COSI proto at this pinned upstream commit:
```
kubectl apply -k 'https://github.com/kubernetes-sigs/container-object-storage-interface//?ref=274540c69400dc1e13278b149a4e073339cefde2'
```

2. Generate a [values.yaml](../helm/cosi-driver/values.yaml) to reflect your HyperStore configuration. The driver supports S3 key authentication. It uses IAM-generated access keys when system-admin credentials are omitted, and HyperStore users when they are configured.

3. Install using the values.yaml you just created:
```
helm install cloudian-cosi-driver oci://quay.io/cloudian/cosi-driver --version 0.0.7 -f values.yaml
```

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

Configure these options in `BucketClass.spec.parameters`. A BucketClaim selects the class through `spec.bucketClassName`; neither the v1alpha1 nor v1alpha2 BucketClaim schema has a free-form parameters field. The existing parameter is spelled `storagePolicyID` (not `storagePolicID`). All parameter values are strings.

| Parameter | Values | Behavior when omitted |
| --- | --- | --- |
| `storagePolicyID` | HyperStore storage policy ID | Existing default storage policy behavior. |
| `versioning` | `Enabled` or `Suspended` | No versioning request, unless Object Lock is enabled. |
| `objectLockEnabled` | `"true"` or `"false"` | No Object Lock request. |
| `retentionMode` | `GOVERNANCE` or `COMPLIANCE` | No default retention rule. |
| `retentionDays` | Positive integer | Specify exactly one of days or years together with a retention mode. |
| `retentionYears` | Positive integer | Specify exactly one of days or years together with a retention mode. |
| `lifecyclePolicy` | JSON object with S3 `Rules` | No lifecycle configuration request. |

Retention requires `objectLockEnabled: "true"`. Object Lock enables versioning automatically; combining it with `versioning: Suspended` is rejected. Setting `objectLockEnabled: "false"` does not disable Object Lock on an existing bucket. Use Object Lock only when intended: enabling it is not reversible, and retained versions in `COMPLIANCE` mode cannot be deleted or have their retention shortened before expiry. `GOVERNANCE` mode allows bypass only to appropriately authorized callers; the driver does not use a retention bypass.

For example, this class protects new object versions for 30 days and configures lifecycle cleanup:

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

Default retention applies to new object versions, not retroactively to existing objects. Object Lock without a default retention rule does not itself protect every new object; callers must supply object-level retention or a legal hold. Lifecycle expiration on a versioned bucket normally creates a delete marker rather than deleting the old data. `NoncurrentVersionExpiration` is needed to remove old versions, and its duration starts when a version becomes noncurrent, not when it was uploaded. Cleanup is asynchronous and is not guaranteed exactly 30 days after upload. Active retention or a legal hold continues to prevent permanent deletion.

The lifecycle JSON uses S3 configuration field names, such as `Rules`, `Status`, `Filter`, `Expiration`, and `NoncurrentVersionExpiration`. Malformed JSON, unknown JSON fields, missing rule status/filter/action, and invalid retention/versioning combinations are rejected before creating the bucket. HyperStore validates the remaining S3-specific constraints and supported actions. Ensure your HyperStore version supports the requested APIs and the driver credentials allow `s3:PutBucketVersioning`, `s3:PutBucketObjectLockConfiguration`, and `s3:PutLifecycleConfiguration` as needed, in addition to bucket creation.

Configuration is applied during dynamic provisioning, not to statically imported buckets or as a continuous configuration controller. On a partial provisioning failure, the driver returns an error and reapplies the requested configuration on a COSI create retry; it does not delete the partially configured bucket. Omitted settings do not reset existing settings. BucketClass specs are immutable, so define a new class to provision new buckets with different settings.

## Delete a Bucket

To delete a bucket, simply remove the corresponding BucketClaim, e.g:
```
kubectl delete bucketclaim cloudian-bucketclaim
```
This will delete the `bucket` kubernetes resource object. Whether the actual bucket in HyperStore is deleted depends
on the `deletionPolicy` set in the BucketClass.

The driver does not empty buckets, purge object versions, or bypass Object Lock. With `deletionPolicy: Delete`, bucket deletion can fail while versions, delete markers, or protected data remain. `Retain` is recommended for retention-protected buckets until the data has expired and the bucket can be safely removed.

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
