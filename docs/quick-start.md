
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

## Delete a Bucket

To delete a bucket, simply remove the corresponding BucketClaim, e.g:
```
kubectl delete bucketclaim cloudian-bucketclaim
```
This will delete the `bucket` kubernetes resource object. Whether the actual bucket in HyperStore is deleted depends
on the `deletionPolicy` set in the BucketClass.

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
