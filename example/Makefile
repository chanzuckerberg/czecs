AWS_PROFILE=my_aws_profile
AWS_REGION=us-west-2
CLUSTER=example-cluster

all: staging

staging:
	AWS_PROFILE=${AWS_PROFILE} AWS_REGION=${AWS_REGION} czecs upgrade -f balances.staging.json --set region=${AWS_REGION} ${CLUSTER} example-staging-helloworld czecs.json

prod:
	AWS_PROFILE=${AWS_PROFILE} AWS_REGION=${AWS_REGION} czecs upgrade -f balances.prod.json --set region=${AWS_REGION} ${CLUSTER} example-prod-helloworld czecs.json
