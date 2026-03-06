from crossplane.function import resource
from crossplane.function.proto.v1 import run_function_pb2 as fnv1


def compose(req: fnv1.RunFunctionRequest, rsp: fnv1.RunFunctionResponse):
    pass
    # Example to retrieve variables from "xr"; update as needed
    # observed_xr = v1alpha1.XBucket(**req.observed.composite.resource)
    # region = "us-west-1"
    # if observed_xr.spec.region is not None:
    #     region = observed_xr.spec.region

    # Example S3 Bucket managed resource configuration; update as needed
    # bucket = bucketv1beta1.Bucket(
    #     spec=bucketv1beta1.Spec(
    #         forProvider=bucketv1beta1.ForProvider(
    #             region=region,
    #         ),
    #     ),
    # )

    # resource.update(rsp.desired.resources["bucket"], bucket)
