package cmd

import (
	"fmt"
	"path"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ecs/ecsiface"
	"github.com/chanzuckerberg/czecs/tasks"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type installCmd struct {
	templateCmd
	rollback bool
	service  string
}

func newInstallCmd() *cobra.Command {
	inst := &installCmd{}
	cmd := &cobra.Command{
		Use:   "install [cluster] [path containing czecs.json]",
		Short: "Install a service into an ECS cluster",
		Long: `This command installs a service into an ECS cluster.

Limitations: No support for setting up load balancers through this command;
if you need load balancers; manually create an ECS service outside this tool
(e.g. using Terraform or aws command line tool), then use czecs upgrade.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sess := session.Must(session.NewSessionWithOptions(session.Options{
				SharedConfigState: session.SharedConfigEnable,
			}))
			svc := ecs.New(sess)
			return inst.run(args, svc)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&inst.strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&inst.balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&inst.values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&inst.stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.BoolVar(&inst.rollback, "rollback", false, "delete service if deployment failed")
	f.StringVarP(&inst.service, "name", "n", "", "service name; required for now")
	cmd.MarkFlagRequired("name")

	return cmd
}

func (i *installCmd) run(args []string, svc ecsiface.ECSAPI) error {
	cluster := args[0]
	czecsPath := args[1]
	var balances map[string]interface{}
	balances, err := mergeValues(i.balanceFiles, i.values, i.stringValues)
	if err != nil {
		return err
	}
	values := map[string]interface{}{
		"Values": balances,
	}
	registerTaskDefinitionInput, err := tasks.ParseTaskDefinition(path.Join(czecsPath, "czecs.json"), values, i.strict)
	if err != nil {
		return errors.Wrap(err, "cannot parse task definition")
	}
	if debug {
		fmt.Printf("%+v\n", registerTaskDefinitionInput)
	}

	describeServicesOutput, err := svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster:  &cluster,
		Services: []*string{&i.service},
	})
	if err != nil {
		return errors.Wrap(err, "cannot describe services")
	}
	if len(describeServicesOutput.Failures) != 0 {
		return fmt.Errorf("Error retrieving information about existing service %v: %v", i.service, describeServicesOutput.Failures)
	}
	var oldTaskDefinition *string
	for _, existingService := range describeServicesOutput.Services {
		if *existingService.ServiceName == i.service || *existingService.ServiceArn == i.service {
			oldTaskDefinition = existingService.TaskDefinition
		}
	}
	if oldTaskDefinition != nil {
		return fmt.Errorf("Service %v already exists in cluster %v. Use czecs upgrade command to upgrade existing service", i.service, cluster)
	}
	registerTaskDefinitionOutput, err := svc.RegisterTaskDefinition(registerTaskDefinitionInput)
	if err != nil {
		return errors.Wrap(err, "cannot register task definition")
	}
	taskDefn := registerTaskDefinitionOutput.TaskDefinition
	err = deployInstall(svc, cluster, i.service, *taskDefn.TaskDefinitionArn)
	if err != nil && i.rollback {
		err = rollbackInstall(svc, cluster, i.service)
		if err != nil {
			return errors.Wrap(err, "cannot rollback install")
		}
		svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
			TaskDefinition: taskDefn.TaskDefinitionArn,
		})
	}
	return nil
}

func deployInstall(svc ecsiface.ECSAPI, cluster string, service string, taskDefnArn string) error {
	// Get the primary deployment's updated date, default to now if missing
	createdAt := time.Now()
	createServiceOutput, err := svc.CreateService(&ecs.CreateServiceInput{
		Cluster:        &cluster,
		ServiceName:    &service,
		TaskDefinition: &taskDefnArn,
	})
	if err != nil {
		// TODO(mbarrien) Avoid rollback?
		return err
	}
	for _, deployment := range createServiceOutput.Service.Deployments {
		if *deployment.Status == "PRIMARY" {
			createdAt = *deployment.CreatedAt
			break
		}
	}

	opts := []request.WaiterOption{getFailOnAbortContext(createdAt)}
	if !quiet {
		opts = append(opts, sleepProgressWithContext)
	}
	return svc.WaitUntilServicesStableWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{createServiceOutput.Service.ServiceArn}},
		opts...)
}

func rollbackInstall(svc ecsiface.ECSAPI, cluster string, service string) error {
	// Get the primary deployment's updated date, default to now if missing
	deleteServiceOutput, err := svc.DeleteService(&ecs.DeleteServiceInput{
		Cluster: &cluster,
		Service: &service,
	})
	if err != nil {
		return err
	}

	opts := []request.WaiterOption{}
	if !quiet {
		opts = append(opts, sleepProgressWithContext)
	}
	return svc.WaitUntilServicesInactiveWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{deleteServiceOutput.Service.ServiceArn}},
		opts...)
}

func init() {
	rootCmd.AddCommand(newInstallCmd())
}
