# Example usage

This is an example of usage of czecs. It is not actually runnable (the container specified will not work as is), but illustrates general usage.

## Assumptions
This example makes several assumptions about infrastructure already set up before running czecs.
  * Your ~/.aws/config has a section for a profile called `my_aws_profile`.
  * This profile has the necessary permissions to create and destroy ECS task definitions and change ECS services.
  * There is an ECS cluster called `example-cluster` in the us-west-2 region.
  * There are task roles with the names `example-prod-helloworld` and `example-staging-helloworld`, and in the Fargate example an execution role with the name `example-staging-helloworld-execution`.
  * For the Fargate example using private Docker registry, the username/password for that registry are stored in the AWS secrets manager with a name `example-staging-credentials/privatedocker`.
  * There are Cloudwatch log groups with the name `example-prod-logs-group` and `example-staging-logs-group`.

## Files

  * `czecs.json` - A basic template that shows how to run an ECS task on an EC2-backed cluster.
  * `balances.staging.json` and `balances.prod.json` - Simple balances files showing how to pass different values in staging and prod environments while still using the same czecs.json service template.
  * `Makefile` - A simple makefile showing how to deploy to prod/staging using the above files. It also shows how to use environment variables to affect which AWS region and role is used when deploying the service.
  * `czecs.fargate.json` and `balances.staging.json` - A more advanced template that shows how to run an ECS task on Fargate. It also includes an example of using a private Docker registry, where the credentials are stored in the AWS secrets manager. The big differences from the EC2-backed cluster include:
     * `RequiresCompatibilities` must contain `"FARGATE"` as one of the value in the list.
     * `Cpu`/`Memory` hard limits must be specified at the task level instead of the container level, and the values must be one of the valid CPU/Memory combinations supported by Fargate.
     * An `ExecutionRoleArn` must be provided that has permissions to any private repo and to any configured awslogs
     * `NetworkMode` must be `awsvpc`.
     * The `hostPort` must match the `containerPort`; 0 is not a valid port value to allow automatic port assignment in Fargate.

